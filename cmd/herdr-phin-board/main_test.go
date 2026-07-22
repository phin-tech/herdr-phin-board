package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phin-tech/herdr-phin-board/internal/herdrtest"
	"github.com/phin-tech/herdr-phin-board/internal/store"
)

// version and config must work outside a Herdr session -- that is exactly where
// someone reaches for them, and CI has no socket at all.
func TestCommandsThatNeedNoHerdr(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	for _, args := range [][]string{
		{"version"},
		{"--version"},
		{"-v"},
		{"config"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := run(args); err != nil {
				t.Fatalf("run(%v) outside Herdr: %v", args, err)
			}
		})
	}
}

// prune only needs the board file, not a socket.
func TestPruneNeedsNoSocket(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	if err := run([]string{"prune"}); err != nil {
		t.Fatalf("prune outside Herdr: %v", err)
	}
}

// Anything that does talk to Herdr must still say so plainly.
func TestSyncRefusesWithoutHerdr(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	err := run([]string{"sync"})
	if err == nil {
		t.Fatal("sync claimed to work with no Herdr")
	}
	if !strings.Contains(err.Error(), "HERDR_SOCKET_PATH") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestUnknownCommandIsReported(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", os.Getenv("HERDR_SOCKET_PATH"))
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())

	if err := run([]string{"nonsense"}); err == nil {
		t.Fatal("an unknown command was accepted")
	}
}

// sync reconciles the sidebar badge with the board file. Tokens do not survive
// a Herdr restart, so getting this wrong leaves the sidebar lying.
func TestSyncSetsAndClearsTokens(t *testing.T) {
	raw := t.TempDir()
	key := store.Key(raw)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{
		"session.snapshot": map[string]any{
			"snapshot": map[string]any{
				"workspaces": []map[string]any{
					{"workspace_id": "w1", "label": "api", "active_tab_id": "w1:t1"},
				},
				"panes": []map[string]any{
					{"pane_id": "w1:p1", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": raw},
				},
			},
		},
	})

	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	board.SetStatus(key, "waiting", "api")
	if err := board.Save(); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"sync"}); err != nil {
		t.Fatal(err)
	}

	params := herdrtest.Params(t, f.Last(t, "workspace.report_metadata"))
	tokens, ok := params["tokens"].(map[string]any)
	if !ok || tokens["status"] != "Waiting" {
		t.Fatalf("tokens = %+v, want the status set", params["tokens"])
	}
}

// A space in the default status gets no badge, and a stale one must be cleared
// rather than left behind.
func TestSyncClearsTheDefaultStatus(t *testing.T) {
	raw := t.TempDir()
	key := store.Key(raw)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{
		"session.snapshot": map[string]any{
			"snapshot": map[string]any{
				"workspaces": []map[string]any{
					{"workspace_id": "w1", "label": "api", "active_tab_id": "w1:t1"},
				},
				"panes": []map[string]any{
					{"pane_id": "w1:p1", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": raw},
				},
			},
		},
	})

	board, _ := store.Load()
	board.SetStatus(key, board.DefaultStatusID(), "api")
	if err := board.Save(); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"sync"}); err != nil {
		t.Fatal(err)
	}

	params := herdrtest.Params(t, f.Last(t, "workspace.report_metadata"))
	tokens := params["tokens"].(map[string]any)
	if v, ok := tokens["status"]; !ok || v != nil {
		t.Fatalf("the default status sent %#v, want an explicit null", v)
	}
}

// A workspace the board has never heard of must still be reported, so a badge
// from an earlier status does not linger for ever.
func TestSyncClearsUnknownWorkspaces(t *testing.T) {
	raw := t.TempDir()
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{
		"session.snapshot": map[string]any{
			"snapshot": map[string]any{
				"workspaces": []map[string]any{
					{"workspace_id": "w1", "label": "unknown", "active_tab_id": "w1:t1"},
				},
				"panes": []map[string]any{
					{"pane_id": "w1:p1", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": raw},
				},
			},
		},
	})

	if err := run([]string{"sync"}); err != nil {
		t.Fatal(err)
	}
	if f.Called("workspace.report_metadata") != 1 {
		t.Fatal("an unknown workspace was skipped, so a stale badge would remain")
	}
}

func TestConfigInitWritesATemplate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	t.Setenv("HERDR_SOCKET_PATH", "")

	if err := run([]string{"config", "--init"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); err != nil {
		t.Fatalf("no template was written: %v", err)
	}
	// Running it again must not clobber what is now the user's file.
	if err := run([]string{"config", "--init"}); err == nil {
		t.Fatal("a second --init overwrote the settings")
	}
}
