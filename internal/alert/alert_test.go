package alert

import (
	"testing"
	"time"

	"github.com/phin-tech/herdr-phin-board/internal/gh"
)

func TestTransitionsOnlyFireOnChange(t *testing.T) {
	failing := gh.PR{Number: 1, State: "OPEN", Checks: gh.ChecksFail}

	// Already failing and still failing is not news. Announcing state rather
	// than change would re-notify on every poll until it was fixed.
	if got := Transitions("api", failing, failing); len(got) != 0 {
		t.Fatalf("a steady state raised %d alerts", len(got))
	}

	passing := gh.PR{Number: 1, State: "OPEN", Checks: gh.ChecksPass}
	got := Transitions("api", passing, failing)
	if len(got) != 1 || got[0].Kind != ChecksFailed {
		t.Fatalf("pass to fail raised %+v", got)
	}
}

// A PR seen for the first time is the starting point, not news -- otherwise the
// first poll would be a wall of toasts for work you already knew about.
func TestFirstSightIsNotAnAlert(t *testing.T) {
	after := gh.PR{Number: 1, State: "OPEN", Checks: gh.ChecksFail, Review: "CHANGES_REQUESTED"}
	if got := Transitions("api", gh.PR{}, after); len(got) != 0 {
		t.Fatalf("first sight raised %+v", got)
	}
}

func TestEveryWatchedTransition(t *testing.T) {
	base := gh.PR{Number: 7, State: "OPEN", Checks: gh.ChecksPass}

	cases := []struct {
		name  string
		after gh.PR
		want  Kind
	}{
		{"checks broke", gh.PR{Number: 7, State: "OPEN", Checks: gh.ChecksFail}, ChecksFailed},
		{"approved", gh.PR{Number: 7, State: "OPEN", Checks: gh.ChecksPass, Review: "APPROVED"}, ReviewApproved},
		{"changes asked", gh.PR{Number: 7, State: "OPEN", Checks: gh.ChecksPass, Review: "CHANGES_REQUESTED"}, ChangesAsked},
		{"conflicted", gh.PR{Number: 7, State: "OPEN", Checks: gh.ChecksPass, Merge: gh.MergeConflict}, Conflicted},
		{"merged", gh.PR{Number: 7, State: "MERGED", Checks: gh.ChecksPass}, Merged},
		{"closed", gh.PR{Number: 7, State: "CLOSED", Checks: gh.ChecksPass}, Closed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Transitions("api", base, tc.after)
			if len(got) != 1 || got[0].Kind != tc.want {
				t.Fatalf("got %+v, want one %s", got, tc.want)
			}
			if got[0].Text == "" || got[0].Title() == "" {
				t.Fatal("an alert with no words to show")
			}
		})
	}
}

// Things needing a decision sound different from things merely finishing.
func TestSoundsSplitByUrgency(t *testing.T) {
	for _, k := range []Kind{ChecksFailed, ChangesAsked, Conflicted} {
		if got := (Alert{Kind: k}).Sound(); got != "request" {
			t.Fatalf("%s sounds %q, want request", k, got)
		}
	}
	for _, k := range []Kind{ReviewApproved, Merged, Closed} {
		if got := (Alert{Kind: k}).Sound(); got != "done" {
			t.Fatalf("%s sounds %q, want done", k, got)
		}
	}
}

func TestStoreRoundTripAndClear(t *testing.T) {
	dir := t.TempDir()
	s := Load(dir)
	s.Add("/tmp/api", Alert{Kind: ChecksFailed, Text: "#1 checks failed"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again := Load(dir)
	if again.Count("/tmp/api") != 1 {
		t.Fatalf("alert did not survive: %+v", again.Unread)
	}
	if !again.Clear("/tmp/api") {
		t.Fatal("Clear reported no change on a space with an alert")
	}
	if again.Clear("/tmp/api") {
		t.Fatal("Clear reported a change on an already-clear space")
	}
}

// The watcher appends while the board clears, each writing the whole file. A
// clear must not swallow an alert that landed a moment earlier.
func TestMergeKeepsAlertsThatArrivedDuringAClear(t *testing.T) {
	board := &Store{Unread: map[string][]Alert{}}
	board.Add("/tmp/api", Alert{Kind: ChecksFailed, At: time.Now()})

	// The board clears api, but meanwhile the watcher recorded one for web.
	onDisk := &Store{Unread: map[string][]Alert{}}
	onDisk.Add("/tmp/api", Alert{Kind: ChecksFailed, At: time.Now()})
	onDisk.Add("/tmp/web", Alert{Kind: Merged, At: time.Now()})

	merged := &Store{Unread: map[string][]Alert{}}
	merged.Merge(onDisk, map[string]bool{"/tmp/api": true})

	if merged.Count("/tmp/api") != 0 {
		t.Fatal("the cleared space came back")
	}
	if merged.Count("/tmp/web") != 1 {
		t.Fatal("an alert recorded during the clear was lost")
	}
}

func TestHistoryIsBounded(t *testing.T) {
	s := &Store{Unread: map[string][]Alert{}}
	for i := 0; i < maxPerSpace*3; i++ {
		s.Add("/tmp/api", Alert{Kind: ChecksFailed, At: time.Now().Add(time.Duration(i) * time.Second)})
	}
	if got := s.Count("/tmp/api"); got != maxPerSpace {
		t.Fatalf("kept %d alerts, want it capped at %d", got, maxPerSpace)
	}
	// The newest survive, not the oldest.
	if latest, ok := s.Latest("/tmp/api"); !ok || latest.At.IsZero() {
		t.Fatal("the most recent alert was dropped")
	}
}
