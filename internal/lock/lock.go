// Package lock owns the repo-committed state file tailvault.lock — the source
// of truth for what is stored, where, and when. It reads and writes the lock in
// canonical form (entries sorted by path, fixed field order, versions[]
// newest-first) so the file is deterministic: the precondition for the per-path
// union merge driver (Task 24) to produce minimal, conflict-free diffs.
package lock

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
)

// SchemaVersion is the only tailvault.lock schema version this build reads or
// writes. v2 (SPEC v2 §2) embeds the federated file id + genesis record in each
// entry, making every clone an off-node identity backup. Per D29 there is no
// v1-tolerance machinery: no real v1 vaults exist, so Parse simply requires
// version = 2 and rejects version = 1 outright (recreate, don't migrate).
const SchemaVersion = 2

// ErrIncompatibleVersion is returned by Parse when the lock's top-level version
// is not SchemaVersion. The command boundary (loadLockOrEmpty) wraps it as a
// TV-CFG-style config error (exit 2).
var ErrIncompatibleVersion = errors.New("lock: incompatible schema version (expected 2)")

type Lock struct {
	Version     int     `toml:"version"`
	GeneratedBy string  `toml:"generated_by"`
	Entries     []Entry `toml:"entry"`
}

// Entry's field order IS the canonical on-disk field order (go-toml/v2 emits
// fields in declaration order). Do not reorder without updating SPEC.md.
type Entry struct {
	Path string `toml:"path"`
	// ID is the 64-hex genesis hash (SPEC v2 §11) for a federated file; empty
	// for a non-federated vault entry (legal in v2 — skipped by pull-WARN/heal).
	// Omitted from on-disk form when empty so plain single-node locks stay lean.
	ID string `toml:"id,omitempty"`
	// Genesis is the full embedded birth record (SPEC v2 §11) — the off-node
	// identity backup that lets pull/heal reason about a file whose home moved.
	// A pointer so a non-federated entry omits the inline table entirely (a zero
	// value struct would otherwise serialize as an empty genesis = {…}). When
	// present its hash MUST equal ID (self-certification, see Validate).
	Genesis  *identity.Genesis `toml:"genesis,inline,omitempty"`
	SHA256   string            `toml:"sha256"`
	Size     int64             `toml:"size"`
	Location string            `toml:"location"`
	PushedAt time.Time         `toml:"pushed_at"`
	Pusher   string            `toml:"pusher"`
	History  bool              `toml:"history"`
	Preserve bool              `toml:"preserve"`
	// Deleted marks a tombstone: the working file is gone but its blob must
	// survive (the path was preserved, or auto_delete was opted out). push keeps
	// such entries with Deleted=true instead of dropping them, so the sha stays
	// in ReferencedSHAs/BuildPreserveSet and GC does not sweep the blob. Omitted
	// from on-disk form for live entries so existing locks are byte-identical.
	Deleted bool `toml:"deleted,omitempty"`
	// Versions is newest-first; emitted only for history-on entries.
	Versions []string `toml:"versions,omitempty"`
}

// Load reads and unmarshals a tailvault.lock from a file path. It delegates to
// Parse so the file- and byte-oriented parsers stay identical.
func Load(path string) (*Lock, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
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
	l.Version = SchemaVersion
	l.GeneratedBy = generatedBy
	l.Canonicalize()
	b, err := toml.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Validate self-certifies a parsed lock: the schema version must be current,
// and every federated entry (one carrying an ID) must embed a genesis record
// that hashes to that ID (identity.Verify). A lock that fails self-certification
// is rejected as corrupt — a committed lock is an identity backup, so a torn id↔
// genesis pairing must never be trusted. Non-federated entries (empty ID) are
// legal and skipped. The command boundary maps a failure to a config error
// (exit 2).
func (l *Lock) Validate() error {
	if l.Version != SchemaVersion {
		return ErrIncompatibleVersion
	}
	for i := range l.Entries {
		e := &l.Entries[i]
		if e.ID == "" {
			if e.Genesis != nil {
				return fmt.Errorf("lock: entry %q carries a genesis record but no id", e.Path)
			}
			continue
		}
		if e.Genesis == nil {
			return fmt.Errorf("lock: entry %q has id %s but no genesis record (cannot self-certify)", e.Path, identity.Short(e.ID))
		}
		ok, err := identity.Verify(*e.Genesis, e.ID)
		if err != nil {
			return fmt.Errorf("lock: entry %q genesis is malformed: %w", e.Path, err)
		}
		if !ok {
			return fmt.Errorf("lock: entry %q fails self-certification: genesis does not hash to id %s", e.Path, identity.Short(e.ID))
		}
	}
	return nil
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
