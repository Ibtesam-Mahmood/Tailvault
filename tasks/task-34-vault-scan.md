# Task 34: `vault scan` — Disk↔Catalog Reconcile & Manual-File Freshness

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Ingestion — three paths" (path 1: manual + track, `vault scan`); "File identity" (edited-vs-corrupt, H12); Part II task breakdown → 3.8 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 33 (`internal/ingest` walk/ignore machinery, bootstrap baseline), Task 28 (`catalog`), Task 29 (`wal`), Task 30 (`identity`) · **Type:** Implementation

## Summary

Manual files are edited, moved, and deleted by hand — by design. `tailvault
vault scan` is the **serverless answer to filesystem drift** (D23, replacing
the rejected resident OS-hook watcher, H10): an on-demand reconcile that diffs
the disk against the catalog and emits **catch-up WAL entries** for everything
that happened outside tailvault — manual adds (new untracked files →
catch-up ingest with a fresh genesis), manual edits (content drift → re-hash,
update catalog `sha256` + `last_scanned`), manual moves (same content, new
path → `move` entry preserving the file id), and manual deletes (catalog
entry without a file → `delete` entry, after confirmation or `--prune`).

Scan is also where **manual-file freshness** (H12) is computed: because the
git side is immutable but manual files mutate in place, a catalog `sha256`
that no longer matches disk is *either* a legitimate edit *or* corruption.
The heuristic: if mtime or size changed since `last_scanned`, classify as
**edited** (absorb: re-hash, update catalog); if mtime and size are unchanged
but the content hash differs, classify as **suspect/corrupt** (report, do NOT
absorb — verify (Task 38) owns the corruption verdict). `last_scanned` is
scan's bookkeeping field and is refreshed on every reconciled entry.

Move detection makes ids do their job: a vanished path plus an appeared path
with the same content hash is one `move`, not a delete+ingest — the genesis
id is preserved, so locks and links referencing it keep working. Per §8
layering, engine code returns plain errors; the command wraps `tserr`.

## Context

### Related packages

- `internal/ingest` — **extended here** (`scan.go`): reuses walk + ignore.
- `cmd/tailvault` — **`vault scan` subcommand created here.**
- `internal/catalog` (28), `internal/wal` (29), `internal/identity` (30).
- Downstream: verify 3-way (Task 38) consumes the edited-vs-suspect
  classification; gc (Task 36) relies on scan-honest catalogs.

### Prerequisites

- [ ] Task 33 merged (`BuildPlan`, `Ignore`, bootstrap op-id conventions).
- [ ] SPEC v2 §9 `last_scanned` semantics + §10 `scan` op type frozen.

## Changes Required

### internal/ingest/scan.go

- **File:** `internal/ingest/scan.go`
- **Action:** create
- **Purpose:** the diff + classification engine and catch-up application.

```go
package ingest

type ChangeKind int

const (
	Added   ChangeKind = iota // on disk, not in catalog → catch-up ingest
	Edited                    // hash drift + mtime/size moved since last_scanned
	Suspect                   // hash drift, mtime+size UNCHANGED → corrupt? report only
	Moved                     // same content hash: old path gone, new path present
	Deleted                   // in catalog, not on disk
)

type Change struct {
	Kind    ChangeKind
	Path    string // current/new path
	OldPath string // Moved only
	File    catalog.File // existing entry (zero for Added)
	SHA256  string       // disk hash (where computed)
	Size    int64
}

// Diff walks root (reusing BuildPlan + Ignore), compares against cat, and
// classifies. Hashing is lazy: only entries whose mtime/size moved since
// last_scanned (or that are candidates for move-pairing) are re-hashed.
func Diff(ctx context.Context, root string, ig *Ignore,
	cat *catalog.Catalog, p Progress) ([]Change, error)

// Apply emits catch-up WAL entries and updates the catalog for each change:
// Added → ingest (new genesis), Edited → scan entry + sha/last_scanned bump,
// Moved → move entry (id preserved), Deleted → delete entry.
// Suspect changes are NEVER applied — returned for reporting only.
func Apply(ctx context.Context, log *wal.Log, cat *catalog.Catalog,
	catPath string, changes []Change) (applied, skipped []Change, err error)
```

Implementation Notes:

- **Freshness heuristic (H12, normative here):** for an existing manual
  entry — (1) mtime AND size unchanged since `last_scanned` → assume fresh,
  skip hashing (cheap scans on big vaults); (2) mtime or size changed →
  re-hash; if hash differs → `Edited`; (3) the paranoid path (`--paranoid`
  flag) hashes everything; an entry whose hash differs while mtime+size are
  unchanged → `Suspect`. Document that (1) trades a sliver of certainty for
  scan speed; verify (Task 38) is the thorough pass.
- **Move pairing:** pair Deleted+Added candidates by content hash before
  emitting either; ambiguous many-to-many matches (same hash several places)
  fall back to delete+ingest with a notice — log the case in `EDGE-CASES.md`.
- **Catch-up entries** use `op_type` values from §10 (`ingest`, `move`,
  `delete`, `scan`) with `args` recording old/new paths and hashes; each goes
  through the full intent→execute→done lifecycle (per-blob lock included) so
  a scan racing another op on the same blob fails "op in flight" rather than
  corrupting state.
- `last_scanned` is bumped on **every** reconciled entry, including unchanged
  ones touched by `--paranoid` (batch the catalog write).
- `sync_mode = "git"` entries are skipped: the git flow owns their lifecycle;
  scan only reports them if missing on disk (verify's territory).

### cmd/tailvault/vault_scan.go

- **File:** `cmd/tailvault/vault_scan.go`
- **Action:** create
- **Purpose:** the Cobra command.

```go
// tailvault vault scan <location> [--dry-run] [--prune] [--paranoid]
//   --dry-run   print the classified diff, change nothing
//   --prune     apply Deleted changes without per-file confirmation
//   --paranoid  hash every entry regardless of mtime/size
```

Implementation Notes:

- Output groups by kind with counts; Suspect entries print loudly with "run
  `tailvault verify` — possible corruption" and force a non-zero summary note
  (but scan itself exits 0 if its own work succeeded — corruption verdicts
  belong to verify; record this choice in `EDGE-CASES.md`).
- Deletes confirm interactively by default (destructive catalog change);
  `--prune` for scripts.

## Implementation Checklist

- [ ] `Diff` with lazy hashing + the three-branch freshness heuristic.
- [ ] Move pairing by content hash with ambiguity fallback.
- [ ] `Apply` emitting WAL-journaled catch-up entries; Suspect never applied.
- [ ] git-mode entries skipped.
- [ ] `vault scan` command with `--dry-run`/`--prune`/`--paranoid`.

## Testing Requirements

`internal/ingest/scan_test.go` (temp dirs + `FSBackend`; no real node):

- **Added:** new file → catch-up ingest with fresh genesis id, manual mode.
- **Edited:** modify a file (mtime moves) → `Edited`; catalog sha +
  `last_scanned` updated; WAL `scan` entry present; id unchanged.
- **Suspect:** rewrite bytes then restore mtime+size (set times explicitly) →
  `--paranoid` scan classifies `Suspect`; not applied; catalog untouched.
- **Moved:** rename a file → single `move` entry; id preserved; no
  ingest/delete pair.
- **Deleted:** remove a file → `delete` with confirmation/`--prune`.
- **Lazy hashing:** untouched files are not re-hashed (count hash invocations
  via a seam).
- **Race:** pending WAL intent on a blob → scan's catch-up op on it fails
  "op in flight", other changes still apply.
- **Dry-run:** classification printed, zero WAL/catalog writes.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Manual adds/edits/moves/deletes performed by hand are absorbed into WAL +
  catalog by a single `vault scan`; ids survive moves.
- Edited and Suspect are distinguished per the H12 heuristic; Suspect is
  reported, never silently absorbed.
- Scan is cheap on unchanged vaults (lazy hashing) and safe under concurrent
  ops (per-blob lock respected).

## Related Proposal Sections

> **Manual + track**: drop a file into the storage folder by hand, then run
> `track` … → catch-up WAL entries. On-demand `vault scan` reconciles disk ↔
> catalog (absorbs manual moves/deletes). A resident OS-hook watcher is
> explicitly OUT (first daemon-shaped thing).

> The ID is **not** the content hash: manual files are editable in place, so
> content sha drifts until a scan re-hashes (verify distinguishes "corrupt"
> from "edited since last scan" via mtime/size + `last_scanned`).

## Notes & Considerations

- **Gotcha:** mtime granularity differs by filesystem (1s on some); an edit
  within the same second as the last scan can look unchanged. Mitigate by
  also comparing size, and treat `last_scanned >= mtime` boundaries
  conservatively (re-hash on equality). Log findings in `EDGE-CASES.md`.
- **Gotcha:** never mint a new genesis for a `Moved` file — that is the whole
  point of dual addressing.
- **For Next Task:** Task 35's `heal` and Task 38's verify both lean on
  catalogs that scan keeps honest; Task 38 reuses the Edited/Suspect
  classification.
- **Prev:** [task-33-vault-init-bootstrap](./task-33-vault-init-bootstrap.md) ·
  **Next:** [task-35-lock-v2-heal](./task-35-lock-v2-heal.md)
