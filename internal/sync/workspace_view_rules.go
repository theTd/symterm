package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
)

const WorkspaceIgnoreFileName = ".stignore"

type WorkspaceViewResolver struct {
	root string

	mu          stdsync.Mutex
	fingerprint string
	rules       *WorkspaceViewRules
}

type WorkspaceViewRules struct {
	root        string
	fingerprint string
	rules       []workspaceViewRule
}

type workspaceViewRule struct {
	include      bool
	pattern      string
	segments     []string
	staticPrefix string
}

func NewWorkspaceViewResolver(root string) *WorkspaceViewResolver {
	return &WorkspaceViewResolver{root: root}
}

func (r *WorkspaceViewResolver) Rules() (*WorkspaceViewRules, error) {
	if r == nil {
		return LoadWorkspaceViewRules("")
	}
	fingerprint, data, err := workspaceRulesSource(strings.TrimSpace(r.root))
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.rules != nil && r.fingerprint == fingerprint {
		return r.rules, nil
	}
	rules, err := parseWorkspaceViewRules(strings.TrimSpace(r.root), fingerprint, data)
	if err != nil {
		return nil, err
	}
	r.fingerprint = fingerprint
	r.rules = rules
	return rules, nil
}

func LoadWorkspaceViewRules(root string) (*WorkspaceViewRules, error) {
	root = strings.TrimSpace(root)
	fingerprint, data, err := workspaceRulesSource(root)
	if err != nil {
		return nil, err
	}
	return parseWorkspaceViewRules(root, fingerprint, data)
}

func (r *WorkspaceViewRules) Fingerprint() string {
	if r == nil {
		return ""
	}
	return r.fingerprint
}

func (r *WorkspaceViewRules) AllowsPath(path string, isDir bool) bool {
	allowed, _ := r.evaluate(path, isDir)
	return allowed
}

func (r *WorkspaceViewRules) CanPruneDirectory(path string) bool {
	path = normalizeWorkspaceViewPath(path)
	if path == "" {
		return false
	}
	allowed, lastMatch := r.evaluate(path, true)
	if allowed {
		return false
	}
	for idx := lastMatch + 1; idx < len(r.rules); idx++ {
		rule := r.rules[idx]
		if !rule.include {
			continue
		}
		if rule.mightMatchSubtree(path) {
			return false
		}
	}
	return true
}

func PathVisibleOnDisk(root string, rules *WorkspaceViewRules, workspacePath string) (bool, error) {
	root = strings.TrimSpace(root)
	workspacePath = normalizeWorkspaceViewPath(workspacePath)
	if workspacePath == "" {
		return true, nil
	}
	target := filepath.Join(root, filepath.FromSlash(workspacePath))
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return visibleExistingWorkspacePath(root, rules, workspacePath, info.IsDir())
}

func VisibleDirectoryNamesOnDisk(root string, rules *WorkspaceViewRules, workspacePath string) ([]string, error) {
	root = strings.TrimSpace(root)
	workspacePath = normalizeWorkspaceViewPath(workspacePath)
	target := root
	if workspacePath != "" {
		target = filepath.Join(root, filepath.FromSlash(workspacePath))
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		childPath := entry.Name()
		if workspacePath != "" {
			childPath = workspacePath + "/" + entry.Name()
		}
		visible, err := visibleExistingWorkspacePath(root, rules, childPath, entry.IsDir())
		if err != nil {
			return nil, err
		}
		if visible {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func visibleExistingWorkspacePath(root string, rules *WorkspaceViewRules, workspacePath string, isDir bool) (bool, error) {
	workspacePath = normalizeWorkspaceViewPath(workspacePath)
	if workspacePath == "" {
		return true, nil
	}
	if !isDir {
		return rules.AllowsPath(workspacePath, false), nil
	}
	if rules.AllowsPath(workspacePath, true) {
		return true, nil
	}
	if rules.CanPruneDirectory(workspacePath) {
		return false, nil
	}

	target := filepath.Join(strings.TrimSpace(root), filepath.FromSlash(workspacePath))
	entries, err := os.ReadDir(target)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		childPath := workspacePath + "/" + entry.Name()
		visible, err := visibleExistingWorkspacePath(root, rules, childPath, entry.IsDir())
		if err != nil {
			return false, err
		}
		if visible {
			return true, nil
		}
	}
	return false, nil
}

func workspaceRulesSource(root string) (string, []byte, error) {
	rulesPath := filepath.Join(strings.TrimSpace(root), WorkspaceIgnoreFileName)
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", nil, nil
		}
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, nil
}

func parseWorkspaceViewRules(root string, fingerprint string, data []byte) (*WorkspaceViewRules, error) {
	rules := &WorkspaceViewRules{
		root:        root,
		fingerprint: fingerprint,
	}
	if len(data) == 0 {
		return rules, nil
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		include := false
		if strings.HasPrefix(line, "!") {
			include = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		line = normalizeWorkspaceViewPattern(line)
		if line == "" {
			continue
		}

		segments := splitWorkspaceViewSegments(line)
		rules.rules = append(rules.rules, workspaceViewRule{
			include:      include,
			pattern:      line,
			segments:     segments,
			staticPrefix: workspaceViewStaticPrefix(segments),
		})
	}
	return rules, nil
}

func (r *WorkspaceViewRules) evaluate(path string, isDir bool) (bool, int) {
	path = normalizeWorkspaceViewPath(path)
	if path == "" {
		return true, -1
	}
	if isWorkspaceIgnorePath(path) {
		return true, -1
	}

	allowed := true
	lastMatch := -1
	for idx, rule := range r.rules {
		if !rule.matches(path, isDir) {
			continue
		}
		allowed = rule.include
		lastMatch = idx
	}
	return allowed, lastMatch
}

func (r workspaceViewRule) matches(path string, _ bool) bool {
	pathSegments := splitWorkspaceViewSegments(path)
	return matchWorkspaceViewSegments(r.segments, pathSegments)
}

func (r workspaceViewRule) mightMatchSubtree(dir string) bool {
	dir = normalizeWorkspaceViewPath(dir)
	if dir == "" {
		return true
	}
	if r.staticPrefix == "" {
		return true
	}
	if r.staticPrefix == dir || strings.HasPrefix(r.staticPrefix, dir+"/") {
		return true
	}
	if strings.HasPrefix(dir, r.staticPrefix+"/") {
		return true
	}
	return r.matches(dir, true)
}

func matchWorkspaceViewSegments(pattern []string, path []string) bool {
	type matchState struct {
		patternIndex int
		pathIndex    int
	}

	memo := make(map[matchState]bool)
	var match func(patternIndex int, pathIndex int) bool
	match = func(patternIndex int, pathIndex int) bool {
		state := matchState{patternIndex: patternIndex, pathIndex: pathIndex}
		if cached, ok := memo[state]; ok {
			return cached
		}
		defer func() {
			memo[state] = false
		}()

		if patternIndex == len(pattern) {
			return pathIndex == len(path)
		}
		if pattern[patternIndex] == "**" {
			if match(patternIndex+1, pathIndex) {
				memo[state] = true
				return true
			}
			if pathIndex < len(path) && match(patternIndex, pathIndex+1) {
				memo[state] = true
				return true
			}
			return false
		}
		if pathIndex >= len(path) {
			return false
		}
		matched, err := filepath.Match(pattern[patternIndex], path[pathIndex])
		if err != nil || !matched {
			return false
		}
		if match(patternIndex+1, pathIndex+1) {
			memo[state] = true
			return true
		}
		return false
	}
	return match(0, 0)
}

func workspaceViewStaticPrefix(segments []string) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "**" || strings.ContainsAny(segment, "*?[") {
			break
		}
		parts = append(parts, segment)
	}
	return strings.Join(parts, "/")
}

func normalizeWorkspaceViewPattern(pattern string) string {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	for strings.HasPrefix(pattern, "./") {
		pattern = strings.TrimPrefix(pattern, "./")
	}
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "." {
		return ""
	}
	if strings.HasSuffix(pattern, "/") {
		pattern = strings.TrimSuffix(pattern, "/")
		if pattern == "" {
			return ""
		}
		pattern += "/**"
	}
	return pattern
}

func normalizeWorkspaceViewPath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	for strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}
	path = strings.TrimPrefix(path, "/")
	if path == "." {
		return ""
	}
	return path
}

func splitWorkspaceViewSegments(path string) []string {
	path = normalizeWorkspaceViewPath(path)
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func isWorkspaceIgnorePath(path string) bool {
	return normalizeWorkspaceViewPath(path) == WorkspaceIgnoreFileName
}
