// Package store persists the board: the user's status definitions and the
// status of each space, keyed by directory.
//
// Herdr workspace ids (w4, w5) are reassigned every session, so they are
// useless as a durable key. Directory paths are stable, so a status set today
// is still attached to the project when it is reopened next week.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Status is one user-defined column of the board.
type Status struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Color string `json:"color"` // lipgloss color: ANSI index or hex
}

// Entry is what the user recorded about one directory.
type Entry struct {
	Status string `json:"status"`
	// Note carries the external context -- who you are waiting on and why.
	Note  string `json:"note,omitempty"`
	Label string `json:"label,omitempty"`
	// Order is a manual 1-based rank within the status group, written when the
	// user drags a row. Zero means unranked: those sort after ranked rows by
	// recency, so the board stays useful before anything is arranged by hand.
	Order     int       `json:"order,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Board is the whole persisted file.
type Board struct {
	Version   int      `json:"version"`
	Statuses  []Status `json:"statuses"`
	Collapsed []string `json:"collapsed"`
	// Layout is "list", "table" or "kanban". Persisted so the board reopens in
	// whichever view you were last using.
	Layout string `json:"layout,omitempty"`
	// TableSort is "status", "name" or "changed": how the table is ordered.
	TableSort string `json:"table_sort,omitempty"`
	// HideDetail turns off the detail pane in the list view. Stored inverted
	// so the zero value means "show it", which is the default.
	HideDetail bool             `json:"hide_detail,omitempty"`
	Entries    map[string]Entry `json:"entries"`

	path string
}

const currentVersion = 1

// DefaultStatuses is the starting set. Everything here is editable in the TUI;
// none of these ids are special-cased anywhere in the code.
//
// Triage leads deliberately. The first status is where every space that you
// have never touched lands, so it has to mean "not looked at yet" rather than
// being a real choice -- otherwise Todo would mean both, and its badge could
// not tell you which.
func DefaultStatuses() []Status {
	return []Status{
		{ID: "triage", Label: "Triage", Color: "244"},
		{ID: "todo", Label: "Todo", Color: "111"},
		{ID: "in_progress", Label: "In Progress", Color: "39"},
		{ID: "waiting", Label: "Waiting", Color: "214"},
		{ID: "done", Label: "Done", Color: "78"},
	}
}

// PluginID must match herdr-plugin.toml, since Herdr keys plugin state by it.
const PluginID = "phin-board"

// Path returns the board file location.
//
// Herdr injects HERDR_PLUGIN_STATE_DIR when it launches the plugin. Running the
// binary by hand -- the documented development loop -- has no such variable, so
// the same location is reconstructed rather than inventing a second file that
// would silently diverge from the one the plugin actually uses.
func Path() (string, error) {
	if dir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "board.json"), nil
	}

	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "herdr", "plugins", PluginID, "board.json"), nil
}

// Load reads the board, creating an empty one if the file does not exist yet.
func Load() (*Board, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	b := &Board{
		Version:   currentVersion,
		Statuses:  DefaultStatuses(),
		Collapsed: []string{"done"},
		Entries:   map[string]Entry{},
		path:      path,
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, b); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	b.path = path
	if len(b.Statuses) == 0 {
		b.Statuses = DefaultStatuses()
	}
	if b.Entries == nil {
		b.Entries = map[string]Entry{}
	}
	return b, nil
}

// Save writes the board atomically so a crash mid-write cannot truncate it.
func (b *Board) Save() error {
	if b.path == "" {
		return errors.New("board has no path")
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(b.path), ".board-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, b.path)
}

// Key canonicalizes a directory into the board's entry key. Symlinks are
// resolved so that /tmp and /private/tmp, or two paths into the same worktree,
// do not become separate rows.
func Key(dir string) string {
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return filepath.Clean(dir)
}

// DefaultStatusID is the status a newly seen space lands in.
func (b *Board) DefaultStatusID() string {
	if len(b.Statuses) == 0 {
		return ""
	}
	return b.Statuses[0].ID
}

// StatusByID looks up a status definition.
func (b *Board) StatusByID(id string) (Status, bool) {
	for _, s := range b.Statuses {
		if s.ID == id {
			return s, true
		}
	}
	return Status{}, false
}

// SetStatus records a status for a directory, preserving any existing note.
func (b *Board) SetStatus(key, statusID, label string) {
	e := b.Entries[key]
	e.Status = statusID
	e.UpdatedAt = time.Now().UTC()
	if label != "" {
		e.Label = label
	}
	b.Entries[key] = e
}

// SetOrder records a manual rank. It deliberately leaves UpdatedAt alone:
// rearranging rows is not the same as working on something, and UpdatedAt is
// the fallback sort for everything still unranked.
func (b *Board) SetOrder(key string, order int) {
	e, ok := b.Entries[key]
	if !ok {
		return
	}
	e.Order = order
	b.Entries[key] = e
}

// SetNote records the external context for a directory.
func (b *Board) SetNote(key, note string) {
	e := b.Entries[key]
	e.Note = strings.TrimSpace(note)
	e.UpdatedAt = time.Now().UTC()
	b.Entries[key] = e
}

// IsCollapsed reports whether a status group is folded shut.
func (b *Board) IsCollapsed(statusID string) bool {
	for _, id := range b.Collapsed {
		if id == statusID {
			return true
		}
	}
	return false
}

// ToggleCollapsed folds or unfolds a status group.
func (b *Board) ToggleCollapsed(statusID string) {
	for i, id := range b.Collapsed {
		if id == statusID {
			b.Collapsed = append(b.Collapsed[:i], b.Collapsed[i+1:]...)
			return
		}
	}
	b.Collapsed = append(b.Collapsed, statusID)
}

// AddStatus appends a new user-defined status, deriving a unique id from the
// label so the file stays readable.
func (b *Board) AddStatus(label, color string) Status {
	base := slugify(label)
	if base == "" {
		base = "status"
	}
	id := base
	for i := 2; ; i++ {
		if _, exists := b.StatusByID(id); !exists {
			break
		}
		id = fmt.Sprintf("%s_%d", base, i)
	}
	s := Status{ID: id, Label: label, Color: color}
	b.Statuses = append(b.Statuses, s)
	return s
}

// RemoveStatus deletes a status and moves any entries using it to the first
// remaining status, so no space can be orphaned into an invisible group.
func (b *Board) RemoveStatus(id string) error {
	if len(b.Statuses) <= 1 {
		return errors.New("cannot remove the last status")
	}
	idx := -1
	for i, s := range b.Statuses {
		if s.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("unknown status %q", id)
	}
	b.Statuses = append(b.Statuses[:idx], b.Statuses[idx+1:]...)

	fallback := b.Statuses[0].ID
	for key, e := range b.Entries {
		if e.Status == id {
			e.Status = fallback
			b.Entries[key] = e
		}
	}
	for i, c := range b.Collapsed {
		if c == id {
			b.Collapsed = append(b.Collapsed[:i], b.Collapsed[i+1:]...)
			break
		}
	}
	return nil
}

// MoveStatus shifts a status up or down, which reorders the board's groups.
func (b *Board) MoveStatus(id string, delta int) {
	for i, s := range b.Statuses {
		if s.ID != id {
			continue
		}
		j := i + delta
		if j < 0 || j >= len(b.Statuses) {
			return
		}
		b.Statuses[i], b.Statuses[j] = b.Statuses[j], b.Statuses[i]
		return
	}
}

// RenameStatus updates a status label in place, keeping its id and entries.
func (b *Board) RenameStatus(id, label string) {
	for i, s := range b.Statuses {
		if s.ID == id {
			b.Statuses[i].Label = label
			return
		}
	}
}

// Prune drops entries for directories that no longer exist on disk.
func (b *Board) Prune() int {
	var gone []string
	for key := range b.Entries {
		if _, err := os.Stat(key); errors.Is(err, os.ErrNotExist) {
			gone = append(gone, key)
		}
	}
	sort.Strings(gone)
	for _, key := range gone {
		delete(b.Entries, key)
	}
	return len(gone)
}

func slugify(s string) string {
	var out strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore && out.Len() > 0 {
				out.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(out.String(), "_")
}
