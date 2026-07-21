package ui

import (
	"time"

	"fmt"
	"github.com/phin-tech/herdr-phin-board/internal/gh"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/herdr"
	"github.com/phin-tech/herdr-phin-board/internal/store"
)

// loadTestBoard gives every UI test the same four statuses, rather than
// whatever the product currently ships. Changing the defaults is a product
// decision; it should not renumber the keys these tests press.
func loadTestBoard(t *testing.T) *store.Board {
	t.Helper()
	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	board.Statuses = []store.Status{
		{ID: "todo", Label: "Todo", Color: "244"},
		{ID: "in_progress", Label: "In Progress", Color: "39"},
		{ID: "waiting", Label: "Waiting", Color: "214"},
		{ID: "done", Label: "Done", Color: "78"},
	}
	board.Collapsed = []string{"done"}
	if err := board.Save(); err != nil {
		t.Fatal(err)
	}
	return board
}

// newTestModel builds a model with no live socket. Commands returned by Update
// are never run here, so the nil client is never dialled.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	m := New(nil, loadTestBoard(t))
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

	board := loadTestBoard(t)
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
	for _, want := range []string{"▾ List", "Waiting", "web", "waiting on Dave"} {
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
	if out := m.View(); !strings.Contains(out, "▾ List") {
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
	board := loadTestBoard(t)
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
	m.toggleLayout() // table
	m.toggleLayout() // kanban
	if m.layout != layoutKanban {
		t.Fatal("K did not reach kanban")
	}
	return m
}

func tableBoard(t *testing.T) *Model {
	t.Helper()
	m := threeInTodo(t)
	m.toggleLayout()
	if m.layout != layoutTable {
		t.Fatal("K did not reach the table")
	}
	return m
}

// K cycles all three views and comes back round.
func TestLayoutCycles(t *testing.T) {
	m := threeInTodo(t)
	for _, want := range []layout{layoutTable, layoutKanban, layoutList, layoutTable} {
		send(t, m, key("K"))
		if m.layout != want {
			t.Fatalf("cycle reached %v, want %v", m.layout, want)
		}
	}
}

func TestLayoutTogglePersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	board := loadTestBoard(t)
	m := New(nil, board)
	m.width, m.height = 100, 30
	send(t, m, liveWorkspaces())
	send(t, m, key("K"))
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
	board := loadTestBoard(t)
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
	if out := m.View(); !strings.Contains(out, "▾ List") {
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

func TestTableListsEveryFlatRow(t *testing.T) {
	m := tableBoard(t)
	if len(m.flat) != 3 {
		t.Fatalf("table has %d rows, want 3", len(m.flat))
	}
	// The table is flat: no headers, so every row is selectable.
	for i := range m.flat {
		m.cursor = i
		if m.selected() == nil {
			t.Fatalf("row %d selects nothing", i)
		}
	}
}

func TestTableSortCyclesAndPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	board := loadTestBoard(t)
	m := New(nil, board)
	m.width, m.height = 110, 24
	send(t, m, liveWorkspaces())
	send(t, m, key("K")) // table

	send(t, m, key("o"))
	if m.sort != sortName {
		t.Fatalf("sort is %v, want name", m.sort)
	}
	send(t, m, key("o"))
	if m.sort != sortChanged {
		t.Fatalf("sort is %v, want changed", m.sort)
	}
	send(t, m, key("o"))
	if m.sort != sortStatus {
		t.Fatalf("sort did not wrap back to status: %v", m.sort)
	}

	send(t, m, key("o"))
	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.TableSort != "name" {
		t.Fatalf("sort not persisted: %q", reloaded.TableSort)
	}
}

func TestTableSortByNameOrders(t *testing.T) {
	m := tableBoard(t)
	send(t, m, key("o")) // name

	var got []string
	for _, sp := range m.flat {
		got = append(got, sp.Label)
	}
	want := []string{"alpha", "beta", "gamma"}
	if !equal(got, want) {
		t.Fatalf("name sort gave %v, want %v", got, want)
	}
}

// Sorting by status must lay rows out exactly as the list groups them, so a
// row's neighbours are the ones a grab-move would swap with.
func TestTableStatusSortMatchesGroups(t *testing.T) {
	m := tableBoard(t)
	m.cursor = 0
	send(t, m, key("3")) // move the first row to Waiting

	var got []string
	for _, sp := range m.flat {
		got = append(got, sp.Label)
	}
	var want []string
	for _, st := range m.board.Statuses {
		for _, sp := range m.groups[st.ID] {
			want = append(want, sp.Label)
		}
	}
	if !equal(got, want) {
		t.Fatalf("table order %v does not match group order %v", got, want)
	}
}

func TestTableGrabReordersAndCrosses(t *testing.T) {
	m := tableBoard(t)
	first := m.flat[0].Label
	m.cursor = 0

	send(t, m, key("v"))
	send(t, m, key("j"))
	if m.flat[1].Label != first {
		t.Fatalf("row did not move down: %v", m.flat[1].Label)
	}

	// Walking off the end of the group retags, exactly as in the list.
	send(t, m, key("j"))
	send(t, m, key("j"))
	if got := m.board.Entries["/tmp/"+first].Status; got != "in_progress" {
		t.Fatalf("status is %q, want in_progress", got)
	}
}

// A rank is a position within a status. Sorted any other way, the row above is
// not the one it would swap with, so grabbing must be refused.
func TestTableGrabRefusedWhenNotSortedByStatus(t *testing.T) {
	m := tableBoard(t)
	send(t, m, key("o")) // name
	m.cursor = 0

	send(t, m, key("v"))
	if m.grabbed != "" {
		t.Fatal("grab was allowed against a non-status sort")
	}
	if m.status == "" {
		t.Fatal("no explanation given")
	}
}

// Changing the sort while holding a row must drop it rather than move it
// somewhere arbitrary.
func TestTableSortChangeDropsGrab(t *testing.T) {
	m := tableBoard(t)
	m.cursor = 0
	send(t, m, key("v"))
	if m.grabbed == "" {
		t.Fatal("setup: nothing grabbed")
	}
	send(t, m, key("o"))
	if m.grabbed != "" {
		t.Fatal("the held row survived a sort change")
	}
}

func TestTableRendersColumns(t *testing.T) {
	m := tableBoard(t)
	m.width, m.height = 118, 20
	m.cursor = 0
	m.board.SetNote("/tmp/"+m.flat[0].Label, "table-note-text")
	m.rebuild()

	out := m.View()
	for _, want := range []string{"SPACE", "STATUS", "NOTE", "AGENT", "CHANGED", "table-note-text"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table is missing %q:\n%s", want, out)
		}
	}
	// The sort marker sits on the column actually being sorted.
	if !strings.Contains(out, "↓STATUS") {
		t.Fatalf("status sort is not marked:\n%s", out)
	}
}

func TestTableDetailIsAModal(t *testing.T) {
	m := tableBoard(t)
	m.width, m.height = 118, 20
	m.cursor = 0

	if m.detailPaneWidth() != 0 {
		t.Fatal("the table must not reserve a side pane")
	}
	send(t, m, key("d"))
	if m.mode != modeDetail {
		t.Fatal("d did not open the modal in the table")
	}
}

// Narrow terminals must still produce a usable table rather than overflowing.
func TestTableWidthsStayInBounds(t *testing.T) {
	m := tableBoard(t)
	for _, width := range []int{60, 80, 100, 140} {
		m.width = width
		w := m.tableWidths()
		total := w.name + w.status + w.note + w.agent + w.changed + 4 + 3
		if total > width {
			t.Fatalf("at width %d the columns total %d", width, total)
		}
		if w.note < 1 || w.name < 1 {
			t.Fatalf("at width %d a column collapsed: %+v", width, w)
		}
	}
}

// o outside the table must explain itself rather than doing nothing.
func TestSortKeyOutsideTableExplains(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*testing.T) *Model
	}{
		{"list", threeInTodo},
		{"kanban", kanbanBoard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			m.status = ""
			send(t, m, key("o"))
			if m.status == "" {
				t.Fatal("o gave no feedback outside the table")
			}
		})
	}
}

func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}

// The title names the view rather than saying "Board".
func TestTitleNamesTheView(t *testing.T) {
	m := threeInTodo(t)
	if got := m.title(); got != "▾ List" {
		t.Fatalf("title is %q, want ▾ List", got)
	}
	if out := m.View(); !strings.Contains(out, "▾ List") {
		t.Fatalf("header is not showing the view name:\n%s", out)
	}

	send(t, m, key("K"))
	if got := m.title(); got != "▾ Table" {
		t.Fatalf("title is %q, want ▾ Table", got)
	}
	send(t, m, key("K"))
	if got := m.title(); got != "▾ Kanban" {
		t.Fatalf("title is %q, want ▾ Kanban", got)
	}
}

func TestClickingTitleOpensAndClosesMenu(t *testing.T) {
	m := threeInTodo(t)

	send(t, m, click(menuIndent+1, menuRow))
	if !m.menuOpen {
		t.Fatal("clicking the title did not open the menu")
	}
	if !strings.Contains(m.View(), "▴ List") {
		t.Fatal("the caret did not flip while open")
	}
	out := m.View()
	for _, want := range []string{"List", "Table", "Kanban"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dropdown is missing %q:\n%s", want, out)
		}
	}

	send(t, m, click(menuIndent+1, menuRow))
	if m.menuOpen {
		t.Fatal("clicking the title again did not close the menu")
	}
}

func TestClickingMenuItemSwitchesView(t *testing.T) {
	m := threeInTodo(t)
	m.openMenu()

	// The third entry is kanban.
	send(t, m, click(menuIndent+2, menuFirstItemRow+2))
	if m.layout != layoutKanban {
		t.Fatalf("layout is %v, want kanban", m.layout)
	}
	if m.menuOpen {
		t.Fatal("choosing did not close the menu")
	}
	if m.board.Layout != "kanban" {
		t.Fatalf("choice not persisted: %q", m.board.Layout)
	}
}

// Clicking away from an open menu dismisses it rather than acting on the board.
func TestClickingOutsideDismissesMenu(t *testing.T) {
	m := threeInTodo(t)
	m.openMenu()
	before := m.layout

	send(t, m, click(60, 9))
	if m.menuOpen {
		t.Fatal("the menu stayed open")
	}
	if m.layout != before {
		t.Fatal("dismissing changed the view")
	}
}

func TestMenuIsKeyboardDrivable(t *testing.T) {
	m := threeInTodo(t)
	m.openMenu()

	send(t, m, key("j"))
	send(t, m, key("enter"))
	if m.layout != layoutTable {
		t.Fatalf("layout is %v, want table", m.layout)
	}

	m.openMenu()
	send(t, m, key("esc"))
	if m.menuOpen {
		t.Fatal("esc did not close the menu")
	}
	if m.layout != layoutTable {
		t.Fatal("esc changed the view")
	}
}

// The open menu must swallow navigation, or j/k would move the board cursor
// underneath it at the same time.
func TestOpenMenuSwallowsNavigation(t *testing.T) {
	m := threeInTodo(t)
	cursor := m.cursor
	m.openMenu()

	send(t, m, key("j"))
	if m.cursor != cursor {
		t.Fatal("j moved the board cursor while the menu was open")
	}
	if m.menuIdx != 1 {
		t.Fatalf("j did not move the menu cursor: %d", m.menuIdx)
	}
}

// Clicking a row selects it, so the mouse is useful beyond the switcher.
func TestClickingARowSelectsIt(t *testing.T) {
	m := threeInTodo(t)
	m.width, m.height = 100, 24

	// Row 0 of the list is the Todo header; its first space is one line below.
	send(t, m, click(10, 3))
	if sp := m.selected(); sp == nil {
		t.Fatal("clicking a row selected nothing")
	}
}

func TestMenuHitTesting(t *testing.T) {
	m := threeInTodo(t)
	if m.titleHit(0, 1) {
		t.Fatal("a click below the title counted as a hit")
	}
	if m.titleHit(99, menuRow) {
		t.Fatal("a click far right counted as a hit")
	}
	if !m.titleHit(menuIndent, menuRow) {
		t.Fatal("the first title column is not clickable")
	}

	m.openMenu()
	if got := m.menuItemAt(menuIndent, menuFirstItemRow); got != 0 {
		t.Fatalf("first item resolved to %d", got)
	}
	if got := m.menuItemAt(menuIndent, menuFirstItemRow+len(menuLayouts)); got != -1 {
		t.Fatalf("a click past the last item resolved to %d", got)
	}
}

func TestRenameUpdatesStoredLabel(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")

	send(t, m, key("R"))
	if m.mode != modeRename {
		t.Fatal("R did not open the rename prompt")
	}
	// The prompt starts from the current name rather than empty.
	if m.input.Value() != "api" {
		t.Fatalf("prompt seeded with %q, want api", m.input.Value())
	}

	m.input.SetValue("api-gateway")
	send(t, m, key("enter"))

	if m.mode != modeNormal {
		t.Fatal("rename did not close the prompt")
	}
	if got := m.board.Entries["/tmp/api"].Label; got != "api-gateway" {
		t.Fatalf("stored label is %q, want api-gateway", got)
	}
	if sp := m.selected(); sp == nil || sp.Label != "api-gateway" {
		t.Fatalf("row still shows %+v", sp)
	}
}

// A rename targets the workspace whose name is on screen, not every workspace
// sharing the directory -- those keep distinct names on purpose.
func TestRenameTargetsTheDisplayedWorkspace(t *testing.T) {
	m := newTestModel(t)
	send(t, m, workspacesMsg{
		{ID: "w1", Label: "api", Cwd: "/tmp/api"},
		{ID: "w2", Label: "api-2", Cwd: "/tmp/api", Focused: true},
	})

	sp := m.selected()
	if sp == nil || len(sp.WorkspaceIDs) != 2 {
		t.Fatalf("setup: expected a merged row, got %+v", sp)
	}
	// The focused workspace owns the visible label.
	if sp.DisplayWorkspaceID != "w2" {
		t.Fatalf("display workspace is %q, want w2", sp.DisplayWorkspaceID)
	}
}

func TestRenameArchivedSpaceNeedsNoWorkspace(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("3"))

	// The workspace goes away; the entry stays and is still renameable.
	send(t, m, workspacesMsg{{ID: "w2", Label: "web", Cwd: "/tmp/web"}})
	send(t, m, key("a"))
	selectSpace(t, m, "/tmp/api")

	if sp := m.selected(); sp.DisplayWorkspaceID != "" {
		t.Fatalf("archived space claims workspace %q", sp.DisplayWorkspaceID)
	}
	send(t, m, key("R"))
	m.input.SetValue("old-api")
	send(t, m, key("enter"))

	if got := m.board.Entries["/tmp/api"].Label; got != "old-api" {
		t.Fatalf("archived rename not stored: %q", got)
	}
}

// An empty name would erase the row's identity, so it is ignored.
func TestRenameIgnoresEmptyName(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")

	send(t, m, key("R"))
	m.input.SetValue("   ")
	send(t, m, key("enter"))

	if sp := m.selected(); sp == nil || sp.Label != "api" {
		t.Fatalf("empty rename changed the row: %+v", sp)
	}
}

func TestRenameShowsItsOwnPrompt(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("R"))

	if out := m.View(); !strings.Contains(out, "rename:") {
		t.Fatalf("no rename prompt in the footer:\n%s", out)
	}
}

// tokenPushes replays what syncTokens would send, without a socket.
func tokenPushes(m *Model) map[string]*string {
	out := map[string]*string{}
	for _, group := range m.groups {
		for _, sp := range group {
			if !sp.Live {
				continue
			}
			st, ok := m.board.StatusByID(sp.StatusID)
			if !ok {
				continue
			}
			var value *string
			if st.ID != m.board.DefaultStatusID() {
				label := st.Label
				value = &label
			}
			for _, id := range sp.WorkspaceIDs {
				out[id] = value
			}
		}
	}
	return out
}

// The default status is where untouched spaces sit, so badging it would put
// the same word on every sidebar row and say nothing.
func TestDefaultStatusClearsItsToken(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())

	pushes := tokenPushes(m)
	if len(pushes) != 2 {
		t.Fatalf("want a push per live workspace, got %d", len(pushes))
	}
	for id, v := range pushes {
		if v != nil {
			t.Fatalf("%s got token %q, want it cleared", id, *v)
		}
	}
}

func TestNonDefaultStatusSetsItsToken(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("3")) // Waiting

	pushes := tokenPushes(m)
	if v := pushes["w1"]; v == nil || *v != "Waiting" {
		t.Fatalf("w1 token is %v, want Waiting", v)
	}
	// The untouched one stays clear.
	if v := pushes["w2"]; v != nil {
		t.Fatalf("w2 got token %q, want it cleared", *v)
	}
}

// Moving back to the default must clear the badge, not leave a stale one.
func TestReturningToDefaultClearsTheToken(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("3"))
	if v := tokenPushes(m)["w1"]; v == nil {
		t.Fatal("setup: expected a token")
	}

	selectSpace(t, m, "/tmp/api")
	send(t, m, key("1")) // back to Todo
	if v := tokenPushes(m)["w1"]; v != nil {
		t.Fatalf("token %q survived the return to default", *v)
	}
}

// Which status counts as default follows the board's order, so reordering
// changes which one is suppressed.
func TestSuppressionFollowsStatusOrder(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	// File it as Todo explicitly. An untouched space has no stored status at
	// all, so it follows whichever status is default rather than staying put.
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("1"))
	if v := tokenPushes(m)["w1"]; v != nil {
		t.Fatalf("Todo is the default, so it should be cleared; got %q", *v)
	}

	m.board.MoveStatus("todo", 1) // In Progress becomes first
	m.rebuild()

	if v := tokenPushes(m)["w1"]; v == nil || *v != "Todo" {
		t.Fatalf("w1 token is %v, want Todo once it is no longer default", v)
	}
}

// D in the status manager sets the default, and the suppression follows it.
func TestSetDefaultStatusFromManager(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("3")) // Waiting

	// Waiting is badged while Todo is the default.
	if v := tokenPushes(m)["w1"]; v == nil || *v != "Waiting" {
		t.Fatalf("w1 token is %v, want Waiting", v)
	}

	send(t, m, key("S"))
	m.manageIdx = 2 // Waiting
	send(t, m, key("D"))

	if got := m.board.DefaultStatusID(); got != "waiting" {
		t.Fatalf("default is %q, want waiting", got)
	}
	// Now Waiting is the quiet one and Todo speaks up.
	if v := tokenPushes(m)["w1"]; v != nil {
		t.Fatalf("the new default still badges: %q", *v)
	}
	if v := tokenPushes(m)["w2"]; v == nil || *v != "Todo" {
		t.Fatalf("w2 token is %v, want Todo once it is no longer default", v)
	}
}

// Reordering must not change which status is quiet -- that was the whole point
// of making the default explicit.
func TestReorderingDoesNotMoveTheDefault(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	m.board.SetDefaultStatus("todo")

	send(t, m, key("S"))
	m.manageIdx = 0
	send(t, m, key("J")) // push Todo down the order

	if got := m.board.DefaultStatusID(); got != "todo" {
		t.Fatalf("default moved with the order: %q", got)
	}
}

func TestManagerMarksTheDefault(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	send(t, m, key("S"))

	out := m.View()
	if !strings.Contains(out, "default") {
		t.Fatalf("the manager does not mark the default:\n%s", out)
	}
	if !strings.Contains(out, "D set default") {
		t.Fatalf("the manager does not advertise D:\n%s", out)
	}
}

func withPR(m *Model, key string, pr gh.PR) {
	if pr.Fetched.IsZero() {
		pr.Fetched = time.Now()
	}
	m.prCache.Put(key, pr)
	m.rebuild()
}

// PR state is context. It must never change where a space sits.
func TestPRNeverChangesGrouping(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	send(t, m, key("3")) // Waiting

	before := m.board.Entries["/tmp/api"]
	withPR(m, "/tmp/api", gh.PR{Number: 9, State: "MERGED", Checks: gh.ChecksFail})

	after := m.board.Entries["/tmp/api"]
	if after.Status != before.Status {
		t.Fatalf("a merged PR changed the status: %q -> %q", before.Status, after.Status)
	}
	if after.Order != before.Order {
		t.Fatal("a PR changed the manual order")
	}
	// And the row is still in the group the user put it in.
	found := false
	for _, sp := range m.groups["waiting"] {
		if sp.Key == "/tmp/api" {
			found = true
		}
	}
	if !found {
		t.Fatal("the space left the group the user chose")
	}
}

func TestPRColumnAppearsOnlyWhenThereIsAPR(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 130, 20
	send(t, m, liveWorkspaces())
	send(t, m, key("K")) // table

	if m.tableWidths().pr != 0 {
		t.Fatal("the PR column took space with no PRs on the board")
	}
	if strings.Contains(m.View(), " PR ") {
		t.Fatal("the PR heading rendered with no PRs")
	}

	withPR(m, "/tmp/api", gh.PR{Number: 42, State: "OPEN", Review: "APPROVED", Checks: gh.ChecksPass})
	if m.tableWidths().pr == 0 {
		t.Fatal("the PR column did not appear")
	}
	out := m.View()
	for _, want := range []string{"PR", "#42", "approved"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table is missing %q:\n%s", want, out)
		}
	}
}

func TestPRRendersInDetailPane(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 120, 24
	send(t, m, liveWorkspaces())
	withPR(m, "/tmp/api", gh.PR{
		Number: 7, State: "OPEN", Review: "CHANGES_REQUESTED",
		Checks: gh.ChecksFail, Title: "Rework the parser",
	})
	selectSpace(t, m, "/tmp/api")

	out := m.View()
	for _, want := range []string{"#7", "changes", "Rework the parser"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail pane is missing %q:\n%s", want, out)
		}
	}
}

func TestPRShortFormForSidebar(t *testing.T) {
	cases := []struct {
		pr   gh.PR
		want string
	}{
		{gh.PR{Number: 1, State: "OPEN", Checks: gh.ChecksPass}, "#1 ●✓"},
		{gh.PR{Number: 2, State: "OPEN", IsDraft: true, Checks: gh.ChecksFail}, "#2 ○✗"},
		{gh.PR{Number: 3, State: "MERGED"}, "#3 ◆"},
		{gh.PR{Number: 4, State: "CLOSED", Checks: gh.ChecksPending}, "#4 ✕·"},
	}
	for _, tc := range cases {
		if got := prShort(tc.pr); got != tc.want {
			t.Fatalf("prShort(%+v) = %q, want %q", tc.pr, got, tc.want)
		}
	}
}

// A merged branch loses its PR; the cached badge must go with it.
func TestApplyPRsForgetsMissing(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	withPR(m, "/tmp/api", gh.PR{Number: 11, State: "OPEN"})

	m.applyPRs(prLoadedMsg{missing: []string{"/tmp/api"}})

	if _, ok := m.prFor("/tmp/api"); ok {
		t.Fatal("a PR that no longer exists is still cached")
	}
}

// Spaces that left the board should not accumulate in the cache.
func TestApplyPRsPrunesUnknownSpaces(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	m.prCache.Put("/tmp/long-gone", gh.PR{Number: 1, Fetched: time.Now()})

	m.applyPRs(prLoadedMsg{})

	if _, ok := m.prFor("/tmp/long-gone"); ok {
		t.Fatal("the cache kept a space that is not on the board")
	}
}

// Only stale entries are refetched, so reopening the board is not a burst of
// network calls.
func TestLoadPRsSkipsFreshEntries(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	for _, k := range []string{"/tmp/api", "/tmp/web"} {
		m.prCache.Put(k, gh.PR{Number: 1, Fetched: time.Now()})
	}
	m.rebuild()

	if cmd := m.loadPRs(); cmd != nil {
		t.Fatal("loadPRs queued work with everything fresh")
	}
}

// The board refreshes on every Herdr workspace event. If a space with no PR
// were not remembered as such, each of those refreshes would respawn gh for it
// -- which is what made the input loop feel blocked.
func TestNoPRSpacesAreNotRefetchedEveryRefresh(t *testing.T) {
	m := newTestModel(t)

	// The first workspace snapshot asks about both spaces, since neither is
	// known yet.
	send(t, m, liveWorkspaces())
	if !m.prLoading {
		t.Fatal("the first snapshot did not start a fetch")
	}
	m.applyPRs(prLoadedMsg{missing: []string{"/tmp/api", "/tmp/web"}})

	// A workspace event arrives and the board refreshes.
	send(t, m, liveWorkspaces())

	if m.prLoading {
		t.Fatal("a refresh respawned gh for spaces already known to have no PR")
	}
	if cmd := m.loadPRs(); cmd != nil {
		t.Fatal("an explicit load respawned gh for known-absent spaces")
	}
}

// Overlapping rounds must not stack up while one is in flight.
func TestLoadPRsGuardsAgainstOverlap(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	if !m.prLoading {
		t.Fatal("the first snapshot did not start a fetch")
	}

	if cmd := m.loadPRs(); cmd != nil {
		t.Fatal("a second load started while the first was still running")
	}

	// Once the round lands, the guard clears and fetching is allowed again.
	m.applyPRs(prLoadedMsg{missing: []string{"/tmp/api", "/tmp/web"}})
	if m.prLoading {
		t.Fatal("the in-flight guard was not cleared")
	}
}

// A cached absence is a record that we looked, not something to render.
func TestAbsenceIsNotDisplayed(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	m.applyPRs(prLoadedMsg{missing: []string{"/tmp/api"}})

	if _, ok := m.prFor("/tmp/api"); ok {
		t.Fatal("a cached absence is being reported as a PR")
	}
	if m.anyPR() {
		t.Fatal("an absence caused the PR column to appear")
	}
}

// captureOpens swaps the browser opener for the duration of a test.
func captureOpens(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	original := openURL
	openURL = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	t.Cleanup(func() { openURL = original })
	return &opened
}

func TestChordGPOpensThePR(t *testing.T) {
	opened := captureOpens(t)
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	withPR(m, "/tmp/api", gh.PR{
		Number: 42, State: "OPEN",
		URL: "https://github.com/o/r/pull/42",
	})
	selectSpace(t, m, "/tmp/api")

	send(t, m, key("g"))
	if m.chord != "g" {
		t.Fatal("g did not start a chord")
	}
	if _, cmd := m.Update(key("p")); cmd != nil {
		if msg := cmd(); msg != nil {
			if e, ok := msg.(errMsg); ok {
				t.Fatalf("open failed: %v", e.err)
			}
		}
	}

	if len(*opened) != 1 || (*opened)[0] != "https://github.com/o/r/pull/42" {
		t.Fatalf("opened %v, want the PR URL", *opened)
	}
	if m.chord != "" {
		t.Fatal("the chord was not cleared")
	}
}

// gg is vim's "go to top"; bare g must not do it any more, or the chord could
// never have a second key.
func TestChordGGGoesToTop(t *testing.T) {
	m := threeInTodo(t)
	m.cursor = 3

	send(t, m, key("g"))
	if m.cursor != 3 {
		t.Fatal("bare g moved the cursor, leaving no room for a chord")
	}
	send(t, m, key("g"))
	if m.cursor != 0 {
		t.Fatalf("gg left the cursor at %d, want 0", m.cursor)
	}
}

func TestChordOnASpaceWithoutAPRExplains(t *testing.T) {
	opened := captureOpens(t)
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")

	send(t, m, key("g"))
	send(t, m, key("p"))

	if len(*opened) != 0 {
		t.Fatalf("opened %v with no PR present", *opened)
	}
	if m.status == "" {
		t.Fatal("gp with no PR gave no feedback")
	}
}

// An unknown chord must do nothing rather than falling through to whatever
// that key means alone -- gx should not archive the board.
func TestUnknownChordIsSwallowed(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	before := m.showArchive

	send(t, m, key("g"))
	send(t, m, key("a"))

	if m.showArchive != before {
		t.Fatal("ga fell through to the archive toggle")
	}
	if m.chord != "" {
		t.Fatal("the chord was not cleared")
	}
}

func TestChordCancelledByEsc(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())

	send(t, m, key("g"))
	send(t, m, key("esc"))
	if m.chord != "" {
		t.Fatal("esc did not cancel the chord")
	}
	// And the board is still open: esc cancelled the chord, not the board.
	if m.quitting {
		t.Fatal("esc cancelling a chord also quit")
	}
}

// The chord works from the detail modal too, where you are most likely to want
// the PR you are reading about.
func TestChordWorksInTheDetailModal(t *testing.T) {
	opened := captureOpens(t)
	m := kanbanBoard(t)
	target := labelsIn(m, "todo")[0]
	withPR(m, "/tmp/"+target, gh.PR{Number: 8, State: "OPEN", URL: "https://example.test/8"})
	selectSpace(t, m, "/tmp/"+target)

	send(t, m, key("d"))
	if m.mode != modeDetail {
		t.Fatal("d did not open the modal")
	}
	send(t, m, key("g"))
	if _, cmd := m.Update(key("p")); cmd != nil {
		cmd()
	}

	if len(*opened) != 1 {
		t.Fatalf("opened %v from the modal, want one URL", *opened)
	}
	if m.mode != modeDetail {
		t.Fatal("opening the PR closed the modal")
	}
}
