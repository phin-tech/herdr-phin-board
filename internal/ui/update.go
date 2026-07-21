package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/store"
)

// Update routes messages, dispatching keys to whichever mode is active.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampCursor()
		return m, nil

	case workspacesMsg:
		m.live = msg
		m.err = nil
		m.rebuild()
		return m, m.syncTokens()

	case eventMsg:
		// Any workspace change invalidates the list; refetch and keep listening.
		return m, tea.Batch(m.refresh(), waitForEvent(m.events))

	case eventsDoneMsg:
		return m, nil

	case tokensSyncedMsg:
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case statusMsg:
		m.status = string(msg)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m.quit()
	}

	switch m.mode {
	case modeNormal:
		return m.handleNormalKey(msg)
	case modeStatusPick:
		return m.handlePickKey(msg)
	case modeManage:
		return m.handleManageKey(msg)
	case modeDetail:
		return m.handleDetailKey(msg)
	case modeNote, modeFilter, modeManageAdd, modeManageRename:
		return m.handleInputKey(msg)
	case modeHelp:
		m.mode = modeNormal
		return m, nil
	}
	return m, nil
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	if m.cancel != nil {
		m.cancel()
	}
	return m, tea.Quit
}

func (m *Model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""

	switch key := msg.String(); key {
	case "q", "esc":
		if m.grabbed != "" {
			m.grabbed = ""
			m.status = ""
			return m, nil
		}
		if m.filter != "" {
			m.filter = ""
			m.rebuild()
			return m, nil
		}
		return m.quit()

	case "v":
		m.toggleGrab()

	case "K":
		m.toggleLayout()

	case "o":
		m.cycleSort()

	case "d":
		// Only the list has room alongside for a pane. The table and kanban
		// both use the full width, so there detail opens over the board.
		if m.layout != layoutList {
			if !m.requireSpace() {
				return m, nil
			}
			m.mode = modeDetail
			return m, nil
		}
		m.board.HideDetail = !m.board.HideDetail
		m.save()

	case "j", "down":
		if m.grabbed != "" {
			m.moveGrabbed(1)
			return m, m.syncTokens()
		}
		m.moveCursor(1)
	case "k", "up":
		if m.grabbed != "" {
			m.moveGrabbed(-1)
			return m, m.syncTokens()
		}
		m.moveCursor(-1)
	case "g", "home":
		m.cursor = 0
		m.clampCursor()
	case "G", "end":
		m.cursor = m.cursorLimit() - 1
		m.clampCursor()
	case "ctrl+d", "pgdown":
		m.cursor += m.listHeight() / 2
		m.clampCursor()
	case "ctrl+u", "pgup":
		m.cursor -= m.listHeight() / 2
		m.clampCursor()

	case "enter":
		// While holding a row, enter means "drop it here" rather than "jump".
		if m.grabbed != "" {
			m.grabbed = ""
			m.status = ""
			return m, nil
		}
		return m.openSelected()

	case "h", "left", "l", "right":
		delta := 1
		if key == "h" || key == "left" {
			delta = -1
		}
		// Sideways is column movement in kanban, and collapse in the list --
		// there is nothing to move between when the groups are stacked.
		if m.layout == layoutKanban {
			if m.grabbed != "" {
				m.moveGrabbedAcross(delta)
				return m, m.syncTokens()
			}
			m.col += delta
			m.rowInCol = 0
			m.clampColumnCursor()
			return m, nil
		}
		if m.layout == layoutTable {
			return m, nil
		}
		return m.toggleCurrentGroup()

	case " ", "tab":
		// Only the list has groups to fold.
		if m.layout != layoutList {
			return m, nil
		}
		return m.toggleCurrentGroup()

	case "s":
		if !m.requireSpace() {
			return m, nil
		}
		m.mode = modeStatusPick
		m.manageIdx = m.statusIndex(m.selected().StatusID)

	case "n":
		if !m.requireSpace() {
			return m, nil
		}
		sp := m.selected()
		m.mode = modeNote
		m.input.SetValue(sp.Note)
		m.input.CursorEnd()
		m.input.Focus()

	case "/":
		m.mode = modeFilter
		m.input.SetValue(m.filter)
		m.input.CursorEnd()
		m.input.Focus()

	case "a":
		m.showArchive = !m.showArchive
		m.rebuild()

	case "S":
		m.mode = modeManage
		m.manageIdx = 0

	case "x":
		return m.forgetSelected()

	case "r":
		return m, m.refresh()

	case "?":
		m.mode = modeHelp

	default:
		// 1-9 assign a status by its position on the board.
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			idx := int(key[0] - '1')
			if idx < len(m.board.Statuses) {
				if !m.requireSpace() {
					return m, nil
				}
				return m.applyStatus(m.board.Statuses[idx])
			}
		}
	}
	return m, nil
}

func (m *Model) handlePickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.mode = m.inputParentMode()
	case "j", "down":
		if m.manageIdx < len(m.board.Statuses)-1 {
			m.manageIdx++
		}
	case "k", "up":
		if m.manageIdx > 0 {
			m.manageIdx--
		}
	case "enter":
		selected := m.manageIdx < len(m.board.Statuses)
		m.mode = m.inputParentMode()
		if selected {
			return m.applyStatus(m.board.Statuses[m.manageIdx])
		}
	}
	return m, nil
}

func (m *Model) handleManageKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "S":
		m.mode = modeNormal
		m.rebuild()

	case "j", "down":
		if m.manageIdx < len(m.board.Statuses)-1 {
			m.manageIdx++
		}
	case "k", "up":
		if m.manageIdx > 0 {
			m.manageIdx--
		}

	case "J":
		if st, ok := m.statusAt(m.manageIdx); ok {
			m.board.MoveStatus(st.ID, 1)
			if m.manageIdx < len(m.board.Statuses)-1 {
				m.manageIdx++
			}
			m.save()
		}
	case "K":
		if st, ok := m.statusAt(m.manageIdx); ok {
			m.board.MoveStatus(st.ID, -1)
			if m.manageIdx > 0 {
				m.manageIdx--
			}
			m.save()
		}

	case "a":
		m.mode = modeManageAdd
		m.input.SetValue("")
		m.input.Focus()

	case "r":
		if st, ok := m.statusAt(m.manageIdx); ok {
			m.mode = modeManageRename
			m.input.SetValue(st.Label)
			m.input.CursorEnd()
			m.input.Focus()
		}

	case "d":
		if st, ok := m.statusAt(m.manageIdx); ok {
			if err := m.board.RemoveStatus(st.ID); err != nil {
				m.status = err.Error()
			} else {
				m.status = fmt.Sprintf("removed %q; its spaces moved to %s", st.Label, m.board.Statuses[0].Label)
				if m.manageIdx >= len(m.board.Statuses) {
					m.manageIdx = len(m.board.Statuses) - 1
				}
				m.save()
			}
		}
	}
	return m, nil
}

// handleDetailKey drives the kanban modal. It keeps the actions you would
// reach for while reading a note, so you do not have to close it first.
func (m *Model) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "esc", "q", "d":
		m.mode = modeNormal

	case "n":
		if sp := m.selected(); sp != nil {
			m.prevMode = modeDetail
			m.mode = modeNote
			m.input.SetValue(sp.Note)
			m.input.CursorEnd()
			m.input.Focus()
		}

	case "s":
		if sp := m.selected(); sp != nil {
			m.prevMode = modeDetail
			m.mode = modeStatusPick
			m.manageIdx = m.statusIndex(sp.StatusID)
		}

	case "j", "down", "k", "up":
		// Browse without closing: the modal follows the selection.
		delta := 1
		if key == "k" || key == "up" {
			delta = -1
		}
		m.moveCursor(delta)

	case "enter":
		m.mode = modeNormal
		return m.openSelected()

	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			idx := int(key[0] - '1')
			if idx < len(m.board.Statuses) && m.selected() != nil {
				return m.applyStatus(m.board.Statuses[idx])
			}
		}
	}
	return m, nil
}

func (m *Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.input.Blur()
		if m.mode == modeFilter {
			m.filter = ""
			m.rebuild()
		}
		m.mode = m.inputParentMode()
		return m, nil

	case "enter":
		value := strings.TrimSpace(m.input.Value())
		m.input.Blur()
		mode := m.mode
		m.mode = m.inputParentMode()

		switch mode {
		case modeNote:
			if sp := m.selected(); sp != nil {
				m.ensureEntry(sp)
				m.board.SetNote(sp.Key, value)
				m.save()
				m.rebuild()
			}
		case modeFilter:
			m.filter = value
			m.rebuild()
		case modeManageAdd:
			if value != "" {
				st := m.board.AddStatus(value, nextColor(len(m.board.Statuses)))
				m.manageIdx = m.statusIndex(st.ID)
				m.save()
			}
		case modeManageRename:
			if value != "" {
				if st, ok := m.statusAt(m.manageIdx); ok {
					m.board.RenameStatus(st.ID, value)
					m.save()
				}
			}
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)

	// The filter is live: every keystroke refilters the board.
	if m.mode == modeFilter {
		m.filter = m.input.Value()
		m.rebuild()
	}
	return m, cmd
}

// inputParentMode is where an input or picker returns to when it closes --
// normally the board, but the kanban modal reopens so reading is uninterrupted.
func (m *Model) inputParentMode() mode {
	switch m.mode {
	case modeManageAdd, modeManageRename:
		return modeManage
	default:
		if m.prevMode == modeDetail {
			m.prevMode = modeNormal
			return modeDetail
		}
		return modeNormal
	}
}

// applyStatus records a status for the selected space and mirrors it into
// Herdr's workspace tokens.
func (m *Model) applyStatus(st store.Status) (tea.Model, tea.Cmd) {
	sp := m.selected()
	if sp == nil {
		return m, nil
	}
	m.board.SetStatus(sp.Key, st.ID, sp.Label)
	m.save()
	m.rebuild()
	m.status = fmt.Sprintf("%s → %s", sp.Label, st.Label)
	return m, m.syncTokens()
}

// ensureEntry makes sure a space has a stored entry before a note is attached,
// so noting an untouched space does not silently drop its status.
func (m *Model) ensureEntry(sp *space) {
	if _, ok := m.board.Entries[sp.Key]; !ok {
		statusID := sp.StatusID
		if statusID == "" {
			statusID = m.board.DefaultStatusID()
		}
		m.board.SetStatus(sp.Key, statusID, sp.Label)
	}
}

// openSelected jumps to the space: focus it if it is live, or reopen a
// workspace at its directory if it is archived.
func (m *Model) openSelected() (tea.Model, tea.Cmd) {
	sp := m.selected()
	if sp == nil {
		return m, nil
	}
	client := m.client
	key, label, id := sp.Key, sp.Label, sp.workspaceID()

	if m.cancel != nil {
		m.cancel()
	}
	m.quitting = true

	return m, tea.Sequence(
		func() tea.Msg {
			if id != "" {
				_ = client.FocusWorkspace(id)
			} else {
				_, _ = client.CreateWorkspace(key, label)
			}
			return nil
		},
		tea.Quit,
	)
}

// forgetSelected drops a space's stored status. Live spaces reappear
// immediately with the default status; archived ones vanish for good.
func (m *Model) forgetSelected() (tea.Model, tea.Cmd) {
	sp := m.selected()
	if sp == nil {
		return m, nil
	}
	delete(m.board.Entries, sp.Key)
	m.save()

	var cmd tea.Cmd
	if id := sp.workspaceID(); id != "" {
		client := m.client
		cmd = func() tea.Msg {
			_ = client.ReportToken(id, "status", nil)
			return tokensSyncedMsg{}
		}
	}
	m.status = fmt.Sprintf("forgot %s", sp.Label)
	m.rebuild()
	return m, cmd
}

// moveCursor steps the selection, in whichever layout is active.
func (m *Model) moveCursor(delta int) {
	if m.layout == layoutKanban {
		m.rowInCol += delta
		m.clampColumnCursor()
		return
	}
	m.cursor += delta
	m.clampCursor()
}

func (m *Model) toggleCurrentGroup() (tea.Model, tea.Cmd) {
	if m.grabbed != "" {
		return m, nil
	}
	if st, ok := m.currentStatus(); ok {
		m.board.ToggleCollapsed(st.ID)
		m.save()
		m.rebuild()
	}
	return m, nil
}

// toggleLayout cycles list -> table -> kanban, keeping the same space selected
// and remembering the choice for next time.
func (m *Model) toggleLayout() {
	selected := m.selectedKey()

	m.layout = m.layout.next()
	m.board.Layout = m.layout.String()
	m.save()

	m.rebuild()
	m.restoreCursor(selected)
	m.restoreColumnCursor(selected)
	m.clampCursor()
	m.clampColumnCursor()
	m.status = "view: " + m.layout.String()
}

// cycleSort reorders the table. Grab-moves are only coherent when rows sit in
// status order, so switching away from it drops anything being held.
func (m *Model) cycleSort() {
	// Only the table has a sortable order; the list and kanban are arranged by
	// status. Saying so beats doing nothing.
	if m.layout != layoutTable {
		m.status = "sorting is a table thing — press K to get there"
		return
	}
	selected := m.selectedKey()

	m.sort = m.sort.next()
	m.board.TableSort = m.sort.String()
	m.grabbed = ""
	m.save()

	m.rebuild()
	m.restoreCursor(selected)
	m.clampCursor()
	m.status = "sorted by " + m.sort.String()
}

// requireSpace guards the actions that only make sense on a space row. Landing
// on a group header used to make them no-ops, which just looked broken.
func (m *Model) requireSpace() bool {
	if m.selected() != nil {
		return true
	}
	m.status = "that's a group header — press j to move down to a space"
	return false
}

func (m *Model) statusAt(i int) (store.Status, bool) {
	if i < 0 || i >= len(m.board.Statuses) {
		return store.Status{}, false
	}
	return m.board.Statuses[i], true
}

func (m *Model) statusIndex(id string) int {
	for i, s := range m.board.Statuses {
		if s.ID == id {
			return i
		}
	}
	return 0
}

func (m *Model) save() {
	if err := m.board.Save(); err != nil {
		m.err = err
	}
}

// palette cycles colors for statuses the user adds.
var palette = []string{"170", "39", "214", "78", "203", "111", "220", "141"}

func nextColor(n int) string {
	return palette[n%len(palette)]
}
