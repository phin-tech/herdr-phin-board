// Command herdr-board renders a status board over your Herdr spaces.
//
// With no arguments it runs the TUI. `herdr-board sync` runs headlessly and
// re-applies stored statuses to Herdr's workspace tokens -- that is what the
// workspace.created event hook calls, so a status survives a server restart.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-board/internal/herdr"
	"github.com/phin-tech/herdr-board/internal/store"
	"github.com/phin-tech/herdr-board/internal/ui"
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
		case "prune":
			n := board.Prune()
			if err := board.Save(); err != nil {
				return err
			}
			fmt.Printf("pruned %d entries for directories that no longer exist\n", n)
			return nil
		default:
			return fmt.Errorf("unknown command %q (want: sync, prune)", args[0])
		}
	}

	prog := tea.NewProgram(ui.New(client, board), tea.WithAltScreen())
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
	var applied int
	for _, ws := range workspaces {
		entry, ok := board.Entries[store.Key(ws.Cwd)]
		if !ok {
			continue
		}
		status, ok := board.StatusByID(entry.Status)
		if !ok {
			continue
		}
		label := status.Label
		if err := client.ReportToken(ws.ID, "status", &label); err != nil {
			return err
		}
		applied++
	}
	fmt.Printf("synced %d of %d workspaces\n", applied, len(workspaces))
	return nil
}
