package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/herdr"
	"github.com/phin-tech/herdr-phin-board/internal/store"
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

// selectSpace moves the cursor onto a space, using whichever cursor the active
// layout actually reads.
func selectSpace(t *testing.T, m *Model, key string) {
	t.Helper()
	if m.layout == layoutKanban {
		for col, st := range m.board.Statuses {
			for i, sp := range m.groups[st.ID] {
				if sp.Key == key {
					m.col, m.rowInCol = col, i
					return
				}
			}
		}
		t.Fatalf("card %q is not on the board", key)
	}
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

// Opening the board must not leave the cursor on a group header, where n, s
// and 1-9 would all silently do nothing.
func TestCursorStartsOnASpace(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())

	if m.selected() == nil {
		t.Fatalf("cursor landed on a header (row %d of %d)", m.cursor, len(m.rows))
	}
}

func TestNoteWorksImmediatelyAfterOpen(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())

	send(t, m, key("n"))
	if m.mode != modeNote {
		t.Fatal("n did not open the note editor on a freshly opened board")
	}
	m.input.SetValue("waiting on procurement")
	send(t, m, key("enter"))

	sp := m.selected()
	if got := m.board.Entries[sp.Key].Note; got != "waiting on procurement" {
		t.Fatalf("note not saved: %q", got)
	}
}

func TestHeaderRowExplainsItself(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	m.cursor = 0 // a group header

	send(t, m, key("n"))
	if m.mode == modeNote {
		t.Fatal("note editor opened on a header row")
	}
	if m.status == "" {
		t.Fatal("header row gave no feedback")
	}
}

// An empty board has no space to land on; the cursor logic must not wedge.
func TestCursorHandlesEmptyBoard(t *testing.T) {
	m := newTestModel(t)
	send(t, m, workspacesMsg{})
	if m.selected() != nil {
		t.Fatal("empty board should have nothing selected")
	}
	send(t, m, key("n"))
	send(t, m, key("1"))
}

func threeInTodo(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	send(t, m, workspacesMsg{
		{ID: "w1", Label: "alpha", Cwd: "/tmp/alpha", AgentStatus: "idle"},
		{ID: "w2", Label: "beta", Cwd: "/tmp/beta", AgentStatus: "idle"},
		{ID: "w3", Label: "gamma", Cwd: "/tmp/gamma", AgentStatus: "idle"},
	})
	return m
}

func labelsIn(m *Model, statusID string) []string {
	var out []string
	for _, sp := range m.groups[statusID] {
		out = append(out, sp.Label)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGrabReordersWithinGroup(t *testing.T) {
	m := threeInTodo(t)
	before := labelsIn(m, "todo")
	if len(before) != 3 {
		t.Fatalf("setup: want 3 spaces, got %v", before)
	}

	selectSpace(t, m, "/tmp/"+strings.ToLower(before[0]))
	send(t, m, key("v"))
	if m.grabbed == "" {
		t.Fatal("v did not grab the row")
	}
	send(t, m, key("j"))

	want := []string{before[1], before[0], before[2]}
	if got := labelsIn(m, "todo"); !equal(got, want) {
		t.Fatalf("order is %v, want %v", got, want)
	}
	// The cursor stays on the row being moved, or a second j would move a
	// different one.
	if sp := m.selected(); sp == nil || sp.Label != before[0] {
		t.Fatalf("cursor left the grabbed row: %+v", sp)
	}
}

// The reordering has to survive a reload, otherwise the row springs back.
func TestManualOrderPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, board)
	m.width, m.height = 100, 30
	send(t, m, workspacesMsg{
		{ID: "w1", Label: "alpha", Cwd: "/tmp/alpha"},
		{ID: "w2", Label: "beta", Cwd: "/tmp/beta"},
	})
	first := labelsIn(m, "todo")[0]

	selectSpace(t, m, "/tmp/"+first)
	send(t, m, key("v"))
	send(t, m, key("j"))

	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	m2 := New(nil, reloaded)
	m2.width, m2.height = 100, 30
	send(t, m2, workspacesMsg{
		{ID: "w1", Label: "alpha", Cwd: "/tmp/alpha"},
		{ID: "w2", Label: "beta", Cwd: "/tmp/beta"},
	})
	if got := labelsIn(m2, "todo"); got[len(got)-1] != first {
		t.Fatalf("manual order did not survive reload: %v", got)
	}
}

// Moving off the bottom of a group carries the row into the next status.
func TestGrabCrossesBoundaryDown(t *testing.T) {
	m := threeInTodo(t)
	last := labelsIn(m, "todo")[2]
	selectSpace(t, m, "/tmp/"+last)

	send(t, m, key("v"))
	send(t, m, key("j"))

	if got := m.board.Entries["/tmp/"+last].Status; got != "in_progress" {
		t.Fatalf("status is %q, want in_progress", got)
	}
	// It enters at the top, since it arrived from above.
	if got := labelsIn(m, "in_progress"); len(got) != 1 || got[0] != last {
		t.Fatalf("in_progress is %v, want [%s]", got, last)
	}
	if m.grabbed == "" {
		t.Fatal("the row was dropped on crossing a boundary")
	}
}

func TestGrabCrossesBoundaryUp(t *testing.T) {
	m := threeInTodo(t)
	first := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+first)
	// Park it in Waiting, then walk it back up into In Progress.
	send(t, m, key("3"))
	send(t, m, key("v"))
	send(t, m, key("k"))

	if got := m.board.Entries["/tmp/"+first].Status; got != "in_progress" {
		t.Fatalf("status is %q, want in_progress", got)
	}
}

// A row must never vanish because it was moved into a folded group.
func TestGrabIntoCollapsedGroupExpandsIt(t *testing.T) {
	m := threeInTodo(t)
	if !m.board.IsCollapsed("done") {
		t.Fatal("setup: done should start collapsed")
	}
	target := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+target)
	send(t, m, key("3")) // Waiting, which sits directly above Done
	selectSpace(t, m, "/tmp/"+target)

	// Grab it and walk it down across the boundary into collapsed Done.
	send(t, m, key("v"))
	send(t, m, key("j"))

	if got := m.board.Entries["/tmp/"+target].Status; got != "done" {
		t.Fatalf("status is %q, want done", got)
	}
	if m.board.IsCollapsed("done") {
		t.Fatal("target group stayed collapsed, hiding the moved row")
	}
	if m.selected() == nil {
		t.Fatal("the moved row is not visible")
	}
}

func TestGrabStopsAtTheEnds(t *testing.T) {
	m := threeInTodo(t)
	first := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+first)
	send(t, m, key("v"))
	send(t, m, key("k")) // already in the first status, at the top

	if got := m.board.Entries["/tmp/"+first]; got.Status != "" && got.Status != "todo" {
		t.Fatalf("row escaped off the top into %q", got.Status)
	}
	if len(labelsIn(m, "todo")) != 3 {
		t.Fatalf("todo lost a row: %v", labelsIn(m, "todo"))
	}
}

func TestGrabBlockedWhileFiltering(t *testing.T) {
	m := threeInTodo(t)
	m.filter = "alpha"
	m.rebuild()

	send(t, m, key("v"))
	if m.grabbed != "" {
		t.Fatal("grabbing while filtered would reorder against hidden rows")
	}
	if m.status == "" {
		t.Fatal("no explanation given")
	}
}

func TestGrabToggleAndDrop(t *testing.T) {
	m := threeInTodo(t)
	selectSpace(t, m, "/tmp/"+labelsIn(m, "todo")[0])

	send(t, m, key("v"))
	send(t, m, key("v"))
	if m.grabbed != "" {
		t.Fatal("v did not drop the row")
	}
	send(t, m, key("v"))
	send(t, m, key("enter"))
	if m.grabbed != "" {
		t.Fatal("enter did not drop the row")
	}
}

// Numbers must keep working, in grab mode too.
func TestNumberKeySendsToStatusWhileGrabbed(t *testing.T) {
	m := threeInTodo(t)
	target := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+target)

	send(t, m, key("v"))
	send(t, m, key("3"))

	if got := m.board.Entries["/tmp/"+target].Status; got != "waiting" {
		t.Fatalf("status is %q, want waiting", got)
	}
}

// The footer must name the statuses, not just say "1-9".
func TestFooterListsNumberedStatuses(t *testing.T) {
	m := threeInTodo(t)
	out := m.View()
	for i, st := range m.board.Statuses {
		want := fmt.Sprintf("%d %s", i+1, st.Label)
		if !strings.Contains(out, want) {
			t.Fatalf("footer is missing %q:\n%s", want, out)
		}
	}
}

func kanbanBoard(t *testing.T) *Model {
	t.Helper()
	m := threeInTodo(t)
	m.toggleLayout()
	if m.layout != layoutKanban {
		t.Fatal("K did not switch to kanban")
	}
	return m
}

func TestLayoutTogglePersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, board)
	m.width, m.height = 100, 30
	send(t, m, liveWorkspaces())
	send(t, m, key("K"))

	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Layout != "kanban" {
		t.Fatalf("layout not persisted: %q", reloaded.Layout)
	}
	if New(nil, reloaded).layout != layoutKanban {
		t.Fatal("board did not reopen in kanban")
	}
}

// Toggling views must keep you looking at the same space.
func TestLayoutToggleKeepsSelection(t *testing.T) {
	m := threeInTodo(t)
	target := labelsIn(m, "todo")[1]
	selectSpace(t, m, "/tmp/"+target)
	send(t, m, key("3")) // Waiting, so the column is not the first one

	send(t, m, key("K"))
	if sp := m.selected(); sp == nil || sp.Label != target {
		t.Fatalf("kanban lost the selection: %+v", sp)
	}
	send(t, m, key("K"))
	if sp := m.selected(); sp == nil || sp.Label != target {
		t.Fatalf("list lost the selection: %+v", sp)
	}
}

func TestKanbanColumnNavigation(t *testing.T) {
	m := kanbanBoard(t)
	target := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+target)
	send(t, m, key("3")) // Waiting is column index 2

	send(t, m, key("h"))
	if m.col != 1 {
		t.Fatalf("h moved to column %d, want 1", m.col)
	}
	send(t, m, key("l"))
	if m.col != 2 {
		t.Fatalf("l moved to column %d, want 2", m.col)
	}
	// Columns stop at the edges rather than wrapping.
	for i := 0; i < 10; i++ {
		send(t, m, key("l"))
	}
	if m.col != len(m.board.Statuses)-1 {
		t.Fatalf("column cursor escaped: %d", m.col)
	}
}

func TestKanbanGrabAcrossColumnsRetags(t *testing.T) {
	m := kanbanBoard(t)
	target := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+target)

	send(t, m, key("v"))
	send(t, m, key("l"))

	if got := m.board.Entries["/tmp/"+target].Status; got != "in_progress" {
		t.Fatalf("status is %q, want in_progress", got)
	}
	// The card lands at the top of its new column and stays selected.
	if got := labelsIn(m, "in_progress"); len(got) == 0 || got[0] != target {
		t.Fatalf("card did not land at the top: %v", got)
	}
	if sp := m.selected(); sp == nil || sp.Label != target {
		t.Fatalf("selection did not follow the card: %+v", sp)
	}

	send(t, m, key("h"))
	if got := m.board.Entries["/tmp/"+target].Status; got != "todo" {
		t.Fatalf("h did not move it back: %q", got)
	}
}

// Vertical movement in kanban reorders within a column and must never retag,
// since the columns are the statuses.
func TestKanbanVerticalMoveNeverRetags(t *testing.T) {
	m := kanbanBoard(t)
	before := labelsIn(m, "todo")
	selectSpace(t, m, "/tmp/"+before[2]) // the last card in the column

	send(t, m, key("v"))
	send(t, m, key("j")) // off the bottom

	if got := m.board.Entries["/tmp/"+before[2]].Status; got != "" && got != "todo" {
		t.Fatalf("vertical move retagged the card to %q", got)
	}
	if got := labelsIn(m, "todo"); len(got) != 3 {
		t.Fatalf("todo lost a card: %v", got)
	}

	// Moving up still reorders inside the column.
	send(t, m, key("k"))
	want := []string{before[0], before[2], before[1]}
	if got := labelsIn(m, "todo"); !equal(got, want) {
		t.Fatalf("order is %v, want %v", got, want)
	}
}

func TestKanbanNumberKeysWork(t *testing.T) {
	m := kanbanBoard(t)
	target := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+target)

	send(t, m, key("3"))
	if got := m.board.Entries["/tmp/"+target].Status; got != "waiting" {
		t.Fatalf("status is %q, want waiting", got)
	}
	if sp := m.selected(); sp == nil || sp.Label != target {
		t.Fatalf("selection did not follow into the Waiting column: %+v", sp)
	}
}

// An empty column has nothing selected; the actions must decline rather than
// operate on a stale card.
func TestKanbanEmptyColumnSelectsNothing(t *testing.T) {
	m := kanbanBoard(t)
	m.col, m.rowInCol = 3, 0 // Done, empty
	m.clampColumnCursor()

	if sp := m.selected(); sp != nil {
		t.Fatalf("empty column selected %+v", sp)
	}
	send(t, m, key("n"))
	if m.mode == modeNote {
		t.Fatal("note editor opened with no card selected")
	}
}

func TestKanbanRendersAllColumns(t *testing.T) {
	m := kanbanBoard(t)
	out := m.View()
	for _, st := range m.board.Statuses {
		if !strings.Contains(out, st.Label) {
			t.Fatalf("kanban is missing the %q column:\n%s", st.Label, out)
		}
	}
}

// Narrow terminals cannot show every column at once; the selected one must
// still be on screen.
func TestKanbanScrollsToSelectedColumn(t *testing.T) {
	m := kanbanBoard(t)
	m.width = 40
	m.col = len(m.board.Statuses) - 1

	out := m.View()
	width := m.columnWidth()
	visible := m.visibleColumns(width)
	if m.col < m.colOffset || m.col >= m.colOffset+visible {
		t.Fatalf("selected column %d is off screen (offset %d, visible %d)", m.col, m.colOffset, visible)
	}
	if !strings.Contains(out, m.board.Statuses[m.col].Label) {
		t.Fatalf("selected column not rendered:\n%s", out)
	}
}

func TestWrapSplitsLongWords(t *testing.T) {
	lines := wrap("supercalifragilistic short", 8)
	for _, l := range lines {
		if len(l) > 8 {
			t.Fatalf("line %q exceeds the width", l)
		}
	}
	if strings.Join(lines, "") == "" {
		t.Fatal("wrap dropped the text")
	}
}

func TestDetailPaneShowsFullNote(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 110, 24
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	long := "waiting on Dave re: API key rotation, he is back Thursday and will re-issue then"
	m.board.SetStatus("/tmp/api", "waiting", "api")
	m.board.SetNote("/tmp/api", long)
	m.rebuild()
	selectSpace(t, m, "/tmp/api")

	out := m.View()
	if m.detailPaneWidth() == 0 {
		t.Fatal("the pane should be on by default at this width")
	}
	// The row truncates the note; the pane must not.
	for _, word := range []string{"rotation,", "Thursday", "re-issue"} {
		if !strings.Contains(out, word) {
			t.Fatalf("pane dropped %q from the note:\n%s", word, out)
		}
	}
	if !strings.Contains(out, "live") {
		t.Fatalf("pane is missing the workspace state:\n%s", out)
	}
}

// The pane follows the cursor with no keypress: that is the whole point of it
// being automatic.
func TestDetailPaneTracksCursor(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 110, 24
	send(t, m, liveWorkspaces())
	m.board.SetStatus("/tmp/api", "todo", "api")
	m.board.SetNote("/tmp/api", "note-for-api")
	m.board.SetStatus("/tmp/web", "todo", "web")
	m.board.SetNote("/tmp/web", "note-for-web")
	m.rebuild()

	selectSpace(t, m, "/tmp/api")
	if out := m.View(); !strings.Contains(out, "note-for-api") {
		t.Fatalf("pane is not showing the selected space:\n%s", out)
	}
	selectSpace(t, m, "/tmp/web")
	if out := m.View(); !strings.Contains(out, "note-for-web") {
		t.Fatalf("pane did not follow the cursor:\n%s", out)
	}
}

func TestDetailPaneTogglesAndPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, board)
	m.width, m.height = 110, 24
	send(t, m, liveWorkspaces())

	send(t, m, key("d"))
	if m.detailPaneWidth() != 0 {
		t.Fatal("d did not hide the pane")
	}
	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.HideDetail {
		t.Fatal("the preference was not persisted")
	}
}

// A narrow terminal must not be split; the list needs the room more.
func TestDetailPaneHiddenWhenNarrow(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	m.width = 60
	if m.detailPaneWidth() != 0 {
		t.Fatal("the pane should stand down on a narrow terminal")
	}
	if out := m.View(); !strings.Contains(out, "Board") {
		t.Fatalf("narrow list did not render:\n%s", out)
	}
}

// In kanban the columns already use the width, so detail is a modal instead.
func TestKanbanDetailIsAModal(t *testing.T) {
	m := kanbanBoard(t)
	m.width, m.height = 110, 24
	target := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+target)
	m.board.SetNote("/tmp/"+target, "modal-note-text")
	m.rebuild()
	selectSpace(t, m, "/tmp/"+target)

	if m.detailPaneWidth() != 0 {
		t.Fatal("kanban must not reserve a side pane")
	}

	send(t, m, key("d"))
	if m.mode != modeDetail {
		t.Fatal("d did not open the modal")
	}
	out := m.View()
	if !strings.Contains(out, "modal-note-text") {
		t.Fatalf("modal is missing the note:\n%s", out)
	}
	if !strings.Contains(out, "╭") {
		t.Fatalf("modal is not drawn as a box:\n%s", out)
	}

	send(t, m, key("esc"))
	if m.mode != modeNormal {
		t.Fatal("esc did not close the modal")
	}
}

// Editing from the modal should return to the modal, not dump you on the board.
func TestModalNoteEditReturnsToModal(t *testing.T) {
	m := kanbanBoard(t)
	m.width, m.height = 110, 24
	target := labelsIn(m, "todo")[0]
	selectSpace(t, m, "/tmp/"+target)

	send(t, m, key("d"))
	send(t, m, key("n"))
	if m.mode != modeNote {
		t.Fatal("n did not open the note editor")
	}
	m.input.SetValue("edited from the modal")
	send(t, m, key("enter"))

	if m.mode != modeDetail {
		t.Fatalf("returned to mode %v, want the modal", m.mode)
	}
	if got := m.board.Entries["/tmp/"+target].Note; got != "edited from the modal" {
		t.Fatalf("note not saved: %q", got)
	}
}

func TestModalBrowsesWithoutClosing(t *testing.T) {
	m := kanbanBoard(t)
	m.width, m.height = 110, 24
	selectSpace(t, m, "/tmp/"+labelsIn(m, "todo")[0])

	send(t, m, key("d"))
	send(t, m, key("j"))
	if m.mode != modeDetail {
		t.Fatal("j closed the modal")
	}
	if sp := m.selected(); sp == nil || sp.Label != labelsIn(m, "todo")[1] {
		t.Fatalf("modal did not move to the next card: %+v", sp)
	}
}

// The modal's rule must fit inside the border rather than wrapping.
func TestModalRuleFitsTheBox(t *testing.T) {
	m := kanbanBoard(t)
	m.width, m.height = 110, 24
	selectSpace(t, m, "/tmp/"+labelsIn(m, "todo")[0])
	send(t, m, key("d"))

	for _, line := range strings.Split(m.View(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "──") && !strings.Contains(line, "│") {
			t.Fatalf("the rule escaped the box: %q", line)
		}
	}
}
