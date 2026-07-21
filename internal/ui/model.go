// Package ui implements the board TUI.
package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-board/internal/herdr"
	"github.com/phin-tech/herdr-board/internal/store"
)

type mode int

const (
	modeNormal mode = iota
	modeStatusPick
	modeNote
	modeFilter
	modeManage
	modeManageAdd
	modeManageRename
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

	StatusID  string
	Note      string
	UpdatedAt time.Time
}

func (s *space) workspaceID() string {
	if len(s.WorkspaceIDs) == 0 {
		return ""
	}
	return s.WorkspaceIDs[0]
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

	live []herdr.Workspace
	rows []row

	cursor int
	offset int
	width  int
	height int

	mode        mode
	showArchive bool
	filter      string
	input       textinput.Model
	manageIdx   int

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

	return &Model{
		client: client,
		board:  board,
		input:  in,
		width:  80,
		height: 24,
		events: make(chan herdr.Event, 64),
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
		value       string
	}
	var pushes []push
	for _, r := range m.rows {
		if r.kind != rowSpace || !r.space.Live {
			continue
		}
		st, ok := m.board.StatusByID(r.space.StatusID)
		if !ok {
			continue
		}
		for _, id := range r.space.WorkspaceIDs {
			pushes = append(pushes, push{id, st.Label})
		}
	}
	client := m.client
	return func() tea.Msg {
		for _, p := range pushes {
			value := p.value
			_ = client.ReportToken(p.workspaceID, "status", &value)
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
			sp = &space{Key: key, Label: ws.Label, Live: true}
			spaces[key] = sp
		}
		// Several workspaces can share a directory; they collapse into one row
		// because status is a property of the project, not the window.
		sp.WorkspaceIDs = append(sp.WorkspaceIDs, ws.ID)
		if ws.Focused {
			sp.Focused = true
			sp.Label = ws.Label
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
			if a.Live != b.Live {
				return a.Live
			}
			if !a.UpdatedAt.Equal(b.UpdatedAt) {
				return a.UpdatedAt.After(b.UpdatedAt)
			}
			return strings.ToLower(a.Label) < strings.ToLower(b.Label)
		})
	}

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

	m.restoreCursor(selected)

	// On first population the cursor would otherwise land on a group header,
	// where n / s / 1-9 all silently do nothing. Start on a real space.
	if !m.landed {
		if i, ok := m.firstSpaceRow(); ok {
			m.cursor = i
			m.landed = true
		}
	}

	m.clampCursor()
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
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor].space
}

func (m *Model) currentStatus() (store.Status, bool) {
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
	for i, r := range m.rows {
		if r.kind == rowSpace && r.space.Key == key {
			m.cursor = i
			return
		}
	}
}

func (m *Model) clampCursor() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
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
	h := m.height - 4 // title, blank, footer, padding
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
