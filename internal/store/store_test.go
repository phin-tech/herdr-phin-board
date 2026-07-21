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
	if len(b.Statuses) != 5 {
		t.Fatalf("want 5 default statuses, got %d", len(b.Statuses))
	}
	// Triage must lead: the first status is where untouched spaces land, so it
	// has to mean "not looked at yet" rather than being a real choice. Todo
	// sits after it, and a Todo badge then means someone decided.
	if b.DefaultStatusID() != "triage" {
		t.Fatalf("want triage as default, got %q", b.DefaultStatusID())
	}
	if b.Statuses[1].ID != "todo" {
		t.Fatalf("want todo after triage, got %q", b.Statuses[1].ID)
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
	b.MoveStatus("triage", 1)
	if b.Statuses[0].ID != "todo" || b.Statuses[1].ID != "triage" {
		t.Fatalf("unexpected order: %v", b.Statuses)
	}
	// Reordering is presentation only; which status is the default is an
	// explicit choice, covered by TestDefaultIsExplicitNotPositional.

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

// The plugin and a hand-run binary must agree on one file, or a status set in
// one would be invisible to the other.
func TestPathMatchesTheHerdrStateLayout(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/state")

	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/state/herdr/plugins/" + PluginID + "/board.json"
	if got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestInjectedStateDirWins(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "/injected")
	t.Setenv("XDG_STATE_HOME", "/tmp/state")

	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/injected/board.json" {
		t.Fatalf("Path() = %q, want the injected dir to win", got)
	}
}

// The default is an explicit choice, so reordering must not move it.
func TestDefaultIsExplicitNotPositional(t *testing.T) {
	b := loadTemp(t)
	if b.DefaultStatusID() != "triage" {
		t.Fatalf("new board default is %q, want triage", b.DefaultStatusID())
	}

	b.MoveStatus("triage", 3)
	if got := b.DefaultStatusID(); got != "triage" {
		t.Fatalf("default moved with the order: %q", got)
	}
}

func TestSetDefaultStatus(t *testing.T) {
	b := loadTemp(t)
	b.SetDefaultStatus("waiting")
	if got := b.DefaultStatusID(); got != "waiting" {
		t.Fatalf("default is %q, want waiting", got)
	}
	if !b.IsDefaultStatus("waiting") || b.IsDefaultStatus("triage") {
		t.Fatal("IsDefaultStatus disagrees with DefaultStatusID")
	}

	// An unknown id clears it rather than leaving a dangling name.
	b.SetDefaultStatus("nonsense")
	if b.Default != "" {
		t.Fatalf("Default is %q, want it cleared", b.Default)
	}
	if got := b.DefaultStatusID(); got != b.Statuses[0].ID {
		t.Fatalf("cleared default did not fall back to first: %q", got)
	}
}

// A board written before this field existed has no default named, and must
// keep behaving as it did: first status wins.
func TestBoardWithoutDefaultFallsBackToFirst(t *testing.T) {
	b := loadTemp(t)
	b.Default = ""
	if got := b.DefaultStatusID(); got != b.Statuses[0].ID {
		t.Fatalf("legacy board default is %q, want %q", got, b.Statuses[0].ID)
	}
}

func TestDeletingTheDefaultReassignsIt(t *testing.T) {
	b := loadTemp(t)
	b.SetDefaultStatus("waiting")
	if err := b.RemoveStatus("waiting"); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.StatusByID(b.Default); !ok {
		t.Fatalf("Default points at a deleted status: %q", b.Default)
	}
	if got := b.DefaultStatusID(); got != b.Statuses[0].ID {
		t.Fatalf("default is %q, want the first status", got)
	}
}

func TestDefaultSurvivesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	b, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b.SetDefaultStatus("in_progress")
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := again.DefaultStatusID(); got != "in_progress" {
		t.Fatalf("default did not persist: %q", got)
	}
}
