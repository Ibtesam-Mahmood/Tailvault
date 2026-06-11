# Task 45: `vault rm` + Sync-Mode Management

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "GC under federation" (manual files die only by explicit action), "Ingestion" (sync-mode enum), "Per-node WAL", "Security & transport" · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 29 (`internal/wal`), Task 28 (`internal/catalog`), Task 32 (resolution), Task 36 (federated gc — must respect mode flips), Task 43 (gate + WAL command patterns), Task 46 (auth gate) · **Type:** Implementation

## Summary

`vault rm` is the **only** way a `sync_mode = manual` file dies (D14): gc never
considers manual files, scan never deletes them, so explicit removal needs a
first-class, deliberate command. `tailvault vault rm <logical-path | id>`
resolves the file's home, runs the full WAL lifecycle (dry-run → intent →
delete blob → remove catalog entry → done), and requires interactive
confirmation (`--yes` for scripts). Removing a `git`-mode file is allowed but
gets an extra warning — git-side pointers/locks referencing it will hard-fail
on next pull until repushed — because deleting bytes out from under a repo is
exactly the kind of thing that must be loud.

The sync-mode management command flips a file between modes remotely:
`tailvault vault sync-mode <logical-path | id> <mode>`. Day-one modes are
`git | manual` (D15 — the enum is extensible; the catalog schema already
reserves room for `s3`, `watch`, …, and the command validates against the
catalog's known set rather than a hardcoded pair). Flipping `git → manual`
shields a file from gc forever; `manual → git` makes it a gc candidate again
and stamps a fresh scan (the content hash must be current before gc may ever
reason about it).

Both commands mutate a node, so both are **password-gated** (D9) and ride the
same WAL-as-lock semantics as put/mv: a pending intent on the blob blocks
concurrent ops, gc skips in-flight blobs (D13), and failures surface as pending
ops in `tailvault ops`.

## Context

### Related packages

- `cmd/tailvault` — **created here:** `vault rm` and `vault sync-mode`
  subcommands.
- `internal/wal` (Task 29) — intent lifecycle, per-blob blocking.
- `internal/catalog` (Task 28) — entry removal / `sync_mode` field update;
  owns the valid-mode set.
- `internal/resolve` (Task 32) — target lookup (path or ID), `moved_to`
  awareness (rm of a moved file targets the **new** home).
- `internal/gc` (Task 36) — consumer of the mode flag; no changes expected,
  but its invariants are re-asserted in tests here.
- `internal/auth` (Task 46) — gate.

### Prerequisites

- [ ] Tasks 27–32, 36, 43 merged.
- [ ] SPEC v2 confirms the extensible sync-mode enum encoding and whether `rm`
  leaves a tombstone/WAL trace beyond the done-entry (it should: the WAL entry
  *is* the audit trail; the catalog entry is gone).

## Changes Required

### cmd/tailvault/vault_rm.go

- **File:** `cmd/tailvault/vault_rm.go`
- **Action:** create
- **Purpose:** explicit delete.

```go
// tailvault vault rm <logical-path | id>
// flags: --yes (skip confirm), --json
func runVaultRm(cmd *cobra.Command, args []string) error {
	// resolve target -> home node (follow moved_to to the live home)
	// auth.Gate(home); confirm (TTY) unless --yes; non-TTY without --yes -> error
	// git-mode target -> extra WARN about referencing repos
	// dry-run -> wal intent -> backend.Delete(objects/<sha>) -> catalog remove
	//   -> wal done
}
```

Implementation Notes:

- **Resolution first:** `rm` by old path of a moved file must follow the
  forwarder and delete at the live home — deleting a `moved_to` stub while the
  real bytes live elsewhere would be a lie. Deleting the *forwarder itself*
  is not exposed (journal gc owns stub cleanup).
- **Partial view:** if the home is unreachable → TV-NODE; if resolution cannot
  prove where the file is (partial view) → TV-FED hard-fail. Deletes never
  tolerate ambiguity.
- **Blob sharing:** content-addressed storage means another catalog entry on
  the same node may reference the same sha — `Delete` the blob only when the
  dry-run confirms this entry is the last referent on that node; otherwise
  remove only the catalog entry.
- WAL done-entry carries what was deleted (id, genesis, sha) — the last trace
  of the identity; mention in output that a pull receipt, if any exists, still
  allows `restore-identity` after a re-ingest.

### cmd/tailvault/vault_syncmode.go

- **File:** `cmd/tailvault/vault_syncmode.go`
- **Action:** create
- **Purpose:** remote sync-mode flip.

```go
// tailvault vault sync-mode <logical-path | id> <git|manual|...>
// flags: --json
func runVaultSyncMode(cmd *cobra.Command, args []string) error {
	// resolve -> home; auth.Gate(home)
	// catalog.ValidModes() drives validation (extensible enum, D15)
	// wal intent -> re-hash if flipping to "git" (fresh sha + last_scanned)
	//   -> catalog update sync_mode -> wal done
}
```

Implementation Notes:

- `manual → git`: run a node-side re-hash (`HashObject`) and stamp
  `last_scanned` in the same op — gc and verify must never reason from a stale
  sha on a freshly-git file.
- `git → manual`: no re-hash needed; print that the file is now exempt from gc
  and editable in place.
- Unknown mode → config-class error listing `catalog.ValidModes()`; setting the
  current mode is a no-op success (idempotent).

## Implementation Checklist

- [ ] `vault rm` with confirm / `--yes`; git-mode extra warning.
- [ ] Forwarder-following deletion; last-referent blob check.
- [ ] `vault sync-mode` with extensible-mode validation; re-hash on → `git`.
- [ ] Full WAL lifecycle + auth gate on both commands.
- [ ] TV-FED ambiguity hard-fail on rm; idempotent retries via op id.

## Testing Requirements

Against the Task 39 harness (stub backends only):

- **rm happy path:** manual file → blob gone, catalog entry gone, WAL
  intent+done recorded with id/genesis.
- **rm shared blob:** two entries, same sha → first rm keeps the blob, second
  removes it.
- **rm moved file:** rm by pre-move path deletes at the new home; forwarder
  handling asserted.
- **rm ambiguity:** home unreachable → hard-fail, nothing deleted.
- **rm git file:** warning emitted; subsequent stubbed pull hits TV-OBJ (the
  loud failure is the point).
- **sync-mode flips:** manual→git re-hashes + stamps `last_scanned`; git→manual
  makes a later `gc` run skip the blob (gc invariant re-asserted); unknown mode
  rejected; same-mode no-op.
- **Auth + confirm:** gate rejection and non-TTY-without-`--yes` both leave the
  node untouched.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- `vault rm` is the only deletion path for manual files and never deletes under
  ambiguity (partial view) or shared-blob conditions.
- `vault sync-mode` flips modes remotely with an extensible enum; gc behavior
  follows the flip (proven by test).
- Both commands are password-gated, WAL-locked, idempotent on retry, and leave
  audit trails in the WAL.

## Related Proposal Sections

> Only `sync_mode = git` objects are ever GC candidates — manual files are
> deleted solely by explicit user action.

> Sync-mode enum extensible: day-one `git | manual`, schema reserves room
> (`s3`, `watch`, …).

> Mutating remote ops (mv, rm, sync-mode change, remote gc, evict) require a
> **per-node password** …

## Notes & Considerations

- **Gotcha:** the last-referent check and the delete must happen under the same
  WAL intent — a put of the same sha racing between check and delete is exactly
  what WAL-as-lock exists to serialize.
- **Gotcha:** do not print the deleted genesis record to stdout by default
  (filename privacy, Block 5); `--json` carries it for tooling.
- **For Next Task:** Task 46 replaces the stub auth gate these commands have
  been calling with the real argon2id implementation.
- **Prev:** [task-44-vault-mv](./task-44-vault-mv.md) ·
  **Next:** [task-46-vault-passwd-auth](./task-46-vault-passwd-auth.md)
