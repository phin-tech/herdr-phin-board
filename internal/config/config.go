// Package config holds the settings a user edits by hand.
//
// This is separate from board.json, which is state the board writes for itself
// -- statuses, notes, arrangement. Settings are the other direction: things you
// tell the board, which it never overwrites.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// PluginID must match herdr-plugin.toml, since Herdr keys the config directory
// by it.
const PluginID = "phin-board"

// Config is the whole settings file.
type Config struct {
	// PollInterval is how often the background watcher asks GitHub. Written as
	// a duration string: "30s", "2m", "10m".
	PollInterval string `toml:"poll_interval"`
	// Notifications turns Herdr toasts on or off. Bells are recorded either
	// way, so turning this off makes the board quiet rather than blind.
	Notifications *bool `toml:"notifications"`
}

const (
	DefaultPollInterval = 2 * time.Minute
	// Below this the watcher would ask GitHub more often than anything it
	// watches realistically changes.
	MinPollInterval = 30 * time.Second
	MaxPollInterval = time.Hour
)

// Settings is the resolved, validated configuration.
type Settings struct {
	PollInterval  time.Duration
	Notifications bool
	// Path is where the file was read from, whether or not it existed.
	Path string
	// Problems are complaints about the file's contents. A bad value falls back
	// to its default rather than stopping the board, but it is reported so a
	// typo is not silently ignored.
	Problems []string
}

// Dir is the plugin's config directory: the one Herdr injects when it launches
// us, or the same path reconstructed when run by hand.
func Dir() (string, error) {
	if dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, ".config", "herdr", "plugins", "config", PluginID), nil
}

// Load reads the settings, falling back to defaults for anything absent or
// unusable. A missing file is the normal case, not an error.
func Load() Settings {
	s := Settings{PollInterval: DefaultPollInterval, Notifications: true}

	dir, err := Dir()
	if err != nil {
		return s
	}
	s.Path = filepath.Join(dir, "config.toml")

	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	if err != nil {
		s.Problems = append(s.Problems, fmt.Sprintf("could not read %s: %v", s.Path, err))
		return s
	}

	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		s.Problems = append(s.Problems, fmt.Sprintf("%s is not valid TOML: %v", s.Path, err))
		return s
	}

	if c.PollInterval != "" {
		d, err := time.ParseDuration(c.PollInterval)
		switch {
		case err != nil:
			s.Problems = append(s.Problems, fmt.Sprintf("poll_interval %q is not a duration — using %s", c.PollInterval, s.PollInterval))
		case d < MinPollInterval:
			s.Problems = append(s.Problems, fmt.Sprintf("poll_interval %s is below the %s minimum — using %s", d, MinPollInterval, MinPollInterval))
			s.PollInterval = MinPollInterval
		case d > MaxPollInterval:
			s.Problems = append(s.Problems, fmt.Sprintf("poll_interval %s is above the %s maximum — using %s", d, MaxPollInterval, MaxPollInterval))
			s.PollInterval = MaxPollInterval
		default:
			s.PollInterval = d
		}
	}

	if c.Notifications != nil {
		s.Notifications = *c.Notifications
	}
	return s
}

// Example is the commented template written by `herdr-phin-board config --init`.
const Example = `# herdr-phin-board settings.
#
# Every value is optional; delete a line to go back to its default.

# How often the background watcher asks GitHub about your pull requests.
# Minimum 30s, maximum 1h. Opening or closing a workspace polls immediately
# regardless, so this only governs noticing a review landing or CI going red.
poll_interval = "2m"

# Herdr toasts when a pull request changes. Bells on the board are recorded
# either way, so turning this off makes the board quiet rather than blind.
notifications = true
`

// WriteExample creates the template, refusing to overwrite an existing file.
func WriteExample() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, "config.toml")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return path, fmt.Errorf("%s already exists — edit it instead", path)
		}
		return "", err
	}
	defer f.Close()

	_, err = f.WriteString(Example)
	return path, err
}
