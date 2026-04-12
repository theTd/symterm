package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type persistentHashRecord struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	MTimeNanos int64  `json:"mtime_nanos"`
	Hash       string `json:"hash"`
}

type PersistentHashCache struct {
	mu      sync.Mutex
	path    string
	records map[string]persistentHashRecord
	dirty   bool
	hits    uint64
	misses  uint64
}

func loadPersistentHashCache(workspaceInstanceID string) (*PersistentHashCache, error) {
	if workspaceInstanceID == "" {
		return &PersistentHashCache{records: make(map[string]persistentHashRecord)}, nil
	}
	cachePath, err := persistentHashCachePath(workspaceInstanceID)
	if err != nil {
		return nil, err
	}
	cache := &PersistentHashCache{
		path:    cachePath,
		records: make(map[string]persistentHashRecord),
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return cache, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return cache, nil
	}
	var records []persistentHashRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	for _, record := range records {
		cache.records[record.Path] = record
	}
	return cache, nil
}

func (c *PersistentHashCache) Lookup(file LocalWorkspaceFile) (string, bool) {
	if c == nil {
		return "", false
	}
	key := file.HashCacheKey()
	c.mu.Lock()
	defer c.mu.Unlock()

	record, ok := c.records[file.Path]
	if !ok || record.Size != key.Size || record.Mode != key.Mode || record.MTimeNanos != key.MTimeNanos || record.Hash == "" {
		c.misses++
		return "", false
	}
	c.hits++
	return record.Hash, true
}

func (c *PersistentHashCache) Store(file LocalWorkspaceFile) {
	if c == nil || file.Path == "" || file.Entry.ContentHash == "" {
		return
	}
	key := file.HashCacheKey()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records[file.Path] = persistentHashRecord{
		Path:       file.Path,
		Size:       key.Size,
		Mode:       key.Mode,
		MTimeNanos: key.MTimeNanos,
		Hash:       file.Entry.ContentHash,
	}
	c.dirty = true
}

func (c *PersistentHashCache) Save() error {
	if c == nil || c.path == "" {
		return nil
	}
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	records := make([]persistentHashRecord, 0, len(c.records))
	for _, record := range c.records {
		records = append(records, record)
	}
	c.dirty = false
	c.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	tempPath := c.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tempPath, c.path)
}

func (c *PersistentHashCache) Stats() (uint64, uint64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

func persistentHashCachePath(workspaceInstanceID string) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	sum := sha256.Sum256([]byte(workspaceInstanceID))
	return filepath.Join(cacheRoot, "symterm", "hash-cache", hex.EncodeToString(sum[:])+".json"), nil
}
