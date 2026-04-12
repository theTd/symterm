package admin

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"symterm/internal/control"
	"symterm/internal/proto"
)

type DaemonInfo struct {
	Version         string    `json:"version"`
	StartedAt       time.Time `json:"started_at"`
	ListenAddr      string    `json:"listen_addr"`
	AdminSocketPath string    `json:"admin_socket_path"`
	AdminWebAddr    string    `json:"admin_web_addr"`
}

type Snapshot struct {
	Cursor   uint64            `json:"cursor"`
	Daemon   DaemonInfo        `json:"daemon"`
	Sessions []SessionSnapshot `json:"sessions"`
	Users    []UserRecord      `json:"users"`
}

type UserDetail struct {
	User            UserRecord        `json:"user"`
	BootstrapTokens []UserTokenRecord `json:"bootstrap_tokens"`
	ManagedTokens   []UserTokenRecord `json:"managed_tokens"`
	LegacyTokens    []UserTokenRecord `json:"legacy_tokens,omitempty"`
}

type Service struct {
	sessions SessionCatalog
	events   *EventHub
	store    *Store
	now      func() time.Time
	tmux     TmuxStatusSource

	daemonMu sync.RWMutex
	daemon   DaemonInfo
}

type TmuxStatusSource interface {
	TmuxStatus(string, string) (proto.TmuxStatusSnapshot, error)
}

func NewService(sessionCatalog SessionCatalog, events *EventHub, store *Store, daemonInfo DaemonInfo, now func() time.Time) (*Service, error) {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if events == nil {
		var err error
		events, err = NewEventHub(defaultEventRetention)
		if err != nil {
			return nil, err
		}
	}
	service := &Service{
		sessions: sessionCatalog,
		events:   events,
		store:    store,
		now:      now,
		daemon:   daemonInfo,
	}
	service.events.RecordDaemon(daemonInfo)
	return service, nil
}

func (s *Service) SetListenAddr(addr string) {
	s.daemonMu.Lock()
	s.daemon.ListenAddr = addr
	s.daemonMu.Unlock()
	s.events.RecordDaemon(s.DaemonInfo())
}

func (s *Service) SetAdminWebAddr(addr string) {
	s.daemonMu.Lock()
	s.daemon.AdminWebAddr = addr
	s.daemonMu.Unlock()
	s.events.RecordDaemon(s.DaemonInfo())
}

func (s *Service) DaemonInfo() DaemonInfo {
	s.daemonMu.RLock()
	defer s.daemonMu.RUnlock()
	return s.daemon
}

func (s *Service) SetTmuxStatusSource(source TmuxStatusSource) {
	s.tmux = source
}

func (s *Service) Snapshot() Snapshot {
	return Snapshot{
		Cursor:   s.events.CurrentCursor(),
		Daemon:   s.DaemonInfo(),
		Sessions: s.ListSessions(),
		Users:    s.store.ListUsers(),
	}
}

func (s *Service) GetSession(sessionID string) (SessionSnapshot, bool) {
	if s.events != nil {
		if snapshot, ok := s.events.GetSession(sessionID); ok {
			return snapshot, true
		}
	}
	if s.sessions == nil {
		return SessionSnapshot{}, false
	}
	return s.sessions.GetSession(sessionID)
}

func (s *Service) TerminateSession(actor string, sessionID string) error {
	if s.sessions == nil {
		err := errors.New("session catalog is unavailable")
		_ = s.store.AppendAudit("terminate_session", actor, sessionID, "error:"+err.Error(), s.now())
		return err
	}
	if err := s.sessions.TerminateSession(sessionID, "terminated by admin"); err != nil {
		_ = s.store.AppendAudit("terminate_session", actor, sessionID, "error:"+err.Error(), s.now())
		return err
	}
	_ = s.store.AppendAudit("terminate_session", actor, sessionID, "ok", s.now())
	return nil
}

func (s *Service) ListSessions() []SessionSnapshot {
	if s.events == nil {
		if s.sessions == nil {
			return nil
		}
		return s.sessions.ListSessions()
	}
	snapshots := s.events.ListSessions()
	if len(snapshots) == 0 && s.sessions != nil {
		return s.sessions.ListSessions()
	}
	return snapshots
}

func (s *Service) GetTmuxStatus(clientID string, commandID string) (proto.TmuxStatusSnapshot, error) {
	if s.tmux == nil {
		return proto.TmuxStatusSnapshot{}, errors.New("tmux status source is unavailable")
	}
	return s.tmux.TmuxStatus(clientID, commandID)
}

func (s *Service) SubscribeEvents(since uint64) ([]Event, uint64, <-chan struct{}, error) {
	return s.events.Subscribe(since)
}

func (s *Service) EventsSince(since uint64) ([]Event, error) {
	return s.events.EventsSince(since)
}

func (s *Service) UnsubscribeEvents(subscriberID uint64) {
	s.events.Unsubscribe(subscriberID)
}

func (s *Service) ListUsers() []UserRecord {
	return s.store.ListUsers()
}

func (s *Service) GetUserDetail(username string) (UserDetail, bool) {
	user, ok := s.store.GetUser(username)
	if !ok {
		return UserDetail{}, false
	}
	return UserDetail{
		User:            user,
		BootstrapTokens: s.store.ListBootstrapTokens(username),
		ManagedTokens:   s.store.ListManagedTokens(username),
		LegacyTokens:    s.store.ListLegacyTokens(username),
	}, true
}

func (s *Service) CreateUser(actor string, username string, note string) (UserRecord, error) {
	record, err := s.store.CreateUser(username, note, s.now())
	if err != nil {
		_ = s.store.AppendAudit("create_user", actor, username, "error:"+err.Error(), s.now())
		return UserRecord{}, err
	}
	_ = s.store.AppendAudit("create_user", actor, username, "ok", s.now())
	s.events.RecordUser(record)
	return record, nil
}

func (s *Service) DisableUser(actor string, username string) (UserRecord, error) {
	record, err := s.store.DisableUser(username, s.now())
	if err != nil {
		_ = s.store.AppendAudit("disable_user", actor, username, "error:"+err.Error(), s.now())
		return UserRecord{}, err
	}
	_ = s.store.AppendAudit("disable_user", actor, username, "ok", s.now())
	s.events.RecordUser(record)
	return record, nil
}

func (s *Service) IssueUserToken(actor string, username string, description string) (IssuedToken, error) {
	token, err := s.store.IssueToken(username, description, s.now())
	if err != nil {
		_ = s.store.AppendAudit("issue_managed_token", actor, username, "error:"+err.Error(), s.now())
		return IssuedToken{}, err
	}
	_ = s.store.AppendAudit("issue_managed_token", actor, fmt.Sprintf("%s:%s", username, token.Record.TokenID), "ok", s.now())
	if user, ok := s.store.GetUser(username); ok {
		s.events.RecordUser(user)
	}
	s.events.RecordToken(EventKindTokenIssued, token.Record)
	return token, nil
}

func (s *Service) RevokeUserToken(actor string, tokenID string) (UserTokenRecord, error) {
	if token, ok := s.store.GetToken(tokenID); ok && token.Source == control.TokenSourceBootstrap {
		err := fmt.Errorf("bootstrap tokens are imported and cannot be revoked individually")
		_ = s.store.AppendAudit("reject_revoke_bootstrap_token", actor, tokenID, "error:"+err.Error(), s.now())
		return UserTokenRecord{}, err
	}
	record, err := s.store.RevokeToken(tokenID, s.now())
	if err != nil {
		_ = s.store.AppendAudit("revoke_managed_token", actor, tokenID, "error:"+err.Error(), s.now())
		return UserTokenRecord{}, err
	}
	action := "revoke_managed_token"
	if record.Source == control.TokenSourceLegacy {
		action = "revoke_legacy_token"
	}
	_ = s.store.AppendAudit(action, actor, tokenID, "ok", s.now())
	s.events.RecordToken(EventKindTokenRevoked, record)
	return record, nil
}

func (s *Service) GetUserEntrypoint(username string) ([]string, error) {
	return s.store.GetUserEntrypoint(username)
}

func (s *Service) SetUserEntrypoint(actor string, username string, argv []string) (UserRecord, error) {
	record, err := s.store.SetUserEntrypoint(username, argv, s.now())
	if err != nil {
		_ = s.store.AppendAudit("set_entrypoint", actor, username, "error:"+err.Error(), s.now())
		return UserRecord{}, err
	}
	_ = s.store.AppendAudit("set_entrypoint", actor, username, "ok", s.now())
	s.events.RecordUser(record)
	return record, nil
}
