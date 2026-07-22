package herdr

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phin-tech/herdr-phin-board/internal/herdrtest"
)

func TestNewRequiresASocketPath(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	if _, err := New(); err == nil {
		t.Fatal("a client was built with no socket path")
	}
}

func TestRequestRoundTrip(t *testing.T) {
	f := herdrtest.Start(t)
	f.OK(map[string]any{"type": "ok"})

	c, err := New()
	if err != nil {
		t.Fatal(err)
	}

	var out struct {
		Type string `json:"type"`
	}
	if err := c.Request("ping", map[string]any{"a": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "ok" {
		t.Fatalf("decoded %+v", out)
	}

	req := f.LastAny(t)
	if req.Method != "ping" {
		t.Fatalf("method = %q", req.Method)
	}
	if req.ID == "" {
		t.Fatal("requests must carry an id, or replies cannot be matched")
	}
}

// An error reply must surface as an error, not as an empty success.
func TestErrorReplyBecomesAnError(t *testing.T) {
	f := herdrtest.Start(t)
	f.Fail("not_found", "no such workspace")

	c, _ := New()
	err := c.Request("workspace.get", map[string]any{"workspace_id": "w9"}, nil)
	if err == nil {
		t.Fatal("an error reply was read as success")
	}
	for _, want := range []string{"no such workspace", "not_found", "workspace.get"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// A server that accepts and says nothing must not hang the board for ever.
func TestNoReplyDoesNotHangForEver(t *testing.T) {
	f := herdrtest.Start(t)
	f.Handle(func(herdrtest.Request) any { return nil })

	c, _ := New()
	done := make(chan error, 1)
	go func() { done <- c.Request("ping", map[string]any{}, nil) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent server was treated as success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the client is still waiting on a server that never answered")
	}
}

func TestUnreachableSocketIsAnError(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/nonexistent/herdr.sock")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Request("ping", map[string]any{}, nil); err == nil {
		t.Fatal("a missing socket was treated as success")
	}
}

// Snapshots run well past bufio.Scanner's 64KiB default, which is why the
// buffer is raised. A reply larger than that must still decode.
func TestLargeReplyDecodes(t *testing.T) {
	f := herdrtest.Start(t)

	panes := make([]map[string]any, 0, 400)
	for i := 0; i < 400; i++ {
		panes = append(panes, map[string]any{
			"pane_id":      "w1:p" + string(rune('a'+i%26)),
			"workspace_id": "w1",
			"tab_id":       "w1:t1",
			"cwd":          "/tmp/" + strings.Repeat("deep/", 40),
		})
	}
	f.OK(map[string]any{
		"snapshot": map[string]any{
			"workspaces": []map[string]any{{"workspace_id": "w1", "label": "big", "active_tab_id": "w1:t1"}},
			"panes":      panes,
		},
	})

	c, _ := New()
	got, err := c.Workspaces()
	if err != nil {
		t.Fatalf("a large snapshot failed to decode: %v", err)
	}
	if len(got) != 1 || got[0].Cwd == "" {
		t.Fatalf("unexpected workspaces: %+v", got)
	}
}

// The workspace object carries no cwd; it comes from the panes. The active
// tab's pane wins, since that is the directory you are actually looking at.
func TestWorkspaceCwdComesFromTheActiveTabsPane(t *testing.T) {
	f := herdrtest.Start(t)
	f.OK(map[string]any{
		"snapshot": map[string]any{
			"workspaces": []map[string]any{
				{"workspace_id": "w1", "label": "api", "active_tab_id": "w1:t2"},
			},
			"panes": []map[string]any{
				{"pane_id": "w1:p1", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": "/tmp/other-tab"},
				{"pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t2", "cwd": "/tmp/active-tab"},
			},
		},
	})

	c, _ := New()
	got, err := c.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Cwd != "/tmp/active-tab" {
		t.Fatalf("cwd = %q, want the active tab's pane", got[0].Cwd)
	}
	if len(got[0].PaneIDs) != 2 {
		t.Fatalf("pane ids = %v, want both", got[0].PaneIDs)
	}
}

func TestWorkspaceWithNoPanesHasNoCwd(t *testing.T) {
	f := herdrtest.Start(t)
	f.OK(map[string]any{
		"snapshot": map[string]any{
			"workspaces": []map[string]any{{"workspace_id": "w1", "label": "empty"}},
			"panes":      []map[string]any{},
		},
	})

	c, _ := New()
	got, _ := c.Workspaces()
	if len(got) != 1 || got[0].Cwd != "" {
		t.Fatalf("unexpected: %+v", got)
	}
}

// A nil token clears the badge; a value sets it. Getting this backwards would
// leave stale statuses in the sidebar.
func TestReportTokenSendsNullToClear(t *testing.T) {
	f := herdrtest.Start(t)
	f.OK(map[string]any{"type": "ok"})
	c, _ := New()

	value := "Waiting"
	if err := c.ReportToken("w1", "status", &value); err != nil {
		t.Fatal(err)
	}
	params := herdrtest.Params(t, f.LastAny(t))
	tokens := params["tokens"].(map[string]any)
	if tokens["status"] != "Waiting" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if params["source"] != MetadataSource {
		t.Fatalf("source = %v, want %q", params["source"], MetadataSource)
	}

	if err := c.ReportToken("w1", "status", nil); err != nil {
		t.Fatal(err)
	}
	tokens = herdrtest.Params(t, f.LastAny(t))["tokens"].(map[string]any)
	if v, ok := tokens["status"]; !ok || v != nil {
		t.Fatalf("clearing sent %#v, want an explicit null", v)
	}
}

func TestReportTokenRejectsAnEmptyWorkspace(t *testing.T) {
	herdrtest.Start(t)
	c, _ := New()
	value := "x"
	if err := c.ReportToken("", "status", &value); err == nil {
		t.Fatal("a token was reported against no workspace")
	}
}

func TestAgentsIgnoresNothingAndParsesAll(t *testing.T) {
	f := herdrtest.Start(t)
	f.OK(map[string]any{
		"agents": []map[string]any{
			{"agent": "claude", "agent_status": "idle", "pane_id": "w1:p1", "workspace_id": "w1", "tab_id": "w1:t1"},
			{"agent_status": "unknown", "pane_id": "w1:p2", "workspace_id": "w1", "tab_id": "w1:t1"},
		},
	})

	c, _ := New()
	got, err := c.Agents()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d agents, want both rows", len(got))
	}
	// The absent agent field must stay absent, since that is how a plain shell
	// is told apart from a real agent.
	if got[0].Agent == nil || *got[0].Agent != "claude" {
		t.Fatalf("first row: %+v", got[0])
	}
	if got[1].Agent != nil {
		t.Fatalf("a pane with no agent parsed as one: %+v", got[1])
	}
}

func TestSendToAgentAndFocusUseTarget(t *testing.T) {
	f := herdrtest.Start(t)
	f.OK(map[string]any{"type": "ok"})
	c, _ := New()

	if err := c.SendToAgent("w1:p1", "hello"); err != nil {
		t.Fatal(err)
	}
	req := f.LastAny(t)
	// Not agent.prompt: that submits, and the point is that the person does.
	// Not agent.send_keys: those are logical keys, not text.
	if req.Method != "pane.send_text" {
		t.Fatalf("method = %q, want pane.send_text", req.Method)
	}
	params := herdrtest.Params(t, req)
	if params["pane_id"] != "w1:p1" || params["text"] != "hello" {
		t.Fatalf("params = %+v", params)
	}

	if err := c.FocusAgent("w1:p1"); err != nil {
		t.Fatal(err)
	}
	if f.LastAny(t).Method != "agent.focus" {
		t.Fatal("focus used the wrong method")
	}
}

func TestNotifyOmitsEmptyFields(t *testing.T) {
	f := herdrtest.Start(t)
	f.OK(map[string]any{"type": "notification_show"})
	c, _ := New()

	if err := c.Notify("Title", "", ""); err != nil {
		t.Fatal(err)
	}
	params := herdrtest.Params(t, f.LastAny(t))
	if params["title"] != "Title" {
		t.Fatalf("params = %+v", params)
	}
	for _, k := range []string{"body", "sound"} {
		if _, ok := params[k]; ok {
			t.Fatalf("%q was sent despite being empty: %+v", k, params)
		}
	}
}

func TestReadPaneAcceptsEitherField(t *testing.T) {
	for _, field := range []string{"content", "text"} {
		t.Run(field, func(t *testing.T) {
			f := herdrtest.Start(t)
			f.OK(map[string]any{field: "line one\nline two"})

			c, _ := New()
			got, err := c.ReadPane("w1:p1", 100)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "line two") {
				t.Fatalf("read %q", got)
			}
		})
	}
}

func TestRenameAndCreateWorkspace(t *testing.T) {
	f := herdrtest.Start(t)
	f.Handle(func(req herdrtest.Request) any {
		if req.Method == "workspace.create" {
			return map[string]any{
				"id":     req.ID,
				"result": map[string]any{"workspace": map[string]any{"workspace_id": "w9"}},
			}
		}
		return map[string]any{"id": req.ID, "result": map[string]any{"type": "ok"}}
	})

	c, _ := New()
	if err := c.RenameWorkspace("w1", "new name"); err != nil {
		t.Fatal(err)
	}
	if params := herdrtest.Params(t, f.LastAny(t)); params["label"] != "new name" {
		t.Fatalf("params = %+v", params)
	}

	id, err := c.CreateWorkspace("/tmp/x", "label")
	if err != nil {
		t.Fatal(err)
	}
	if id != "w9" {
		t.Fatalf("created workspace id = %q", id)
	}
}

// The subscription acks first and then streams; the ack must not be delivered
// as though it were an event.
func TestSubscribeStreamsEventsAfterTheAck(t *testing.T) {
	f := herdrtest.Start(t)

	c, _ := New()
	events := make(chan Event, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = c.Subscribe(ctx, WorkspaceSubscriptions, events) }()

	f.Stream <- `{"event":"workspace_created","data":{"type":"workspace_created"}}`
	f.Stream <- `{"event":"workspace_focused","data":{"type":"workspace_focused"}}`

	for _, want := range []string{"workspace_created", "workspace_focused"} {
		select {
		case ev := <-events:
			if ev.Event != want {
				t.Fatalf("event = %q, want %q", ev.Event, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("never received %q", want)
		}
	}

	// The subscription request asks for the types the board cares about.
	herdrtest.WaitFor(t, "the subscribe request", func() bool { return len(f.Requests()) > 0 })
	params := herdrtest.Params(t, f.LastAny(t))
	subs, ok := params["subscriptions"].([]any)
	if !ok || len(subs) != len(WorkspaceSubscriptions) {
		t.Fatalf("subscriptions = %#v", params["subscriptions"])
	}
}

// Herdr going away must end the stream, since that is the watcher's signal to
// stop.
func TestSubscribeReturnsWhenTheServerGoes(t *testing.T) {
	f := herdrtest.Start(t)

	c, _ := New()
	done := make(chan error, 1)
	go func() {
		done <- c.Subscribe(context.Background(), WorkspaceSubscriptions, make(chan Event, 4))
	}()

	herdrtest.WaitFor(t, "the subscription", func() bool { return len(f.Requests()) > 0 })
	f.Close()
	close(f.Stream)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived the server")
	}
}

func TestSubscribeStopsOnContextCancel(t *testing.T) {
	herdrtest.Start(t)

	c, _ := New()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- c.Subscribe(ctx, WorkspaceSubscriptions, make(chan Event, 4)) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled subscription kept running")
	}
}

// Garbage on the wire must be skipped rather than delivered as an empty event.
func TestSubscribeSkipsUnparseableLines(t *testing.T) {
	f := herdrtest.Start(t)

	c, _ := New()
	events := make(chan Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Subscribe(ctx, WorkspaceSubscriptions, events) }()

	f.Stream <- `not json at all`
	f.Stream <- `{"no_event_field":true}`
	f.Stream <- `{"event":"workspace_closed","data":{}}`

	select {
	case ev := <-events:
		if ev.Event != "workspace_closed" {
			t.Fatalf("first delivered event was %q", ev.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the good event never arrived")
	}
}

func TestPluginStateDirIsNotRequired(t *testing.T) {
	// Nothing here uses it, but a missing variable must not break the client.
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	herdrtest.Start(t)
	if _, err := New(); err != nil {
		t.Fatal(err)
	}
	_ = os.Getenv("HERDR_PLUGIN_STATE_DIR")
}
