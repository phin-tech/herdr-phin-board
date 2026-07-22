package watch

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
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
