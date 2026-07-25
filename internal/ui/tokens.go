package ui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Tokens are how the board reaches Herdr's own sidebar: a status word and a PR
// summary per workspace. Each one is a socket round trip, and Herdr does the
// work at the far end, so what matters here is not sending what it already has.
// A thirty-space board otherwise pays thirty round trips every time a single
// row moves -- and pays them again on the next refresh, and the one after.

// tokenID names one token on one workspace.
type tokenID struct{ workspace, name string }

// tokenCleared stands for a token pushed as absent, which keeps "cleared"
// distinct from "never pushed" -- an id absent from the map -- without needing
// a pointer to tell them apart.
const tokenCleared = "\x00cleared"

type tokenPush struct {
	id    tokenID
	value string // tokenCleared to clear
}

// tokensSyncedMsg reports what actually reached Herdr. Recording it back here
// rather than inside the command keeps m.sent off the command's goroutine, and
// means a push that failed is retried next time instead of assumed done.
type tokensSyncedMsg struct{ sent []tokenPush }

// unsent drops the pushes Herdr already has.
func (m *Model) unsent(pushes []tokenPush) []tokenPush {
	var out []tokenPush
	for _, p := range pushes {
		if was, ok := m.sent[p.id]; ok && was == p.value {
			continue
		}
		out = append(out, p)
	}
	return out
}

// pushTokens sends whatever Herdr does not already have, one round trip each.
func (m *Model) pushTokens(pushes []tokenPush) tea.Cmd {
	if m.client == nil {
		return nil
	}
	pushes = m.unsent(pushes)
	if len(pushes) == 0 {
		return nil
	}

	client := m.client
	return func() tea.Msg {
		var done []tokenPush
		for _, p := range pushes {
			var value *string
			if p.value != tokenCleared {
				v := p.value
				value = &v
			}
			if err := client.ReportToken(p.id.workspace, p.id.name, value); err == nil {
				done = append(done, p)
			}
		}
		return tokensSyncedMsg{sent: done}
	}
}

// recordTokens remembers what landed, so the next sync can skip it.
func (m *Model) recordTokens(pushes []tokenPush) {
	for _, p := range pushes {
		m.sent[p.id] = p.value
	}
}

// forgetTokens drops the record for workspaces that are no longer live.
func (m *Model) forgetTokens(live map[string]bool) {
	for id := range m.sent {
		if !live[id.workspace] {
			delete(m.sent, id)
		}
	}
}
