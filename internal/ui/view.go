package ui

import (
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	noteStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	labelStyle    = lipgloss.NewStyle()
	archivedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	keyStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	focusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	grabStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	branchStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("108"))
)

// View renders the board.
func (m *Model) View() string {
	// Link regions are rebuilt every frame, so a click is always tested
	// against what is on screen now rather than a stale layout.
	m.resetLinks()
	return m.overlayMenu(m.viewFrame())
}

func (m *Model) viewFrame() string {
	if m.quitting {
		return ""
	}

	switch m.mode {
	case modeHelp:
		return m.viewHelp()
	case modeManage, modeManageAdd, modeManageRename:
		return m.viewManage()
	case modeStatusPick:
		return m.viewPicker()
	case modeDetail:
		return m.viewDetailModal()
	}

	switch m.layout {
	case layoutKanban:
		return m.viewKanbanBoard()
	case layoutTable:
		return m.viewTable()
	}

	var b strings.Builder
	b.WriteString(m.viewHeader())
	b.WriteString("\n\n")

	height := m.listHeight()
	left := make([]string, height)
	end := min(m.offset+height, len(m.rows))
	for i := m.offset; i < end; i++ {
		left[i-m.offset] = m.renderRow(i)
	}

	paneWidth := m.detailPaneWidth()
	if paneWidth == 0 {
		for _, line := range left {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString(m.viewFooter())
		return b.String()
	}

	// The pane tracks the cursor, so it always describes what you are looking
	// at without needing to open anything.
	inner := paneWidth - 3
	right := m.detailLines(m.selected(), inner)
	body := m.bodyWidth()

	// The pane starts two rows down (header, blank) and to the right of the
	// separator, which is where its links are drawn.
	m.trackLinks(right, 2, body+1, m.width-1)

	for i := 0; i < height; i++ {
		cell := ""
		if i < len(right) {
			cell = right[i].text
		}
		b.WriteString(padCell(truncateStyled(left[i], body-1), body-1))
		b.WriteString(dimStyle.Render("│ "))
		b.WriteString(strings.TrimRight(cell, " "))
		b.WriteString("\n")
	}

	b.WriteString(m.viewFooter())
	return b.String()
}

func (m *Model) viewHeader() string {
	// Count from the groups rather than the rows, so a collapsed group or a
	// scrolled-off column still contributes.
	var live, archived int
	for _, group := range m.groups {
		for _, sp := range group {
			if sp.Live {
				live++
			} else {
				archived++
			}
		}
	}

	// The title names the current view and opens the switcher.
	left := " " + titleStyle.Render(m.title())
	if st, ok := m.board.StatusByID(m.statusFilter); ok {
		left += lipgloss.NewStyle().Foreground(lipgloss.Color(st.Color)).Render("  " + st.Label + " only")
	}
	if m.filter != "" {
		left += dimStyle.Render(fmt.Sprintf("  /%s", m.filter))
	}

	right := ""
	if b := m.bellSummary(); b != "" {
		right += b + dimStyle.Render(" · ")
	}
	right += fmt.Sprintf("%d live", live)
	if m.showArchive {
		right += fmt.Sprintf(" · %d archived", archived)
	} else {
		right += " · archive hidden"
	}
	right += " "

	return joinEnds(left, dimStyle.Render(right), m.width)
}

func (m *Model) viewFooter() string {
	if m.err != nil {
		return errStyle.Render(" " + truncate(m.err.Error(), m.width-2))
	}

	switch m.mode {
	case modeNote:
		return keyStyle.Render(" note: ") + m.input.View()
	case modeRename:
		return keyStyle.Render(" rename: ") + m.input.View()
	case modeMessage:
		return keyStyle.Render(" to agent: ") + m.input.View()
	case modeFilter:
		return keyStyle.Render(" filter: ") + m.input.View()
	}

	// The numbered statuses are the fastest way to file something, so show the
	// actual mapping rather than a generic "1-9".
	var keys strings.Builder
	keys.WriteString(" ")
	for i, st := range m.board.Statuses {
		if i >= 9 {
			break
		}
		if i > 0 {
			keys.WriteString(dimStyle.Render("  "))
		}
		numbered := lipgloss.NewStyle().Foreground(lipgloss.Color(st.Color))
		keys.WriteString(numbered.Render(fmt.Sprintf("%d %s", i+1, st.Label)))
	}

	var hint string
	switch {
	case m.status != "":
		hint = truncate(m.status, m.width-2)
	case m.grabbed != "" && m.layout == layoutKanban:
		hint = "h/l move between columns to retag · j/k reorder · enter drop"
	case m.grabbed != "":
		hint = "j/k move · across a group changes status · enter drop"
	case m.layout == layoutKanban:
		hint = "K list · d detail · v move · n note · enter jump · ? help"
	case m.layout == layoutTable:
		hint = "K kanban · o sort · d detail · v move · n note · enter jump · ? help"
	case m.board.HideDetail:
		hint = "K table · d detail · v move · n note · enter jump · ? help"
	default:
		hint = "K table · v move · n note · gp open PR · enter jump · ? help"
	}
	return keys.String() + "\n" + dimStyle.Render(" "+hint)
}

func (m *Model) renderRow(i int) string {
	r := m.rows[i]
	selected := i == m.cursor

	switch r.kind {
	case rowEmpty:
		return dimStyle.Render("  no spaces yet — open a workspace in Herdr and it appears here")

	case rowHeader:
		arrow := "▾"
		if m.board.IsCollapsed(r.status.ID) && m.filter == "" {
			arrow = "▸"
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(r.status.Color)).Bold(true)
		line := fmt.Sprintf("%s %s", arrow, r.status.Label)
		out := " " + style.Render(line) + dimStyle.Render(fmt.Sprintf(" (%d)", r.count))
		if selected {
			return cursorStyle.Render("❯") + out[1:]
		}
		return out
	}

	sp := r.space
	held := sp.Key == m.grabbed

	prefix := "   "
	switch {
	case held:
		prefix = grabStyle.Render(" ▌ ")
	case selected:
		prefix = cursorStyle.Render(" ❯ ")
	}

	name := sp.Label
	if b := m.bellFor(sp.Key); b != "" {
		name = bellGlyph + " " + name
	}
	nameStyled := labelStyle.Render(pad(name, 22))
	switch {
	case held:
		nameStyled = grabStyle.Render(pad(name, 22))
	case !sp.Live:
		nameStyled = archivedStyle.Render(pad(name, 22))
	case sp.Focused:
		nameStyled = focusStyle.Render(pad(name, 22))
	}

	// The note is the point of the "waiting" status, so it wins the middle
	// column whenever there is one; otherwise show where the space lives.
	detail := abbreviate(sp.Key)
	detailStyle := dimStyle
	if sp.Note != "" {
		detail = sp.Note
		detailStyle = noteStyle
	}

	hint := agentHint(sp)
	// Rows share the width with the detail pane when it is open.
	body := m.rowWidth()
	room := body - 3 - 22 - lipgloss.Width(hint) - 2
	if room < 8 {
		room = 8
	}

	line := prefix + nameStyled + " " + detailStyle.Render(truncate(detail, room))
	return joinEnds(line, dimStyle.Render(hint+" "), body)
}

// agentHint is the dim secondary signal. It never groups or sorts anything --
// the board tracks the user's own work, not the agent's.
func agentHint(sp *space) string {
	if !sp.Live {
		return "offline"
	}
	switch sp.AgentStatus {
	case "working", "blocked", "done", "idle":
		return "·" + sp.AgentStatus
	default:
		return ""
	}
}

func (m *Model) viewPicker() string {
	var b strings.Builder
	name := "space"
	if sp := m.selected(); sp != nil {
		name = sp.Label
	}
	b.WriteString(titleStyle.Render(" Set status") + dimStyle.Render("  "+name) + "\n\n")

	for i, st := range m.board.Statuses {
		cursor := "   "
		if i == m.manageIdx {
			cursor = cursorStyle.Render(" ❯ ")
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(st.Color))
		b.WriteString(cursor + dimStyle.Render(fmt.Sprintf("%d ", i+1)) + style.Render(st.Label) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render(" enter select · esc cancel"))
	return b.String()
}

func (m *Model) viewManage() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" Statuses") + dimStyle.Render("  order here is the order on the board") + "\n\n")

	for i, st := range m.board.Statuses {
		cursor := "   "
		if i == m.manageIdx {
			cursor = cursorStyle.Render(" ❯ ")
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(st.Color))
		count := 0
		for _, e := range m.board.Entries {
			if e.Status == st.ID {
				count++
			}
		}
		noun := "spaces"
		if count == 1 {
			noun = "space"
		}
		marker := pad("", 9)
		if m.board.IsDefaultStatus(st.ID) {
			marker = keyStyle.Render(pad("default", 9))
		}
		b.WriteString(cursor + style.Render(pad(st.Label, 24)) + marker +
			dimStyle.Render(fmt.Sprintf("%d %s", count, noun)) + "\n")
	}

	b.WriteString("\n")
	switch m.mode {
	case modeManageAdd:
		b.WriteString(keyStyle.Render(" new status: ") + m.input.View())
	case modeManageRename:
		b.WriteString(keyStyle.Render(" rename to: ") + m.input.View())
	default:
		b.WriteString(dimStyle.Render(" a add · r rename · d delete · D set default · J/K reorder · esc back"))
		if m.status != "" {
			b.WriteString("\n" + dimStyle.Render(" "+m.status))
		}
	}
	return b.String()
}

func (m *Model) viewHelp() string {
	rows := [][2]string{
		{"K", "cycle the view: list → table → kanban"},
		{"o", "table only: sort by status, name, or when it last changed"},
		{"d", "list: show or hide the detail pane · elsewhere: detail modal"},
		{"j / k", "move"},
		{"gg / G", "first row · last row"},
		{"gp", "open the pull request in a browser"},
		{"h / l", "kanban: move between columns · list: collapse / expand"},
		{"v", "grab a row, then move it — leaving its group changes its status"},
		{"enter", "jump to space (reopens archived ones)"},
		{"1-9", "send to that status, numbered along the bottom"},
		{"s", "status picker"},
		{"n", "edit note — who or what you are waiting on"},
		{"R", "rename the space — renames the Herdr workspace too"},
		{"m", "type a message into that space's agent, then go there to send it"},
		{"space", "collapse / expand group"},
		{"F", "show only the status under the cursor — F or esc for all"},
		{"O", "reorder Herdr's own Spaces sidebar to match this board"},
		{"a", "show or hide archived spaces"},
		{"/", "filter by name, path or note"},
		{"S", "manage statuses (add, rename, reorder, delete)"},
		{"x", "forget the selected space"},
		{"r", "refresh"},
		{"q", "quit"},
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(" Board") + "\n\n")
	for _, r := range rows {
		b.WriteString("   " + keyStyle.Render(pad(r[0], 10)) + dimStyle.Render(r[1]) + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("   status is yours; the dim right column is Herdr's agent state\n"))
	b.WriteString("\n" + dimStyle.Render(" any key to go back"))
	return b.String()
}

// --- text helpers ---

func pad(s string, n int) string {
	s = truncate(s, n)
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	if n <= 1 {
		return string(r[:1])
	}
	return string(r[:min(len(r), n-1)]) + "…"
}

// truncateStyled clips an already-styled string without cutting through an
// escape sequence, which plain slicing would do.
func truncateStyled(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "")
}

// joinEnds puts left and right on one line, right-aligned to width.
func joinEnds(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// abbreviate shortens a home-relative path for display.
func abbreviate(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
