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
	"fmt"
	"os/exec"
	"sort"
	"strings"
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
	// Herdr launches plugins with a minimal PATH, so gh may be installed and
	// still not resolve here. That has to be distinguishable from "no PR".
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, ErrUnavailable
	}

	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir

	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	// gh puts its reason on stderr; keep it so the caller can classify.
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(exit.Stderr)))
	}
	return nil, err
}

var (
	// ErrNoPR means the directory has no pull request for its current branch --
	// including not being a repo at all, or having no remote.
	ErrNoPR = errors.New("no pull request for this directory")
	// ErrUnavailable means gh is not on PATH. Left indistinguishable from
	// ErrNoPR this would silently disable the whole feature: every column
	// would simply be empty, which looks exactly like having no PRs.
	ErrUnavailable = errors.New("gh was not found on PATH")
	// ErrAuth means gh is installed but not logged in.
	ErrAuth = errors.New("gh is not logged in — run: gh auth login")
)

// classify turns a gh failure into something worth telling the user about, or
// ErrNoPR when it is just an ordinary absence.
func classify(err error) error {
	if errors.Is(err, ErrUnavailable) {
		return ErrUnavailable
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not logged in"),
		strings.Contains(msg, "authentication"),
		strings.Contains(msg, "gh auth login"):
		return ErrAuth
	default:
		return ErrNoPR
	}
}

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
		// gh exits non-zero for "no PR", "not a repo" and "no remote" alike;
		// those are ordinary absences. A missing or unauthenticated gh is not,
		// because it silently disables the feature everywhere.
		return PR{}, classify(err)
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
// that have one, plus any problem that applies to every directory rather than
// to one -- gh missing or logged out.
func (c *Client) FetchAll(ctx context.Context, dirs []string) (map[string]PR, error) {
	sort.Strings(dirs)

	workers := c.Workers
	if workers < 1 {
		workers = 1
	}

	var (
		mu      sync.Mutex
		out     = map[string]PR{}
		problem error
		wg      sync.WaitGroup
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
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !errors.Is(err, ErrNoPR) && problem == nil {
					problem = err
				}
				return
			}
			out[dir] = pr
		}(dir)
	}
	wg.Wait()
	return out, problem
}
