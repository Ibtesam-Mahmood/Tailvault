// Package lock owns the repo-committed state file tailvault.lock — the source
// of truth for what is stored, where, and when. It reads and writes the lock in
// canonical form (entries sorted by path, fixed field order, versions[]
// newest-first) so the file is deterministic: the precondition for the per-path
// union merge driver (Task 24) to produce minimal, conflict-free diffs.
package lock

import (
	"os"
	"sort"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

type Lock struct {
	Version     int     `toml:"version"`
	GeneratedBy string  `toml:"generated_by"`
	Entries     []Entry `toml:"entry"`
}

// Entry's field order IS the canonical on-disk field order (go-toml/v2 emits
// fields in declaration order). Do not reorder without updating SPEC.md.
type Entry struct {
	Path     string    `toml:"path"`
	SHA256   string    `toml:"sha256"`
	Size     int64     `toml:"size"`
	Location string    `toml:"location"`
	PushedAt time.Time `toml:"pushed_at"`
	Pusher   string    `toml:"pusher"`
	History  bool      `toml:"history"`
	Preserve bool      `toml:"preserve"`
	// Versions is newest-first; emitted only for history-on entries.
	Versions []string `toml:"versions,omitempty"`
}

// Load reads and unmarshals a tailvault.lock.
func Load(path string) (*Lock, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := toml.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// Canonicalize sorts entries by Path (byte-wise, stable) and normalizes
// PushedAt to UTC so serialized timestamps are stable across machines. It never
// reorders Versions — that order is semantic (newest-first), maintained by
// callers that prepend.
func (l *Lock) Canonicalize() {
	for i := range l.Entries {
		l.Entries[i].PushedAt = l.Entries[i].PushedAt.UTC()
	}
	sort.SliceStable(l.Entries, func(i, j int) bool {
		return l.Entries[i].Path < l.Entries[j].Path
	})
}

// Write sets version + generated_by, canonicalizes, and marshals. Two writes of
// the same logical lock (entries in any in-memory order) produce identical bytes.
func Write(path string, l *Lock, generatedBy string) error {
	l.Version = 1
	l.GeneratedBy = generatedBy
	l.Canonicalize()
	b, err := toml.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Upsert replaces or inserts an entry by Path. For history-on entries the
// caller is responsible for prepending the new sha to Versions (newest-first)
// before calling.
func (l *Lock) Upsert(e Entry) {
	for i := range l.Entries {
		if l.Entries[i].Path == e.Path {
			l.Entries[i] = e
			return
		}
	}
	l.Entries = append(l.Entries, e)
}

// Remove drops the entry with the given path, if present.
func (l *Lock) Remove(path string) {
	out := l.Entries[:0]
	for _, e := range l.Entries {
		if e.Path != path {
			out = append(out, e)
		}
	}
	l.Entries = out
}
