// Package ui implements the board TUI.
package ui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/alert"
	"github.com/phin-tech/herdr-phin-board/internal/gh"
	"github.com/phin-tech/herdr-phin-board/internal/herdr"
	"github.com/phin-tech/herdr-phin-board/internal/store"
)

type mode int

const (
	modeNormal mode = iota
	modeStatusPick
	modeNote
	modeRename
	modeMessage
	modeFilter
	modeManage
	modeManageAdd
	modeManageRename
	modeDetail
	modeHelp
)

// space is one row of the board: a directory, whatever the user recorded about
// it, and the live Herdr workspaces (if any) currently open there.
type space struct {
	Key   string // canonical directory, the durable identity
	Label string
	Live  bool

	WorkspaceIDs []string
	AgentStatus  string
	Focused      bool
	// DisplayWorkspaceID owns the label being shown. When several workspaces
	// share a directory they keep distinct names, so a rename must target the
	// one whose name is actually on screen rather than all of them.
	DisplayWorkspaceID string

	StatusID  string
	Note      string
	Order     int
	UpdatedAt time.Time
}

func (s *space) workspaceID() string {
	if len(s.WorkspaceIDs) == 0 {
		return ""
	}
	return s.WorkspaceIDs[0]
}

// layout selects between the grouped list and the kanban columns. Both render
// the same spaces and the same statuses; only the arrangement differs.
type layout int

const (
	layoutList layout = iota
	layoutTable
	layoutKanban
)

func (l layout) String() string {
	switch l {
	case layoutTable:
		return "table"
	case layoutKanban:
		return "kanban"
	default:
		return "list"
	}
}

func parseLayout(s string) layout {
	switch s {
	case "table":
		return layoutTable
	case "kanban":
		return layoutKanban
	default:
		return layoutList
	}
}

// next cycles list -> table -> kanban -> list.
func (l layout) next() layout {
	return (l + 1) % 3
}

type rowKind int

const (
	rowHeader rowKind = iota
	rowSpace
	rowEmpty
)

type row struct {
	kind   rowKind
	status store.Status
	count  int
	space  *space
}

// Model is the board.
type Model struct {
	client *herdr.Client
	board  *store.Board
	// gh and prCache carry pull request context. Both are optional: without
	// them the board works exactly as before, just without PR columns.
	gh      *gh.Client
	prCache *gh.Cache
	// alerts is what the watcher recorded while the board was closed.
	alerts   *alert.Store
	stateDir string
	// prLoading guards against overlapping fetch rounds.
	prLoading bool
	// prProblemSaid keeps a broken gh from repeating itself every refresh.
	prProblemSaid bool

	live []herdr.Workspace
	rows []row
	// flat is the table's ordered spaces: no headers, no collapse.
	flat []*space
	// groups holds every space per status in display order, including those
	// hidden by a collapsed group -- grab-moves need the full picture.
	groups map[string][]*space
	// grabbed is the key of the space being moved with j/k, if any.
	grabbed string

	cursor int
	offset int
	width  int
	height int

	// Kanban keeps its own cursor: a column plus a row within that column.
	// Columns are statuses, so moving sideways is what retags a card.
	layout    layout
	sort      tableSort
	col       int
	rowInCol  int
	colOffset int

	mode mode
	// prevMode is where an input or picker returns when it closes, so opening
	// one from the kanban modal does not drop you back onto the board.
	prevMode    mode
	showArchive bool
	filter      string
	input       textinput.Model
	manageIdx   int

	// chord holds a pending multi-key prefix, vim style: g then g or p.
	chord string

	// The title doubles as a view switcher dropdown.
	menuOpen bool
	menuIdx  int

	landed bool   // the cursor has been placed on a space at least once
	status string // transient message shown in the footer
	err    error

	events   chan herdr.Event
	cancel   context.CancelFunc
	quitting bool
}

// New builds the initial model.
func New(client *herdr.Client, board *store.Board) *Model {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 240

	// The cache lives beside the board file. PR state is derived, so a missing
	// or unreadable cache costs one refetch rather than any of the user's work.
	stateDir := os.TempDir()
	if path, err := store.Path(); err == nil {
		stateDir = filepath.Dir(path)
	}
	cache := gh.LoadCache(stateDir)

	return &Model{
		client:   client,
		board:    board,
		gh:       gh.New(),
		prCache:  cache,
		alerts:   alert.Load(stateDir),
		stateDir: stateDir,
		input:    in,
		layout:   parseLayout(board.Layout),
		sort:     parseSort(board.TableSort),
		width:    80,
		height:   24,
		events:   make(chan herdr.Event, 64),
	}
}

// --- messages ---

type workspacesMsg []herdr.Workspace
type errMsg struct{ err error }
type statusMsg string
type eventMsg struct{}
type eventsDoneMsg struct{}
type tokensSyncedMsg struct{}

func (m *Model) Init() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	return tea.Batch(
		m.refresh(),
		m.subscribe(ctx),
		waitForEvent(m.events),
	)
}

func (m *Model) refresh() tea.Cmd {
	return func() tea.Msg {
		ws, err := m.client.Workspaces()
		if err != nil {
			return errMsg{err}
		}
		return workspacesMsg(ws)
	}
}

func (m *Model) subscribe(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		// Errors here are not fatal: the board still works, it just stops
		// updating on its own. `r` forces a refresh.
		_ = m.client.Subscribe(ctx, herdr.WorkspaceSubscriptions, m.events)
		return eventsDoneMsg{}
	}
}

func waitForEvent(ch chan herdr.Event) tea.Cmd {
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return eventsDoneMsg{}
		}
		return eventMsg{}
	}
}

// syncTokens pushes each space's status into Herdr's workspace metadata, which
// is what makes the status visible in Herdr's own Space rows.
func (m *Model) syncTokens() tea.Cmd {
	type push struct {
		workspaceID string
		// value is nil for the default status, which clears the token: every
		// untouched space sits there, so a badge for it would be noise on
		// every row and would not distinguish filed from untouched.
		value *string
	}
	var pushes []push
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
				pushes = append(pushes, push{id, value})
			}
		}
	}
	client := m.client
	return func() tea.Msg {
		for _, p := range pushes {
			_ = client.ReportToken(p.workspaceID, "status", p.value)
		}
		return tokensSyncedMsg{}
	}
}

// --- rebuild ---

// rebuild merges live workspaces with stored entries into the visible rows.
// Two axes are kept separate here: status (the user's, which groups) and
// liveness (Herdr's, which decides main list vs archive).
func (m *Model) rebuild() {
	selected := m.selectedKey()

	spaces := map[string]*space{}
	for _, ws := range m.live {
		key := store.Key(ws.Cwd)
		if key == "" {
			continue
		}
		sp, ok := spaces[key]
		if !ok {
			sp = &space{Key: key, Label: ws.Label, Live: true, DisplayWorkspaceID: ws.ID}
			spaces[key] = sp
		}
		// Several workspaces can share a directory; they collapse into one row
		// because status is a property of the project, not the window.
		sp.WorkspaceIDs = append(sp.WorkspaceIDs, ws.ID)
		if ws.Focused {
			sp.Focused = true
			sp.Label = ws.Label
			sp.DisplayWorkspaceID = ws.ID
		}
		if rank(ws.AgentStatus) > rank(sp.AgentStatus) {
			sp.AgentStatus = ws.AgentStatus
		}
	}

	for key, entry := range m.board.Entries {
		sp, ok := spaces[key]
		if !ok {
			if !m.showArchive {
				continue
			}
			sp = &space{Key: key, Label: entry.Label}
			spaces[key] = sp
		}
		sp.StatusID = entry.Status
		sp.Note = entry.Note
		sp.Order = entry.Order
		sp.UpdatedAt = entry.UpdatedAt
		if sp.Label == "" {
			sp.Label = entry.Label
		}
	}

	fallback := m.board.DefaultStatusID()
	list := make([]*space, 0, len(spaces))
	for _, sp := range spaces {
		if _, ok := m.board.StatusByID(sp.StatusID); !ok {
			sp.StatusID = fallback
		}
		if sp.Label == "" {
			sp.Label = baseName(sp.Key)
		}
		if m.matches(sp) {
			list = append(list, sp)
		}
	}

	byStatus := map[string][]*space{}
	for _, sp := range list {
		byStatus[sp.StatusID] = append(byStatus[sp.StatusID], sp)
	}
	for _, group := range byStatus {
		sort.Slice(group, func(i, j int) bool {
			a, b := group[i], group[j]
			// Rows the user arranged by hand come first, in their chosen order.
			// Everything else falls back to recency, then name.
			if (a.Order > 0) != (b.Order > 0) {
				return a.Order > 0
			}
			if a.Order > 0 && b.Order > 0 && a.Order != b.Order {
				return a.Order < b.Order
			}
			if a.Live != b.Live {
				return a.Live
			}
			if !a.UpdatedAt.Equal(b.UpdatedAt) {
				return a.UpdatedAt.After(b.UpdatedAt)
			}
			return strings.ToLower(a.Label) < strings.ToLower(b.Label)
		})
	}
	m.groups = byStatus

	m.rows = m.rows[:0]
	for _, st := range m.board.Statuses {
		group := byStatus[st.ID]
		// While filtering, empty groups are noise; otherwise they are useful
		// drop targets and show the shape of the board.
		if m.filter != "" && len(group) == 0 {
			continue
		}
		m.rows = append(m.rows, row{kind: rowHeader, status: st, count: len(group)})
		// A filter overrides collapse, otherwise matches could be hidden.
		if m.board.IsCollapsed(st.ID) && m.filter == "" {
			continue
		}
		for _, sp := range group {
			m.rows = append(m.rows, row{kind: rowSpace, status: st, space: sp})
		}
	}
	if len(m.rows) == 0 {
		m.rows = append(m.rows, row{kind: rowEmpty})
	}

	m.buildFlat()

	m.restoreCursor(selected)
	m.restoreColumnCursor(selected)

	// On first population the cursor would otherwise land on a group header
	// (or an empty column), where n / s / 1-9 all silently do nothing.
	if !m.landed {
		if m.layout == layoutTable {
			if len(m.flat) > 0 {
				m.cursor = 0
				m.landed = true
			}
		} else if i, ok := m.firstSpaceRow(); ok {
			m.cursor = i
			m.landed = true
		}
		if col, ok := m.firstOccupiedColumn(); ok {
			m.col, m.rowInCol = col, 0
			m.landed = true
		}
	}

	m.clampCursor()
	m.clampColumnCursor()
}

// selectedBellCleared marks the space under the cursor seen.
func (m *Model) selectedBellCleared() tea.Cmd {
	sp := m.selected()
	if sp == nil {
		return nil
	}
	return m.clearBell(sp.Key)
}

// restoreColumnCursor keeps the kanban selection on the same card across a
// rebuild, including when a move carried it into another column.
func (m *Model) restoreColumnCursor(key string) {
	if key == "" {
		return
	}
	for col, st := range m.board.Statuses {
		for i, sp := range m.groups[st.ID] {
			if sp.Key == key {
				m.col, m.rowInCol = col, i
				return
			}
		}
	}
}

func (m *Model) firstOccupiedColumn() (int, bool) {
	for col, st := range m.board.Statuses {
		if len(m.groups[st.ID]) > 0 {
			return col, true
		}
	}
	return 0, false
}

func (m *Model) clampColumnCursor() {
	if m.col >= len(m.board.Statuses) {
		m.col = len(m.board.Statuses) - 1
	}
	if m.col < 0 {
		m.col = 0
	}
	group := m.columnSpaces(m.col)
	if m.rowInCol >= len(group) {
		m.rowInCol = len(group) - 1
	}
	if m.rowInCol < 0 {
		m.rowInCol = 0
	}
}

func (m *Model) firstSpaceRow() (int, bool) {
	for i, r := range m.rows {
		if r.kind == rowSpace {
			return i, true
		}
	}
	return 0, false
}

// rank orders agent states so the busiest one wins when a directory has several
// workspaces. This is a display hint only; it never affects grouping.
func rank(state string) int {
	switch state {
	case "working":
		return 4
	case "blocked":
		return 3
	case "done":
		return 2
	case "idle":
		// Must outrank the empty zero value, or a lone idle workspace would
		// never win the merge and its hint would silently vanish.
		return 1
	default:
		return 0
	}
}

func (m *Model) matches(sp *space) bool {
	if m.filter == "" {
		return true
	}
	needle := strings.ToLower(m.filter)
	for _, hay := range []string{sp.Label, sp.Key, sp.Note} {
		if strings.Contains(strings.ToLower(hay), needle) {
			return true
		}
	}
	return false
}

func (m *Model) selectedKey() string {
	if sp := m.selected(); sp != nil {
		return sp.Key
	}
	return ""
}

func (m *Model) selected() *space {
	switch m.layout {
	case layoutKanban:
		group := m.columnSpaces(m.col)
		if m.rowInCol < 0 || m.rowInCol >= len(group) {
			return nil
		}
		return group[m.rowInCol]

	case layoutTable:
		if m.cursor < 0 || m.cursor >= len(m.flat) {
			return nil
		}
		return m.flat[m.cursor]
	}

	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor].space
}

// cursorLimit is how far the vertical cursor may travel in the active layout.
func (m *Model) cursorLimit() int {
	if m.layout == layoutTable {
		return len(m.flat)
	}
	return len(m.rows)
}

// columnSpaces returns the spaces in one kanban column.
func (m *Model) columnSpaces(col int) []*space {
	if col < 0 || col >= len(m.board.Statuses) {
		return nil
	}
	return m.groups[m.board.Statuses[col].ID]
}

func (m *Model) currentStatus() (store.Status, bool) {
	if m.layout == layoutKanban {
		if m.col < 0 || m.col >= len(m.board.Statuses) {
			return store.Status{}, false
		}
		return m.board.Statuses[m.col], true
	}
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return store.Status{}, false
	}
	r := m.rows[m.cursor]
	if r.kind == rowEmpty {
		return store.Status{}, false
	}
	return r.status, true
}

// restoreCursor keeps the selection on the same space across a rebuild, which
// matters because changing a status moves the row to a different group.
func (m *Model) restoreCursor(key string) {
	if key == "" {
		return
	}
	if m.layout == layoutTable {
		for i, sp := range m.flat {
			if sp.Key == key {
				m.cursor = i
				return
			}
		}
		return
	}
	for i, r := range m.rows {
		if r.kind == rowSpace && r.space.Key == key {
			m.cursor = i
			return
		}
	}
}

func (m *Model) clampCursor() {
	if m.cursor >= m.cursorLimit() {
		m.cursor = m.cursorLimit() - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	visible := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *Model) listHeight() int {
	h := m.height - 5 // title, blank, two footer lines, padding
	if h < 3 {
		h = 3
	}
	return h
}

func baseName(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	if path == "" {
		return "(unknown)"
	}
	return path
}
