// Package alert records what changed about a space while you were not looking.
//
// A Herdr toast is transient: fired while you are away, it is simply gone. So
// every notification is also written here, and the board renders a bell until
// you have seen it. The toast is the interruption; this is the record.
package alert

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Kind names why a space is asking for attention.
type Kind string

const (
	ChecksFailed   Kind = "checks_failed"
	ReviewApproved Kind = "review_approved"
	ChangesAsked   Kind = "changes_requested"
	Conflicted     Kind = "conflicted"
	Merged         Kind = "merged"
	Closed         Kind = "closed"
)

// Alert is one thing that happened.
type Alert struct {
	Kind Kind      `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Store holds unread alerts per space directory.
type Store struct {
	Unread map[string][]Alert `json:"unread"`

	path string
}

// Load reads the store, returning an empty one when absent or unreadable.
// These are notifications, not the user's work: rebuilding beats failing.
func Load(dir string) *Store {
	s := &Store{Unread: map[string][]Alert{}, path: filepath.Join(dir, "alerts.json")}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var loaded Store
	if err := json.Unmarshal(data, &loaded); err != nil || loaded.Unread == nil {
		return s
	}
	s.Unread = loaded.Unread
	return s
}

// Save writes atomically.
func (s *Store) Save() error {
	if s.path == "" {
		return errors.New("store has no path")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".alerts-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

// maxPerSpace bounds the history kept for one space. A space nobody visits for
// a week should not accumulate a hundred entries.
const maxPerSpace = 10

// Add records an alert, newest last.
func (s *Store) Add(dir string, a Alert) {
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	list := append(s.Unread[dir], a)
	if len(list) > maxPerSpace {
		list = list[len(list)-maxPerSpace:]
	}
	s.Unread[dir] = list
}

// Count is how many unread alerts a space has.
func (s *Store) Count(dir string) int {
	return len(s.Unread[dir])
}

// Latest is the most recent unread alert, for the row summary.
func (s *Store) Latest(dir string) (Alert, bool) {
	list := s.Unread[dir]
	if len(list) == 0 {
		return Alert{}, false
	}
	return list[len(list)-1], true
}

// All returns every unread alert for a space, oldest first.
func (s *Store) All(dir string) []Alert {
	list := append([]Alert(nil), s.Unread[dir]...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].At.Before(list[j].At) })
	return list
}

// Clear marks a space seen, reporting whether anything changed so the caller
// can avoid writing the file on every cursor move.
func (s *Store) Clear(dir string) bool {
	if len(s.Unread[dir]) == 0 {
		return false
	}
	delete(s.Unread, dir)
	return true
}

// Prune drops spaces that are no longer on the board.
func (s *Store) Prune(keep map[string]bool) {
	for dir := range s.Unread {
		if !keep[dir] {
			delete(s.Unread, dir)
		}
	}
}

// Merge folds another store's alerts into this one, keeping both sides.
//
// The watcher adds while the board clears, and each writes the whole file. Re-
// reading and merging immediately before a write keeps a clear from swallowing
// an alert that landed a moment earlier.
func (s *Store) Merge(other *Store, cleared map[string]bool) {
	for dir, list := range other.Unread {
		if cleared[dir] {
			continue
		}
		seen := map[Kind]time.Time{}
		for _, a := range s.Unread[dir] {
			seen[a.Kind] = a.At
		}
		for _, a := range list {
			if at, ok := seen[a.Kind]; ok && at.Equal(a.At) {
				continue
			}
			s.Add(dir, a)
		}
	}
}
