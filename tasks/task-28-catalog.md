# Task 28: internal/catalog — Vault Catalog Parse/Write/Atomic-Update

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Catalog (vault-side state) + atomicity standards"; Part II task breakdown → 3.2 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 27 (SPEC v2 §9 catalog schema, §13 roster section) · **Type:** Implementation

## Summary

Each storage location becomes **self-describing**: a `meta/catalog.toml` under
the vault root records every stored file — `{id, genesis record, current
sha256, logical path, sync_mode, size, timestamps, last_scanned}` — plus the
`[federation]` roster and a schema version field. This task ships
`internal/catalog`: parse, validate, canonical write, and **atomic update**
(temp file + fsync + rename), implementing SPEC v2 §9 byte-for-byte the way
`internal/lock` implements §2.

Atomicity is a frozen standard, not a nicety: blob, catalog, and WAL writes
all use temp-file + fsync + atomic rename so a crash at any point leaves
either the old or the new catalog, never a torn one. The catalog is also the
half of the write-ahead ordering (WAL intent → blob bytes → **catalog** → WAL
done) that later tasks (29, 33, 34) sequence around, so this package exposes
a single `WriteAtomic` everyone routes through.

Schema versioning (H7) lands here too: `version = 2` is required; a reader
hitting any other version returns a plain incompatibility error that the
command boundary maps to a `TV-CFG`-style exit-2 failure. Per the §8
error-layering rule, this leaf package returns **plain errors only**.

## Context

### Related packages

- `internal/catalog` — **created here.**
- `internal/lock` (Task 04) — canonical-ordering conventions to mirror.
- `internal/fed` (Task 31) — consumes the `[federation]` roster types.
- `internal/wal` (Task 29), `vault init`/`scan` (Tasks 33–34), verify
  (Task 38) — downstream readers/writers.

### Prerequisites

- [ ] Task 27 merged; SPEC v2 §9 + §13 frozen (sample block available as a
  fixture).
- [ ] Confirm the on-node location: `meta/catalog.toml` under
  `<base_path>/<subpath>/`.

## Changes Required

### internal/catalog/catalog.go

- **File:** `internal/catalog/catalog.go`
- **Action:** create
- **Purpose:** types + parse/validate per SPEC v2 §9.

```go
package catalog

// Catalog mirrors meta/catalog.toml (SPEC v2 §9).
type Catalog struct {
	Version    int        `toml:"version"` // MUST be 2
	VaultName  string     `toml:"vault_name"`
	Node       string     `toml:"node"`
	Federation Federation `toml:"federation"`
	Files      []File     `toml:"file"`
}

// File is one [[file]] entry, sorted by Path byte-wise ascending.
type File struct {
	ID          string    `toml:"id"`      // 64-hex genesis hash (SPEC v2 §11)
	Genesis     Genesis   `toml:"genesis"` // full record, inline table
	SHA256      string    `toml:"sha256"`  // current content hash
	Path        string    `toml:"path"`    // vault-relative logical path
	SyncMode    string    `toml:"sync_mode"` // "git" | "manual" | future values
	Size        int64     `toml:"size"`
	CreatedAt   time.Time `toml:"created_at"`
	UpdatedAt   time.Time `toml:"updated_at"`
	LastScanned time.Time `toml:"last_scanned"`
}

// Genesis mirrors the §11 record; identity (Task 30) owns the hashing.
type Genesis struct {
	ContentSHA256 string `toml:"content_sha256"`
	OriginalPath  string `toml:"original_path"`
	IngestOpID    string `toml:"ingest_op_id"`
	OriginNode    string `toml:"origin_node"`
}

// Federation / Member mirror §13; fed (Task 31) adds behavior.
type Federation struct {
	FedID   string   `toml:"fed_id"`
	Members []Member `toml:"member"`
}
type Member struct {
	Name     string    `toml:"name"`
	Node     string    `toml:"node"`
	JoinedAt time.Time `toml:"joined_at"`
	Status   string    `toml:"status"` // active | left | evicted
}

var ErrIncompatibleVersion = errors.New("catalog: incompatible schema version")

func Parse(b []byte) (*Catalog, error)   // TOML decode + Validate
func (c *Catalog) Validate() error       // version==2 (else ErrIncompatibleVersion), hex ids, non-empty paths
func (c *Catalog) Canonicalize()         // sort Files by Path byte-wise asc
func (c *Catalog) Find(path string) (File, bool)
func (c *Catalog) FindID(id string) (File, bool)
func (c *Catalog) Upsert(f File)
func (c *Catalog) Remove(path string) bool
```

Implementation Notes:

- `SyncMode` is an **open** string enum (D15): unknown values round-trip
  unchanged; only gc (Task 36) interprets them ("not `git`" = never a gc
  candidate). Do not validate against a closed list.
- Canonical field order within `[[file]]` follows SPEC v2 §9 exactly so diffs
  of catalog files are minimal — mirror how `lock.Write` fixes field order.
- Plain errors only (§8 layering); commands wrap with `tserr` at the boundary
  (incompatible version → exit 2).

### internal/catalog/write.go

- **File:** `internal/catalog/write.go`
- **Action:** create
- **Purpose:** canonical serialization + atomic local write.

```go
// Encode renders the canonical TOML bytes (sorted entries, fixed field order).
func Encode(c *Catalog) ([]byte, error)

// WriteAtomic writes Encode(c) to path via temp file + fsync + rename in the
// same directory. The single write seam for every local catalog mutation.
func WriteAtomic(path string, c *Catalog) error

// Load reads + parses a local catalog file.
func Load(path string) (*Catalog, error)
```

Implementation Notes:

- Temp file lives in the **same directory** as the target (rename across
  filesystems is not atomic); fsync the file, rename, then fsync the directory.
- Remote catalogs (on a storage node) are read/written through the `Backend`
  interface by callers: `Get("meta/catalog.toml")` → `Parse`, and `Encode` →
  `Put("meta/catalog.toml")` — the SSH backend's temp-then-`mv` write (Task 09)
  supplies remote atomicity. This package stays transport-free.

## Implementation Checklist

- [ ] `Catalog`/`File`/`Genesis`/`Federation`/`Member` types matching §9/§13.
- [ ] `Parse`/`Validate` with `ErrIncompatibleVersion` on `version != 2`.
- [ ] `Canonicalize` byte-wise path sort + fixed field order in `Encode`.
- [ ] `Find`/`FindID`/`Upsert`/`Remove` helpers.
- [ ] `WriteAtomic` (temp + fsync + rename + dir fsync) and `Load`.

## Testing Requirements

`internal/catalog/*_test.go`:

- **Round-trip:** SPEC v2 §9 sample block → `Parse` → `Encode` → byte-identical
  output (canonical-form fixture, like the lock tests).
- **Version gate:** `version = 1` / `version = 3` → `ErrIncompatibleVersion`.
- **Canonical ordering:** entries inserted out of order are emitted sorted by
  path byte-wise ascending; `Encode` is deterministic across runs.
- **Open enum:** a `sync_mode = "s3"` entry parses, round-trips unchanged.
- **Atomicity:** `WriteAtomic` leaves no `*.tmp` debris on success; simulated
  encode failure leaves the original file intact.
- **Upsert/Remove/Find:** behavior incl. replace-by-path.

Fixtures: `t.TempDir()`; §9 sample pasted verbatim into `testdata/`.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- §9 sample parses and re-encodes byte-identically.
- Unknown schema version fails with `ErrIncompatibleVersion` (mapped to exit 2
  at the command boundary).
- All catalog mutations flow through `WriteAtomic`; no in-place writes exist.
- Unknown `sync_mode` values survive a round-trip.

## Related Proposal Sections

> Each location's catalog: object set, per-file {id, genesis record, current
> sha256, logical path, sync_mode, timestamps, last_scanned}, a `[federation]`
> roster section, and a schema version field.

> Atomicity: temp-file + fsync + atomic rename for every blob/catalog/WAL
> write; write-ahead ordering (WAL intent → blob bytes → catalog → WAL done);
> crash anywhere = detectable + repairable by `verify`/`heal`.

## Notes & Considerations

- **Gotcha:** `pelletier/go-toml/v2`'s default marshaling won't give the frozen
  field order — render entries explicitly (as `lock` does) rather than relying
  on struct-tag order.
- **Gotcha:** never write the catalog before the blob bytes exist — the
  write-ahead ordering is enforced by *callers* (Tasks 29/33/34), but document
  it on `WriteAtomic` so nobody inverts it.
- **For Next Task:** Task 29's WAL entries reference catalog file IDs in
  `blob_refs`; Task 31 reuses `Federation`/`Member` for roster merging.
- Log any serialization/ordering surprises in `EDGE-CASES.md`.
- **Prev:** [task-27-spec-v2-freeze](./task-27-spec-v2-freeze.md) ·
  **Next:** [task-29-wal](./task-29-wal.md)
