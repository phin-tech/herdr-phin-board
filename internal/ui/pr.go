package ui

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/phin-tech/herdr-phin-board/internal/gh"
	"github.com/phin-tech/herdr-phin-board/internal/herdr"
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

	// Each stale space is looked up by directory, and the panes open on it are
	// carried along so an agent's own output can be searched for a PR URL.
	panes := map[string][]string{}
	for _, ws := range m.live {
		key := store.Key(ws.Cwd)
		panes[key] = append(panes[key], ws.PaneIDs...)
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
	ghClient, herdrClient := m.gh, m.client
	return func() tea.Msg {
		targets := make([]gh.Target, 0, len(dirs))
		for _, dir := range dirs {
			targets = append(targets, gh.Target{
				Dir: dir,
				URL: scrapePRURL(herdrClient, panes[dir]),
			})
		}

		found, problem := ghClient.FetchAll(context.Background(), targets)

		var missing []string
		for _, dir := range dirs {
			if _, ok := found[dir]; !ok {
				missing = append(missing, dir)
			}
		}
		return prLoadedMsg{found: found, missing: missing, problem: problem}
	}
}

// prURLPattern matches a pull request URL an agent printed, on any GitHub host.
var prURLPattern = regexp.MustCompile(`https://[^\s"'<>]+/pull/\d+`)

// scrapePRURL looks through a space's panes for the most recent pull request
// URL. Borrowed from Matovidlo/herdr-pr-tracker: an agent announces the PR it
// just opened, which finds it before any branch lookup could.
//
// It is a hint, not an answer -- gh still resolves the URL, so a stale or
// mistyped one simply falls back to nothing.
func scrapePRURL(client *herdr.Client, paneIDs []string) string {
	if client == nil {
		return ""
	}
	for _, id := range paneIDs {
		text, err := client.ReadPane(id, prScrapeLines)
		if err != nil {
			continue
		}
		if matches := prURLPattern.FindAllString(text, -1); len(matches) > 0 {
			// The last one wins: an agent that opened two PRs in a session
			// most recently announced the one you care about.
			return matches[len(matches)-1]
		}
	}
	return ""
}

// prScrapeLines bounds how far back to look. Deep enough to survive a chatty
// agent, shallow enough that reading several panes stays cheap.
const prScrapeLines = 2000

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

// prMerge names a branch that cannot land as it stands. A mergeable branch
// says nothing: it is the normal case, and the column is for what needs doing.
func prMerge(pr gh.PR) string {
	switch pr.Merge {
	case gh.MergeConflict:
		return "conflict"
	case gh.MergeBehind:
		return "behind"
	default:
		return ""
	}
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
	if mg := prMerge(pr); mg != "" {
		parts = append(parts, mg)
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
	case pr.Merge == gh.MergeConflict:
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
	switch pr.Merge {
	case gh.MergeConflict:
		lines = append(lines, prFailStyle.Render(truncate("conflicts with base — needs a rebase", width)))
	case gh.MergeBehind:
		lines = append(lines, prPendingStyle.Render(truncate("behind base", width)))
	}
	for _, line := range wrap(pr.Title, width) {
		lines = append(lines, prDimStyle.Render(line))
	}
	return lines
}
