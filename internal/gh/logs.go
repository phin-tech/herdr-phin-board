package gh

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

// A failing check names itself, which tells you to go and look. What you
// actually need is the error, and gh can fetch it: a check run carries the URL
// of its Actions job, and the job has a log.

// ErrNoLog means the check has no log to read here. A status context --
// CircleCI, Buildkite, anything not Actions -- is a link into somebody else's
// system, and its URL is all there is.
var ErrNoLog = errors.New("that check has no log to read")

// jobURLPattern picks the job id out of an Actions check URL, which looks like
// https://github.com/o/r/actions/runs/123/job/456.
var jobURLPattern = regexp.MustCompile(`/job/(\d+)`)

// logTailLines and logTailBytes bound what comes back. The message this ends up
// in is typed into an agent's input for a person to read and add to, so it has
// to stay something a person can read: the end of a failed step is where the
// error is, and the rest is the build that got there.
const (
	logTailLines = 30
	logTailBytes = 2500
)

// logTimeout is a floor under whatever the client is configured for. Answering
// this makes gh download the run's whole log archive, which is a real download
// on a chatty build -- unlike a PR lookup, which is one API call.
const logTimeout = 30 * time.Second

// FailedLog reads the tail of a failing check's log.
//
// The directory is the space's, so gh resolves the repository the same way it
// does everywhere else here -- no repo argument, no guessing which checkout a
// job belongs to.
func (c *Client) FailedLog(ctx context.Context, dir string, check Check) (string, error) {
	match := jobURLPattern.FindStringSubmatch(check.URL)
	if match == nil {
		return "", ErrNoLog
	}

	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return "", ErrNoLog
		}
	}
	timeout := c.Timeout
	if timeout < logTimeout {
		timeout = logTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := c.Run(ctx, dir, "run", "view", "--job", match[1], "--log-failed")
	if err != nil {
		return "", classify(err)
	}

	tail := tailLog(string(out))
	if tail == "" {
		// A job that failed before it ran a step -- cancelled, or a workflow
		// that would not parse -- has nothing in its log.
		return "", ErrNoLog
	}
	return tail, nil
}

// tailLog reduces gh's log output to the end of what went wrong.
//
// Each line arrives prefixed with its job, step and timestamp, tab separated.
// None of that survives: the job and step are already known from the check, and
// a timestamp on every line would be most of the width of the message.
func tailLog(raw string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		// gh prefixes each line with its job and step, tab separated. Only
		// those two are the prefix -- taking the last tabbed field instead
		// would swallow a log line's own tabs, which is most of a Go test
		// failure.
		if fields := strings.SplitN(line, "\t", 3); len(fields) == 3 && looksTimestamped(fields[2]) {
			line = fields[2]
		}
		// The first line of a job's log carries a byte order mark, which would
		// otherwise sit in front of the timestamp and save it from being cut.
		line = strings.TrimPrefix(line, "\ufeff")
		line = stripANSI(stripTimestamp(line))
		line = strings.TrimRight(stripAnnotation(line), " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > logTailLines {
		lines = lines[len(lines)-logTailLines:]
	}

	out := strings.Join(lines, "\n")
	for len(out) > logTailBytes {
		// Whole lines, from the front: a message cut mid-line reads as though
		// the log itself were truncated.
		cut := strings.Index(out, "\n")
		if cut < 0 {
			return out[len(out)-logTailBytes:]
		}
		out = out[cut+1:]
	}
	return out
}

// timestampPattern matches the RFC3339 stamp Actions puts at the head of every
// log line, with the seven-digit fraction it actually emits.
var timestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z\s?`)

// looksTimestamped reports whether a field starts the way a log line does,
// which is what distinguishes gh's prefix from a tab inside the log itself.
func looksTimestamped(field string) bool {
	return timestampPattern.MatchString(strings.TrimPrefix(field, "\ufeff"))
}

func stripTimestamp(line string) string {
	return timestampPattern.ReplaceAllString(line, "")
}

// ansiPattern matches the colour and cursor sequences a build tool writes when
// it thinks it has a terminal. This text is about to be typed into somebody
// else's input, where an escape sequence is at best noise.
//
// Both spellings of the escape are matched. Actions stores the byte itself in
// some lines and the two characters "^[" in others -- the caret form is what a
// downloaded log actually contains, and stripping only the byte leaves the
// output looking untouched.
var ansiPattern = regexp.MustCompile(`(?:\x1b|\^\[)\[[0-9;?]*[ -/]*[@-~]|(?:\x1b|\^\[)\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func stripANSI(line string) string {
	return ansiPattern.ReplaceAllString(line, "")
}

// annotationPattern matches the ##[...] markers Actions uses to fold groups and
// tag errors.
var annotationPattern = regexp.MustCompile(`^##\[[a-z]+\]`)

// stripAnnotation drops the marker and keeps what it was marking, so
// "##[error]exit code 1" reads as a line rather than as markup. A bare
// ##[endgroup] becomes empty and is dropped with the other blank lines.
func stripAnnotation(line string) string {
	return annotationPattern.ReplaceAllString(line, "")
}
