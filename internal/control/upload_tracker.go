package control

import (
	"strings"
	"sync"

	"symterm/internal/proto"
)

type UploadTracker struct {
	mu      sync.Mutex
	uploads map[string]string
}

func newUploadTracker() *UploadTracker {
	return &UploadTracker{uploads: make(map[string]string)}
}

func (t *UploadTracker) Begin(projectKey proto.ProjectKey, fileID string, path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.uploads[t.key(projectKey, fileID)] = path
}

func (t *UploadTracker) Commit(projectKey proto.ProjectKey, fileID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := t.key(projectKey, fileID)
	path := t.uploads[key]
	delete(t.uploads, key)
	return path
}

func (t *UploadTracker) Abort(projectKey proto.ProjectKey, fileID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.uploads, t.key(projectKey, fileID))
}

func (t *UploadTracker) CleanupProject(projectKey proto.ProjectKey) {
	t.mu.Lock()
	defer t.mu.Unlock()

	prefix := projectKey.String() + ":"
	for key := range t.uploads {
		if strings.HasPrefix(key, prefix) {
			delete(t.uploads, key)
		}
	}
}

func (t *UploadTracker) key(projectKey proto.ProjectKey, fileID string) string {
	return projectKey.String() + ":" + fileID
}
