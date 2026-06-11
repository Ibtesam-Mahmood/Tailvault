# Task 49: `track` Manual-Ingest Mode

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Ingestion — three paths for non-git files" (path 1: Manual + track), "CLI surface (v2 additions)" (`track` gains manual-ingest registration mode) · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 29 (`internal/wal` catch-up entries), Task 30 (`internal/identity` genesis mint), Task 28 (`internal/catalog`), Task 33 (`vault init` — `.tailvaultignore` engine), Task 34 (`vault scan` — the sibling reconcile path), Task 46 (auth gate for the remote form) · **Type:** Implementation

## Summary

Ingestion path 1 (D18.1): a user copies a file by hand into a storage folder —
locally on the node, or onto a mounted share — and then **registers** it.
Block 1's `track <glob>` writes include rules into a repo's `tailvault.toml`;
this task gives the same verb a second, vault-side mode:
`tailvault track <location>/<relative-path>` (no repo context) registers an
already-present file with the vault by emitting **catch-up WAL entries** —
entries that describe a write that already happened on disk — minting the
genesis record (`{content sha256, relative path, ingest op id, origin node}`),
deriving the file ID, and adding the catalog entry with `sync_mode = manual`
(D21). Run on the node itself it operates locally; run from another machine it
executes over SSH like every other remote op, and since it mutates the node's
catalog/WAL it is **password-gated** in that remote form.

The interplay with its two siblings is precise. `vault scan` (Task 34) is the
bulk reconcile — it would eventually find the file anyway; `track` is the
targeted, immediate registration of one path (or glob) without paying a full
disk walk. `.tailvaultignore` (Task 33, doublestar globs) is honored by scan
and bootstrap, but **explicit `track` overrides it** (D22): naming a file is a
stronger signal than a glob, so tracking an ignored path succeeds with a notice
that the path is ignore-listed.

`track` of a path the catalog already knows is an idempotent no-op (with a
freshness note if the on-disk sha drifted — that is `scan`'s job to absorb,
and the command says so). `track` of a missing path is a clean error: this
command registers reality, it never creates it.

## Context

### Related packages

- `cmd/tailvault` — **modified here:** `track` gains vault-mode argument
  parsing (`<location>/<path>` vs repo-glob) and the new code path.
- `internal/wal` (Task 29) — catch-up entry kind (intent+done describing an
  already-applied disk fact; confirm the shape Task 29 froze).
- `internal/identity` (Task 30) — genesis mint at track time.
- `internal/catalog` (Task 28) — entry add, `last_scanned` stamp.
- `internal/ignore` (Task 33) — `.tailvaultignore` matching (override
  semantics applied here).
- `internal/auth` (Task 46) — gate on the remote form only.

### Prerequisites

- [ ] Tasks 27–30, 33, 34 merged.
- [ ] Confirm with Task 27/29 the catch-up WAL entry encoding (it must be
  distinguishable from a normal mutating intent in `ops` output and in the
  hash chain).

## Changes Required

### cmd/tailvault/track.go

- **File:** `cmd/tailvault/track.go`
- **Action:** modify
- **Purpose:** mode dispatch + the vault-mode implementation.

```go
// Repo mode (Block 1, unchanged):  tailvault track "**/*.pdf"   (inside a repo)
// Vault mode (new):                tailvault track home-pi/media/demo.mp4
//                                  tailvault track home-pi/media/**/*.mp4
// flags (vault mode): --json
func runTrack(cmd *cobra.Command, args []string) error {
	// dispatch: arg resolves to a known location name prefix -> vault mode;
	// otherwise repo mode (must be inside a repo with tailvault.toml).
	// Ambiguity (both plausible) -> error demanding --vault / --repo.
}

func runTrackVault(ctx context.Context, target string) error {
	// 1. resolve location -> node/backend; remote? auth.Gate(node)
	// 2. expand glob against the node's disk (backend List), minus already-
	//    catalogued paths; honor .tailvaultignore EXCEPT for exact-path args
	// 3. per file: HashObject -> sha; wal catch-up entry (op id) ->
	//    genesis mint -> catalog add {id, genesis, sha, path,
	//    sync_mode: "manual", last_scanned: now} -> done
	// 4. report: tracked / already-tracked / ignored-but-tracked notices
}
```

Implementation Notes:

- **Catch-up semantics:** the bytes are already on disk, so there is no blob
  transfer; the WAL entry records the *registration* (and the hash observed at
  registration), preserving write-ahead ordering for everything tailvault
  itself does (catalog write still follows the WAL append).
- **Ignore override (D22):** an **exact path** argument tracks even when
  ignore-listed (with notice). A **glob** argument does *not* override
  ignores — globs are sweep semantics, exact names are intent.
- **Idempotence:** already-catalogued path → no new WAL entry, no re-mint
  (re-minting would fork identity); exit 0 with "already tracked (id <short>)".
  Drifted sha → add "content changed since last scan; run `tailvault vault
  scan`" — track does not silently re-hash an existing entry (that is scan's
  contract, Task 34).
- **Local vs remote:** on the node itself (location resolves to a local
  base_path) no password is required — D9 gates *remote* mutating ops; local
  execution is the same trust domain as editing the disk by hand. Remote form
  gates via `auth.Gate`.
- **Batch resumability:** a glob matching many files processes one WAL op per
  file (per-blob lock granularity); an interruption leaves complete files
  tracked and the rest untouched — re-running resumes naturally (idempotence).
- SPEC §8 layering at the command boundary as usual.

## Implementation Checklist

- [ ] Mode dispatch repo-glob vs `<location>/<path|glob>`; ambiguity error.
- [ ] Exact-path track of an ignored file succeeds with notice; glob respects
  `.tailvaultignore`.
- [ ] Catch-up WAL entry + genesis mint + catalog add (`sync_mode = manual`,
  `last_scanned` stamped) per file.
- [ ] Idempotent re-track; drifted-sha notice deferring to `scan`.
- [ ] Missing path → clean error; nothing minted.
- [ ] Remote form password-gated; local form not.

## Testing Requirements

Against the Task 39 harness (stub backends only):

- **Single file:** drop bytes into a stub member's tree → `track` → catalog
  entry with self-certifying id, manual mode, WAL catch-up entry present;
  `vault ls` now shows it.
- **Glob:** N files, one ignore-listed → N−1 tracked; exact-path track of the
  ignored one succeeds with notice.
- **Idempotence:** second `track` of the same path → no new WAL entry, same id.
- **Drift:** edit bytes after tracking → re-track notes drift and defers to
  scan; `scan` (Task 34) then absorbs it (integration assertion).
- **Missing path / unknown location:** clean errors, zero WAL entries.
- **Remote auth:** wrong password on the remote form → untouched node; local
  form needs none.
- **Interrupted glob batch:** fault-inject mid-batch → completed files
  tracked; rerun completes the rest with no duplicate identities.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A hand-dropped file becomes a first-class federated file (id, genesis,
  catalog entry, WAL trail) via one `track` invocation, locally or remotely.
- Explicit-path track overrides `.tailvaultignore`; glob track respects it.
- Re-track never re-mints an identity; interrupted batches resume cleanly.
- Repo-mode `track` behavior from Block 1 is byte-for-byte unchanged.

## Related Proposal Sections

> **Manual + track**: drop a file into the storage folder by hand, then run
> `track` (locally or remotely) → catch-up WAL entries. On-demand `vault scan`
> reconciles disk ↔ catalog (absorbs manual moves/deletes).

> Bootstrap ignore file = `.tailvaultignore`, gitignore-style glob patterns
> (doublestar); overridden by explicit `track`.

> Default `sync_mode = manual` for all three ingestion paths.

## Notes & Considerations

- **Gotcha:** the dispatch heuristic must never let a repo glob that happens to
  start with a location name silently become a vault op — when both
  interpretations are possible, refuse and demand an explicit flag.
- **Gotcha:** hashing happens **on the node** (`HashObject`, Task 40) — pulling
  a multi-GB hand-dropped file across the tailnet just to register it would
  defeat the point of path 1.
- **For Next Task:** Task 50 exercises this command (and every other Block 4
  surface) end-to-end on the multi-node harness.
- **Prev:** [task-48-restore-identity](./task-48-restore-identity.md) ·
  **Next:** [task-50-fed-integration-suite](./task-50-fed-integration-suite.md)
