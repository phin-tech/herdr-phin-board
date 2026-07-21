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

	// Number matters: a zero-numbered record is a cached absence, which is
	// held for the longer NegativeTTL instead.
	c.Put("/tmp/old", PR{Number: 3, Fetched: time.Now().Add(-2 * TTL)})
	if !c.Stale("/tmp/old") {
		t.Fatal("an entry past the TTL should be stale")
	}
}

// A merged branch stops having a PR; the badge must not linger, but the fact
// that we looked must be remembered.
func TestPutAbsentReplacesAStalePR(t *testing.T) {
	c := LoadCache(t.TempDir())
	c.Put("/tmp/merged", PR{Number: 5, Fetched: time.Now()})
	c.PutAbsent("/tmp/merged")

	pr, ok := c.Entries["/tmp/merged"]
	if !ok {
		t.Fatal("PutAbsent dropped the record, so the board would re-ask forever")
	}
	if pr.Found() {
		t.Fatalf("the old PR is still being reported: %+v", pr)
	}
}

// This is the bug that made the board feel frozen: an absence that is not
// recorded is permanently stale, so every workspace event respawned gh for
// every space that will never have a PR.
func TestAbsenceIsNotImmediatelyStale(t *testing.T) {
	c := LoadCache(t.TempDir())
	c.PutAbsent("/tmp/no-pr")

	if c.Stale("/tmp/no-pr") {
		t.Fatal("a just-checked absence is stale, so it would be refetched at once")
	}

	// It does expire eventually, so a repo that gains a PR is picked up.
	c.Entries["/tmp/no-pr"] = PR{Fetched: time.Now().Add(-NegativeTTL - time.Minute)}
	if !c.Stale("/tmp/no-pr") {
		t.Fatal("an absence never expires, so a new PR would never be noticed")
	}
}

// Absences are held longer than PRs: a repo gaining a PR is less urgent than
// an open PR's checks changing.
func TestAbsencesOutliveFoundPRs(t *testing.T) {
	c := LoadCache(t.TempDir())
	past := time.Now().Add(-TTL - time.Minute)

	c.Entries["/tmp/found"] = PR{Number: 1, Fetched: past}
	c.Entries["/tmp/absent"] = PR{Fetched: past}

	if !c.Stale("/tmp/found") {
		t.Fatal("a found PR past its TTL should be stale")
	}
	if c.Stale("/tmp/absent") {
		t.Fatal("an absence should still be fresh at the same age")
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
