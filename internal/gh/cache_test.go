package gh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := LoadCache(dir)
	c.Put("/tmp/repo", PR{Number: 7, State: "OPEN", Checks: ChecksPass, Fetched: time.Now()})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	again := LoadCache(dir)
	pr, ok := again.Entries["/tmp/repo"]
	if !ok || pr.Number != 7 || pr.Checks != ChecksPass {
		t.Fatalf("cache did not survive: %+v", again.Entries)
	}
}

// A corrupt cache is derived data, so it must degrade to empty rather than
// failing the board.
func TestCorruptCacheLoadsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pr-cache.json"), []byte("{{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := LoadCache(dir)
	if len(c.Entries) != 0 {
		t.Fatalf("want an empty cache, got %+v", c.Entries)
	}
	// And it must still be usable.
	c.Put("/tmp/a", PR{Number: 1})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestStale(t *testing.T) {
	c := LoadCache(t.TempDir())
	if !c.Stale("/tmp/unknown") {
		t.Fatal("an unknown directory should be stale")
	}

	c.Put("/tmp/fresh", PR{Fetched: time.Now()})
	if c.Stale("/tmp/fresh") {
		t.Fatal("a just-fetched entry should not be stale")
	}

	c.Put("/tmp/old", PR{Fetched: time.Now().Add(-2 * TTL)})
	if !c.Stale("/tmp/old") {
		t.Fatal("an entry past the TTL should be stale")
	}
}

// A merged branch stops having a PR; the badge must not linger.
func TestForgetDropsAStalePR(t *testing.T) {
	c := LoadCache(t.TempDir())
	c.Put("/tmp/merged", PR{Number: 5})
	c.Forget("/tmp/merged")

	if _, ok := c.Entries["/tmp/merged"]; ok {
		t.Fatal("Forget left the entry behind")
	}
}

func TestPruneDropsSpacesNoLongerOnTheBoard(t *testing.T) {
	c := LoadCache(t.TempDir())
	c.Put("/tmp/keep", PR{Number: 1})
	c.Put("/tmp/gone", PR{Number: 2})

	c.Prune(map[string]bool{"/tmp/keep": true})

	if _, ok := c.Entries["/tmp/gone"]; ok {
		t.Fatal("Prune kept a directory that is no longer on the board")
	}
	if _, ok := c.Entries["/tmp/keep"]; !ok {
		t.Fatal("Prune dropped a directory that is still on the board")
	}
}
