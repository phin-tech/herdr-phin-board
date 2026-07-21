package ui

import (
	"fmt"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
)

// g is a chord prefix, vim style: gg jumps to the top, gp opens the pull
// request. Bare g used to jump to the top on its own, which was never quite
// vim and left no room for a second key.

// openURL launches a URL in the user's browser. A variable so tests can
// observe it without opening anything.
var openURL = func(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, url).Start()
}

// chordModes are the modes where g means a chord rather than a letter.
func (m *Model) chordMode() bool {
	return m.mode == modeNormal || m.mode == modeDetail
}

// handleChord consumes a key when a chord is pending, or starts one. The bool
// reports whether the key was dealt with here.
func (m *Model) handleChord(msg tea.KeyMsg) (bool, tea.Cmd) {
	if !m.chordMode() {
		return false, nil
	}
	key := msg.String()

	if m.chord == "g" {
		m.chord = ""
		switch key {
		case "g":
			m.cursor = 0
			m.rowInCol = 0
			m.clampCursor()
			m.clampColumnCursor()
		case "p":
			return true, m.openPR()
		case "esc":
			// Cancelled; nothing to do.
		default:
			// An unknown chord does nothing rather than falling through to
			// whatever that key means on its own, which would be a surprise.
			m.status = "g" + key + " is not a command"
		}
		return true, nil
	}

	if key == "g" {
		m.chord = "g"
		m.status = "g… gg top · gp open pull request"
		return true, nil
	}
	return false, nil
}

// openPR opens the selected space's pull request in a browser.
func (m *Model) openPR() tea.Cmd {
	sp := m.selected()
	if sp == nil {
		m.status = "no space selected"
		return nil
	}

	pr, ok := m.prFor(sp.Key)
	if !ok {
		m.status = "no pull request for " + sp.Label
		return nil
	}
	if pr.URL == "" {
		// Cached before the URL was recorded, or gh returned none.
		m.status = fmt.Sprintf("#%d has no URL recorded — press r to refresh", pr.Number)
		return nil
	}

	url := pr.URL
	m.status = fmt.Sprintf("opening #%d", pr.Number)
	return func() tea.Msg {
		if err := openURL(url); err != nil {
			return errMsg{err}
		}
		return nil
	}
}
