package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/phin-tech/herdr-phin-board/internal/alert"
)

// A bell marks a space something happened to while you were not looking. It
// clears when the cursor lands on the row -- you have seen it -- and survives
// closing the board, so nothing is lost by not looking straight away.

var bellStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

const bellGlyph = "🔔"

// bellFor renders the marker for a space, or empty when there is nothing new.
func (m *Model) bellFor(key string) string {
	if m.alerts == nil || m.alerts.Count(key) == 0 {
		return ""
	}
	return bellStyle.Render(bellGlyph)
}

// hasBell reports whether a space is asking for attention.
func (m *Model) hasBell(key string) bool {
	return m.alerts != nil && m.alerts.Count(key) > 0
}

// alertLines describe what happened, for the detail view.
func (m *Model) alertLines(key string, width int) []string {
	if m.alerts == nil {
		return nil
	}
	var out []string
	for _, a := range m.alerts.All(key) {
		out = append(out, bellStyle.Render(truncate(bellGlyph+" "+a.Text, width)))
	}
	return out
}

// clearBell marks the selected space seen. It writes only when something
// actually changed, so moving the cursor is not a stream of disk writes.
//
// The file is re-read and merged first: the watcher appends to the same store
// from another process, and a clear must not swallow an alert that landed a
// moment ago.
func (m *Model) clearBell(key string) tea.Cmd {
	if m.alerts == nil || key == "" || m.alerts.Count(key) == 0 {
		return nil
	}
	m.alerts.Clear(key)

	dir := m.stateDir
	cleared := map[string]bool{key: true}
	snapshot := m.alerts
	return func() tea.Msg {
		latest := alert.Load(dir)
		latest.Clear(key)
		latest.Merge(snapshot, cleared)
		_ = latest.Save()
		return nil
	}
}

// reloadAlerts picks up whatever the watcher has recorded since the last look.
func (m *Model) reloadAlerts() {
	if m.stateDir == "" {
		return
	}
	m.alerts = alert.Load(m.stateDir)
}

// bellSummary is the header count, so a bell on a collapsed group or an
// off-screen column is still visible.
func (m *Model) bellSummary() string {
	if m.alerts == nil {
		return ""
	}
	spaces := 0
	for _, group := range m.groups {
		for _, sp := range group {
			if m.alerts.Count(sp.Key) > 0 {
				spaces++
			}
		}
	}
	if spaces == 0 {
		return ""
	}
	return bellStyle.Render(bellGlyph + " " + strings.TrimSpace(plural(spaces, "space", "spaces")))
}

func plural(n int, one, many string) string {
	word := many
	if n == 1 {
		word = one
	}
	return fmt.Sprintf("%d %s", n, word)
}
