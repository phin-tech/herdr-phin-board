package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/gh"
	"github.com/phin-tech/herdr-phin-board/internal/herdr"
	"github.com/phin-tech/herdr-phin-board/internal/herdrtest"
)

// Herdr emits workspace.updated on every agent status tick, and a busy session
// emits them continuously. What the board does in response is what decides
// whether a large session stays usable, so these tests count round trips
// rather than inspecting output.

// wiredModel builds a model against a live fake socket, with gh stubbed out so
// no real repository or network is touched.
func wiredModel(t *testing.T) (*Model, *herdrtest.Fake) {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	f := herdrtest.Start(t)
	f.Route(map[string]any{
		"worktree.list": map[string]any{"worktrees": []map[string]any{}},
	})

	client, err := herdr.New()
	if err != nil {
		t.Fatal(err)
	}

	m := New(client, loadTestBoard(t))
	m.width, m.height = 100, 30
	// gh answers "no pull request" without running anything.
	m.gh = &gh.Client{
		Workers: 2,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no pull requests found")
		},
	}
	return m, f
}

// pump delivers a message and runs everything it sets off, feeding each result
// back in, so a test sees the same round trips the running board would make.
func pump(t *testing.T, m *Model, msg tea.Msg) {
	t.Helper()

	queue := []tea.Msg{msg}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatal("the board never settled")
		}
		next := queue[0]
		queue = queue[1:]
		if _, isTick := next.(tickMsg); isTick {
			// The tick's own rearm would block the test for a minute.
			continue
		}
		_, cmd := m.Update(next)
		queue = append(queue, drainCmd(cmd)...)
	}
}

// drainCmd runs a command and flattens whatever it produces.
func drainCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drainCmd(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// The event stream says the workspace list may have changed. When it has not,
// that must cost nothing at all: PR state ages on its own clock, branches
// change on a checkout Herdr never reports, and statuses are the board's own.
func TestARepeatedWorkspaceListCostsNothing(t *testing.T) {
	m, f := wiredModel(t)

	pump(t, m, liveWorkspaces())
	settled := len(f.Requests())
	if settled == 0 {
		t.Fatal("the first list asked Herdr nothing at all")
	}

	// Ten events with nothing behind them, as an idling agent would produce.
	for i := 0; i < 10; i++ {
		pump(t, m, liveWorkspaces())
	}

	if got := len(f.Requests()); got != settled {
		t.Fatalf("ten empty events cost %d further round trips:\n%v",
			got-settled, methodsAfter(f, settled))
	}
}

// A space that has just opened is the one thing an event does witness, and it
// is worth the round trips -- but only for itself.
func TestOnlyANewSpaceIsWorkedUp(t *testing.T) {
	m, f := wiredModel(t)
	pump(t, m, liveWorkspaces())
	settled := len(f.Requests())

	opened := append(liveWorkspaces(), herdr.Workspace{
		ID: "w3", Label: "docs", Cwd: "/tmp/docs", PaneIDs: []string{"w3:p1"},
	})
	pump(t, m, opened)

	after := f.Requests()[settled:]
	if len(after) == 0 {
		t.Fatal("a newly opened space was never looked at")
	}
	for _, req := range after {
		params := herdrtest.Params(t, req)
		for _, field := range []string{"workspace_id", "pane_id", "cwd"} {
			value, ok := params[field].(string)
			if !ok {
				continue
			}
			if value != "w3" && value != "w3:p1" && value != "/tmp/docs" {
				t.Fatalf("%s asked about %s = %q, which is not the new space",
					req.Method, field, value)
			}
		}
	}
}

// Reading agent output for a PR URL is gone: pane.read is the most expensive
// call the board can make of Herdr, and a board of thirty spaces made one per
// pane per round. The branch lookup finds the PR without it.
func TestThePanesAreNeverRead(t *testing.T) {
	m, f := wiredModel(t)
	pump(t, m, workspacesMsg{
		{ID: "w1", Label: "api", Cwd: "/tmp/api", PaneIDs: []string{"w1:p1", "w1:p2"}},
		{ID: "w2", Label: "web", Cwd: "/tmp/web", PaneIDs: []string{"w2:p1"}},
	})

	// A forced round, which is what `r` does, on a board with no PRs found yet
	// -- the case the scraper existed for.
	for _, msg := range drainCmd(m.loadPRsNow()) {
		pump(t, m, msg)
	}

	if got := f.Called("pane.read"); got != 0 {
		t.Fatalf("read %d panes", got)
	}
}

// A PR already known is looked up by its own URL, which reaches it after the
// branch has moved on. A finished one goes back to the branch, so the row can
// find whatever replaced it.
func TestAKnownPRIsLookedUpByURL(t *testing.T) {
	m, _ := wiredModel(t)
	pump(t, m, liveWorkspaces())

	var asked []string
	m.gh = &gh.Client{
		Workers: 1,
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			asked = append(asked, strings.Join(args, " "))
			return nil, errors.New("no pull requests found")
		},
	}

	withPR(m, "/tmp/api", gh.PR{Number: 4, State: "OPEN", URL: "https://example.test/4"})
	withPR(m, "/tmp/web", gh.PR{Number: 9, State: "MERGED", URL: "https://example.test/9"})
	for _, msg := range drainCmd(m.loadPRsNow()) {
		pump(t, m, msg)
	}

	joined := strings.Join(asked, "\n")
	if !strings.Contains(joined, "https://example.test/4") {
		t.Fatalf("an open PR was not looked up by its URL:\n%s", joined)
	}
	if strings.Contains(joined, "https://example.test/9") {
		t.Fatalf("a merged PR pinned the row to itself:\n%s", joined)
	}
}

func methodsAfter(f *herdrtest.Fake, from int) []string {
	var out []string
	for _, req := range f.Requests()[from:] {
		out = append(out, req.Method)
	}
	return out
}
