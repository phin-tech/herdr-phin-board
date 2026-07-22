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
	"github.com/phin-tech/herdr-phin-board/internal/config"
	"github.com/phin-tech/herdr-phin-board/internal/gh"
	"github.com/phin-tech/herdr-phin-board/internal/herdr"
	"github.com/phin-tech/herdr-phin-board/internal/store"
)

// Interval is the default cadence, overridden by poll_interval in the config
// file. Slow enough to be inconsequential against GitHub's rate limit, fast
// enough that a failing check reaches you while you still care.
const Interval = config.DefaultPollInterval

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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// An event subscription doubles as a liveness signal. Herdr closing drops
	// the connection, which ends the watcher at once rather than leaving it to
	// discover the loss on its next tick -- up to a whole interval later.
	//
	// It is also a nudge: a workspace appearing or closing changes what there
	// is to watch, so the poll happens then instead of waiting out the timer.
	nudge := subscribe(ctx, cancel)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	settings := config.Load()

	for {
		if err := poll(ctx, stateDir, settings.Notifications); err != nil {
			// Herdr has stopped, so there is nothing to watch and nowhere to
			// deliver a notification. Exiting beats spinning.
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-nudge:
			// Coalesce a burst of events into one poll: Herdr emits freely,
			// and gh calls are the expensive part.
			drain(nudge, settle)
		}
	}
}

// settle is how long to wait for an event burst to finish before polling.
const settle = 2 * time.Second

// subscribe streams Herdr events, cancelling ctx when the stream ends. The
// returned channel carries a nudge per event.
func subscribe(ctx context.Context, cancel context.CancelFunc) <-chan struct{} {
	out := make(chan struct{}, 1)

	client, err := herdr.New()
	if err != nil {
		cancel()
		return out
	}

	events := make(chan herdr.Event, 32)
	go func() {
		// Returns when Herdr goes away, which is exactly the signal wanted.
		_ = client.Subscribe(ctx, herdr.WorkspaceSubscriptions, events)
		cancel()
		close(events)
	}()

	go func() {
		for range events {
			select {
			case out <- struct{}{}:
			default: // a nudge is already pending
			}
		}
	}()
	return out
}

// drain swallows further nudges for a moment, so a burst of events costs one
// poll rather than one per event.
func drain(ch <-chan struct{}, window time.Duration) {
	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		select {
		case <-ch:
		case <-timer.C:
			return
		}
	}
}

// poll does one round: read the board, ask GitHub, notify on what changed.
func poll(ctx context.Context, stateDir string, notify bool) error {
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

	found, problem := gh.NewBackground().FetchAll(ctx, targets)
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
	// With notifications off the bell still appears -- quiet, not blind.
	if notify {
		for _, a := range raised {
			_ = client.Notify(a.Title(), a.Text, a.Sound())
		}
	}
	return nil
}
