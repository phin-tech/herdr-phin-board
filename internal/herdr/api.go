package herdr

import (
	"fmt"
	"sort"
)

// Workspace is one entry from a session snapshot. Note that the API's workspace
// object carries no cwd -- that lives on panes, so Snapshot fills Cwd in from
// the workspace's panes.
type Workspace struct {
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
	ActiveTabID string `json:"active_tab_id"`
	PaneCount   int    `json:"pane_count"`
	TabCount    int    `json:"tab_count"`

	Cwd string `json:"-"`
}

type pane struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Cwd         string `json:"cwd"`
}

type snapshotResult struct {
	Snapshot struct {
		Workspaces []Workspace `json:"workspaces"`
		Panes      []pane      `json:"panes"`
		Version    string      `json:"version"`
	} `json:"snapshot"`
}

// Workspaces returns every live workspace with its directory resolved.
func (c *Client) Workspaces() ([]Workspace, error) {
	var res snapshotResult
	if err := c.Request("session.snapshot", map[string]any{}, &res); err != nil {
		return nil, err
	}
	snap := res.Snapshot

	// Prefer a pane on the workspace's active tab; fall back to any pane. Pane
	// IDs sort lexically within a workspace, so the first match is stable.
	byWorkspace := map[string][]pane{}
	for _, p := range snap.Panes {
		byWorkspace[p.WorkspaceID] = append(byWorkspace[p.WorkspaceID], p)
	}
	for _, panes := range byWorkspace {
		sort.Slice(panes, func(i, j int) bool { return panes[i].PaneID < panes[j].PaneID })
	}

	out := make([]Workspace, 0, len(snap.Workspaces))
	for _, ws := range snap.Workspaces {
		for _, p := range byWorkspace[ws.ID] {
			if p.Cwd == "" {
				continue
			}
			if ws.Cwd == "" || p.TabID == ws.ActiveTabID {
				ws.Cwd = p.Cwd
			}
			if p.TabID == ws.ActiveTabID {
				break
			}
		}
		out = append(out, ws)
	}
	return out, nil
}

// FocusWorkspace brings a workspace to the foreground.
func (c *Client) FocusWorkspace(id string) error {
	return c.Request("workspace.focus", map[string]any{"workspace_id": id}, nil)
}

// CreateWorkspace opens a new workspace rooted at cwd and returns its id.
func (c *Client) CreateWorkspace(cwd, label string) (string, error) {
	params := map[string]any{"cwd": cwd, "focus": true}
	if label != "" {
		params["label"] = label
	}
	var res struct {
		Workspace Workspace `json:"workspace"`
	}
	if err := c.Request("workspace.create", params, &res); err != nil {
		return "", err
	}
	return res.Workspace.ID, nil
}

// MetadataSource identifies this plugin's metadata reports to Herdr, so our
// tokens never collide with another reporter's.
const MetadataSource = "board"

// ReportToken sets or clears one workspace token. A nil value clears it.
// Tokens are what surface the status in Herdr's own Space rows via a
// [ui.sidebar.spaces] row entry like "$status".
func (c *Client) ReportToken(workspaceID, name string, value *string) error {
	if workspaceID == "" {
		return fmt.Errorf("report token %q: empty workspace id", name)
	}
	return c.Request("workspace.report_metadata", map[string]any{
		"workspace_id": workspaceID,
		"source":       MetadataSource,
		"tokens":       map[string]*string{name: value},
	}, nil)
}

// PopupClose dismisses the popup this process is rendering into. Quitting the
// TUI usually suffices, but an explicit close keeps focus restoration crisp
// when the board opened another workspace.
func (c *Client) PopupClose() error {
	return c.Request("popup.close", map[string]any{}, nil)
}
