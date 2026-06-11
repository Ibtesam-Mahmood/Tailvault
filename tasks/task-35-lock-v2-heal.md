# Task 35: Lock Schema v2 (id + genesis), Pull WARN & `tailvault heal`

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "File identity" (identity recovery: lock-embedded genesis records), "Vision" point 4 (moves: pull warns, heal rewrites); Part II task breakdown → 3.9 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 30 (`identity.Genesis`/`Verify`), Task 32 (resolution engine — heal's lookup), Task 04 (`internal/lock` v1), Task 15 (`pull`) · **Type:** Implementation

## Summary

The repo-committed `tailvault.lock` becomes federation-aware: **schema v2**
entries embed the file **id** and the **full genesis record** alongside the
v1 fields. That single change makes every git clone an off-node identity
backup (D24a) and lets pull/heal reason about files whose home moved. Per
D29 there is **no v1-tolerance machinery**: no real v1 vaults exist, so
`lock.Parse` simply requires `version = 2` going forward and rejects
`version = 1` with the standard incompatibility error (exit 2) — old test
vaults are recreated, not migrated.

`tailvault pull` gains the **WARN path** (D5): when a needed blob is not at
its recorded location but resolution finds it elsewhere (moved file, or a
home that left the federation), pull still succeeds — hard-fail is reserved
for *unprovable* states — but prints a structured warning per entry:
"`<path>` (id `<short>`) has moved to `<member>` — run `tailvault heal`".
A partial view or genuinely missing blob keeps the v1 hard-fail behavior
(TV-FED-01 / TV-OBJ-01 via Task 32's mapping).

`tailvault heal` is the explicit, separate repair command (never automatic):
it resolves every lock entry through the resolution engine and **rewrites
entries whose recorded location no longer matches reality** — updating
`location` (and path metadata if the logical path changed) while ids and
genesis records stay fixed. The rewritten lock is committed by the user like
any lock change; `--dry-run` previews. Entries that resolve to PartialView
are left untouched and reported (heal never guesses under partial views).

## Context

### Related packages

- `internal/lock` — **modified here**: v2 schema fields + version gate.
- `cmd/tailvault` — **`heal` command created here**; `pull` modified.
- `internal/identity` (30) — `Genesis`, `Verify`, `Short`.
- `internal/fed` (31–32) — `Resolver` drives both the pull WARN and heal.
- Task 24's merge driver — must keep working against v2 entries.

### Prerequisites

- [ ] Tasks 30 and 32 merged.
- [ ] SPEC v2 §11 statement that lock entries embed genesis records (Task 27)
  re-read; this task adds the **normative v2 lock section** to `SPEC.md`
  (extending §2) with the new canonical field order.

## Changes Required

### SPEC.md — §2 extension (lock v2)

- **File:** `SPEC.md`
- **Action:** modify
- **Purpose:** freeze the v2 entry form before code: `version = 2`; new entry
  fields `id` (64-hex) and `genesis` (inline table, §11 record) inserted in
  the canonical order after `path`; everything else unchanged (sorting,
  tombstones, versions, RFC3339). `version = 1` locks are rejected (D29).
  Include an updated verbatim sample.

### internal/lock/lock.go

- **File:** `internal/lock/lock.go`
- **Action:** modify
- **Purpose:** the v2 fields + gate.

```go
type Entry struct {
	Path    string
	ID      string           // NEW: 64-hex genesis hash (SPEC v2 §11)
	Genesis identity.Genesis // NEW: full embedded record (off-node backup)
	SHA256  string
	// … existing v1 fields unchanged: Size, Location, PushedAt, Pusher,
	// History, Preserve, Deleted, Versions
}

// Parse: version MUST be 2; version 1 → plain incompatibility error
// (command boundary → tserr.ConfigErr, exit 2). Write emits version = 2 and
// the new canonical field order (path, id, genesis, sha256, …).
// Validate (new): for every entry with an ID, identity.Verify(Genesis, ID)
// must hold — a lock that fails self-certification is rejected as corrupt.
```

Implementation Notes:

- `push` (Task 14 code) populates `ID`/`Genesis` from the catalog when the
  blob's vault is federated; for a plain v1-style vault that has no catalog
  yet, push mints the genesis at first push (the push WAL ingest entry) —
  coordinate the exact seam with the existing push pipeline and keep it in
  this PR small: read id+genesis from the catalog if present, else leave the
  fields empty (omitted from TOML) and note it. Empty-id entries are legal in
  v2 (non-federated vaults) — heal and pull-WARN simply skip them.
- The union merge driver (Task 24) must treat `id`/`genesis` as ordinary
  per-entry fields; add a regression test there.

### cmd/tailvault — pull WARN

- **File:** `cmd/tailvault/pull.go` (and the engine seam it calls)
- **Action:** modify
- **Purpose:** moved/foreign entry warnings.

```go
// For each lock entry whose blob Stat misses at the recorded location:
//   resolve via fed.Resolver (homeHint = entry.Location)
//   FoundElsewhere → fetch from the answering member; succeed; WARN:
//     "pnp/board.pdf (id 9f2b1c4d8a01) moved to office-nas — run `tailvault heal`"
//   FoundAtHome (transient earlier miss) → proceed normally
//   PartialView → tserr.FedPartialViewErr (exit 6); Missing → TV-OBJ-01 (exit 5)
// Entries whose home member has status left/evicted WARN even when found:
//   "home 'pi-2' left the federation — repush or heal" (D28).
```

### cmd/tailvault/heal.go

- **File:** `cmd/tailvault/heal.go`
- **Action:** create
- **Purpose:** the explicit lock-repair command.

```go
// tailvault heal [--dry-run]
// For every lock entry with an ID: Resolve(id, entry.Location).
//   FoundAtHome   → untouched
//   FoundElsewhere→ rewrite entry.Location (and logical-path metadata if the
//                   catalog path changed); collect into the change report
//   PartialView   → untouched + reported ("cannot heal under partial view")
//   Missing       → untouched + reported as TV-OBJ candidate (verify/repush)
// Writes the canonical lock once at the end (atomic); --dry-run only prints.
// Exit: 0 if all entries FoundAtHome/healed; 6 if any PartialView remained;
// 5 if any Missing.
```

Implementation Notes:

- Heal **never** changes `id`, `genesis`, `sha256`, or history fields — it
  repoints location/path only. Identity is immutable; bytes-curation belongs
  to push/verify.
- Heal is repo-side only: no WAL writes, no node mutation — it edits the
  committed lock, which the user then commits/pushes (call this out in help
  text).
- Batch resolution: resolve distinct (id, home) pairs once; reuse `Reach`
  across entries from the same fan-out to avoid N full roster sweeps.

## Implementation Checklist

- [ ] SPEC §2 v2 extension with sample + canonical order.
- [ ] `lock.Entry` gains `ID`/`Genesis`; version-2 gate; self-certification in
  `Validate`; canonical write order updated.
- [ ] Empty-id entries legal (non-federated vaults), skipped by WARN/heal.
- [ ] Pull WARN on moved/foreign/left-home entries; hard-fail classes
  unchanged.
- [ ] `heal` command with `--dry-run`, per-outcome handling, exit mapping.
- [ ] Merge-driver regression test for the new fields.

## Testing Requirements

`internal/lock/*_test.go`, `cmd/tailvault/*_test.go` (stub resolver/backends):

- **Round-trip:** v2 sample parses + re-encodes byte-identically; field order
  `path, id, genesis, sha256, …`.
- **Version gate:** `version = 1` rejected with the incompatibility error.
- **Self-certification:** perturbed genesis or id → `Validate` fails.
- **Pull WARN:** stub resolution returns FoundElsewhere → pull succeeds, blob
  fetched from the actual member, warning names new member + short id;
  PartialView → exit 6, nothing fetched for that entry; left-home → WARN text
  per D28.
- **Heal:** moved entry rewritten to the new location, id/genesis/sha
  untouched; PartialView entry untouched + reported; `--dry-run` writes
  nothing; exit codes 0/6/5 per outcome mix.
- **Merge driver:** two-sided lock merge with v2 fields produces a valid
  canonical v2 lock.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Every federated lock entry embeds a self-certifying id + genesis record;
  every clone is thereby an identity backup.
- Pull succeeds-with-WARN on moved/foreign entries and keeps hard-failing on
  partial views and missing blobs.
- `heal` rewrites stale locations from live resolution results, never touches
  identity fields, and refuses to act under partial views.
- v1 locks are rejected outright (D29 — no tolerance machinery).

## Related Proposal Sections

> **Moves** — files move between/within locations; the next git sync resolves
> the new home through the logical layer (pull warns; `heal` rewrites the
> lock).

> (a) lock entries referencing federated files embed the full genesis record,
> making every repo clone an off-node identity backup;

> **No migration path needed** — no real Blocks 1–2 vaults exist; … Lock
> schema v2 (id+genesis) can land without v1-tolerance machinery. (D29)

## Notes & Considerations

- **Gotcha:** heal under a partial view must do *nothing* to the affected
  entries — rewriting a location based on incomplete answers is exactly the
  silent-corruption class this system exists to prevent.
- **Gotcha:** the WARN path still verifies the fetched blob's sha256 against
  the lock entry — "found elsewhere" never relaxes integrity.
- **For Next Task:** Task 36's gc consumes v2 locks' `ReferencedSHAs` exactly
  as before — confirm the helper ignores the new fields cleanly.
- Log any healing edge case (e.g. two members both claiming a file) in
  `EDGE-CASES.md`.
- **Prev:** [task-34-vault-scan](./task-34-vault-scan.md) ·
  **Next:** [task-36-gc-federation](./task-36-gc-federation.md)
