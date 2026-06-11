# Task 30: internal/identity — Genesis Records, File IDs & Pull Receipts

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "File identity — genesis-hash IDs (dual addressing)"; Part II task breakdown → 3.4 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 27 (SPEC v2 §11 ID derivation, §12 receipt format), Task 29 (`wal` — the genesis record is the ingest WAL entry's identity fields) · **Type:** Implementation

## Summary

Every federated file gets **dual addressing**: a stable **file ID** that
survives moves and edits, and a **logical path** (`<location>/<relative-path>`)
for display and navigation. Moves change the path, never the ID; locks and
links reference the ID. The ID is a **genesis hash**:
`id = sha256(genesis record)` where the record is the file's ingest WAL entry
identity — `{original content sha256, original relative path, ingest op id,
origin node}`. That makes it unique (op id + path salt), location-independent,
deterministic, and **self-certifying**: any record hashing to a claimed id
proves itself. The ID is deliberately *not* the content hash — manual files
are editable in place, so content sha drifts between scans (H12/D19).

This task ships `internal/identity`: minting a genesis record at ingest,
recomputing/verifying ids from records, the 12-hex short display form, and
**pull receipts** — `~/.tailvault/receipts/<id>.toml` files written by every
`vault get` so each download becomes an off-node identity backup (D24b).
Receipts plus genesis-embedding lock entries (Task 35) are the identity
recovery story: `vault restore-identity` (Block 4) re-seeds a rebuilt catalog
from any surviving record after verifying sha256(record)==id. Recovery is
never implicit.

Byte-exact determinism of the genesis serialization is the whole game: two
implementations disagreeing by one whitespace byte mint different ids for the
same file. This package owns that canonical serialization (frozen in SPEC v2
§11, with a worked test vector) and everyone else calls in. Plain errors only
(§8 layering).

## Context

### Related packages

- `internal/identity` — **created here.**
- `internal/wal` (Task 29) — ingest entries supply the record fields.
- `internal/catalog` (Task 28) — `catalog.Genesis` mirrors the record;
  catalogs store id + record per file.
- Downstream: lock v2 (Task 35 embeds records), resolution (Task 32 displays
  short ids), `vault get` (Block 4 writes receipts), `restore-identity`
  (Block 4).

### Prerequisites

- [ ] Task 27 merged; §11 canonical serialization + worked id test vector and
  §12 receipt schema frozen.
- [ ] Task 29 merged (`wal.Entry` for ingest-entry interop).

## Changes Required

### internal/identity/genesis.go

- **File:** `internal/identity/genesis.go`
- **Action:** create
- **Purpose:** the record type + canonical bytes + mint/recompute/verify.

```go
package identity

// Genesis is the immutable birth record of a federated file (SPEC v2 §11).
type Genesis struct {
	ContentSHA256 string `toml:"content_sha256"` // original content hash at ingest
	OriginalPath  string `toml:"original_path"`  // original vault-relative path
	IngestOpID    string `toml:"ingest_op_id"`   // wal op id of the ingest entry
	OriginNode    string `toml:"origin_node"`
}

// CanonicalBytes renders the §11 frozen serialization (fixed field order,
// canonical TOML, LF). Byte-exact determinism is normative.
func (g Genesis) CanonicalBytes() ([]byte, error)

// MintID computes id = hex(sha256(CanonicalBytes(g))).
func MintID(g Genesis) (string, error)

// Verify reports whether g self-certifies the claimed id
// (MintID(g) == id; case-insensitive hex compare, stored lowercase).
func Verify(g Genesis, id string) (bool, error)

// Short returns the 12-hex display form (like git short SHAs).
func Short(id string) string

// FromIngestEntry extracts the record from an op_type="ingest" wal entry
// (args: content_sha256, path; plus OpID and the node), erroring on any
// other op type.
func FromIngestEntry(e wal.Entry, originNode string) (Genesis, error)

func (g Genesis) Validate() error // 64-hex content sha, non-empty fields
```

Implementation Notes:

- `CanonicalBytes` must match SPEC v2 §11 byte-for-byte; the §11 worked
  example (record → id) is a mandatory test vector. Do not round-trip through
  `pelletier/go-toml/v2` marshaling for the canonical form — render the four
  lines explicitly so library version bumps can never shift bytes.
- `Short` is display-only; all storage/lookup uses the full 64-hex id.
  Resolution/CLI tasks may accept short-id prefixes, but ambiguity handling
  lives there, not here.
- Two identical files ingested at different paths/times mint different ids
  (op id + path salt) — assert this in tests; it is the uniqueness property.

### internal/identity/receipt.go

- **File:** `internal/identity/receipt.go`
- **Action:** create
- **Purpose:** pull receipt read/write per SPEC v2 §12.

```go
// Receipt is one ~/.tailvault/receipts/<id>.toml (SPEC v2 §12).
type Receipt struct {
	ID           string    `toml:"id"`
	Genesis      Genesis   `toml:"genesis"`
	Path         string    `toml:"path"` // logical path at pull time
	SHA256AtPull string    `toml:"sha256_at_pull"`
	PulledAt     time.Time `toml:"pulled_at"`
	SourceNode   string    `toml:"source_node"`
}

// ReceiptDir resolves the receipts directory (default ~/.tailvault/receipts,
// overridable for tests via an explicit dir argument on the funcs below).
func WriteReceipt(dir string, r Receipt) error          // atomic; verifies Verify(r.Genesis, r.ID) first
func ReadReceipt(dir, id string) (Receipt, error)
func ListReceipts(dir string) ([]Receipt, error)
```

Implementation Notes:

- `WriteReceipt` refuses a receipt whose genesis does not certify its id —
  receipts are recovery material; a corrupt one is worse than none.
- Atomic write via the same temp+fsync+rename discipline (reuse a small shared
  helper or mirror `catalog.WriteAtomic`).
- Receipts are overwritten on re-pull (latest pull wins); they are advisory
  recovery artifacts, never authoritative state.

## Implementation Checklist

- [ ] `Genesis` + `CanonicalBytes` matching §11 byte-for-byte.
- [ ] `MintID`/`Verify`/`Short`; lowercase-hex normalization.
- [ ] `FromIngestEntry` bridging from `wal.Entry`.
- [ ] `Receipt` read/write/list with self-certification check on write.
- [ ] §11 worked test vector encoded as a unit test.

## Testing Requirements

`internal/identity/*_test.go`:

- **Test vector:** the §11 worked example record produces exactly the §11 id.
- **Determinism:** `MintID` of the same record across 100 runs / fresh structs
  is identical; field reordering in input does not change output.
- **Uniqueness:** identical content at two paths → different ids; identical
  content+path with different op ids → different ids.
- **Self-certification:** `Verify` true for matching pairs; false when any
  record field or the id is perturbed.
- **Short form:** `Short` = first 12 hex chars.
- **Receipts:** write→read round-trip; `WriteReceipt` rejects a non-certifying
  genesis; re-pull overwrites.
- **FromIngestEntry:** correct extraction; non-ingest op type errors.

Fixtures: `t.TempDir()` as the receipts dir; no network, no real node.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- ID derivation matches SPEC v2 §11's test vector byte-for-byte.
- IDs are stable across moves/edits (nothing about current home or current
  content is an input) and unique across distinct ingests.
- `Verify` makes any surviving genesis record self-certifying.
- Pull receipts round-trip and refuse non-certifying records.

## Related Proposal Sections

> `id = sha256(genesis record)` where the genesis record is the ingest WAL
> entry `{original content sha256, original relative path, ingest op id,
> origin node}`. Unique (op id + path salt), location-independent,
> deterministic — regeneratable by anyone holding the genesis record.
> Short 12-hex display form.

> **Identity recovery** (self-certifying: a record hashing to the claimed id
> proves itself): lock entries embed the full genesis record …; every
> `vault get` writes a pull receipt (`~/.tailvault/receipts/<id>.toml`) …
> Never implicit.

## Notes & Considerations

- **Gotcha:** the ID ≠ content hash. Anything that "helpfully" recomputes an
  id from current file bytes is wrong by design — edits and moves must never
  change identity.
- **Gotcha:** residual risk is accepted (D25): a never-referenced file on a
  destroyed node loses identity; redundancy (GH-3) closes this later. Do not
  build replication here.
- **For Next Task:** Task 31's roster/caches and Task 32's resolution display
  `Short(id)`; Task 33 mints a genesis per bootstrapped file; Task 35 embeds
  `Genesis` into lock entries.
- **Prev:** [task-29-wal](./task-29-wal.md) ·
  **Next:** [task-31-fed-roster-caches](./task-31-fed-roster-caches.md)
