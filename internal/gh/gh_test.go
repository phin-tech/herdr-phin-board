package gh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

	got, problem := c.FetchAll(context.Background(), []string{"/tmp/withpr", "/tmp/nopr", "/tmp/other"})
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

	got, _ := c.FetchAll(context.Background(), dirs)
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

	got, problem := c.FetchAll(context.Background(), []string{"/a", "/b"})
	if len(got) != 0 {
		t.Fatalf("got results from a broken gh: %+v", got)
	}
	if !errors.Is(problem, ErrUnavailable) {
		t.Fatalf("problem = %v, want ErrUnavailable", problem)
	}
}
