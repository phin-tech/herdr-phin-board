package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phin-tech/herdr-phin-board/internal/gh"
)

// `gf` hands a failing check to the space's agent: the check's name, the end of
// its log, and the link. The board is where you notice CI has gone red; this is
// the rest of that thought, without opening a browser to copy an error out of
// it.
//
// It stops where `m` stops -- the text is typed into the agent's input, not
// submitted. What to do about the failure is still yours to say.

type failureFetchedMsg struct{ text string }

// sendFailure builds the message and hands it to the same send path as `m`.
func (m *Model) sendFailure() tea.Cmd {
	sp := m.selected()
	if sp == nil {
		m.status = "no space selected"
		return nil
	}

	pr, ok := m.prFor(sp.Key)
	if !ok {
		m.status = "no pull request for " + sp.Label
		return nil
	}

	check, ok := firstFailing(pr)
	if !ok {
		if pr.Checks == gh.ChecksFail {
			// The rollup says something failed but no name came with it, which
			// happens when the cache predates the notable list.
			m.status = fmt.Sprintf("#%d is failing — press r to fetch which check", pr.Number)
			return nil
		}
		m.status = fmt.Sprintf("nothing failing on #%d", pr.Number)
		return nil
	}

	ghClient, dir := m.gh, sp.Key
	m.status = fmt.Sprintf("reading %s…", check.Name)

	return func() tea.Msg {
		log, err := ghClient.FailedLog(context.Background(), dir, check)
		if err != nil && !errors.Is(err, gh.ErrNoLog) {
			// gh missing or logged out is worth saying; anything else about
			// this one job is not, because the message is still worth sending
			// without its log.
			return statusMsg(err.Error())
		}
		return failureFetchedMsg{text: failureMessage(pr, check, log)}
	}
}

// firstFailing picks the check to talk about. Notable already leads with the
// failures, so this is the first one -- and when several broke, the message
// says so rather than pretending it is the whole story.
func firstFailing(pr gh.PR) (gh.Check, bool) {
	for _, c := range pr.Notable {
		if c.State == gh.ChecksFail {
			return c, true
		}
	}
	return gh.Check{}, false
}

func countFailing(pr gh.PR) int {
	n := 0
	for _, c := range pr.Notable {
		if c.State == gh.ChecksFail {
			n++
		}
	}
	return n
}

// failureMessage is what lands in the agent's input.
//
// It opens with the fact, so a person skimming their own input line knows what
// it is, and closes with the link, so the full log is one click away when the
// tail is not enough.
func failureMessage(pr gh.PR, check gh.Check, log string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "CI is failing on #%d: %s", pr.Number, check.Name)
	if others := countFailing(pr) - 1; others > 0 {
		fmt.Fprintf(&b, " (and %s)", plural(others, "other check", "other checks"))
	}
	b.WriteString("\n")

	if log != "" {
		b.WriteString("\n" + log + "\n")
	}
	if url := check.URL; url != "" {
		b.WriteString("\n" + url + "\n")
	}
	return b.String()
}
