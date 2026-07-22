package ui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The table is the flat view: every space on one line, in aligned columns,
// including the fields the list has no room for. It has no groups and no
// collapse, so it is the view for scanning everything at once, and the only
// one you can re-sort.

type tableSort int

const (
	sortStatus tableSort = iota
	sortName
	sortChanged
)

func (s tableSort) String() string {
	switch s {
	case sortName:
		return "name"
	case sortChanged:
		return "changed"
	default:
		return "status"
	}
}

func parseSort(s string) tableSort {
	switch s {
	case "name":
		return sortName
	case "changed":
		return sortChanged
	default:
		return sortStatus
	}
}

func (s tableSort) next() tableSort { return (s + 1) % 3 }

var tableHeadStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)

// buildFlat orders every space for the table. Sorting by status reuses the
// group order exactly, so a row's position matches the list and grab-moves
// stay meaningful.
func (m *Model) buildFlat() {
	m.flat = m.flat[:0]
	for _, st := range m.board.Statuses {
		m.flat = append(m.flat, m.groups[st.ID]...)
	}
	if m.sort == sortStatus {
		return
	}

	sort.SliceStable(m.flat, func(i, j int) bool {
		a, b := m.flat[i], m.flat[j]
		switch m.sort {
		case sortName:
			return strings.ToLower(a.Label) < strings.ToLower(b.Label)
		default:
			if a.UpdatedAt.Equal(b.UpdatedAt) {
				return strings.ToLower(a.Label) < strings.ToLower(b.Label)
			}
			return a.UpdatedAt.After(b.UpdatedAt)
		}
	})
}

type tableWidths struct {
	name, status, note, pr, agent, changed int
}

func (m *Model) tableWidths() tableWidths {
	statusW := 8
	for _, st := range m.board.Statuses {
		if w := lipgloss.Width(st.Label); w > statusW {
			statusW = w
		}
	}
	statusW = min(statusW, 14)

	w := tableWidths{name: 20, status: statusW, agent: 8, changed: 10}

	// The PR column only earns its space when something on the board has one.
	if m.anyPR() {
		w.pr = 24
	}

	gaps := 4
	if w.pr > 0 {
		gaps++
	}
	fixed := w.name + w.status + w.pr + w.agent + w.changed + gaps + 3
	w.note = m.width - fixed
	if w.note < 12 {
		// Give the note room by squeezing the name before dropping columns.
		shrink := min(12-w.note, w.name-10)
		w.name -= shrink
		w.note += shrink
	}
	if w.note < 1 {
		w.note = 1
	}
	return w
}

// anyPR reports whether the board has PR context worth a column.
func (m *Model) anyPR() bool {
	if m.prCache == nil {
		return false
	}
	for _, group := range m.groups {
		for _, sp := range group {
			if _, ok := m.prFor(sp.Key); ok {
				return true
			}
		}
	}
	return false
}

func (m *Model) viewTable() string {
	var b strings.Builder
	b.WriteString(m.viewHeader())
	b.WriteString("\n")

	w := m.tableWidths()
	cols := []string{
		pad(m.sortMarker(sortName)+"SPACE", w.name),
		pad(m.sortMarker(sortStatus)+"STATUS", w.status),
		pad("NOTE", w.note),
	}
	if w.pr > 0 {
		cols = append(cols, pad("PR", w.pr))
	}
	cols = append(cols,
		pad("AGENT", w.agent),
		pad(m.sortMarker(sortChanged)+"CHANGED", w.changed),
	)
	head := "   " + strings.Join(cols, " ")
	b.WriteString(tableHeadStyle.Render(truncate(head, m.width)))
	b.WriteString("\n")

	height := m.listHeight() - 1 // the header row costs a line
	end := min(m.offset+height, len(m.flat))
	for i := m.offset; i < end; i++ {
		b.WriteString(m.renderTableRow(i, w))
		b.WriteString("\n")
	}
	for i := end - m.offset; i < height; i++ {
		b.WriteString("\n")
	}

	b.WriteString(m.viewFooter())
	return b.String()
}

// sortMarker flags which column the table is ordered by.
func (m *Model) sortMarker(s tableSort) string {
	if m.sort == s {
		return "↓"
	}
	return ""
}

func (m *Model) renderTableRow(i int, w tableWidths) string {
	sp := m.flat[i]
	held := sp.Key == m.grabbed

	prefix := "   "
	switch {
	case held:
		prefix = grabStyle.Render(" ▌ ")
	case i == m.cursor:
		prefix = cursorStyle.Render(" ❯ ")
	}

	name := pad(sp.Label, w.name)
	switch {
	case held:
		name = grabStyle.Render(name)
	case !sp.Live:
		name = archivedStyle.Render(name)
	case sp.Focused:
		name = focusStyle.Render(name)
	}

	statusCell := pad("", w.status)
	if st, ok := m.board.StatusByID(sp.StatusID); ok {
		statusCell = lipgloss.NewStyle().
			Foreground(lipgloss.Color(st.Color)).
			Render(pad(st.Label, w.status))
	}

	note := dimStyle.Render(pad("—", w.note))
	if sp.Note != "" {
		note = noteStyle.Render(pad(sp.Note, w.note))
	}

	agent := "—"
	if sp.Live && sp.AgentStatus != "" {
		agent = sp.AgentStatus
	} else if !sp.Live {
		agent = "offline"
	}

	changed := "—"
	if !sp.UpdatedAt.IsZero() {
		changed = humanAge(sp.UpdatedAt)
	}

	cells := []string{name, statusCell, note}
	if w.pr > 0 {
		cell := dimStyle.Render(pad("—", w.pr))
		if pr, ok := m.prFor(sp.Key); ok {
			cell = pad(prStyled(pr, w.pr), w.pr)
		}
		cells = append(cells, cell)
	}
	cells = append(cells,
		dimStyle.Render(pad(agent, w.agent)),
		dimStyle.Render(pad(changed, w.changed)),
	)
	return prefix + strings.Join(cells, " ")
}
