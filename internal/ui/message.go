package ui

import (
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/herdr"
)

// `m` types a message into the agent running in the selected space, without
// submitting it. The board knows which space is which and what you are waiting
// on; this closes the loop from noticing to saying something.

var (
	errNoAgentHere  = errors.New("no agent in that space")
	errSeveralHere  = errors.New("several agents in that space — focus the one you mean")
	errAgentUnknown = errors.New("that space is not open, so it has no agent")
)

// pickAgent finds the single agent to send to for a space.
//
// Panes with no agent field are shells and plugin sidebars, and this board is
// itself one of them, so they are excluded. Ambiguity is refused rather than
// guessed: sending a review comment to the wrong agent is worse than not
// sending it.
func pickAgent(agents []herdr.Agent, workspaceIDs []string) (herdr.Agent, error) {
	if len(workspaceIDs) == 0 {
		return herdr.Agent{}, errAgentUnknown
	}
	wanted := map[string]bool{}
	for _, id := range workspaceIDs {
		wanted[id] = true
	}

	var found []herdr.Agent
	for _, a := range agents {
		if a.Agent == nil || *a.Agent == "" {
			continue
		}
		if wanted[a.WorkspaceID] {
			found = append(found, a)
		}
	}

	switch len(found) {
	case 0:
		return herdr.Agent{}, errNoAgentHere
	case 1:
		return found[0], nil
	default:
		return herdr.Agent{}, errSeveralHere
	}
}

type agentSentMsg struct {
	label string
	pane  string
}

// sendToAgent types the message into the space's agent and goes there, so the
// reviewer can add a line and submit it themselves.
func (m *Model) sendToAgent(text string) tea.Cmd {
	sp := m.selected()
	if sp == nil || text == "" {
		return nil
	}
	if m.client == nil {
		return nil
	}

	client := m.client
	ids := append([]string(nil), sp.WorkspaceIDs...)
	label := sp.Label

	return func() tea.Msg {
		agents, err := client.Agents()
		if err != nil {
			return errMsg{err}
		}
		agent, err := pickAgent(agents, ids)
		if err != nil {
			return statusMsg(fmt.Sprintf("%s: %s", label, err))
		}
		if err := client.SendToAgent(agent.PaneID, text); err != nil {
			return errMsg{err}
		}
		return agentSentMsg{label: label, pane: agent.PaneID}
	}
}

// focusAgentAndQuit hands over to the agent pane: the text is waiting in its
// input, unsent, for the user to finish and submit.
func (m *Model) focusAgentAndQuit(pane string) tea.Cmd {
	client := m.client
	if m.cancel != nil {
		m.cancel()
	}
	m.quitting = true

	return tea.Sequence(
		func() tea.Msg {
			_ = client.FocusAgent(pane)
			return nil
		},
		tea.Quit,
	)
}
