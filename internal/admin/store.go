package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"symterm/internal/control"
	"symterm/internal/proto"
)

type UserRecord struct {
	Username          string    `json:"username"`
	Disabled          bool      `json:"disabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	DefaultEntrypoint []string  `json:"default_entrypoint,omitempty"`
	TokenIDs          []string  `json:"token_ids"`
	Note              string    `json:"note,omitempty"`
}

type UserTokenRecord struct {
	TokenID     string              `json:"token_id"`
	Username    string              `json:"username"`
	CreatedAt   time.Time           `json:"created_at"`
	LastUsedAt  *time.Time          `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time          `json:"revoked_at,omitempty"`
	Description string              `json:"description,omitempty"`
	SecretHash  string              `json:"secret_hash"`
	Source      control.TokenSource `json:"source"`
}

type IssuedToken struct {
	Record      UserTokenRecord
	PlainSecret string
}

type Store struct {
	mu sync.Mutex

	root      string
	usersDir  string
	tokensDir string
	auditPath string

	users  map[string]UserRecord
	tokens map[string]UserTokenRecord
}

func OpenStore(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("admin store root is required")
	}
	store := &Store{
		root:      root,
		usersDir:  filepath.Join(root, "users"),
		tokensDir: filepath.Join(root, "tokens"),
		auditPath: filepath.Join(root, "audit.log"),
		users:     make(map[string]UserRecord),
		tokens:    make(map[string]UserTokenRecord),
	}
	if err := os.MkdirAll(store.usersDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.tokensDir, 0o755); err != nil {
		return nil, err
	}
	if err := store.reloadLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) reloadLocked() error {
	users, err := loadDir[UserRecord](s.usersDir)
	if err != nil {
		return err
	}
	tokens, err := loadDir[UserTokenRecord](s.tokensDir)
	if err != nil {
		return err
	}
	s.users = users
	s.tokens = tokens
	return nil
}

func (s *Store) ImportBootstrapTokens(static map[string]string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for secret, username := range static {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		user := s.userOrNewLocked(username, now)
		tokenID := "bootstrap-" + shortHash(secret)
		if _, ok := s.tokens[tokenID]; ok {
			continue
		}
		token := UserTokenRecord{
			TokenID:     tokenID,
			Username:    username,
			CreatedAt:   now,
			Description: "imported bootstrap token",
			SecretHash:  hashSecret(secret),
			Source:      control.TokenSourceBootstrap,
		}
		user.TokenIDs = appendUnique(user.TokenIDs, tokenID)
		user.UpdatedAt = now
		if err := s.saveUserLocked(user); err != nil {
			return err
		}
		if err := s.saveTokenLocked(token); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Authenticate(_ context.Context, secret string) (control.AuthenticatedPrincipal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	secretHash := hashSecret(secret)
	now := time.Now().UTC()
	for tokenID, record := range s.tokens {
		if record.SecretHash != secretHash {
			continue
		}
		if record.RevokedAt != nil {
			return control.AuthenticatedPrincipal{}, proto.NewError(proto.ErrAuthenticationFailed, "token authentication failed")
		}
		user, ok := s.users[record.Username]
		if !ok {
			return control.AuthenticatedPrincipal{}, proto.NewError(proto.ErrAuthenticationFailed, "token authentication failed")
		}
		record.LastUsedAt = &now
		s.tokens[tokenID] = record
		if err := s.writeJSON(filepath.Join(s.tokensDir, safeName(tokenID)+".json"), record); err != nil {
			return control.AuthenticatedPrincipal{}, err
		}
		return control.AuthenticatedPrincipal{
			Username:        record.Username,
			UserDisabled:    user.Disabled,
			TokenID:         record.TokenID,
			TokenSource:     record.Source,
			AuthenticatedAt: now,
		}, disabledAuthError(user.Disabled)
	}
	return control.AuthenticatedPrincipal{}, proto.NewError(proto.ErrAuthenticationFailed, "token authentication failed")
}

func (s *Store) ListUsers() []UserRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortUserRecords(s.users)
}

func (s *Store) GetUser(username string) (UserRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.users[strings.TrimSpace(username)]
	return cloneUserRecord(record), ok
}

func (s *Store) ListTokens(username string) []UserTokenRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	return listTokenRecords(s.tokens, username, nil)
}

func (s *Store) ListBootstrapTokens(username string) []UserTokenRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listTokenRecords(s.tokens, username, func(record UserTokenRecord) bool {
		return record.Source == control.TokenSourceBootstrap
	})
}

func (s *Store) ListManagedTokens(username string) []UserTokenRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listTokenRecords(s.tokens, username, func(record UserTokenRecord) bool {
		return record.Source == control.TokenSourceManaged
	})
}

func (s *Store) ListLegacyTokens(username string) []UserTokenRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listTokenRecords(s.tokens, username, func(record UserTokenRecord) bool {
		return record.Source == control.TokenSourceLegacy
	})
}

func (s *Store) GetToken(tokenID string) (UserTokenRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.tokens[strings.TrimSpace(tokenID)]
	return cloneTokenRecord(record), ok
}

func (s *Store) CreateUser(username string, note string, now time.Time) (UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	username = strings.TrimSpace(username)
	if username == "" {
		return UserRecord{}, proto.NewError(proto.ErrInvalidArgument, "username is required")
	}
	if _, ok := s.users[username]; ok {
		return UserRecord{}, proto.NewError(proto.ErrConflict, "user already exists")
	}
	user := UserRecord{
		Username:  username,
		CreatedAt: now,
		UpdatedAt: now,
		TokenIDs:  nil,
		Note:      strings.TrimSpace(note),
	}
	if err := s.saveUserLocked(user); err != nil {
		return UserRecord{}, err
	}
	return cloneUserRecord(user), nil
}

func (s *Store) DisableUser(username string, now time.Time) (UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[strings.TrimSpace(username)]
	if !ok {
		return UserRecord{}, proto.NewError(proto.ErrInvalidArgument, "user does not exist")
	}
	user.Disabled = true
	user.UpdatedAt = now
	if err := s.saveUserLocked(user); err != nil {
		return UserRecord{}, err
	}
	return cloneUserRecord(user), nil
}

func (s *Store) IssueToken(username string, description string, now time.Time) (IssuedToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[strings.TrimSpace(username)]
	if !ok {
		return IssuedToken{}, proto.NewError(proto.ErrInvalidArgument, "user does not exist")
	}
	return s.issueTokenLocked(user, description, now, control.TokenSourceManaged)
}

func (s *Store) issueTokenLocked(user UserRecord, description string, now time.Time, source control.TokenSource) (IssuedToken, error) {
	secret, err := randomToken()
	if err != nil {
		return IssuedToken{}, err
	}
	tokenID := "tok-" + shortHash(secret+"-"+now.Format(time.RFC3339Nano))
	record := UserTokenRecord{
		TokenID:     tokenID,
		Username:    user.Username,
		CreatedAt:   now,
		Description: strings.TrimSpace(description),
		SecretHash:  hashSecret(secret),
		Source:      source,
	}
	user.TokenIDs = appendUnique(user.TokenIDs, tokenID)
	user.UpdatedAt = now
	if err := s.saveUserLocked(user); err != nil {
		return IssuedToken{}, err
	}
	if err := s.saveTokenLocked(record); err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{
		Record:      cloneTokenRecord(record),
		PlainSecret: secret,
	}, nil
}

func (s *Store) RevokeToken(tokenID string, now time.Time) (UserTokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.tokens[strings.TrimSpace(tokenID)]
	if !ok {
		return UserTokenRecord{}, proto.NewError(proto.ErrInvalidArgument, "token does not exist")
	}
	if record.Source == control.TokenSourceBootstrap {
		return UserTokenRecord{}, proto.NewError(proto.ErrInvalidArgument, "bootstrap tokens are imported and cannot be revoked individually")
	}
	record.RevokedAt = &now
	if err := s.saveTokenLocked(record); err != nil {
		return UserTokenRecord{}, err
	}
	return cloneTokenRecord(record), nil
}

func (s *Store) GetUserEntrypoint(username string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[strings.TrimSpace(username)]
	if !ok {
		return nil, proto.NewError(proto.ErrInvalidArgument, "user does not exist")
	}
	return append([]string(nil), user.DefaultEntrypoint...), nil
}

func (s *Store) SetUserEntrypoint(username string, argv []string, now time.Time) (UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[strings.TrimSpace(username)]
	if !ok {
		return UserRecord{}, proto.NewError(proto.ErrInvalidArgument, "user does not exist")
	}
	user.DefaultEntrypoint = append([]string(nil), argv...)
	user.UpdatedAt = now
	if err := s.saveUserLocked(user); err != nil {
		return UserRecord{}, err
	}
	return cloneUserRecord(user), nil
}

func (s *Store) EffectiveEntrypoint(username string, fallback []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if user, ok := s.users[strings.TrimSpace(username)]; ok && len(user.DefaultEntrypoint) > 0 {
		return append([]string(nil), user.DefaultEntrypoint...)
	}
	return append([]string(nil), fallback...)
}

func (s *Store) AppendAudit(action string, actor string, target string, result string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := map[string]string{
		"timestamp": now.Format(time.RFC3339Nano),
		"action":    strings.TrimSpace(action),
		"actor":     strings.TrimSpace(actor),
		"target":    strings.TrimSpace(target),
		"result":    strings.TrimSpace(result),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}

func (s *Store) userOrNewLocked(username string, now time.Time) UserRecord {
	if user, ok := s.users[username]; ok {
		return user
	}
	return UserRecord{
		Username:  username,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (s *Store) saveUserLocked(user UserRecord) error {
	user.TokenIDs = sortedStrings(user.TokenIDs)
	if err := s.writeJSON(filepath.Join(s.usersDir, safeName(user.Username)+".json"), user); err != nil {
		return err
	}
	s.users[user.Username] = cloneUserRecord(user)
	return nil
}

func (s *Store) saveTokenLocked(record UserTokenRecord) error {
	if err := s.writeJSON(filepath.Join(s.tokensDir, safeName(record.TokenID)+".json"), record); err != nil {
		return err
	}
	s.tokens[record.TokenID] = cloneTokenRecord(record)
	return nil
}

func (s *Store) writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func loadDir[T any](dir string) (map[string]T, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]T), nil
		}
		return nil, err
	}
	result := make(map[string]T)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var value T
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		switch typed := any(value).(type) {
		case UserRecord:
			result[typed.Username] = any(typed).(T)
		case UserTokenRecord:
			result[typed.TokenID] = any(typed).(T)
		default:
			return nil, fmt.Errorf("unsupported admin record type %T", value)
		}
	}
	return result, nil
}

func disabledAuthError(disabled bool) error {
	if !disabled {
		return nil
	}
	return proto.NewError(proto.ErrAuthenticationFailed, "user is disabled")
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func safeName(value string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(strings.TrimSpace(value))
}

func appendUnique(items []string, value string) []string {
	for _, existing := range items {
		if existing == value {
			return items
		}
	}
	return append(items, value)
}

func sortedStrings(items []string) []string {
	items = append([]string(nil), items...)
	sort.Strings(items)
	return items
}

func sortUserRecords(records map[string]UserRecord) []UserRecord {
	items := make([]UserRecord, 0, len(records))
	for _, record := range records {
		items = append(items, cloneUserRecord(record))
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Username < items[j].Username
	})
	return items
}

func cloneUserRecord(record UserRecord) UserRecord {
	record.DefaultEntrypoint = append([]string(nil), record.DefaultEntrypoint...)
	record.TokenIDs = append([]string(nil), record.TokenIDs...)
	return record
}

func cloneTokenRecord(record UserTokenRecord) UserTokenRecord {
	if record.LastUsedAt != nil {
		value := *record.LastUsedAt
		record.LastUsedAt = &value
	}
	if record.RevokedAt != nil {
		value := *record.RevokedAt
		record.RevokedAt = &value
	}
	return record
}

func listTokenRecords(records map[string]UserTokenRecord, username string, allow func(UserTokenRecord) bool) []UserTokenRecord {
	var tokens []UserTokenRecord
	for _, token := range records {
		if username != "" && token.Username != username {
			continue
		}
		if allow != nil && !allow(token) {
			continue
		}
		tokens = append(tokens, cloneTokenRecord(token))
	}
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].Username == tokens[j].Username {
			return tokens[i].TokenID < tokens[j].TokenID
		}
		return tokens[i].Username < tokens[j].Username
	})
	return tokens
}
