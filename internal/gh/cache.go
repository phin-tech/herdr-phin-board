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

const (
	// TTL is how long a cached PR is served before a refetch is worthwhile.
	TTL = 5 * time.Minute
	// NegativeTTL applies to directories checked and found to have no PR.
	// It is longer because a repo gaining a PR is far less time-critical than
	// an existing PR's checks changing -- and because most spaces never have
	// one, so this is what stops the board re-asking about all of them.
	NegativeTTL = 30 * time.Minute
)

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
	ttl := TTL
	if !pr.Found() {
		ttl = NegativeTTL
	}
	return time.Since(pr.Fetched) > ttl
}

// Put records a fetch result.
func (c *Cache) Put(dir string, pr PR) {
	c.Entries[dir] = pr
}

// PutAbsent records that a directory was checked and has no PR.
//
// This is the difference between "no PR" and "never asked". Deleting the entry
// instead would leave it permanently stale, and every board refresh -- which
// happens on every Herdr workspace event -- would spawn gh again for every
// space that will never have a PR.
func (c *Cache) PutAbsent(dir string) {
	c.Entries[dir] = PR{Fetched: time.Now().UTC()}
}

// Prune drops directories that are no longer on the board.
func (c *Cache) Prune(keep map[string]bool) {
	for dir := range c.Entries {
		if !keep[dir] {
			delete(c.Entries, dir)
		}
	}
}
