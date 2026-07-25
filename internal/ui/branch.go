package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/herdr"
	"github.com/phin-tech/herdr-phin-board/internal/store"
)

// Branches come from Herdr's worktree list rather than the session snapshot,
// which carries a workspace's directory but not what is checked out in it.
//
// A worktree per branch is the workflow this board suits best, and until now
// the branch was visible in Herdr's own sidebar but not here.

type branchesMsg struct {
	branches map[string]string
	// full is set when the round covered every live space, which is what
	// advances the rescan clock. A round that only resolved a newly opened
	// space says nothing about how stale the rest have become.
	full bool
}

// loadBranches resolves a branch for every space that needs one.
//
// worktree.list answers for a whole repository, so directories that share one
// are resolved together and asked about once. A space that is not a git
// checkout simply has no branch, which is not worth reporting.
//
// Herdr runs a git subprocess per repository to answer this, so it is asked
// about spaces whose branch is unknown, and otherwise only once a branchTTL --
// not on every workspace event, which is what a checkout does not raise anyway.
func (m *Model) loadBranches() tea.Cmd {
	if m.client == nil || m.branchLoading {
		return nil
	}

	full := time.Since(m.branchesAt) > branchTTL

	var dirs []string
	for _, group := range m.groups {
		for _, sp := range group {
			if !sp.Live {
				continue
			}
			if _, known := m.branches[sp.Key]; known && !full {
				continue
			}
			dirs = append(dirs, sp.Key)
		}
	}
	if len(dirs) == 0 {
		return nil
	}

	m.branchLoading = true
	client := m.client
	return func() tea.Msg {
		out := map[string]string{}
		for _, dir := range dirs {
			if _, done := out[dir]; done {
				continue // already answered by another space's repository
			}
			worktrees, err := client.Worktrees(dir)
			if err != nil {
				// Not a repository, or Herdr could not say. Record the answer
				// so another space does not ask about the same directory.
				out[dir] = ""
				continue
			}
			for key, branch := range branchMap(worktrees) {
				out[key] = branch
			}
			if _, ok := out[dir]; !ok {
				// Answered, but this directory was not among the checkouts.
				out[dir] = ""
			}
		}
		return branchesMsg{branches: out, full: full}
	}
}

// branchMap keys a repository's checkouts by canonical path.
//
// A detached HEAD is deliberately omitted: it has a commit, not a branch, and
// showing a bare SHA where a branch name goes reads as a bug.
func branchMap(worktrees []herdr.Worktree) map[string]string {
	out := map[string]string{}
	for _, w := range worktrees {
		if w.Branch == "" || w.IsDetached {
			continue
		}
		if key := store.Key(w.Path); key != "" {
			out[key] = w.Branch
		}
	}
	return out
}

// branchFor returns a space's branch, if it has one.
func (m *Model) branchFor(key string) string {
	return m.branches[key]
}

// anyBranch reports whether any space has one, so the column can stay away
// when nothing would fill it.
func (m *Model) anyBranch() bool {
	for _, b := range m.branches {
		if b != "" {
			return true
		}
	}
	return false
}
