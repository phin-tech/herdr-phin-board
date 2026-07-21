package gh

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The cache exists so the board can paint PR state the instant it opens and
// refresh underneath, rather than showing empty columns while gh runs. It is
// derived data, so it lives in its own file: losing it costs one refetch,
// whereas board.json is the user's own work.

// TTL is how long a cached PR is served before a refetch is worthwhile.
const TTL = 5 * time.Minute

// Cache maps a canonical directory to its last known PR.
type Cache struct {
	Entries map[string]PR `json:"entries"`

	path string
}

// LoadCache reads the cache, returning an empty one when absent or unreadable.
// A corrupt cache is not an error worth surfacing; it is rebuilt on the next
// fetch.
func LoadCache(dir string) *Cache {
	c := &Cache{Entries: map[string]PR{}, path: filepath.Join(dir, "pr-cache.json")}

	data, err := os.ReadFile(c.path)
	if err != nil {
		return c
	}
	var loaded Cache
	if err := json.Unmarshal(data, &loaded); err != nil || loaded.Entries == nil {
		return c
	}
	c.Entries = loaded.Entries
	return c
}

// Save writes the cache atomically. Failures are returned but callers may
// reasonably ignore them: a cache that fails to persist still works in memory.
func (c *Cache) Save() error {
	if c.path == "" {
		return errors.New("cache has no path")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".pr-cache-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, c.path)
}

// Stale reports whether a directory needs refetching.
func (c *Cache) Stale(dir string) bool {
	pr, ok := c.Entries[dir]
	if !ok {
		return true
	}
	return time.Since(pr.Fetched) > TTL
}

// Put records a fetch result.
func (c *Cache) Put(dir string, pr PR) {
	c.Entries[dir] = pr
}

// Forget drops a directory, used when a fetch finds no PR where one was
// cached -- a merged branch, say. Without this a stale badge would linger.
func (c *Cache) Forget(dir string) {
	delete(c.Entries, dir)
}

// Prune drops directories that are no longer on the board.
func (c *Cache) Prune(keep map[string]bool) {
	for dir := range c.Entries {
		if !keep[dir] {
			delete(c.Entries, dir)
		}
	}
}
