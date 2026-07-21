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
	name, status, note, agent, changed int
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

	// Four single-column gaps plus the three-column cursor prefix.
	fixed := w.name + w.status + w.agent + w.changed + 4 + 3
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

func (m *Model) viewTable() string {
	var b strings.Builder
	b.WriteString(m.viewHeader())
	b.WriteString("\n")

	w := m.tableWidths()
	head := "   " + strings.Join([]string{
		pad(m.sortMarker(sortName)+"SPACE", w.name),
		pad(m.sortMarker(sortStatus)+"STATUS", w.status),
		pad("NOTE", w.note),
		pad("AGENT", w.agent),
		pad(m.sortMarker(sortChanged)+"CHANGED", w.changed),
	}, " ")
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

	return prefix + strings.Join([]string{
		name,
		statusCell,
		note,
		dimStyle.Render(pad(agent, w.agent)),
		dimStyle.Render(pad(changed, w.changed)),
	}, " ")
}
