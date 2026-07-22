package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// The detail view exists because a row can only show a truncated note. In the
// list it sits alongside as a pane, updating as you move; in kanban the columns
// already use the full width, so it opens as a modal instead.

const (
	detailPaneMin   = 32
	detailPaneMax   = 60
	detailPaneFloor = 76 // below this terminal width the pane is not worth it
)

var detailKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

// detailPaneWidth is the room the list view gives the pane, separator
// included. Zero means no pane: either turned off, or the terminal is too
// narrow to split without crushing the list.
func (m *Model) detailPaneWidth() int {
	if m.board.HideDetail || m.layout != layoutList || m.width < detailPaneFloor {
		return 0
	}
	// The pane carries the full note, so it earns more room than the rows it
	// sits beside -- those can truncate.
	w := m.width * 2 / 5
	if w < detailPaneMin {
		w = detailPaneMin
	}
	if w > detailPaneMax {
		w = detailPaneMax
	}
	return w
}

// bodyWidth is the width left for the list itself.
func (m *Model) bodyWidth() int {
	return m.width - m.detailPaneWidth()
}

// rowWidth is what a row may actually fill. With the pane open it stops a
// column short, so a right-aligned agent hint cannot touch the separator.
func (m *Model) rowWidth() int {
	if m.detailPaneWidth() > 0 {
		return m.bodyWidth() - 1
	}
	return m.bodyWidth()
}

// detailLines renders one space's full state, wrapped to width.
func (m *Model) detailLines(sp *space, width int) []string {
	if sp == nil {
		return []string{dimStyle.Render(truncate("nothing selected", width))}
	}

	name := sp.Label
	if !sp.Live {
		name = archivedStyle.Render(truncate(name, width))
	} else {
		name = titleStyle.Render(truncate(name, width))
	}

	lines := []string{
		name,
		dimStyle.Render(strings.Repeat("─", width)),
	}

	if st, ok := m.board.StatusByID(sp.StatusID); ok {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color(st.Color)).Render(truncate(st.Label, width)))
	}
	lines = append(lines, "")

	// The note is the reason this view exists, so it gets the room it needs.
	if sp.Note != "" {
		for _, line := range wrap(sp.Note, width) {
			lines = append(lines, noteStyle.Render(line))
		}
	} else {
		lines = append(lines, dimStyle.Render(truncate("no note — press n to add one", width)))
	}
	lines = append(lines, "")

	// What happened while you were away comes first: it is the reason the row
	// was calling for attention.
	if lines2 := m.alertLines(sp.Key, width); len(lines2) > 0 {
		lines = append(lines, lines2...)
		lines = append(lines, "")
	}

	// PR context sits between the note and the machine facts: it is about the
	// work, but unlike the note it is not something you wrote.
	if pr, ok := m.prFor(sp.Key); ok {
		lines = append(lines, prDetailLines(pr, width)...)
		lines = append(lines, "")
	}

	for _, line := range wrap(abbreviate(sp.Key), width) {
		lines = append(lines, dimStyle.Render(line))
	}

	where := "archived"
	if sp.Live {
		where = "live"
		if ids := strings.Join(sp.WorkspaceIDs, ", "); ids != "" {
			where += " · " + ids
		}
		if sp.AgentStatus != "" {
			where += " · " + sp.AgentStatus
		}
	}
	lines = append(lines, dimStyle.Render(truncate(where, width)))

	if !sp.UpdatedAt.IsZero() {
		lines = append(lines, dimStyle.Render(truncate("changed "+humanAge(sp.UpdatedAt), width)))
	}
	return lines
}

// viewDetailModal is the kanban form: the same content, centred in a box.
func (m *Model) viewDetailModal() string {
	width := min(m.width-8, 54)
	if width < detailPaneMin {
		width = detailPaneMin
	}

	// lipgloss counts padding inside Width, so the content gets two fewer
	// columns than the box -- otherwise the rule wraps onto a second line.
	inner := width - 2
	body := strings.Join(m.detailLines(m.selected(), inner), "\n")
	body += "\n\n" + detailKeyStyle.Render(truncate("n note · s status · enter jump · esc close", inner))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(width).
		Render(body)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}
