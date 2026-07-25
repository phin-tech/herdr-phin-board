package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/phin-tech/herdr-phin-board/internal/gh"
)

func failingPR() gh.PR {
	return gh.PR{
		Number: 123,
		State:  "OPEN",
		Checks: gh.ChecksFail,
		URL:    "https://github.com/o/r/pull/123",
		Notable: []gh.Check{
			{Name: "build", State: gh.ChecksFail, URL: "https://github.com/o/r/actions/runs/1/job/2"},
			{Name: "lint", State: gh.ChecksFail, URL: "https://github.com/o/r/actions/runs/1/job/3"},
			{Name: "e2e", State: gh.ChecksPending},
		},
	}
}

// The message carries the error itself, not a link to go and find it. That is
// the whole difference between this and pressing gp.
func TestTheFailureMessageCarriesTheLog(t *testing.T) {
	m := newTestModel(t)
	m.gh = &gh.Client{
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("build\tRun tests\t2026-07-25T10:00:00.0000000Z FAIL: TestThing\n"), nil
		},
	}
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	withPR(m, "/tmp/api", failingPR())

	cmd := m.sendFailure()
	if cmd == nil {
		t.Fatal("gf did nothing on a failing PR")
	}
	msg, ok := cmd().(failureFetchedMsg)
	if !ok {
		t.Fatalf("got %T, want a composed message", cmd())
	}

	for _, want := range []string{
		"#123",            // which PR
		"build",           // which check
		"FAIL: TestThing", // the actual error
		"https://github.com/o/r/actions/runs/1/job/2", // where to read the rest
		"1 other check", // lint is failing too
	} {
		if !strings.Contains(msg.text, want) {
			t.Fatalf("the message omits %q:\n%s", want, msg.text)
		}
	}
	if strings.Contains(msg.text, "2026-07-25T10") {
		t.Fatalf("the log kept its timestamps:\n%s", msg.text)
	}
}

// A check outside Actions has no log to read. The message is still worth
// sending -- the name and the link are what the user would have copied anyway.
func TestAFailureWithNoLogStillSends(t *testing.T) {
	m := newTestModel(t)
	m.gh = gh.New()
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	withPR(m, "/tmp/api", gh.PR{
		Number: 7, State: "OPEN", Checks: gh.ChecksFail,
		Notable: []gh.Check{{Name: "ci/circleci", State: gh.ChecksFail, URL: "https://circleci.com/gh/o/r/9"}},
	})

	cmd := m.sendFailure()
	if cmd == nil {
		t.Fatal("gf did nothing")
	}
	msg, ok := cmd().(failureFetchedMsg)
	if !ok {
		t.Fatalf("got %T, want a composed message", cmd())
	}
	if !strings.Contains(msg.text, "ci/circleci") || !strings.Contains(msg.text, "circleci.com") {
		t.Fatalf("the message lost the check it is about:\n%s", msg.text)
	}
}

// Nothing failing, nothing to send -- and it says which, rather than sitting
// there having apparently ignored the key.
func TestGFOnAGreenPRExplainsItself(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")
	withPR(m, "/tmp/api", gh.PR{Number: 5, State: "OPEN", Checks: gh.ChecksPass})

	if cmd := m.sendFailure(); cmd != nil {
		t.Fatal("gf queued work for a passing PR")
	}
	if !strings.Contains(m.status, "nothing failing") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestGFWithoutAPRSaysSo(t *testing.T) {
	m := newTestModel(t)
	send(t, m, liveWorkspaces())
	selectSpace(t, m, "/tmp/api")

	if cmd := m.sendFailure(); cmd != nil {
		t.Fatal("gf queued work for a space with no PR")
	}
	if !strings.Contains(m.status, "no pull request") {
		t.Fatalf("status = %q", m.status)
	}
}

// The chord is what the user actually presses, and it must reach the same place
// from the board and from the detail modal.
func TestTheChordReachesTheFailurePath(t *testing.T) {
	for _, mode := range []mode{modeNormal, modeDetail} {
		m := newTestModel(t)
		send(t, m, liveWorkspaces())
		selectSpace(t, m, "/tmp/api")
		withPR(m, "/tmp/api", gh.PR{Number: 5, State: "OPEN", Checks: gh.ChecksPass})
		m.mode = mode

		m.Update(key("g"))
		if m.chord != "g" {
			t.Fatalf("mode %v: g did not start a chord", mode)
		}
		m.Update(key("f"))
		if !strings.Contains(m.status, "nothing failing") {
			t.Fatalf("mode %v: gf did not reach the failure path, status = %q", mode, m.status)
		}
	}
}
