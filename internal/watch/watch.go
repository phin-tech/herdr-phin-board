// Package watch polls pull request state while the board is closed.
//
// Herdr has no timer in its API -- only events -- so periodic work needs a
// process of our own. This one is deliberately small: poll, diff, notify,
// record, and get out of the way when Herdr goes.
package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/phin-tech/herdr-phin-board/internal/alert"
	"github.com/phin-tech/herdr-phin-board/internal/gh"
	"github.com/phin-tech/herdr-phin-board/internal/herdr"
	"github.com/phin-tech/herdr-phin-board/internal/store"
)

// Interval is how often the watcher looks. Slow enough to be inconsequential
// against GitHub's rate limit, fast enough that a failing check reaches you
// while you still care.
const Interval = 2 * time.Minute

// Lock is a pidfile ensuring one watcher per machine. The board spawns a
// watcher whenever it opens, so without this every open would add another.
type Lock struct{ path string }

// Acquire takes the lock, or reports that somebody else holds it.
func Acquire(dir string) (*Lock, bool) {
	path := filepath.Join(dir, "watch.pid")

	if data, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(string(data)); err == nil && alive(pid) {
			return nil, false
		}
		// A pidfile left by a killed watcher, or one whose pid has been
		// reused by something else. Either way it is not a live watcher.
		_ = os.Remove(path)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// Lost the race to another watcher starting at the same moment.
		return nil, false
	}
	fmt.Fprintf(f, "%d", os.Getpid())
	f.Close()
	return &Lock{path: path}, true
}

// Release drops the lock.
func (l *Lock) Release() {
	if l != nil {
		_ = os.Remove(l.path)
	}
}

// alive reports whether a pid is a running process. Signal 0 checks for
// existence without delivering anything.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// Run polls until the context is cancelled or Herdr goes away.
func Run(ctx context.Context, stateDir string, interval time.Duration) error {
	lock, ok := Acquire(stateDir)
	if !ok {
		// Another watcher is already doing this work.
		return nil
	}
	defer lock.Release()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := poll(ctx, stateDir); err != nil {
			// Herdr has stopped, so there is nothing to watch and nowhere to
			// deliver a notification. Exiting beats spinning.
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// poll does one round: read the board, ask GitHub, notify on what changed.
func poll(ctx context.Context, stateDir string) error {
	client, err := herdr.New()
	if err != nil {
		return err
	}
	workspaces, err := client.Workspaces()
	if err != nil {
		return err
	}

	board, err := store.Load()
	if err != nil {
		return err
	}

	// Watch every space the board knows about: the live ones, plus stored
	// entries whose workspace is currently closed. A PR you are waiting on
	// matters whether or not its window happens to be open.
	labels := map[string]string{}
	for _, ws := range workspaces {
		if key := store.Key(ws.Cwd); key != "" {
			labels[key] = ws.Label
		}
	}
	for key, entry := range board.Entries {
		if _, ok := labels[key]; !ok {
			labels[key] = entry.Label
		}
	}
	if len(labels) == 0 {
		return nil
	}

	cache := gh.LoadCache(stateDir)
	targets := make([]gh.Target, 0, len(labels))
	for key := range labels {
		targets = append(targets, gh.Target{Dir: key})
	}

	found, problem := gh.New().FetchAll(ctx, targets)
	if problem != nil {
		// gh is missing or logged out. Nothing to compare, and nothing worth
		// waking the user for.
		return nil
	}

	fresh := alert.Load(stateDir)
	var raised []alert.Alert

	for dir, after := range found {
		before := cache.Entries[dir]
		label := labels[dir]
		if label == "" {
			label = filepath.Base(dir)
		}
		for _, a := range alert.Transitions(label, before, after) {
			a.At = time.Now().UTC()
			fresh.Add(dir, a)
			raised = append(raised, a)
		}
		cache.Put(dir, after)
	}
	for dir := range labels {
		if _, ok := found[dir]; !ok {
			cache.PutAbsent(dir)
		}
	}

	_ = cache.Save()
	if len(raised) > 0 {
		_ = fresh.Save()
	}

	// Notify after saving: a toast the user never sees is still recorded, but
	// a recorded alert the user was never told about is the worse failure.
	for _, a := range raised {
		_ = client.Notify(a.Title(), a.Text, a.Sound())
	}
	return nil
}
