# Task 33: `vault init` — Bootstrap Ingestion of a Storage Root

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Ingestion — three paths for non-git files" (path 2: Creation/bootstrap); Part II task breakdown → 3.7 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 27 (SPEC v2 §9 catalog, §10 WAL, §11 ids, `.tailvaultignore` semantics), Task 28 (`catalog`), Task 29 (`wal` — resumability), Task 30 (`identity` — genesis minting) · **Type:** Implementation

## Summary

`tailvault vault init` is ingestion path 2: the first broadcast of a storage
root into the federation. It walks the root, and **tracks ALL files and
subfolders by default** — the opt-out, not opt-in, posture is deliberate (a
vault should describe everything it holds). Opt-outs are (a) a
`.tailvaultignore` file at the vault root — gitignore-style doublestar globs,
overridden by any explicit `track` — and (b) an interactive deselect flag
(`--select`) that presents the candidate tree for pruning before ingestion
begins. Every ingested file gets a genesis record minted from its ingest WAL
entry, lands in the catalog with **default `sync_mode = "manual"`** (D21 —
`git` is only ever set by the git-repo flow), and the catalog + WAL are
written under the atomicity standards.

Huge roots are the named risk (H11): "track everything" on a big tree means
hashing and cataloguing everything, so the command must show progress
(files/bytes done, current path) and be **resumable via the WAL** — the
bootstrap is itself a WAL-journaled operation, and re-running `vault init`
after an interruption picks up where the intent trail left off instead of
re-hashing completed work. Idempotency comes free from op-id dedupe.

The command operates on a registered location (local path or over the
backend); preflight + hard-fail rules from v1 apply unchanged. Per §8
layering, engine code returns plain errors; the command maps to `tserr`.

## Context

### Related packages

- `cmd/tailvault` — **`vault init` subcommand created here** (under a new
  `vault` command group).
- `internal/ingest` — **created here**: walk/filter/hash/ingest engine shared
  later by `vault scan` (Task 34) and `track` manual mode (Block 4).
- `internal/catalog` (28), `internal/wal` (29), `internal/identity` (30),
  `internal/backend` (09), `internal/locations` (10).
- `bmatcuk/doublestar/v4` — ignore-glob matching (already a dependency).

### Prerequisites

- [ ] Tasks 28–30 merged.
- [ ] SPEC v2 `.tailvaultignore` semantics frozen (Task 27): vault-root file,
  gitignore-style doublestar globs, opt-out only, overridden by explicit track.
- [ ] Confirm the vault-root reserved names excluded from ingestion always:
  `meta/` (catalog, WAL, auth), `.tailvaultignore` itself, `objects/`/`refs/`
  (git-flow storage areas).

## Changes Required

### internal/ingest/ignore.go

- **File:** `internal/ingest/ignore.go`
- **Action:** create
- **Purpose:** `.tailvaultignore` parsing + matching.

```go
package ingest

// Ignore is a parsed .tailvaultignore: gitignore-style doublestar globs,
// one per line; '#' comments and blank lines skipped; later lines win;
// '!' re-includes (gitignore negation).
type Ignore struct{ /* compiled patterns */ }

func LoadIgnore(root string) (*Ignore, error) // missing file → empty Ignore
func ParseIgnore(b []byte) (*Ignore, error)
// Match reports whether rel (slash-separated, vault-relative) is ignored.
// Explicitly tracked paths (the overrides set) always win over ignores.
func (ig *Ignore) Match(rel string, explicit map[string]bool) bool
```

### internal/ingest/bootstrap.go

- **File:** `internal/ingest/bootstrap.go`
- **Action:** create
- **Purpose:** the resumable walk→hash→WAL→catalog pipeline.

```go
// Plan is the candidate set after walk + ignore + deselect.
type Plan struct {
	Root  string
	Files []Candidate // rel path, size; sorted by path
}

// BuildPlan walks root, applies reserved-name exclusions + .tailvaultignore.
func BuildPlan(root string, ig *Ignore) (Plan, error)

// Progress receives per-file events for the CLI's progress UX.
type Progress func(done, total int, doneBytes, totalBytes int64, current string)

// Bootstrap ingests the plan: for each candidate not already completed in the
// WAL — append ingest intent → hash content → mint genesis/id → upsert
// catalog entry (sync_mode="manual") → mark done. Resumable: on entry it
// reads the WAL, skips done ingest ops for this bootstrap, and re-executes
// pending ones (idempotent by op id).
func Bootstrap(ctx context.Context, root string, log *wal.Log,
	cat *catalog.Catalog, catPath string, plan Plan, p Progress) error
```

Implementation Notes:

- **Write-ahead ordering per file:** WAL intent → (bytes already exist on
  disk; hash them) → catalog upsert via `catalog.WriteAtomic` → WAL done.
  Crash between steps leaves a pending intent that the next run re-executes.
- **Resumability (H11):** keying ingest ops by a deterministic op id derived
  from `(bootstrap op id, rel path)` makes "already ingested" detectable via
  `wal` dedupe without re-hashing; alternatively skip candidates already in
  the catalog with `last_scanned` set. Pick one and test the
  interrupt-at-every-step matrix.
- **Catalog write batching:** upserting + atomically rewriting the catalog
  per file is O(n²) bytes on huge roots — batch catalog flushes (every N
  files / M bytes), since the WAL, not the catalog, is the recovery record.
  Note the chosen N in `EDGE-CASES.md`.
- Genesis minting: `identity.FromIngestEntry` on each ingest entry; the id
  goes into the catalog row with the full record (SPEC v2 §9).
- Hashing streams (`io.Copy` into `sha256.New()`) — never read a blob fully
  into memory.

### cmd/tailvault/vault_init.go

- **File:** `cmd/tailvault/vault_init.go`
- **Action:** create
- **Purpose:** the Cobra command.

```go
// tailvault vault init <location> [--select] [--dry-run]
//   --select   interactive deselect: show the plan tree, let the user prune
//   --dry-run  print the plan (kept/ignored counts + ignored sample) and exit
```

Implementation Notes:

- Default `sync_mode = manual` for everything (D21); there is no flag to set
  `git` here.
- Progress line: `ingesting 412/9301 files (1.2 GB / 18.4 GB) pnp/board.pdf`
  — rewrite in place on a TTY, log every Nth file otherwise. Sizes display in
  binary units (SPEC §7).
- Re-running after completion is a clean no-op ("already bootstrapped; run
  `vault scan` to reconcile changes").
- Command boundary wraps errors: unreadable root / bad ignore file →
  `tserr.ConfigErr` (exit 2); node issues → TV-NODE; chain break →
  `tserr.FedChainBrokenErr` once Task 32 lands (plain `wal.ErrChainBroken`
  mapping may land here directly if 33 merges first).

## Implementation Checklist

- [ ] `Ignore` parse/match incl. negation + explicit-track override.
- [ ] `BuildPlan` with reserved-name exclusions.
- [ ] `Bootstrap` pipeline with per-file WAL lifecycle + batched catalog
  flushes + streaming hash.
- [ ] Resume: interrupted run re-executes pending, skips done.
- [ ] `vault init` command: `--select` interactive deselect, `--dry-run`,
  progress UX, clean re-run no-op.

## Testing Requirements

`internal/ingest/*_test.go` + a command-level test (temp dirs, `FSBackend`
for the WAL — never a real node):

- **Track-all default:** a fixture tree ingests every file; reserved names
  (`meta/`, `.tailvaultignore`) excluded.
- **Ignore:** patterns drop matches; `!` re-includes; explicit-track set
  overrides an ignore; bad glob → error naming the line.
- **Genesis/catalog:** each ingested file has id == `identity.MintID(genesis)`,
  `sync_mode = "manual"`, populated timestamps.
- **Resume:** kill the pipeline after the WAL intent / after the catalog
  upsert (injected fault) → second run completes; total ingest ops in the WAL
  show no duplicates; final catalog identical to an uninterrupted run.
- **Idempotent re-run:** completed bootstrap re-run makes zero new WAL
  entries.
- **Dry-run:** plan printed, no WAL/catalog writes.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- First broadcast tracks everything by default; `.tailvaultignore` +
  `--select` are the only opt-outs and explicit track beats ignore.
- Every ingested file carries a self-certifying genesis id and
  `sync_mode = "manual"`.
- An interrupted bootstrap of a large fixture tree resumes to a catalog
  byte-identical to an uninterrupted run.
- Progress is visible during ingestion; `--dry-run` is side-effect-free.

## Related Proposal Sections

> **Creation (bootstrap)**: first broadcast of a storage root tracks ALL
> files/subfolders by default; opt-out via `.tailvaultignore` (gitignore-style
> globs, overridden by explicit `track`) and an init-time deselect flag.
> Resumable via WAL (huge roots).

> H11 … **Bootstrap of huge roots**: "track everything by default" … needs
> progress UX + resumability (WAL makes resumable natural).

## Notes & Considerations

- **Gotcha:** symlinks — decide (and log in `EDGE-CASES.md`) whether to skip
  or follow; skipping with a warning is the safe default (cycles, out-of-root
  escapes).
- **Gotcha:** files changing mid-walk (size at plan time ≠ size at hash time)
  — hash wins; update the candidate, don't fail the run.
- **For Next Task:** Task 34's `vault scan` reuses `internal/ingest`'s walk +
  ignore machinery to diff disk against the catalog after the bootstrap.
- **Prev:** [task-32-resolution-engine](./task-32-resolution-engine.md) ·
  **Next:** [task-34-vault-scan](./task-34-vault-scan.md)
