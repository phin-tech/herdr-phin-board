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

// detailLine is one rendered line, optionally standing for a URL.
//
// Herdr opens URLs in pane content on click, but only in panes that have not
// taken the mouse -- and this board has, for the view switcher. So the links
// are ours to handle: the text can then be a short label rather than a raw URL
// that would wrap and break the click anyway.
type detailLine struct {
	text string
	url  string
}

func plain(lines ...string) []detailLine {
	out := make([]detailLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, detailLine{text: l})
	}
	return out
}

// texts drops the links, for the callers that only draw.
func texts(lines []detailLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.text)
	}
	return out
}

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
func (m *Model) detailLines(sp *space, width int) []detailLine {
	if sp == nil {
		return plain(dimStyle.Render(truncate("nothing selected", width)))
	}

	name := sp.Label
	if !sp.Live {
		name = archivedStyle.Render(truncate(name, width))
	} else {
		name = titleStyle.Render(truncate(name, width))
	}

	lines := plain(name, dimStyle.Render(strings.Repeat("─", width)))

	if st, ok := m.board.StatusByID(sp.StatusID); ok {
		lines = append(lines, plain(lipgloss.NewStyle().Foreground(lipgloss.Color(st.Color)).Render(truncate(st.Label, width)))...)
	}
	lines = append(lines, plain("")...)

	// The note is the reason this view exists, so it gets the room it needs.
	if sp.Note != "" {
		for _, line := range wrap(sp.Note, width) {
			lines = append(lines, plain(noteStyle.Render(line))...)
		}
	} else {
		lines = append(lines, plain(dimStyle.Render(truncate("no note — press n to add one", width)))...)
	}
	lines = append(lines, plain("")...)

	// What happened while you were away comes first: it is the reason the row
	// was calling for attention.
	if alerts := m.alertLines(sp.Key, width); len(alerts) > 0 {
		lines = append(lines, plain(alerts...)...)
		lines = append(lines, plain("")...)
	}

	// PR context sits between the note and the machine facts: it is about the
	// work, but unlike the note it is not something you wrote.
	if pr, ok := m.prFor(sp.Key); ok {
		lines = append(lines, prDetailLines(pr, width)...)
		lines = append(lines, plain("")...)
	}

	for _, line := range wrap(abbreviate(sp.Key), width) {
		lines = append(lines, plain(dimStyle.Render(line))...)
	}
	if branch := m.branchFor(sp.Key); branch != "" {
		lines = append(lines, plain(branchStyle.Render(truncate("⎇ "+branch, width)))...)
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
	lines = append(lines, plain(dimStyle.Render(truncate(where, width)))...)

	if !sp.UpdatedAt.IsZero() {
		lines = append(lines, plain(dimStyle.Render(truncate("changed "+humanAge(sp.UpdatedAt), width)))...)
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
	content := m.detailLines(m.selected(), inner)
	body := strings.Join(texts(content), "\n")
	body += "\n\n" + detailKeyStyle.Render(truncate("n note · s status · enter jump · esc close", inner))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(width).
		Render(body)

	// Record where each line landed so a click can find its URL. The box is
	// centred, and its border and padding sit inside that.
	boxW, boxH := lipgloss.Width(box), lipgloss.Height(box)
	originX := max0((m.width - boxW) / 2)
	originY := max0((m.height - boxH) / 2)
	m.trackLinks(content, originY+1, originX+2, originX+1+inner)

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
