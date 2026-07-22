package main

import (
	"os"
	"strings"
	"testing"
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
