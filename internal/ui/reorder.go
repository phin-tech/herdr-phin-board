package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// `O` pushes the board's order onto Herdr itself, so the Spaces sidebar reads
// in the same order as the board: by status, then by however you arranged them.
//
// Herdr can filter its Agent panel by a metadata token but cannot filter or
// sort Spaces that way, and plenty of spaces have no agent at all. Moving the
// workspaces is the one mechanism that reaches the Spaces panel.
//
// It is a deliberate action rather than something the board does continuously.
// Reordering somebody's sidebar every time a status changed would fight anyone
// who arranges their spaces by hand.

type reorderedMsg struct{ moved int }

// reorderWorkspaces walks the board top to bottom and gives Herdr that order.
func (m *Model) reorderWorkspaces() tea.Cmd {
	// Board order, flattened: statuses in their order, spaces within each.
	var ids []string
	seen := map[string]bool{}
	for _, st := range m.board.Statuses {
		for _, sp := range m.groups[st.ID] {
			for _, id := range sp.WorkspaceIDs {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	// Say why nothing happened before worrying about whether there is anyone
	// to say it to: an empty board is the interesting case, not a nil client.
	if len(ids) == 0 {
		m.status = "no live spaces to reorder"
		return nil
	}
	if m.client == nil {
		return nil
	}

	client := m.client
	return func() tea.Msg {
		// Placing each in turn: everything already positioned stays put, so
		// walking the desired order front to back lands them all correctly.
		for i, id := range ids {
			if err := client.MoveWorkspace(id, i); err != nil {
				return errMsg{fmt.Errorf("reordering %s: %w", id, err)}
			}
		}
		return reorderedMsg{moved: len(ids)}
	}
}
