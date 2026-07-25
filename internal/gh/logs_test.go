package gh

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func logClient(out string, err error) (*Client, *[]string) {
	var args []string
	c := &Client{
		Timeout: time.Second,
		Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
			args = append(args, strings.Join(a, " "))
			return []byte(out), err
		},
	}
	return c, &args
}

// The job id is the whole point of the check URL: it is what turns "something
// called build failed" into the error itself.
func TestFailedLogAsksForTheJobBehindTheCheck(t *testing.T) {
	c, args := logClient("job\tstep\t2026-07-25T10:00:00.1234567Z boom\n", nil)

	check := Check{
		Name:  "build",
		State: ChecksFail,
		URL:   "https://github.com/o/r/actions/runs/123/job/456",
	}
	got, err := c.FailedLog(context.Background(), "/tmp/api", check)
	if err != nil {
		t.Fatal(err)
	}
	if got != "boom" {
		t.Fatalf("log = %q, want the line without its job, step and timestamp", got)
	}
	if len(*args) != 1 || !strings.Contains((*args)[0], "--job 456") {
		t.Fatalf("ran %v, want the job from the URL", *args)
	}
	if !strings.Contains((*args)[0], "--log-failed") {
		t.Fatalf("ran %v, want only the failed steps", *args)
	}
}

// A status context is a link into somebody else's CI. There is nothing to
// fetch, and pretending otherwise would put an error in front of the user
// where a plain link is the honest answer.
func TestANonActionsCheckHasNoLog(t *testing.T) {
	c, args := logClient("", nil)

	check := Check{Name: "ci/circleci", State: ChecksFail, URL: "https://circleci.com/gh/o/r/99"}
	if _, err := c.FailedLog(context.Background(), "/tmp/api", check); !errors.Is(err, ErrNoLog) {
		t.Fatalf("err = %v, want ErrNoLog", err)
	}
	if len(*args) != 0 {
		t.Fatalf("ran %v for a check with no job", *args)
	}
}

// A job that failed before running a step -- cancelled, or a workflow that
// would not parse -- has an empty log rather than an error.
func TestAnEmptyLogIsNotALog(t *testing.T) {
	c, _ := logClient("\n \n", nil)

	check := Check{URL: "https://github.com/o/r/actions/runs/1/job/2"}
	if _, err := c.FailedLog(context.Background(), "/tmp/api", check); !errors.Is(err, ErrNoLog) {
		t.Fatalf("err = %v, want ErrNoLog", err)
	}
}

// Being logged out is worth saying; it is the same fault that empties the whole
// PR column, and it must not look like a check with nothing to show.
func TestALoggedOutGhSaysSo(t *testing.T) {
	c, _ := logClient("", errors.New("gh auth login required: not logged in"))

	check := Check{URL: "https://github.com/o/r/actions/runs/1/job/2"}
	_, err := c.FailedLog(context.Background(), "/tmp/api", check)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

// The message is typed into somebody's input for them to read. The end of the
// step is where the error is; the build that got there is not worth the room.
func TestTheLogIsTrimmedToItsTail(t *testing.T) {
	var raw strings.Builder
	for i := 0; i < logTailLines*3; i++ {
		fmt.Fprintf(&raw, "build\tRun tests\t2026-07-25T10:00:00.0000000Z line %d\n", i)
	}

	c, _ := logClient(raw.String(), nil)
	check := Check{URL: "https://github.com/o/r/actions/runs/1/job/2"}
	got, err := c.FailedLog(context.Background(), "/tmp/api", check)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(got, "\n")
	if len(lines) > logTailLines {
		t.Fatalf("kept %d lines, want at most %d", len(lines), logTailLines)
	}
	if last := lines[len(lines)-1]; last != fmt.Sprintf("line %d", logTailLines*3-1) {
		t.Fatalf("the tail ends at %q, which is not the end of the log", last)
	}
	if len(got) > logTailBytes {
		t.Fatalf("kept %d bytes, want at most %d", len(got), logTailBytes)
	}
}

// A single enormous line -- a minified stack trace, a base64 blob -- must not
// walk past the cap just because it has no newline to cut at.
func TestOneHugeLineIsStillCapped(t *testing.T) {
	c, _ := logClient(strings.Repeat("x", logTailBytes*2)+"\n", nil)

	check := Check{URL: "https://github.com/o/r/actions/runs/1/job/2"}
	got, err := c.FailedLog(context.Background(), "/tmp/api", check)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > logTailBytes {
		t.Fatalf("kept %d bytes, want at most %d", len(got), logTailBytes)
	}
}

// Real Actions logs open with a byte order mark and are littered with the
// markers that fold groups and tag errors. None of that belongs in a message
// somebody is about to read in their agent's input.
func TestTheLogLosesItsMarkup(t *testing.T) {
	raw := "job\tSet up\t\ufeff2026-07-25T11:22:44.3985568Z ##[group]Runner Image\n" +
		"job\tSet up\t2026-07-25T11:22:44.4011687Z ##[endgroup]\n" +
		"job\tRun tests\t2026-07-25T11:22:45.0000000Z ##[error]Process completed with exit code 1\n"

	c, _ := logClient(raw, nil)
	got, err := c.FailedLog(context.Background(), "/tmp/api", Check{URL: "https://github.com/o/r/actions/runs/1/job/2"})
	if err != nil {
		t.Fatal(err)
	}

	want := "Runner Image\nProcess completed with exit code 1"
	if got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

// A build tool that thinks it has a terminal writes colour sequences. Typed
// into an agent's input those are at best noise, at worst a mess.
func TestTheLogLosesItsColours(t *testing.T) {
	// Actions writes the escape byte in some lines and the two characters "^["
	// in others; a downloaded log is full of the second kind.
	raw := "job\tstep\t2026-07-25T10:00:00.0000000Z \x1b[36;1mgo test ./...\x1b[0m\n" +
		"job\tstep\t2026-07-25T10:00:01.0000000Z ^[[31mFAIL^[[0m\tpkg\t0.1s\n"

	c, _ := logClient(raw, nil)
	got, err := c.FailedLog(context.Background(), "/tmp/api", Check{URL: "https://github.com/o/r/actions/runs/1/job/2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escape sequences survived: %q", got)
	}
	if strings.Contains(got, "^[") || strings.Contains(got, "[36;1m") {
		t.Fatalf("escape sequences survived in their caret form: %q", got)
	}
	if !strings.Contains(got, "FAIL\tpkg\t0.1s") {
		t.Fatalf("stripping colour ate the text: %q", got)
	}
}
