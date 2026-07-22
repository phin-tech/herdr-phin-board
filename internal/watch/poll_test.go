package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/phin-tech/herdr-phin-board/internal/alert"
	"github.com/phin-tech/herdr-phin-board/internal/gh"
	"github.com/phin-tech/herdr-phin-board/internal/herdrtest"
	"github.com/phin-tech/herdr-phin-board/internal/store"
)

// tempSpace returns a directory as Herdr would report it, and as the board
// keys it. On macOS a temporary directory is a symlink, and store.Key resolves
// symlinks -- so the two differ, and conflating them hides real behaviour.
func tempSpace(t *testing.T) (raw, key string) {
	t.Helper()
	raw = t.TempDir()
	return raw, store.Key(raw)
}

// snapshotWith answers session.snapshot with one workspace on a directory.
func snapshotWith(dir, label string) map[string]any {
	return map[string]any{
		"snapshot": map[string]any{
			"workspaces": []map[string]any{
				{"workspace_id": "w1", "label": label, "active_tab_id": "w1:t1"},
			},
			"panes": []map[string]any{
				{"pane_id": "w1:p1", "workspace_id": "w1", "tab_id": "w1:t1", "cwd": dir},
			},
		},
	}
}

// prClient returns a GitHub client that answers with a fixed PR payload.
func prClient(body string) *gh.Client {
	return &gh.Client{
		Workers: 1,
		Timeout: time.Second,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			if body == "" {
				return nil, fmt.Errorf("no pull requests found")
			}
			return []byte(body), nil
		},
	}
}

// seedCache plants what the previous round saw, which is what a transition is
// measured against.
func seedCache(t *testing.T, dir, space string, pr gh.PR) {
	t.Helper()
	c := gh.LoadCache(dir)
	pr.Fetched = time.Now()
	c.Put(space, pr)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestPollNotifiesOnATransition(t *testing.T) {
	space, key := tempSpace(t)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{"session.snapshot": snapshotWith(space, "api")})

	// Previously green; now failing.
	seedCache(t, state, key, gh.PR{Number: 7, State: "OPEN", Checks: gh.ChecksPass})
	body := `{"number":7,"state":"OPEN","statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE","name":"test"}]}`

	if err := poll(context.Background(), state, true, prClient(body)); err != nil {
		t.Fatal(err)
	}

	// A toast went out...
	if n := f.Called("notification.show"); n != 1 {
		t.Fatalf("notification.show called %d times, want 1", n)
	}
	params := herdrtest.Params(t, f.Last(t, "notification.show"))
	if params["title"] != "Checks failed" {
		t.Fatalf("title = %v", params["title"])
	}

	// ...and it was recorded, since a toast alone is lost if nobody is there.
	if n := alert.Load(state).Count(key); n != 1 {
		t.Fatalf("%d alerts recorded, want 1", n)
	}
}

// Notifications off should make the board quiet, not blind.
func TestPollRecordsEvenWithNotificationsOff(t *testing.T) {
	space, key := tempSpace(t)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{"session.snapshot": snapshotWith(space, "api")})

	seedCache(t, state, key, gh.PR{Number: 7, State: "OPEN", Checks: gh.ChecksPass})
	body := `{"number":7,"state":"MERGED","statusCheckRollup":[]}`

	if err := poll(context.Background(), state, false, prClient(body)); err != nil {
		t.Fatal(err)
	}

	if n := f.Called("notification.show"); n != 0 {
		t.Fatalf("a toast went out with notifications off (%d)", n)
	}
	if n := alert.Load(state).Count(key); n != 1 {
		t.Fatalf("%d alerts recorded, want the bell anyway", n)
	}
}

// The steady state must be silent, or the same failing check would be
// announced every two minutes until it was fixed.
func TestPollIsSilentWhenNothingChanged(t *testing.T) {
	space, key := tempSpace(t)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{"session.snapshot": snapshotWith(space, "api")})

	seedCache(t, state, key, gh.PR{Number: 7, State: "OPEN", Checks: gh.ChecksFail})
	body := `{"number":7,"state":"OPEN","statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE","name":"test"}]}`

	if err := poll(context.Background(), state, true, prClient(body)); err != nil {
		t.Fatal(err)
	}
	if n := f.Called("notification.show"); n != 0 {
		t.Fatalf("a steady state raised %d notifications", n)
	}
}

// A PR seen for the first time is the starting point, not news.
func TestPollDoesNotAnnounceAPRItHasJustLearnedOf(t *testing.T) {
	space, key := tempSpace(t)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{"session.snapshot": snapshotWith(space, "api")})

	body := `{"number":7,"state":"OPEN","reviewDecision":"CHANGES_REQUESTED","statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE"}]}`
	if err := poll(context.Background(), state, true, prClient(body)); err != nil {
		t.Fatal(err)
	}

	if n := f.Called("notification.show"); n != 0 {
		t.Fatalf("first sight raised %d notifications", n)
	}
	// But it is remembered, so the next change is measurable.
	if !gh.LoadCache(state).Entries[key].Found() {
		t.Fatal("the PR was not cached")
	}
}

// Spaces whose workspace is closed still matter: a PR you are waiting on does
// not stop mattering because you shut the window.
func TestPollWatchesStoredEntriesWithNoLiveWorkspace(t *testing.T) {
	live, liveKey := tempSpace(t)
	_, closedKey := tempSpace(t)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	board.SetStatus(closedKey, board.DefaultStatusID(), "shut")
	if err := board.Save(); err != nil {
		t.Fatal(err)
	}

	f := herdrtest.Start(t)
	f.Route(map[string]any{"session.snapshot": snapshotWith(live, "api")})

	var asked []string
	client := &gh.Client{
		Workers: 1,
		Timeout: time.Second,
		Run: func(_ context.Context, dir string, _ ...string) ([]byte, error) {
			asked = append(asked, dir)
			return nil, fmt.Errorf("no pull requests found")
		},
	}

	if err := poll(context.Background(), state, true, client); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, d := range asked {
		seen[store.Key(d)] = true
	}
	if !seen[liveKey] || !seen[closedKey] {
		t.Fatalf("asked about %v, want both the live and the closed space", asked)
	}
}

// A broken gh is not worth waking anyone for, and must not be cached as an
// absence -- that would bake the fault in for the negative TTL.
func TestPollStaysQuietWhenGhIsBroken(t *testing.T) {
	space, key := tempSpace(t)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{"session.snapshot": snapshotWith(space, "api")})

	client := &gh.Client{
		Workers: 1,
		Timeout: time.Second,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, gh.ErrUnavailable
		},
	}

	if err := poll(context.Background(), state, true, client); err != nil {
		t.Fatal(err)
	}
	if n := f.Called("notification.show"); n != 0 {
		t.Fatalf("a broken gh raised %d notifications", n)
	}
	if _, ok := gh.LoadCache(state).Entries[key]; ok {
		t.Fatal("a gh fault was cached as though it were an answer")
	}
}

// Herdr being gone is the signal to stop, so it must surface as an error.
func TestPollFailsWhenHerdrIsGone(t *testing.T) {
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)
	t.Setenv("HERDR_SOCKET_PATH", state+"/nothing.sock")

	if err := poll(context.Background(), state, true, prClient("")); err == nil {
		t.Fatal("poll succeeded with no Herdr")
	}
}

// A space with no PR is recorded as checked, not dropped -- otherwise it would
// be re-asked on every single round.
func TestPollRecordsAnAbsence(t *testing.T) {
	space, key := tempSpace(t)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{"session.snapshot": snapshotWith(space, "api")})

	if err := poll(context.Background(), state, true, prClient("")); err != nil {
		t.Fatal(err)
	}

	cache := gh.LoadCache(state)
	entry, ok := cache.Entries[key]
	if !ok {
		t.Fatal("the absence was not recorded, so it would be re-asked every round")
	}
	if entry.Found() {
		t.Fatalf("recorded as a PR: %+v", entry)
	}
	if cache.Stale(key) {
		t.Fatal("a just-checked absence is already stale")
	}
}

// The notification names the space, so a toast is actionable on its own.
func TestNotificationNamesTheSpace(t *testing.T) {
	space, key := tempSpace(t)
	state := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)

	f := herdrtest.Start(t)
	f.Route(map[string]any{"session.snapshot": snapshotWith(space, "billing")})

	seedCache(t, state, key, gh.PR{Number: 3, State: "OPEN", Checks: gh.ChecksPass})
	body := `{"number":3,"state":"OPEN","reviewDecision":"APPROVED","statusCheckRollup":[]}`

	if err := poll(context.Background(), state, true, prClient(body)); err != nil {
		t.Fatal(err)
	}

	params := herdrtest.Params(t, f.Last(t, "notification.show"))
	body2, _ := json.Marshal(params)
	if !contains(string(body2), "billing") {
		t.Fatalf("the notification does not name the space: %s", body2)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
