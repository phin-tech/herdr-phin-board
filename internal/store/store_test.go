package store

import (
	"path/filepath"
	"testing"
)

func loadTemp(t *testing.T) *Board {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	b, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

func TestLoadCreatesDefaults(t *testing.T) {
	b := loadTemp(t)
	if len(b.Statuses) != 4 {
		t.Fatalf("want 4 default statuses, got %d", len(b.Statuses))
	}
	if b.DefaultStatusID() != "todo" {
		t.Fatalf("want todo as default, got %q", b.DefaultStatusID())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	b, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b.SetStatus("/tmp/project", "waiting", "project")
	b.SetNote("/tmp/project", "waiting on Dave")
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := again.Entries["/tmp/project"]
	if !ok {
		t.Fatal("entry did not survive the round trip")
	}
	if entry.Status != "waiting" || entry.Note != "waiting on Dave" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if _, err := filepath.Abs(filepath.Join(dir, "board.json")); err != nil {
		t.Fatal(err)
	}
}

// SetNote must not clobber a status, since notes and statuses are edited
// independently from the TUI.
func TestSetNotePreservesStatus(t *testing.T) {
	b := loadTemp(t)
	b.SetStatus("/tmp/a", "in_progress", "a")
	b.SetNote("/tmp/a", "blocked on vendor")

	if got := b.Entries["/tmp/a"].Status; got != "in_progress" {
		t.Fatalf("status was clobbered: %q", got)
	}
}

func TestRemoveStatusRehomesEntries(t *testing.T) {
	b := loadTemp(t)
	b.SetStatus("/tmp/a", "waiting", "a")

	if err := b.RemoveStatus("waiting"); err != nil {
		t.Fatal(err)
	}
	if got := b.Entries["/tmp/a"].Status; got != b.Statuses[0].ID {
		t.Fatalf("entry orphaned into %q, want %q", got, b.Statuses[0].ID)
	}
	if _, ok := b.StatusByID("waiting"); ok {
		t.Fatal("removed status is still present")
	}
	if b.IsCollapsed("waiting") {
		t.Fatal("collapsed list still references the removed status")
	}
}

func TestRemoveLastStatusFails(t *testing.T) {
	b := loadTemp(t)
	b.Statuses = b.Statuses[:1]
	if err := b.RemoveStatus(b.Statuses[0].ID); err == nil {
		t.Fatal("expected an error when removing the last status")
	}
}

func TestAddStatusUniqueIDs(t *testing.T) {
	b := loadTemp(t)
	first := b.AddStatus("Waiting on review", "170")
	second := b.AddStatus("Waiting on review", "39")

	if first.ID != "waiting_on_review" {
		t.Fatalf("unexpected id %q", first.ID)
	}
	if second.ID == first.ID {
		t.Fatalf("duplicate ids: %q", second.ID)
	}
}

func TestMoveStatusReorders(t *testing.T) {
	b := loadTemp(t)
	b.MoveStatus("todo", 1)
	if b.Statuses[0].ID != "in_progress" || b.Statuses[1].ID != "todo" {
		t.Fatalf("unexpected order: %v", b.Statuses)
	}

	// Moving past an edge is a no-op rather than a panic.
	b.MoveStatus("in_progress", -1)
	b.MoveStatus("in_progress", -1)
	if b.Statuses[0].ID != "in_progress" {
		t.Fatalf("unexpected order after edge move: %v", b.Statuses)
	}
}

func TestToggleCollapsed(t *testing.T) {
	b := loadTemp(t)
	if !b.IsCollapsed("done") {
		t.Fatal("done should start collapsed")
	}
	b.ToggleCollapsed("done")
	if b.IsCollapsed("done") {
		t.Fatal("toggle did not expand")
	}
	b.ToggleCollapsed("done")
	if !b.IsCollapsed("done") {
		t.Fatal("toggle did not re-collapse")
	}
}

func TestKeyCanonicalizes(t *testing.T) {
	if got := Key("/tmp/foo/../foo/"); got != Key("/tmp/foo") {
		t.Fatalf("Key did not canonicalize: %q", got)
	}
	if got := Key(""); got != "" {
		t.Fatalf("empty path should stay empty, got %q", got)
	}
}
