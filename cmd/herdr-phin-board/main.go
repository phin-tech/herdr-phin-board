// Command herdr-board renders a status board over your Herdr spaces.
//
// With no arguments it runs the TUI. `herdr-board sync` runs headlessly and
// re-applies stored statuses to Herdr's workspace tokens -- that is what the
// workspace.created event hook calls, so a status survives a server restart.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/herdr"
	"github.com/phin-tech/herdr-phin-board/internal/store"
	"github.com/phin-tech/herdr-phin-board/internal/ui"
	"github.com/phin-tech/herdr-phin-board/internal/watch"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-board:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	client, err := herdr.New()
	if err != nil {
		return err
	}
	board, err := store.Load()
	if err != nil {
		return err
	}

	if len(args) > 0 {
		switch args[0] {
		case "sync":
			return sync(client, board)
		case "watch":
			// The background poller. Started detached by the board, or run by
			// hand; the lock inside keeps there being only one.
			dir, err := stateDir()
			if err != nil {
				return err
			}
			return watch.Run(context.Background(), dir, watch.Interval)

		case "prune":
			n := board.Prune()
			if err := board.Save(); err != nil {
				return err
			}
			fmt.Printf("pruned %d entries for directories that no longer exist\n", n)
			return nil
		default:
			return fmt.Errorf("unknown command %q (want: sync, watch, prune)", args[0])
		}
	}

	// A watcher keeps polling after the board closes, which is the only way a
	// notification can reach you while you are elsewhere. Spawning it here
	// means there is nothing to set up; the lock means there is only ever one.
	spawnWatcher()

	// Mouse reporting drives the view switcher in the title bar. It also takes
	// over drag-to-select inside the popup; most terminals still allow it with
	// shift held.
	prog := tea.NewProgram(ui.New(client, board), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = prog.Run()
	return err
}

// sync pushes every stored status into the matching live workspace's tokens.
// Tokens do not survive a server restart, so this reconciles them from the
// board file, which does.
func sync(client *herdr.Client, board *store.Board) error {
	workspaces, err := client.Workspaces()
	if err != nil {
		return err
	}
	var applied, cleared int
	for _, ws := range workspaces {
		// A workspace with no entry, or one sitting in the default status, gets
		// no badge. Both cases must still be reported so a stale token from an
		// earlier status is cleared rather than left behind for ever.
		var value *string
		if entry, ok := board.Entries[store.Key(ws.Cwd)]; ok {
			if status, ok := board.StatusByID(entry.Status); ok && status.ID != board.DefaultStatusID() {
				label := status.Label
				value = &label
			}
		}
		if value == nil {
			cleared++
		} else {
			applied++
		}

		if err := client.ReportToken(ws.ID, "status", value); err != nil {
			return err
		}
	}
	fmt.Printf("synced %d of %d workspaces (%d cleared as default)\n", applied, len(workspaces), cleared)
	return nil
}

// stateDir is where the board, the PR cache, alerts and the watcher lock live.
func stateDir() (string, error) {
	path, err := store.Path()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

// spawnWatcher starts the background poller, detached so it outlives the board.
// Failures are deliberately silent: the board works without it, and a noisy
// error on every open would be worse than quietly having no notifications.
func spawnWatcher() {
	dir, err := stateDir()
	if err != nil {
		return
	}
	// This only tests whether a watcher is already running; the child takes the
	// lock properly for its own lifetime. Two boards opening at once can both
	// get past here, and the loser simply exits when its own Acquire fails.
	lock, ok := watch.Acquire(dir)
	if !ok {
		return
	}
	lock.Release()

	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "watch")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	_ = cmd.Start()
	if cmd.Process != nil {
		// Not waited on: it is meant to outlive us.
		_ = cmd.Process.Release()
	}
}
