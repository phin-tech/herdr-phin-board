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

	"golang.org/x/time/rate"
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
	Number  int    `json:"number"`
	State   string `json:"state"` // OPEN, MERGED, CLOSED
	IsDraft bool   `json:"is_draft"`
	Review  string `json:"review"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED
	Checks  Checks `json:"checks"`
	// Merge is the branch's standing against its base, which is actionable in
	// a way passing checks is not: conflicts and being behind need a rebase.
	Merge Merge `json:"merge,omitempty"`
	// Notable names the checks worth reading: the failing ones, then the ones
	// still running. Passing checks are omitted -- a PR can have forty, and
	// "which one broke" is the only question the detail view needs to answer.
	Notable []Check   `json:"notable,omitempty"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Fetched time.Time `json:"fetched"`
}

// Check is one named check run, kept only when it needs attention.
type Check struct {
	Name  string `json:"name"`
	State Checks `json:"state"` // ChecksFail or ChecksPending
	// URL points at the run or job, so a failing check can be opened.
	URL string `json:"url,omitempty"`
}

// Merge summarises whether the PR can actually land.
type Merge string

const (
	MergeUnknown  Merge = ""
	MergeOK       Merge = "ok"
	MergeConflict Merge = "conflict"
	MergeBehind   Merge = "behind"
)

// Found reports whether this record is an actual PR rather than a cached
// "checked, nothing here".
func (p PR) Found() bool { return p.Number > 0 }

// Runner executes a command in a directory. Swapped out in tests.
type Runner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// Client fetches PR state.
type Client struct {
	Run Runner
	// Workers bounds how many gh calls are in flight at once.
	Workers int
	Timeout time.Duration
	// Limiter smooths the rate at which calls leave, independently of how many
	// run concurrently. Workers alone would let a fifty-space board fire fifty
	// requests as fast as four at a time can manage; this paces them. Nil
	// means no limit.
	Limiter *rate.Limiter
}

// Foreground and background want different things. Someone watching the board
// is waiting for an answer, so latency wins; the watcher has two minutes and
// nobody looking, so it can afford to be gentle.
const (
	foregroundWorkers = 4
	foregroundRate    = 5 // calls per second
	foregroundBurst   = 8

	backgroundWorkers = 2
	backgroundRate    = 1
	backgroundBurst   = 4
)

// New builds a client for interactive use: the board is open and someone is
// waiting, so it favours getting answers back quickly.
func New() *Client {
	return &Client{
		Run:     execRunner,
		Workers: foregroundWorkers,
		Timeout: 10 * time.Second,
		Limiter: rate.NewLimiter(foregroundRate, foregroundBurst),
	}
}

// NewBackground builds a client for the watcher: nobody is waiting, so it
// trickles rather than bursts.
func NewBackground() *Client {
	return &Client{
		Run:     execRunner,
		Workers: backgroundWorkers,
		Timeout: 15 * time.Second,
		Limiter: rate.NewLimiter(backgroundRate, backgroundBurst),
	}
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
	Mergeable         string `json:"mergeable"`
	MergeStateStatus  string `json:"mergeStateStatus"`
	StatusCheckRollup []struct {
		Typename     string `json:"__typename"`
		Name         string `json:"name"`         // check runs
		Context      string `json:"context"`      // status contexts
		WorkflowName string `json:"workflowName"` // check runs
		DetailsURL   string `json:"detailsUrl"`   // check runs
		TargetURL    string `json:"targetUrl"`    // status contexts
		Status       string `json:"status"`       // check runs
		Conclusion   string `json:"conclusion"`   // check runs
		State        string `json:"state"`        // status contexts
	} `json:"statusCheckRollup"`
}

const prFields = "number,state,isDraft,reviewDecision,mergeable,mergeStateStatus,statusCheckRollup,title,url"

// maxNotable caps how many check names are kept. A wall of red is no more
// useful than a count, and the cache should stay small.
const maxNotable = 6

// Target is a directory to look up, optionally with a PR URL already known --
// scraped from an agent's output, say, which finds a PR the branch lookup
// cannot see.
type Target struct {
	Dir string
	URL string
}

// Fetch reads the PR for one directory, by its current branch.
func (c *Client) Fetch(ctx context.Context, dir string) (PR, error) {
	return c.fetchTarget(ctx, Target{Dir: dir})
}

func (c *Client) fetchTarget(ctx context.Context, target Target) (PR, error) {
	// Wait for a token before starting the clock: time spent queued behind the
	// rate limit is not the command being slow, and should not time it out.
	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return PR{}, ErrNoPR
		}
	}

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	args := []string{"pr", "view", "--json", prFields}
	if target.URL != "" {
		// gh accepts a URL in place of the branch lookup, which is how a
		// scraped URL reaches a PR the current branch would not resolve.
		args = []string{"pr", "view", target.URL, "--json", prFields}
	}
	dir := target.Dir

	out, err := c.Run(ctx, dir, args...)
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
		Notable: notable(raw),
		Merge:   mergeState(raw),
		Title:   raw.Title,
		URL:     raw.URL,
		Fetched: time.Now().UTC(),
	}, nil
}

// notable lists the checks worth naming: failures first, since those are what
// you act on, then whatever is still running.
func notable(raw rawPR) []Check {
	var failed, running []Check
	for _, c := range raw.StatusCheckRollup {
		name := c.Name
		if name == "" {
			name = c.Context
		}
		if name == "" {
			continue
		}
		url := c.DetailsURL
		if url == "" {
			url = c.TargetURL
		}
		switch {
		case checkFailed(c.Conclusion, c.State):
			failed = append(failed, Check{Name: name, State: ChecksFail, URL: url})
		case checkRunning(c.Status, c.State):
			running = append(running, Check{Name: name, State: ChecksPending, URL: url})
		}
	}

	out := append(failed, running...)
	if len(out) > maxNotable {
		out = out[:maxNotable]
	}
	return out
}

// mergeState reads whether the branch can land, independently of whether it is
// a draft -- a draft with conflicts still needs rebasing.
func mergeState(raw rawPR) Merge {
	switch {
	case raw.Mergeable == "CONFLICTING", raw.MergeStateStatus == "DIRTY":
		return MergeConflict
	case raw.MergeStateStatus == "BEHIND":
		return MergeBehind
	case raw.Mergeable == "MERGEABLE":
		return MergeOK
	default:
		// UNKNOWN means GitHub is still computing it; saying nothing beats
		// claiming a state that is about to change.
		return MergeUnknown
	}
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
		case checkFailed(c.Conclusion, c.State):
			return ChecksFail
		case checkRunning(c.Status, c.State):
			pending = true
		}
	}
	if pending {
		return ChecksPending
	}
	return ChecksPass
}

// checkFailed and checkRunning are shared by the rollup verdict and the named
// list, so the summary and the detail can never disagree.
func checkFailed(conclusion, state string) bool {
	switch conclusion {
	case "FAILURE", "TIMED_OUT", "CANCELLED", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return true
	}
	return state == "FAILURE" || state == "ERROR"
}

func checkRunning(status, state string) bool {
	return (status != "" && status != "COMPLETED") || state == "PENDING"
}

// FetchAll reads PRs for many targets concurrently, returning only those that
// have one, plus any problem that applies to every target rather than to one --
// gh missing or logged out.
func (c *Client) FetchAll(ctx context.Context, targets []Target) (map[string]PR, error) {
	sort.Slice(targets, func(i, j int) bool { return targets[i].Dir < targets[j].Dir })

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

	for _, target := range targets {
		wg.Add(1)
		go func(target Target) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			pr, err := c.fetchTarget(ctx, target)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !errors.Is(err, ErrNoPR) && problem == nil {
					problem = err
				}
				return
			}
			out[target.Dir] = pr
		}(target)
	}
	wg.Wait()
	return out, problem
}
