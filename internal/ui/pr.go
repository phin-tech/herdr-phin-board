package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/phin-tech/herdr-phin-board/internal/gh"
	"github.com/phin-tech/herdr-phin-board/internal/store"
)

// PR state is context, never control: it is rendered beside a space and pushed
// to the sidebar, but it never sets a status, reorders a row, or moves anything
// between groups. The user drives status.

var (
	prPassStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	prFailStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	prPendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	prDimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

type prLoadedMsg struct {
	found map[string]gh.PR
	// missing lists directories that were checked and turned out to have no
	// PR, so a cached one can be dropped rather than lingering.
	missing []string
	// problem is a fault affecting every directory rather than one -- gh
	// missing or logged out. Reported once, because otherwise the whole
	// feature is silently absent and looks like having no PRs.
	problem error
}

// loadPRs refreshes PR state for every space whose cache entry is stale. It
// runs off the UI thread; the board paints cached values meanwhile.
func (m *Model) loadPRs() tea.Cmd {
	if m.gh == nil || m.prLoading {
		// A fetch is already running. Herdr emits workspace events freely, and
		// each one refreshes the board, so without this guard a busy session
		// would stack overlapping rounds of gh subprocesses.
		return nil
	}

	var dirs []string
	for _, group := range m.groups {
		for _, sp := range group {
			if m.prCache.Stale(sp.Key) {
				dirs = append(dirs, sp.Key)
			}
		}
	}
	if len(dirs) == 0 {
		return nil
	}

	m.prLoading = true
	client := m.gh
	return func() tea.Msg {
		found, problem := client.FetchAll(context.Background(), dirs)

		var missing []string
		for _, dir := range dirs {
			if _, ok := found[dir]; !ok {
				missing = append(missing, dir)
			}
		}
		return prLoadedMsg{found: found, missing: missing, problem: problem}
	}
}

// applyPRs folds a fetch result into the cache and pushes the sidebar token.
func (m *Model) applyPRs(msg prLoadedMsg) tea.Cmd {
	m.prLoading = false

	// A broken gh is worth saying out loud exactly once. Recording the
	// absences below would otherwise bake the fault into the cache and the
	// board would quietly show empty PR columns for half an hour.
	if msg.problem != nil {
		if !m.prProblemSaid {
			m.prProblemSaid = true
			m.status = msg.problem.Error()
		}
		return nil
	}

	for dir, pr := range msg.found {
		m.prCache.Put(dir, pr)
	}
	// Record the absences rather than dropping them, so a space with no PR is
	// not re-asked on every single board refresh.
	for _, dir := range msg.missing {
		m.prCache.PutAbsent(dir)
	}

	keep := map[string]bool{}
	for _, group := range m.groups {
		for _, sp := range group {
			keep[sp.Key] = true
		}
	}
	m.prCache.Prune(keep)
	_ = m.prCache.Save()

	m.rebuild()
	return m.syncPRTokens()
}

// prFor returns the cached PR for a space, if any.
func (m *Model) prFor(key string) (gh.PR, bool) {
	if m.prCache == nil {
		return gh.PR{}, false
	}
	pr, ok := m.prCache.Entries[key]
	// A cached absence is a record that we looked, not a PR to display.
	if !ok || !pr.Found() {
		return gh.PR{}, false
	}
	return pr, true
}

// syncPRTokens mirrors PR state into a $pr sidebar token. The sidebar is
// narrow, so this is the terse form.
func (m *Model) syncPRTokens() tea.Cmd {
	type push struct {
		workspaceID string
		value       *string
	}
	var pushes []push

	for _, group := range m.groups {
		for _, sp := range group {
			if !sp.Live {
				continue
			}
			var value *string
			if pr, ok := m.prFor(sp.Key); ok {
				text := prShort(pr)
				value = &text
			}
			for _, id := range sp.WorkspaceIDs {
				pushes = append(pushes, push{id, value})
			}
		}
	}

	client := m.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		for _, p := range pushes {
			_ = client.ReportToken(p.workspaceID, "pr", p.value)
		}
		return tokensSyncedMsg{}
	}
}

// prStateSymbol distinguishes draft, open, merged and closed at a glance.
func prStateSymbol(pr gh.PR) string {
	switch {
	case pr.IsDraft:
		return "○"
	case pr.State == "MERGED":
		return "◆"
	case pr.State == "CLOSED":
		return "✕"
	default:
		return "●"
	}
}

func prChecksSymbol(pr gh.PR) string {
	switch pr.Checks {
	case gh.ChecksPass:
		return "✓"
	case gh.ChecksFail:
		return "✗"
	case gh.ChecksPending:
		return "·"
	default:
		return ""
	}
}

func prChecksStyle(pr gh.PR) lipgloss.Style {
	switch pr.Checks {
	case gh.ChecksPass:
		return prPassStyle
	case gh.ChecksFail:
		return prFailStyle
	case gh.ChecksPending:
		return prPendingStyle
	default:
		return prDimStyle
	}
}

// prReview renders the review decision in the words GitHub uses for it.
func prReview(pr gh.PR) string {
	switch pr.Review {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes"
	case "REVIEW_REQUIRED":
		return "review"
	default:
		return ""
	}
}

// prShort is the sidebar and narrow-column form: #123 ●✓
func prShort(pr gh.PR) string {
	out := fmt.Sprintf("#%d %s", pr.Number, prStateSymbol(pr))
	if s := prChecksSymbol(pr); s != "" {
		out += s
	}
	return out
}

// prCell is the table form: #123 ● approved ✓
func prCell(pr gh.PR) string {
	parts := []string{fmt.Sprintf("#%d", pr.Number), prStateSymbol(pr)}
	if r := prReview(pr); r != "" {
		parts = append(parts, r)
	}
	if s := prChecksSymbol(pr); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// prStyled colours the cell by whatever most needs attention: a failing check
// first, then a changes-requested review.
func prStyled(pr gh.PR, width int) string {
	text := truncate(prCell(pr), width)
	switch {
	case pr.Checks == gh.ChecksFail:
		return prFailStyle.Render(text)
	case pr.Review == "CHANGES_REQUESTED":
		return prFailStyle.Render(text)
	case pr.Review == "APPROVED" && pr.Checks != gh.ChecksPending:
		return prPassStyle.Render(text)
	case pr.Checks == gh.ChecksPending:
		return prPendingStyle.Render(text)
	default:
		return prDimStyle.Render(text)
	}
}

// prDetailLines are the fuller form used in the detail pane and modal.
func prDetailLines(pr gh.PR, width int) []string {
	state := strings.ToLower(pr.State)
	if pr.IsDraft {
		state = "draft"
	}

	head := fmt.Sprintf("#%d %s", pr.Number, state)
	if r := prReview(pr); r != "" {
		head += " · " + r
	}
	lines := []string{prDimStyle.Render(truncate(head, width))}

	if s := prChecksSymbol(pr); s != "" {
		lines = append(lines, prChecksStyle(pr).Render(truncate("checks "+string(pr.Checks)+" "+s, width)))
	}
	for _, line := range wrap(pr.Title, width) {
		lines = append(lines, prDimStyle.Render(line))
	}
	return lines
}

// prKeys is used by tests and the store to keep the two packages honest about
// what a space key is.
var _ = store.Key
