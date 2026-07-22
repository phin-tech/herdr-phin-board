package gh

import (
	"fmt"
	"sync"

	"context"
	"errors"
	"golang.org/x/time/rate"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeClient(responses map[string]string) *Client {
	return &Client{
		Workers: 2,
		Timeout: time.Second,
		Run: func(_ context.Context, dir string, _ ...string) ([]byte, error) {
			body, ok := responses[dir]
			if !ok {
				return nil, errors.New("exit status 1")
			}
			return []byte(body), nil
		},
	}
}

const openPR = `{
  "number": 123,
  "state": "OPEN",
  "isDraft": false,
  "reviewDecision": "APPROVED",
  "title": "Add the thing",
  "url": "https://github.com/o/r/pull/123",
  "statusCheckRollup": [
    {"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},
    {"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}
  ]
}`

func TestFetchParsesAPR(t *testing.T) {
	c := fakeClient(map[string]string{"/tmp/repo": openPR})

	pr, err := c.Fetch(context.Background(), "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 123 || pr.State != "OPEN" || pr.Review != "APPROVED" {
		t.Fatalf("unexpected PR: %+v", pr)
	}
	if pr.Checks != ChecksPass {
		t.Fatalf("checks = %q, want pass", pr.Checks)
	}
	if pr.Fetched.IsZero() {
		t.Fatal("Fetched not stamped, so the cache could never expire it")
	}
}

// gh exits non-zero for no PR, not a repo, no remote and not logged in alike.
// None of those should surface as an error per row.
func TestFetchTreatsEveryFailureAsNoPR(t *testing.T) {
	c := fakeClient(nil)
	if _, err := c.Fetch(context.Background(), "/tmp/nothing"); !errors.Is(err, ErrNoPR) {
		t.Fatalf("err = %v, want ErrNoPR", err)
	}
}

func TestFetchRejectsGarbage(t *testing.T) {
	for name, body := range map[string]string{
		"not json":  "<html>",
		"no number": `{"state":"OPEN"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeClient(map[string]string{"/tmp/repo": body})
			if _, err := c.Fetch(context.Background(), "/tmp/repo"); !errors.Is(err, ErrNoPR) {
				t.Fatalf("err = %v, want ErrNoPR", err)
			}
		})
	}
}

// A single failure is what you need to act on, so it outranks anything still
// running.
func TestRollupPrefersFailureOverPending(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Checks
	}{
		{"all green", openPR, ChecksPass},
		{"none", `{"number":1,"statusCheckRollup":[]}`, ChecksNone},
		{
			"one failure among successes",
			`{"number":1,"statusCheckRollup":[
			  {"status":"COMPLETED","conclusion":"SUCCESS"},
			  {"status":"COMPLETED","conclusion":"FAILURE"}]}`,
			ChecksFail,
		},
		{
			"failure outranks in-progress",
			`{"number":1,"statusCheckRollup":[
			  {"status":"IN_PROGRESS"},
			  {"status":"COMPLETED","conclusion":"FAILURE"}]}`,
			ChecksFail,
		},
		{
			"still running",
			`{"number":1,"statusCheckRollup":[
			  {"status":"COMPLETED","conclusion":"SUCCESS"},
			  {"status":"IN_PROGRESS"}]}`,
			ChecksPending,
		},
		{
			"legacy status contexts",
			`{"number":1,"statusCheckRollup":[{"state":"FAILURE"}]}`,
			ChecksFail,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeClient(map[string]string{"/tmp/repo": tc.body})
			pr, err := c.Fetch(context.Background(), "/tmp/repo")
			if err != nil {
				t.Fatal(err)
			}
			if pr.Checks != tc.want {
				t.Fatalf("checks = %q, want %q", pr.Checks, tc.want)
			}
		})
	}
}

// Directories without a PR must be absent rather than present-and-empty, so
// callers can tell "no PR" from "not looked at".
func TestFetchAllSkipsDirsWithoutPRs(t *testing.T) {
	c := fakeClient(map[string]string{"/tmp/withpr": openPR})

	got, problem := c.FetchAll(context.Background(), []Target{{Dir: "/tmp/withpr"}, {Dir: "/tmp/nopr"}, {Dir: "/tmp/other"}})
	if problem != nil {
		t.Fatalf("ordinary absences were reported as a problem: %v", problem)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(got), got)
	}
	if _, ok := got["/tmp/withpr"]; !ok {
		t.Fatal("the directory with a PR is missing")
	}
}

func TestFetchAllIsConcurrencySafe(t *testing.T) {
	responses := map[string]string{}
	var dirs []string
	for _, d := range []string{"/a", "/b", "/c", "/d", "/e", "/f"} {
		responses[d] = openPR
		dirs = append(dirs, d)
	}
	c := fakeClient(responses)
	c.Workers = 3

	var targets []Target
	for _, d := range dirs {
		targets = append(targets, Target{Dir: d})
	}
	got, _ := c.FetchAll(context.Background(), targets)
	if len(got) != len(dirs) {
		t.Fatalf("got %d results, want %d", len(got), len(dirs))
	}
}

// The fixture is a real `gh pr view` payload, trimmed to its distinct check
// outcomes. It exists so a change in gh's JSON shape fails here rather than
// silently blanking the PR column.
func TestParsesRealGhPayload(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "pr-real.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := fakeClient(map[string]string{"/tmp/repo": string(body)})

	pr, err := c.Fetch(context.Background(), "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number == 0 || pr.URL == "" || pr.Title == "" {
		t.Fatalf("fields did not populate from real output: %+v", pr)
	}
	if pr.Review != "REVIEW_REQUIRED" {
		t.Fatalf("review = %q, want REVIEW_REQUIRED", pr.Review)
	}
	// The real payload mixes SUCCESS with SKIPPED, which must read as passing:
	// a skipped job is a workflow with nothing to do, not a failure.
	if pr.Checks != ChecksPass {
		t.Fatalf("checks = %q, want pass for success+skipped", pr.Checks)
	}
}

// GitHub uses several conclusions that mean "you need to do something".
func TestActionRequiredCountsAsFailure(t *testing.T) {
	body := `{"number":1,"statusCheckRollup":[{"status":"COMPLETED","conclusion":"ACTION_REQUIRED"}]}`
	c := fakeClient(map[string]string{"/tmp/repo": body})

	pr, err := c.Fetch(context.Background(), "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Checks != ChecksFail {
		t.Fatalf("checks = %q, want fail", pr.Checks)
	}
}

// Neutral and skipped must not cry wolf.
func TestSkippedAndNeutralAreNotFailures(t *testing.T) {
	body := `{"number":1,"statusCheckRollup":[
	  {"status":"COMPLETED","conclusion":"SKIPPED"},
	  {"status":"COMPLETED","conclusion":"NEUTRAL"}]}`
	c := fakeClient(map[string]string{"/tmp/repo": body})

	pr, err := c.Fetch(context.Background(), "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Checks != ChecksPass {
		t.Fatalf("checks = %q, want pass", pr.Checks)
	}
}

// A missing or logged-out gh must be distinguishable from an ordinary absence.
// Collapsed together, the whole feature would disappear silently: every column
// empty, which looks exactly like having no PRs.
func TestGlobalFaultsAreDistinguishedFromNoPR(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"gh missing", ErrUnavailable, ErrUnavailable},
		{"logged out", errors.New("gh: To get started with GitHub CLI, please run: gh auth login"), ErrAuth},
		{"auth wording", errors.New("authentication required"), ErrAuth},
		{"ordinary absence", errors.New(`no pull requests found for branch "main"`), ErrNoPR},
		{"not a repo", errors.New("not a git repository"), ErrNoPR},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{
				Workers: 1,
				Timeout: time.Second,
				Run: func(context.Context, string, ...string) ([]byte, error) {
					return nil, tc.err
				},
			}
			_, err := c.Fetch(context.Background(), "/tmp/repo")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// One broken gh must surface from FetchAll rather than being lost among the
// per-directory results.
func TestFetchAllReportsAGlobalFault(t *testing.T) {
	c := &Client{
		Workers: 2,
		Timeout: time.Second,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, ErrUnavailable
		},
	}

	got, problem := c.FetchAll(context.Background(), []Target{{Dir: "/a"}, {Dir: "/b"}})
	if len(got) != 0 {
		t.Fatalf("got results from a broken gh: %+v", got)
	}
	if !errors.Is(problem, ErrUnavailable) {
		t.Fatalf("problem = %v, want ErrUnavailable", problem)
	}
}

// Merge state is about whether the branch can land, which is separate from
// whether it is a draft or whether checks pass.
func TestMergeState(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Merge
	}{
		{"clean", `{"number":1,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}`, MergeOK},
		{"conflicting", `{"number":1,"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY"}`, MergeConflict},
		{"dirty alone", `{"number":1,"mergeStateStatus":"DIRTY"}`, MergeConflict},
		{"behind", `{"number":1,"mergeable":"MERGEABLE","mergeStateStatus":"BEHIND"}`, MergeBehind},
		// UNKNOWN means GitHub is still computing it; claiming a state that is
		// about to change is worse than saying nothing.
		{"still computing", `{"number":1,"mergeable":"UNKNOWN"}`, MergeUnknown},
		{"absent", `{"number":1}`, MergeUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeClient(map[string]string{"/tmp/repo": tc.body})
			pr, err := c.Fetch(context.Background(), "/tmp/repo")
			if err != nil {
				t.Fatal(err)
			}
			if pr.Merge != tc.want {
				t.Fatalf("merge = %q, want %q", pr.Merge, tc.want)
			}
		})
	}
}

// A conflicting draft still needs rebasing, so draft-ness must not mask it.
func TestConflictReportedOnADraft(t *testing.T) {
	c := fakeClient(map[string]string{"/tmp/repo": `{"number":1,"isDraft":true,"mergeable":"CONFLICTING"}`})
	pr, err := c.Fetch(context.Background(), "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !pr.IsDraft || pr.Merge != MergeConflict {
		t.Fatalf("draft=%v merge=%q, want both", pr.IsDraft, pr.Merge)
	}
}

// A known URL is looked up directly rather than through the branch, which is
// how a scraped URL reaches a PR the current branch cannot resolve.
func TestTargetURLIsPassedToGh(t *testing.T) {
	var got []string
	c := &Client{
		Workers: 1,
		Timeout: time.Second,
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			got = args
			return []byte(openPR), nil
		},
	}

	if _, err := c.FetchAll(context.Background(), []Target{
		{Dir: "/tmp/repo", URL: "https://github.com/o/r/pull/9"},
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) < 3 || got[2] != "https://github.com/o/r/pull/9" {
		t.Fatalf("gh called with %v, want the URL as the argument", got)
	}
}

func TestTargetWithoutURLUsesTheBranch(t *testing.T) {
	var got []string
	c := &Client{
		Workers: 1,
		Timeout: time.Second,
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			got = args
			return []byte(openPR), nil
		},
	}

	if _, err := c.FetchAll(context.Background(), []Target{{Dir: "/tmp/repo"}}); err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if strings.HasPrefix(a, "https://") {
			t.Fatalf("a URL was passed with no target URL set: %v", got)
		}
	}
}

// Workers bounds how many gh processes exist at once. Without it a fifty-space
// board would spawn fifty.
func TestWorkersBoundConcurrency(t *testing.T) {
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)

	c := &Client{
		Workers: 3,
		Timeout: time.Second,
		Run: func(context.Context, string, ...string) ([]byte, error) {
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			inFlight--
			mu.Unlock()
			return []byte(openPR), nil
		},
	}

	var targets []Target
	for i := 0; i < 12; i++ {
		targets = append(targets, Target{Dir: fmt.Sprintf("/tmp/%d", i)})
	}
	if _, err := c.FetchAll(context.Background(), targets); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if peak > 3 {
		t.Fatalf("peak concurrency was %d, want at most 3", peak)
	}
	if peak < 2 {
		t.Fatalf("peak concurrency was %d — the workers are not being used", peak)
	}
}

// The limiter paces calls independently of concurrency: workers alone would
// let a big board fire everything as fast as the pool could cycle.
func TestLimiterPacesCalls(t *testing.T) {
	c := &Client{
		Workers: 8, // deliberately more workers than the rate allows
		Timeout: time.Second,
		Limiter: rate.NewLimiter(20, 1), // 20/s, no burst
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(openPR), nil
		},
	}

	var targets []Target
	for i := 0; i < 6; i++ {
		targets = append(targets, Target{Dir: fmt.Sprintf("/tmp/%d", i)})
	}

	start := time.Now()
	if _, err := c.FetchAll(context.Background(), targets); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	// Six calls at 20/s with a burst of one cannot finish in under ~250ms.
	if elapsed < 200*time.Millisecond {
		t.Fatalf("six calls took %s — the limiter is not pacing them", elapsed)
	}
}

// Queueing behind the limiter is not the command being slow, so it must not
// eat the per-call timeout.
func TestLimiterWaitDoesNotConsumeTheTimeout(t *testing.T) {
	c := &Client{
		Workers: 1,
		Timeout: 200 * time.Millisecond,
		Limiter: rate.NewLimiter(5, 1), // 200ms between calls
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(openPR), nil
		},
	}

	got, err := c.FetchAll(context.Background(), []Target{
		{Dir: "/a"}, {Dir: "/b"}, {Dir: "/c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3 — a queued call was timed out", len(got))
	}
}

// The background client exists to be gentler than the interactive one.
func TestBackgroundIsGentlerThanForeground(t *testing.T) {
	fg, bg := New(), NewBackground()

	if bg.Workers >= fg.Workers {
		t.Fatalf("background uses %d workers, foreground %d", bg.Workers, fg.Workers)
	}
	if bg.Limiter.Limit() >= fg.Limiter.Limit() {
		t.Fatalf("background rate %v is not below foreground %v", bg.Limiter.Limit(), fg.Limiter.Limit())
	}
	if bg.Timeout <= fg.Timeout {
		t.Fatal("background should allow a slower call, having nobody waiting")
	}
}

// A nil limiter means no pacing, which the tests elsewhere rely on.
func TestNilLimiterIsUnlimited(t *testing.T) {
	c := fakeClient(map[string]string{"/tmp/repo": openPR})
	if c.Limiter != nil {
		t.Fatal("the fake client should have no limiter")
	}
	if _, err := c.Fetch(context.Background(), "/tmp/repo"); err != nil {
		t.Fatal(err)
	}
}
