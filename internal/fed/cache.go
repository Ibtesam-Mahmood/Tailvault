package fed

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// MemberSummary is the per-member catalog digest a client caches (SPEC v2 §14).
// Field order and toml keys mirror the §14 sample verbatim so cache files
// round-trip byte-stably.
type MemberSummary struct {
	Name      string    `toml:"name"`
	Node      string    `toml:"node"`
	Status    string    `toml:"status"`
	Reachable bool      `toml:"reachable"`
	LastSeen  time.Time `toml:"last_seen"`
	FileCount int       `toml:"file_count"`
	IDs       []string  `toml:"ids"` // file IDs the member reported holding
}

// Snapshot is one cached federation state (SPEC v2 §14). The on-disk form is
// exactly this struct: top-level fed_id + taken_at and a [[member]] array. The
// roster is recoverable from the member rows (name/node/status) via Roster(),
// so it is not stored separately — §14 carries no distinct roster section.
type Snapshot struct {
	FedID   string          `toml:"fed_id"`
	TakenAt time.Time       `toml:"taken_at"`
	Members []MemberSummary `toml:"member"`
}

// Roster reconstructs the advisory roster captured in this snapshot from its
// member rows. joined_at is not stored in the cache (§14), so reconstructed
// members carry a zero JoinedAt — acceptable because the cache is advisory and
// never feeds a merge or a fan-out decision.
func (s *Snapshot) Roster() Roster {
	r := Roster{FedID: s.FedID, Members: make([]Member, 0, len(s.Members))}
	for _, m := range s.Members {
		r.Members = append(r.Members, Member{Name: m.Name, Node: m.Node, Status: m.Status})
	}
	r.sortMembers()
	return r
}

// Cache manages ~/.tailvault/cache/fed-<fed_id>/{current,previous}.toml. Dir is
// injectable so tests use a t.TempDir(). Everything here is advisory (D26):
// nothing in the system may treat a cache hit as authoritative state.
type Cache struct{ Dir string }

func (c *Cache) currentPath() string  { return filepath.Join(c.Dir, "current.toml") }
func (c *Cache) previousPath() string { return filepath.Join(c.Dir, "previous.toml") }

// Load reads the current and previous snapshots. A missing file yields a nil
// snapshot and no error — an empty/uninitialized cache is normal, not a failure.
func (c *Cache) Load() (current, previous *Snapshot, err error) {
	current, err = loadSnapshot(c.currentPath())
	if err != nil {
		return nil, nil, err
	}
	previous, err = loadSnapshot(c.previousPath())
	if err != nil {
		return nil, nil, err
	}
	return current, previous, nil
}

// Record rotates current → previous and writes snap as the new current. Both
// writes use temp-file + fsync + atomic rename so a crash leaves a consistent
// pair, never a torn file.
func (c *Cache) Record(snap Snapshot) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	// Rotate the existing current into previous first (overwriting any older
	// previous). Rename within one directory is atomic.
	if _, err := os.Stat(c.currentPath()); err == nil {
		if err := os.Rename(c.currentPath(), c.previousPath()); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	b, err := toml.Marshal(snap)
	if err != nil {
		return err
	}
	return writeAtomic(c.currentPath(), b)
}

// WasKnown reports whether id appeared in the current or previous snapshot and,
// if so, the member that held it. This is the "was here before, offline now" vs
// "never existed" signal that colors partial-view error messages — it is
// strictly advisory and never upgrades into an authoritative claim about
// current state.
func (c *Cache) WasKnown(id string) (member string, known bool) {
	if s, _ := c.lastKnown(id); s != nil {
		return s.Name, true
	}
	return "", false
}

// lastKnown returns the most recent MemberSummary (current preferred over
// previous) that reported holding id, or nil. Used by the resolution engine
// (Task 32) to attach LastSeen coloring to a PartialView result.
func (c *Cache) lastKnown(id string) (*MemberSummary, bool) {
	current, previous, err := c.Load()
	if err != nil {
		return nil, false
	}
	for _, s := range []*Snapshot{current, previous} {
		if s == nil {
			continue
		}
		for i := range s.Members {
			for _, fid := range s.Members[i].IDs {
				if fid == id {
					m := s.Members[i]
					return &m, true
				}
			}
		}
	}
	return nil, false
}

func loadSnapshot(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // advisory: a missing snapshot is not an error
		}
		return nil, err
	}
	var s Snapshot
	if err := toml.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// writeAtomic writes b to path via a temp file in the same directory, fsync,
// rename, then a directory fsync — the v1 atomicity discipline (mirrors
// backend.FSBackend.Put and catalog.WriteAtomic).
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tv-cache-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
