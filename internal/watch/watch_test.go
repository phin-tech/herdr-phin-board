package watch

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The board spawns a watcher every time it opens, so without a working lock
// every open would add another poller.
func TestLockIsExclusive(t *testing.T) {
	dir := t.TempDir()

	first, ok := Acquire(dir)
	if !ok {
		t.Fatal("could not take a free lock")
	}
	if _, ok := Acquire(dir); ok {
		t.Fatal("a second watcher took a held lock")
	}

	first.Release()
	second, ok := Acquire(dir)
	if !ok {
		t.Fatal("the lock was not released")
	}
	second.Release()
}

// A machine that lost power leaves a pidfile behind. That must not stop a
// watcher ever starting again.
func TestStalePidfileIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watch.pid")

	// A pid that cannot be running: max_pid is well below this everywhere.
	if err := os.WriteFile(path, []byte("4294967294"), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, ok := Acquire(dir)
	if !ok {
		t.Fatal("a stale pidfile permanently blocked the watcher")
	}
	defer lock.Release()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid, _ := strconv.Atoi(string(data)); pid != os.Getpid() {
		t.Fatalf("pidfile holds %s, want this process", data)
	}
}

func TestGarbagePidfileIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "watch.pid"), []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, ok := Acquire(dir)
	if !ok {
		t.Fatal("an unreadable pidfile blocked the watcher")
	}
	lock.Release()
}

// Releasing a lock that was never taken must not panic -- spawnWatcher does
// exactly that on the path where it declines to spawn.
func TestReleaseOfNilIsSafe(t *testing.T) {
	var lock *Lock
	lock.Release()
}

// When Herdr goes, the watcher must go too -- and promptly. The interval here
// is an hour, so a watcher that waited for its next tick would fail this.
func TestExitsPromptlyWhenHerdrIsGone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "nothing-listening.sock"))

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), dir, time.Hour) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher was still running with no Herdr to watch")
	}
}

// The lock must be released on the way out, or the next board to open would
// decline to start a watcher for ever.
func TestLockIsFreedOnExit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "nothing-listening.sock"))

	_ = Run(context.Background(), dir, time.Hour)

	lock, ok := Acquire(dir)
	if !ok {
		t.Fatal("the lock outlived the watcher")
	}
	lock.Release()
}

// A cancelled context stops the watcher even while Herdr is fine.
func TestContextCancelStops(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "nothing-listening.sock"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { _ = Run(ctx, dir, time.Hour); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled context did not stop the watcher")
	}
}

// Herdr emits events freely, and gh calls are the expensive part, so a burst
// must cost one poll rather than one per event.
func TestDrainCoalescesABurst(t *testing.T) {
	ch := make(chan struct{}, 8)
	for i := 0; i < 5; i++ {
		ch <- struct{}{}
	}

	start := time.Now()
	drain(ch, 50*time.Millisecond)

	if len(ch) != 0 {
		t.Fatalf("%d nudges survived the drain", len(ch))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain took %s, far longer than its window", elapsed)
	}
}
