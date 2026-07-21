package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-board/internal/herdr"
	"github.com/phin-tech/herdr-board/internal/store"
)

// newTestModel builds a model with no live socket. Commands returned by Update
// are never run here, so the nil client is never dialled.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, board)
	m.width, m.height = 100, 30
	return m
}

func send(t *testing.T, m *Model, msg tea.Msg) {
	t.Helper()
	if _, cmd := m.Update(msg); cmd != nil {
		_ = cmd // commands touch the socket; not exercised in unit tests
	}
}

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	panic("unmapped key " + s)
}

func liveWorkspaces() workspacesMsg {
	return workspacesMsg{
		{ID: "w1", Label: "api", Cwd: "/tmp/api", AgentStatus: "working"},
		{ID: "w2", Label: "web", Cwd: "/tmp/web", AgentStatus: "idle", Focused: true},
	}
}

// spaceRows returns the space rows currently visible, in order.
func spaceRows(m *Model) []*space {
	var out []*space
	for _, r := range m.rows {
		if r.kind == rowSpace {
			out = append(out, r.space)
		}
	}
	return out
}

func selectSpace(t *testing.T, m *Model, key string) {
	t.Helper()
	for i, r := range m.rows {
		if r.kind == rowSpace && r.space.Key == key {
			m.cursor = i
			return
		}
	}
	t.Fatalf("space %q is not visible", key)
}

func TestLiveWorkspacesLandInDefaultStatus(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())

	rows := spaceRows(m)
	if len(rows) != 2 {
		t.Fatalf("want 2 spaces, got %d", len(rows))
	}
	for _, sp := range rows {
		if sp.StatusID != "todo" {
			t.Fatalf("%s landed in %q, want todo", sp.Label, sp.StatusID)
		}
		if !sp.Live {
			t.Fatalf("%s should be live", sp.Label)
		}
		if sp.AgentStatus == "" {
			t.Fatalf("%s lost its agent hint", sp.Label)
		}
	}
}

// An idle workspace still gets a hint; idle must outrank the zero value.
func TestIdleAgentHintSurvives(t *testing.T) {
	m := newTestModel(t)
	send(t, m, workspacesMsg{{ID: "w1", Label: "web", Cwd: "/tmp/web", AgentStatus: "idle"}})

	rows := spaceRows(m)
	if len(rows) != 1 || rows[0].AgentStatus != "idle" {
		t.Fatalf("idle hint lost: %+v", rows)
	}
	if got := agentHint(rows[0]); got != "·idle" {
		t.Fatalf("agentHint = %q, want ·idle", got)
	}
}

func TestSetStatusByNumberMovesGroup(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")

	send(t, m, key("3")) // Waiting

	if got := m.board.Entries["/tmp/api"].Status; got != "waiting" {
		t.Fatalf("status is %q, want waiting", got)
	}
	// The cursor must follow the space into its new group, or repeated
	// keypresses would silently retag whatever row slid underneath.
	if sp := m.selected(); sp == nil || sp.Key != "/tmp/api" {
		t.Fatalf("cursor did not follow the space: %+v", sp)
	}
}

func TestStatusPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)

	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, board)
	m.width, m.height = 100, 30
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/web")
	send(t, m, key("2")) // In Progress

	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Entries["/tmp/web"].Status; got != "in_progress" {
		t.Fatalf("status did not persist, got %q", got)
	}
}

// A space whose workspace has closed is archived: hidden by default, visible
// under `a`, and still carrying its status.
func TestArchivedSpacesHiddenUntilToggled(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("3"))

	// The workspace goes away; the entry stays.
	send(t, m, workspacesMsg{{ID: "w2", Label: "web", Cwd: "/tmp/web"}})

	for _, sp := range spaceRows(m) {
		if sp.Key == "/tmp/api" {
			t.Fatal("archived space is visible with the archive hidden")
		}
	}

	send(t, m, key("a"))

	var found *space
	for _, sp := range spaceRows(m) {
		if sp.Key == "/tmp/api" {
			found = sp
		}
	}
	if found == nil {
		t.Fatal("archived space not shown after toggling the archive")
	}
	if found.Live {
		t.Fatal("archived space should not be marked live")
	}
	if found.StatusID != "waiting" {
		t.Fatalf("archived space lost its status: %q", found.StatusID)
	}
}

func TestCollapseHidesGroupMembers(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())

	m.cursor = 0 // the Todo header
	send(t, m, key(" "))

	if len(spaceRows(m)) != 0 {
		t.Fatal("collapsing Todo did not hide its spaces")
	}
	send(t, m, key(" "))
	if len(spaceRows(m)) != 2 {
		t.Fatal("expanding Todo did not restore its spaces")
	}
}

// Filtering must win over collapse, otherwise a match inside a collapsed group
// would be silently unreachable.
func TestFilterOverridesCollapse(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("4")) // Done, which starts collapsed

	if len(spaceRows(m)) != 1 {
		t.Fatalf("want only web visible, got %d rows", len(spaceRows(m)))
	}

	m.filter = "api"
	m.rebuild()

	rows := spaceRows(m)
	if len(rows) != 1 || rows[0].Key != "/tmp/api" {
		t.Fatalf("filter did not reach into the collapsed group: %+v", rows)
	}
}

func TestFilterMatchesNote(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/web")
	m.board.SetStatus("/tmp/web", "waiting", "web")
	m.board.SetNote("/tmp/web", "waiting on Dave for the API key")
	m.rebuild()

	m.filter = "dave"
	m.rebuild()

	rows := spaceRows(m)
	if len(rows) != 1 || rows[0].Key != "/tmp/web" {
		t.Fatalf("note text is not searchable: %+v", rows)
	}
}

func TestNoteEditingKeepsStatus(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("3")) // Waiting

	send(t, m, key("n"))
	if m.mode != modeNote {
		t.Fatal("n did not open the note editor")
	}
	m.input.SetValue("waiting on Dave")
	send(t, m, key("enter"))

	entry := m.board.Entries["/tmp/api"]
	if entry.Note != "waiting on Dave" {
		t.Fatalf("note not saved: %q", entry.Note)
	}
	if entry.Status != "waiting" {
		t.Fatalf("note edit clobbered the status: %q", entry.Status)
	}
}

// Adding a note to a space that has never been tagged must not lose it.
func TestNoteOnUntaggedSpaceCreatesEntry(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")

	send(t, m, key("n"))
	m.input.SetValue("check with ops")
	send(t, m, key("enter"))

	entry, ok := m.board.Entries["/tmp/api"]
	if !ok {
		t.Fatal("no entry was created")
	}
	if entry.Status != "todo" {
		t.Fatalf("entry has status %q, want the default", entry.Status)
	}
	if entry.Note != "check with ops" {
		t.Fatalf("note not saved: %q", entry.Note)
	}
}

func TestCustomStatusFlow(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())

	send(t, m, key("S"))
	if m.mode != modeManage {
		t.Fatal("S did not open status management")
	}
	send(t, m, key("a"))
	m.input.SetValue("Blocked on vendor")
	send(t, m, key("enter"))

	st, ok := m.board.StatusByID("blocked_on_vendor")
	if !ok {
		t.Fatalf("custom status not created: %+v", m.board.Statuses)
	}
	if st.Label != "Blocked on vendor" {
		t.Fatalf("unexpected label %q", st.Label)
	}

	send(t, m, key("esc"))
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("5")) // the new status is fifth

	if got := m.board.Entries["/tmp/api"].Status; got != "blocked_on_vendor" {
		t.Fatalf("could not assign the custom status, got %q", got)
	}
}

// Several workspaces open on one directory share a single row, because status
// belongs to the project rather than the window.
func TestDuplicateDirectoriesCollapseToOneRow(t *testing.T) {
	m := newTestModel(t)
	send(t, m, workspacesMsg{
		{ID: "w1", Label: "api", Cwd: "/tmp/api", AgentStatus: "idle"},
		{ID: "w2", Label: "api-2", Cwd: "/tmp/api", AgentStatus: "working"},
	})

	rows := spaceRows(m)
	if len(rows) != 1 {
		t.Fatalf("want 1 merged row, got %d", len(rows))
	}
	if len(rows[0].WorkspaceIDs) != 2 {
		t.Fatalf("merged row lost a workspace: %+v", rows[0].WorkspaceIDs)
	}
	// The busiest agent state wins the display hint.
	if rows[0].AgentStatus != "working" {
		t.Fatalf("agent hint is %q, want working", rows[0].AgentStatus)
	}
}

func TestForgetRemovesEntry(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("3"))
	send(t, m, key("x"))

	if _, ok := m.board.Entries["/tmp/api"]; ok {
		t.Fatal("entry was not forgotten")
	}
	// Still live, so it comes back under the default status.
	selectSpace(t, m, "/tmp/api")
	if sp := m.selected(); sp.StatusID != "todo" {
		t.Fatalf("forgotten live space is in %q, want todo", sp.StatusID)
	}
}

func TestViewRenders(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/web")
	send(t, m, key("3"))
	m.board.SetNote("/tmp/web", "waiting on Dave")
	m.rebuild()

	out := m.View()
	for _, want := range []string{"Board", "Waiting", "web", "waiting on Dave"} {
		if !strings.Contains(out, want) {
			t.Fatalf("view is missing %q:\n%s", want, out)
		}
	}
	// The agent hint is present but must never become a group header.
	if strings.Contains(out, "▾ working") {
		t.Fatal("agent state leaked into the grouping")
	}
}

func TestEmptyBoardRenders(t *testing.T) {
	m := newTestModel(t)
	send(t, m, workspacesMsg{})
	if out := m.View(); !strings.Contains(out, "Board") {
		t.Fatalf("empty board did not render:\n%s", out)
	}
}

var _ = herdr.MetadataSource
