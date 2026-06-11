# Task 43: `vault put` — Remote Ingest

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Ingestion — three paths for non-git files" (path 3: Push), "Per-node WAL", "Security & transport" · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 29 (`internal/wal` intent lifecycle), Task 30 (`internal/identity` genesis mint), Task 28 (`internal/catalog`), Task 32 (resolution, for conflict detection), Task 40 (`HashObject`), Task 46 (auth gate — `auth.Gate` seam; see Notes for landing order) · **Type:** Implementation

## Summary

`vault put` is ingestion path 3: send a local file to a chosen path inside an
**active** (reachable) storage location, from any machine on the tailnet, no
repo involved. `tailvault vault put ./demo.mp4 home-pi/media/demo.mp4` hashes
the local file, runs the full WAL lifecycle on the destination node (dry-run
preflight → append **intent** → receipt → stream bytes → catalog update → mark
**done**), and mints the file's **genesis record** — `{original content sha256,
original relative path, ingest op id, origin node}` — whose sha256 becomes the
permanent file ID. Default `sync_mode = manual` (D21); `git` is only ever set by
the git-repo flow.

If the destination logical path already exists, the command prompts:
**copy** (keep both — vault file untouched, local file stored under a
deduplicated name), **rename** (choose a new destination name), or **stop**
(abort, nothing written). For scripts, `--on-conflict=copy|rename|stop` (D20)
answers non-interactively; an unanswerable prompt in a non-TTY context without
the flag is a hard-fail, never a guess.

Ownership semantics are explicit (D18.3): after a successful put **the vault
copy is the original** — the local source file is now a deletable clone. The
command prints exactly that, and `--rm-source` optionally deletes the local file
after the post-put verification passes. `put` mutates a remote node, so it is
**password-gated** (D9) via the auth seam delivered in Task 46.

## Context

### Related packages

- `cmd/tailvault` — **created here:** `vault put` subcommand.
- `internal/wal` (Task 29) — intent append (= per-blob lock), receipt, done.
- `internal/identity` (Task 30) — genesis record mint + ID derivation.
- `internal/catalog` (Task 28) — atomic catalog update on the destination.
- `internal/backend` (Tasks 09/40) — streaming `Put`, `HashObject` post-check.
- `internal/auth` (Task 46) — `Gate(ctx, node)` password verification.

### Prerequisites

- [ ] Tasks 27–32 merged; Task 40 merged.
- [ ] SPEC v2 genesis-record canonical encoding confirmed (the ID depends on
  byte-exact serialization — never re-derive informally).

## Changes Required

### cmd/tailvault/vault_put.go

- **File:** `cmd/tailvault/vault_put.go`
- **Action:** create
- **Purpose:** the command.

```go
// tailvault vault put <local-file> <location>/<dest-path>
// flags: --on-conflict=copy|rename|stop, --rm-source, --json
func runVaultPut(cmd *cobra.Command, args []string) error {
	// 1. local sha256 + size (streaming)
	// 2. preflight: node reachable (TV-NODE-01 otherwise); auth.Gate(node)
	// 3. dry-run: dest path free? base_path writable? space? -> fail early
	// 4. conflict? prompt / --on-conflict (copy|rename|stop)
	// 5. wal.AppendIntent(op id, "put", args, blob ref)   // WAL-as-lock
	// 6. backend.Put(objects/<sha>) streaming, temp+rename on node
	// 7. genesis := identity.Mint(sha, destRelPath, opID, originNode)
	// 8. catalog.Apply(add entry{id, genesis, sha, path, sync_mode:"manual"})
	// 9. wal.MarkDone(opID); HashObject post-check; report; optional --rm-source
}
```

Implementation Notes:

- **Order is load-bearing** (atomicity standards): WAL intent → blob bytes →
  catalog → WAL done. A crash anywhere leaves a detectable, repairable state
  for `verify`/`heal`; never reorder.
- **WAL-as-lock:** a pending intent on the same destination path/blob means
  another op is in flight — fail with "op in flight" (or queue per Task 29's
  semantics); first appender wins, no coordinator.
- **Idempotence:** the op id is minted client-side; a retry after a network
  drop re-presents the same op id and the WAL dedupes (re-running `put` must
  not mint a second identity for the same interrupted ingest).
- **Conflict modes:** `copy` stores under a deterministic dedup name
  (`name (2).ext` style, frozen in SPEC v2 if specified there); `rename`
  prompts (interactive only — `--on-conflict=rename` without a TTY plus no
  `--rename-to` is an error); `stop` aborts before the WAL intent.
- **Dry-run preflight (D6):** all fail-early checks happen **before** the
  intent is appended — a doomed op should never acquire the lock.
- **`--rm-source` safety:** delete only after the post-put `HashObject` equals
  the local digest. Without the flag, just print the clone notice.
- Auth failure → the SPEC v2 auth error code, exit per its bucket; reads were
  free, this is the first password-gated command to land (see Notes).
- SPEC §8: `wal`/`catalog`/`identity` return plain errors; this command wraps
  at the boundary.

## Implementation Checklist

- [ ] Streaming local hash; preflight + dry-run before any write.
- [ ] `auth.Gate` invoked before the WAL intent; no gate → no mutation.
- [ ] Conflict prompt + `--on-conflict=copy|rename|stop`; non-TTY without flag
  → hard-fail.
- [ ] WAL intent → blob → catalog → done ordering; idempotent op ids.
- [ ] Genesis record minted via `internal/identity`; `sync_mode = manual`.
- [ ] Vault-copy-is-original notice; `--rm-source` post-verify delete.
- [ ] Post-put `HashObject` verification.

## Testing Requirements

Against the Task 39 harness (stub backends only):

- **Happy path:** put → blob present, catalog entry with correct ID
  (sha256(genesis) re-derived in the test), sync_mode manual, WAL shows
  intent+done.
- **Conflict modes:** existing dest → `copy` stores both; `rename` (scripted
  via `--rename-to`) stores under new name; `stop` leaves vault byte-identical;
  non-TTY without flag → error before intent.
- **Auth:** stub gate rejection → no WAL entry, no bytes (Task 50 re-runs this
  end-to-end).
- **Crash points:** kill between intent/bytes and bytes/catalog (harness fault
  injection) → next command surfaces the pending op; retry with same op id
  completes without duplicate identity.
- **Down node:** unreachable destination → TV-NODE-01 before any write.
- **--rm-source:** local file removed only on verified success.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- `vault put` ingests with full WAL lifecycle and mints a self-certifying
  genesis ID; default sync_mode is `manual`.
- All three conflict modes work interactively and via `--on-conflict`.
- After success the CLI states the vault copy is the original; `--rm-source`
  deletes only post-verification.
- Mutation is password-gated; rejection leaves zero trace on the node.
- Interrupted put is resumable/retryable without duplicate identities.

## Related Proposal Sections

> **Push (remote ingest)**: `vault put` sends a local file to a chosen path in
> an active location; on name conflict prompt copy/rename/stop (or
> `--on-conflict=` for scripts). After push the **vault copy is the original**;
> the local source is a deletable clone.

> Every mutating op: **dry-run preflight** (fail early) → append **intent**
> record … → receipt → execute → confirm → mark done. Ops are idempotent with
> unique ids.

> Default `sync_mode = manual` for all three ingestion paths; `git` is only
> ever set by the git-repo flow.

## Notes & Considerations

- **Gotcha (landing order):** `auth.Gate` is owned by Task 46, which lands
  after this numerically. Define the seam here as a one-function interface in
  `internal/auth` with a temporary always-prompt-and-verify-stub
  implementation, and let Task 46 replace the internals — the call site and
  its tests are this task's responsibility, the argon2id machinery is not.
- **Gotcha:** the genesis record must be serialized via Task 30's canonical
  encoder before hashing — an ad-hoc `fmt.Sprintf` would mint
  unrecoverable IDs.
- **Gotcha:** conflict detection must consult the **live** destination catalog,
  not the client cache — caches are advisory, and putting over a ghost is data
  loss.
- **For Next Task:** Task 44 (`vault mv`) reuses the same WAL lifecycle on two
  nodes at once.
- **Prev:** [task-42-vault-get-receipts](./task-42-vault-get-receipts.md) ·
  **Next:** [task-44-vault-mv](./task-44-vault-mv.md)
