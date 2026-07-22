package ui

import tea "github.com/charmbracelet/bubbletea"

// Herdr opens URLs it finds in pane content when you click them -- but only in
// panes that have not taken the mouse. This board has, for the view switcher,
// so Herdr never sees the click and the links are ours to handle.
//
// Doing it ourselves has one advantage: the visible text can be a short label
// rather than a raw URL, which at pane width would wrap across two lines and
// break the click even under Herdr's own handling.

// linkRegion is where a URL was drawn on the last frame.
type linkRegion struct {
	row    int
	x0, x1 int // inclusive
	url    string
}

// trackLinks records the URLs in a block of detail lines, given where the block
// was drawn. Called during render, read on the next click.
func (m *Model) trackLinks(lines []detailLine, originRow, x0, x1 int) {
	for i, l := range lines {
		if l.url == "" {
			continue
		}
		m.links = append(m.links, linkRegion{row: originRow + i, x0: x0, x1: x1, url: l.url})
	}
}

// resetLinks clears the regions at the start of a frame.
func (m *Model) resetLinks() { m.links = m.links[:0] }

// linkAt finds the URL drawn at a point, if any.
func (m *Model) linkAt(x, y int) string {
	for _, r := range m.links {
		if y == r.row && x >= r.x0 && x <= r.x1 {
			return r.url
		}
	}
	return ""
}

// openLinkAt opens whatever was clicked, reporting whether anything was.
func (m *Model) openLinkAt(x, y int) (tea.Cmd, bool) {
	url := m.linkAt(x, y)
	if url == "" {
		return nil, false
	}
	m.status = "opening " + url
	return func() tea.Msg {
		if err := openURL(url); err != nil {
			return errMsg{err}
		}
		return nil
	}, true
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
