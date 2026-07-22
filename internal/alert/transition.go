package alert

import (
	"fmt"

	"github.com/phin-tech/herdr-phin-board/internal/gh"
)

// Transitions compares a PR against what was last known and returns what is
// worth telling the user about.
//
// Only changes qualify, never states. Notifying on state would re-announce the
// same failing check on every poll until it was fixed, which trains you to
// ignore the notifications entirely.
func Transitions(label string, before, after gh.PR) []Alert {
	// Nothing to compare against: a PR first seen is not news, it is just the
	// starting point. Announcing every PR the moment the board learns of it
	// would make the first run a wall of toasts.
	if !before.Found() || !after.Found() {
		return nil
	}

	var out []Alert

	if before.Checks != gh.ChecksFail && after.Checks == gh.ChecksFail {
		out = append(out, Alert{
			Kind: ChecksFailed,
			Text: fmt.Sprintf("#%d checks failed — %s", after.Number, label),
		})
	}

	if before.Review != after.Review {
		switch after.Review {
		case "APPROVED":
			out = append(out, Alert{
				Kind: ReviewApproved,
				Text: fmt.Sprintf("#%d approved — %s", after.Number, label),
			})
		case "CHANGES_REQUESTED":
			out = append(out, Alert{
				Kind: ChangesAsked,
				Text: fmt.Sprintf("#%d changes requested — %s", after.Number, label),
			})
		}
	}

	if before.Merge != gh.MergeConflict && after.Merge == gh.MergeConflict {
		out = append(out, Alert{
			Kind: Conflicted,
			Text: fmt.Sprintf("#%d conflicts with base — %s", after.Number, label),
		})
	}

	if before.State == "OPEN" && after.State != "OPEN" {
		switch after.State {
		case "MERGED":
			out = append(out, Alert{
				Kind: Merged,
				Text: fmt.Sprintf("#%d merged — %s", after.Number, label),
			})
		case "CLOSED":
			out = append(out, Alert{
				Kind: Closed,
				Text: fmt.Sprintf("#%d closed — %s", after.Number, label),
			})
		}
	}

	return out
}

// Sound picks the Herdr notification cue. Anything asking for a decision uses
// the request cue; anything that merely finished uses done.
func (a Alert) Sound() string {
	switch a.Kind {
	case ChecksFailed, ChangesAsked, Conflicted:
		return "request"
	default:
		return "done"
	}
}

// Title is the toast heading.
func (a Alert) Title() string {
	switch a.Kind {
	case ChecksFailed:
		return "Checks failed"
	case ReviewApproved:
		return "Approved"
	case ChangesAsked:
		return "Changes requested"
	case Conflicted:
		return "Conflict"
	case Merged:
		return "Merged"
	case Closed:
		return "Closed"
	default:
		return "Pull request"
	}
}
