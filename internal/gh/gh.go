// Package gh reads pull request state for a directory via the gh CLI.
//
// gh is used rather than the GitHub API directly because it already holds the
// user's credentials and resolves the repo and branch from a working
// directory -- which is exactly the mapping the board needs, since a space is
// a directory and a worktree is a branch.
package gh

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sort"
	"sync"
	"time"
)

// Checks summarises a PR's check runs.
type Checks string

const (
	ChecksNone    Checks = ""
	ChecksPass    Checks = "pass"
	ChecksFail    Checks = "fail"
	ChecksPending Checks = "pending"
)

// PR is the slice of pull request state the board displays.
type PR struct {
	Number  int       `json:"number"`
	State   string    `json:"state"` // OPEN, MERGED, CLOSED
	IsDraft bool      `json:"is_draft"`
	Review  string    `json:"review"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED
	Checks  Checks    `json:"checks"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Fetched time.Time `json:"fetched"`
}

// Found reports whether this record is an actual PR rather than a cached
// "checked, nothing here".
func (p PR) Found() bool { return p.Number > 0 }

// Runner executes a command in a directory. Swapped out in tests.
type Runner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// Client fetches PR state.
type Client struct {
	Run Runner
	// Workers bounds concurrent gh calls so a board with many spaces does not
	// open a connection per row.
	Workers int
	Timeout time.Duration
}

// New builds a client that shells out to gh.
func New() *Client {
	return &Client{Run: execRunner, Workers: 4, Timeout: 10 * time.Second}
}

func execRunner(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// ErrNoPR means the directory has no pull request for its current branch --
// including the common cases of not being a repo at all, or having no remote.
var ErrNoPR = errors.New("no pull request for this directory")

// rawPR mirrors the gh JSON shape.
type rawPR struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	IsDraft           bool   `json:"isDraft"`
	ReviewDecision    string `json:"reviewDecision"`
	Title             string `json:"title"`
	URL               string `json:"url"`
	StatusCheckRollup []struct {
		Typename   string `json:"__typename"`
		Status     string `json:"status"`     // check runs
		Conclusion string `json:"conclusion"` // check runs
		State      string `json:"state"`      // status contexts
	} `json:"statusCheckRollup"`
}

const prFields = "number,state,isDraft,reviewDecision,statusCheckRollup,title,url"

// Fetch reads the PR for one directory.
func (c *Client) Fetch(ctx context.Context, dir string) (PR, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	out, err := c.Run(ctx, dir, "pr", "view", "--json", prFields)
	if err != nil {
		// gh exits non-zero for "no PR", "not a repo", "no remote" and "not
		// logged in" alike. None of them are worth surfacing per row.
		return PR{}, ErrNoPR
	}

	var raw rawPR
	if err := json.Unmarshal(out, &raw); err != nil {
		return PR{}, ErrNoPR
	}
	if raw.Number == 0 {
		return PR{}, ErrNoPR
	}

	return PR{
		Number:  raw.Number,
		State:   raw.State,
		IsDraft: raw.IsDraft,
		Review:  raw.ReviewDecision,
		Checks:  rollup(raw),
		Title:   raw.Title,
		URL:     raw.URL,
		Fetched: time.Now().UTC(),
	}, nil
}

// rollup reduces the check runs to one verdict. A single failure outranks
// anything still running, because that is the thing you need to act on.
func rollup(raw rawPR) Checks {
	if len(raw.StatusCheckRollup) == 0 {
		return ChecksNone
	}
	// SKIPPED and NEUTRAL are deliberately not failures: a skipped job is a
	// workflow saying it had nothing to do, and failing a PR for it would make
	// the column cry wolf on most repos.
	pending := false
	for _, c := range raw.StatusCheckRollup {
		switch {
		case c.Conclusion == "FAILURE", c.Conclusion == "TIMED_OUT",
			c.Conclusion == "CANCELLED", c.Conclusion == "STARTUP_FAILURE",
			c.Conclusion == "ACTION_REQUIRED",
			c.State == "FAILURE", c.State == "ERROR":
			return ChecksFail
		case c.Status != "" && c.Status != "COMPLETED", c.State == "PENDING":
			pending = true
		}
	}
	if pending {
		return ChecksPending
	}
	return ChecksPass
}

// FetchAll reads PRs for many directories concurrently, returning only those
// that have one.
func (c *Client) FetchAll(ctx context.Context, dirs []string) map[string]PR {
	sort.Strings(dirs)

	workers := c.Workers
	if workers < 1 {
		workers = 1
	}

	var (
		mu  sync.Mutex
		out = map[string]PR{}
		wg  sync.WaitGroup
	)
	sem := make(chan struct{}, workers)

	for _, dir := range dirs {
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			pr, err := c.Fetch(ctx, dir)
			if err != nil {
				return
			}
			mu.Lock()
			out[dir] = pr
			mu.Unlock()
		}(dir)
	}
	wg.Wait()
	return out
}
