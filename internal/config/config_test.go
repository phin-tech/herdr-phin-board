package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// No file is the normal case, not an error.
func TestDefaultsWithNoFile(t *testing.T) {
	write(t, "")
	s := Load()

	if s.PollInterval != DefaultPollInterval || !s.Notifications {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if len(s.Problems) != 0 {
		t.Fatalf("a missing file was reported as a problem: %v", s.Problems)
	}
}

func TestReadsValues(t *testing.T) {
	write(t, "poll_interval = \"45s\"\nnotifications = false\n")
	s := Load()

	if s.PollInterval != 45*time.Second {
		t.Fatalf("poll_interval = %s", s.PollInterval)
	}
	if s.Notifications {
		t.Fatal("notifications should be off")
	}
}

// A bad value falls back rather than stopping the board, but it must say so --
// silently ignoring a typo would leave you wondering why nothing changed.
func TestBadValuesFallBackAndComplain(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{"not a duration", `poll_interval = "banana"`, DefaultPollInterval},
		{"below the floor", `poll_interval = "5s"`, MinPollInterval},
		{"above the ceiling", `poll_interval = "6h"`, MaxPollInterval},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write(t, tc.body)
			s := Load()

			if s.PollInterval != tc.want {
				t.Fatalf("poll_interval = %s, want %s", s.PollInterval, tc.want)
			}
			if len(s.Problems) == 0 {
				t.Fatal("the value was corrected silently")
			}
		})
	}
}

func TestBrokenTomlDoesNotStopTheBoard(t *testing.T) {
	write(t, "poll_interval = \n\n[[[")
	s := Load()

	if s.PollInterval != DefaultPollInterval || !s.Notifications {
		t.Fatalf("broken TOML lost the defaults: %+v", s)
	}
	if len(s.Problems) == 0 {
		t.Fatal("broken TOML was not reported")
	}
}

// notifications is a pointer so that "false" is distinguishable from absent.
func TestExplicitFalseIsNotMistakenForAbsent(t *testing.T) {
	write(t, "notifications = false\n")
	if Load().Notifications {
		t.Fatal("an explicit false was read as absent and defaulted to true")
	}
}

func TestHerdrConfigDirWins(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "/injected")
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/injected" {
		t.Fatalf("Dir() = %q, want the injected directory", dir)
	}
}

// Run by hand there is no injected directory, so the same path is rebuilt.
func TestDirReconstructedWhenRunByHand(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "")
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(".config", "herdr", "plugins", "config", PluginID)
	if !strings.HasSuffix(dir, want) {
		t.Fatalf("Dir() = %q, want it to end in %q", dir, want)
	}
}

// The template must be valid, or `config --init` would hand out a broken file.
func TestExampleIsValidAndMatchesDefaults(t *testing.T) {
	write(t, Example)
	s := Load()

	if len(s.Problems) != 0 {
		t.Fatalf("the shipped template does not parse: %v", s.Problems)
	}
	if s.PollInterval != DefaultPollInterval || !s.Notifications {
		t.Fatalf("the template disagrees with the defaults: %+v", s)
	}
}

// Writing over somebody's settings would be unforgivable.
func TestWriteExampleRefusesToOverwrite(t *testing.T) {
	dir := write(t, "poll_interval = \"9m\"\n")

	if _, err := WriteExample(); err == nil {
		t.Fatal("an existing config was overwritten")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if !strings.Contains(string(data), "9m") {
		t.Fatal("the original settings were lost")
	}
}
