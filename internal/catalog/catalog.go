// Package catalog owns the vault-side state file meta/catalog.toml — the
// self-describing record of every file a storage location holds (SPEC v2 §9).
// It mirrors internal/lock: types whose declaration order IS the canonical
// on-disk field order, a Canonicalize step (entries sorted by path byte-wise
// ascending, timestamps normalized to UTC), and deterministic serialization so
// two writes of the same logical catalog produce identical bytes.
//
// Per the SPEC §8 error-layering rule this is a leaf data package: it returns
// plain errors only; commands wrap them in tserr at the boundary (an
// incompatible schema version becomes a TV-CFG-style exit-2 failure).
package catalog

import (
	"errors"
	"sort"
	"time"
)

// SchemaVersion is the only catalog schema version this build understands.
const SchemaVersion = 2

// Catalog mirrors meta/catalog.toml (SPEC v2 §9). Field declaration order is the
// canonical on-disk order; go-toml/v2 emits fields in declaration order.
type Catalog struct {
	Version    int        `toml:"version"` // MUST be SchemaVersion (2)
	VaultName  string     `toml:"vault_name"`
	Node       string     `toml:"node"`
	Federation Federation `toml:"federation"`
	Files      []File     `toml:"file"`
}

// File is one [[file]] entry. Files are kept sorted by Path, byte-wise
// ascending (mirror lock canonical form, SPEC v2 §9). The field order below is
// the frozen §9 canonical order.
type File struct {
	ID          string    `toml:"id"`             // 64-hex genesis hash (SPEC v2 §11)
	Genesis     Genesis   `toml:"genesis,inline"` // full birth record, inline table
	SHA256      string    `toml:"sha256"`         // current content hash
	Path        string    `toml:"path"`           // vault-relative logical path
	SyncMode    string    `toml:"sync_mode"`      // "git" | "manual" | future values (open enum, D15)
	Size        int64     `toml:"size"`           // bytes
	CreatedAt   time.Time `toml:"created_at"`     // RFC3339 UTC Z
	UpdatedAt   time.Time `toml:"updated_at"`     // RFC3339 UTC Z
	LastScanned time.Time `toml:"last_scanned"`   // RFC3339 UTC Z
}

// Genesis mirrors the SPEC v2 §11 record. identity (Task 30) owns ID hashing;
// here it is carried inline so the catalog is itself an identity backup.
type Genesis struct {
	ContentSHA256 string `toml:"content_sha256"`
	OriginalPath  string `toml:"original_path"`
	IngestOpID    string `toml:"ingest_op_id"`
	OriginNode    string `toml:"origin_node"`
}

// Federation mirrors the SPEC v2 §13 [federation] roster section. fed
// (Task 31) adds roster behavior on top of these types.
type Federation struct {
	FedID   string   `toml:"fed_id"`
	Members []Member `toml:"member"`
}

// Member is one [[federation.member]] row. leave/evict keep the row with a
// status change (never delete it) — history matters for WARN messages (D28).
type Member struct {
	Name     string    `toml:"name"`
	Node     string    `toml:"node"`
	JoinedAt time.Time `toml:"joined_at"`
	Status   string    `toml:"status"` // active | left | evicted
}

// Member status values (SPEC v2 §13).
const (
	StatusActive  = "active"
	StatusLeft    = "left"
	StatusEvicted = "evicted"
)

// SyncMode values known on day one. The enum is OPEN (D15): unknown values are
// preserved on round-trip and treated as not-git by gc — never validated
// against a closed list here.
const (
	SyncModeGit    = "git"
	SyncModeManual = "manual"
)

// ErrIncompatibleVersion is returned by Validate/Parse when the catalog's
// schema version is not SchemaVersion. The command boundary maps it to exit 2.
var ErrIncompatibleVersion = errors.New("catalog: incompatible schema version")

// Validate checks the schema version and basic field invariants. It does NOT
// validate sync_mode against a closed list (open enum, D15).
func (c *Catalog) Validate() error {
	if c.Version != SchemaVersion {
		return ErrIncompatibleVersion
	}
	for i := range c.Files {
		f := &c.Files[i]
		if f.Path == "" {
			return errors.New("catalog: file entry has empty path")
		}
		if !isHex64(f.ID) {
			return errors.New("catalog: file id is not 64 hex chars: " + f.Path)
		}
	}
	return nil
}

// Canonicalize sorts Files by Path (byte-wise, stable) and normalizes every
// timestamp to UTC so serialized output is deterministic across machines.
func (c *Catalog) Canonicalize() {
	for i := range c.Files {
		c.Files[i].CreatedAt = c.Files[i].CreatedAt.UTC()
		c.Files[i].UpdatedAt = c.Files[i].UpdatedAt.UTC()
		c.Files[i].LastScanned = c.Files[i].LastScanned.UTC()
	}
	for i := range c.Federation.Members {
		c.Federation.Members[i].JoinedAt = c.Federation.Members[i].JoinedAt.UTC()
	}
	sort.SliceStable(c.Files, func(i, j int) bool {
		return c.Files[i].Path < c.Files[j].Path
	})
}

// Find returns the file entry with the given logical path.
func (c *Catalog) Find(path string) (File, bool) {
	for _, f := range c.Files {
		if f.Path == path {
			return f, true
		}
	}
	return File{}, false
}

// FindID returns the file entry with the given (stable) file ID.
func (c *Catalog) FindID(id string) (File, bool) {
	for _, f := range c.Files {
		if f.ID == id {
			return f, true
		}
	}
	return File{}, false
}

// Upsert replaces the entry with the same Path, or appends a new one. The
// in-memory order is not significant — Canonicalize/Encode sort on write.
func (c *Catalog) Upsert(f File) {
	for i := range c.Files {
		if c.Files[i].Path == f.Path {
			c.Files[i] = f
			return
		}
	}
	c.Files = append(c.Files, f)
}

// Remove drops the entry with the given path, reporting whether one was found.
func (c *Catalog) Remove(path string) bool {
	out := c.Files[:0]
	removed := false
	for _, f := range c.Files {
		if f.Path == path {
			removed = true
			continue
		}
		out = append(out, f)
	}
	c.Files = out
	return removed
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
