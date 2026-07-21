package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The title is the view switcher: it names the view you are in and opens a
// dropdown, by click or by key. K still cycles for anyone who never reaches for
// the mouse.

// menuRow is the terminal row the title sits on, and menuFirstItemRow is where
// the dropdown's first entry lands -- directly beneath it.
const (
	menuRow          = 0
	menuFirstItemRow = 1
	menuIndent       = 1
)

var (
	menuBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	menuSelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
)

var menuLayouts = []layout{layoutList, layoutTable, layoutKanban}

// title is the clickable label: a caret plus the current view's name.
func (m *Model) title() string {
	caret := "▾"
	if m.menuOpen {
		caret = "▴"
	}
	return caret + " " + titleCase(m.layout.String())
}

// titleHit reports whether a click landed on the title.
func (m *Model) titleHit(x, y int) bool {
	if y != menuRow {
		return false
	}
	start := menuIndent
	return x >= start && x < start+lipgloss.Width(m.title())
}

// menuItemAt maps a click to a dropdown entry, or -1.
func (m *Model) menuItemAt(x, y int) int {
	if !m.menuOpen {
		return -1
	}
	i := y - menuFirstItemRow
	if i < 0 || i >= len(menuLayouts) {
		return -1
	}
	if x < menuIndent || x >= menuIndent+m.menuWidth() {
		return -1
	}
	return i
}

func (m *Model) menuWidth() int {
	w := 0
	for _, l := range menuLayouts {
		if n := lipgloss.Width(titleCase(l.String())); n > w {
			w = n
		}
	}
	return w + 4 // caret column, padding, and a little breathing room
}

// openMenu drops the switcher open with the current view highlighted.
func (m *Model) openMenu() {
	m.menuOpen = true
	for i, l := range menuLayouts {
		if l == m.layout {
			m.menuIdx = i
		}
	}
}

// chooseMenu switches to the highlighted view and closes the dropdown.
func (m *Model) chooseMenu(i int) {
	if i < 0 || i >= len(menuLayouts) {
		return
	}
	m.menuOpen = false
	if menuLayouts[i] == m.layout {
		return
	}
	m.setLayout(menuLayouts[i])
}

// menuLines renders the dropdown. It is drawn over the board rather than
// composited into it, because a dropdown obscures what sits behind it anyway.
func (m *Model) menuLines() []string {
	width := m.menuWidth()
	pad := strings.Repeat(" ", menuIndent)

	lines := make([]string, 0, len(menuLayouts))
	for i, l := range menuLayouts {
		name := titleCase(l.String())
		mark := "  "
		if l == m.layout {
			mark = "· "
		}
		row := mark + pad2(name, width-2)
		if i == m.menuIdx {
			row = menuSelStyle.Render(row)
		}
		lines = append(lines, pad+menuBorderStyle.Render("│")+row)
	}
	lines = append(lines, pad+menuBorderStyle.Render("└"+strings.Repeat("─", width)))
	return lines
}

// overlayMenu draws the dropdown onto a rendered frame, replacing whole lines.
func (m *Model) overlayMenu(frame string) string {
	if !m.menuOpen {
		return frame
	}
	lines := strings.Split(frame, "\n")
	for i, menuLine := range m.menuLines() {
		row := menuFirstItemRow + i
		if row < len(lines) {
			lines[row] = menuLine
		}
	}
	return strings.Join(lines, "\n")
}

func pad2(s string, n int) string {
	if n < 1 {
		n = 1
	}
	return pad(s, n)
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
