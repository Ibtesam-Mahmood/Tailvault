# Task 48: `vault restore-identity` — Manual Identity Recovery

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "File identity — genesis-hash IDs" (Identity recovery, D24/D25), "Part II task breakdown" 4.9 · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 0.5 ideal eng-day · **Dependencies:** Task 30 (`internal/identity` — genesis records, ID verify, receipt format), Task 28 (`internal/catalog`), Task 29 (`internal/wal` — restore is a logged op), Task 35 (lock v2 — locks embed genesis records, the other restore source), Task 42 (`vault get` writes the receipts this restores from) · **Type:** Implementation

## Summary

If a node's catalog + WAL are rebuilt after disk loss, re-ingesting its files
would mint **new** IDs — severing every lock entry, receipt, and reference that
pointed at the old ones. The escape hatch is that genesis-hash IDs are
**self-certifying**: `id = sha256(genesis record)`, so any surviving copy of a
genesis record proves itself. D24 replicated those records into two off-node
places — every repo clone's `tailvault.lock` v2 entries and every pull receipt
under `~/.tailvault/receipts/<id>.toml`.

`tailvault vault restore-identity` is the **manual, never implicit** recovery
command: it accepts a genesis record from a receipt file (`--receipt <path>`),
a lock entry (`--lock <tailvault.lock> --path <repo-path>`), or raw record TOML
(`--record <file>`), verifies `sha256(canonical record) == id` (rejecting
anything that does not self-certify), confirms the rebuilt catalog holds a file
whose current content/path the user is re-identifying, and re-seeds that
catalog entry with the **original ID and genesis record** through a normal WAL
op. Identity resurrection is a deliberate, audited act — no scan, heal, or
ingest path ever does this automatically.

The residual risk — a never-referenced file on a destroyed node has no
surviving record anywhere — is accepted (D25) and closed later by GH-3
redundancy; this command's output says so when the user has nothing to restore
from.

## Context

### Related packages

- `cmd/tailvault` — **created here:** `vault restore-identity` subcommand.
- `internal/identity` (Task 30) — canonical record encoding,
  `VerifyID(record) (id, error)`, receipt decode.
- `internal/lock` (Task 35) — lock v2 entries embed `id` + genesis record;
  extraction helper.
- `internal/catalog` (Task 28) — the re-seed target (atomic update).
- `internal/wal` (Task 29) — `restore-identity` intent/done entries (the
  audit trail of the resurrection).
- `internal/auth` (Task 46) — restore mutates the node's catalog → gated.

### Prerequisites

- [ ] Tasks 27–30, 35, 42 merged.
- [ ] SPEC v2 confirms the canonical genesis-record byte encoding (the entire
  command rests on byte-exact re-hashing).

## Changes Required

### cmd/tailvault/vault_restore_identity.go

- **File:** `cmd/tailvault/vault_restore_identity.go`
- **Action:** create
- **Purpose:** the command.

```go
// tailvault vault restore-identity <location>/<current-path> \
//     (--receipt <file> | --lock <lockfile> --path <repo-path> | --record <file>)
// flags: --json
func runVaultRestoreIdentity(cmd *cobra.Command, args []string) error {
	// 1. load genesis record from exactly one source flag
	// 2. id, err := identity.VerifyID(record)   // sha256(canonical) must match
	//    -> mismatch: hard-fail "record does not self-certify"
	// 3. resolve <location>/<current-path> in the (rebuilt) catalog; the entry
	//    must exist and currently carry a DIFFERENT (re-minted) id
	// 4. show old vs original id + record summary; confirm (TTY) unless --yes
	// 5. auth.Gate(node); wal intent -> catalog: swap id+genesis to the
	//    original, keep current sha/path/sync_mode -> wal done
}
```

Implementation Notes:

- **Exactly one source:** `--receipt` / `--lock`+`--path` / `--record` are
  mutually exclusive; zero or two+ → usage error. Receipts and locks both
  decode via the owning packages (Tasks 30/35) — this command never hand-parses
  TOML.
- **Self-certification is the gate, not provenance:** a record from anywhere
  is acceptable *iff* it hashes to its claimed id. When the source carries the
  expected id (receipts are named `<id>.toml`; lock entries carry `id`),
  cross-check against the recomputed value and report both on mismatch.
- **Content sanity check (advisory, not blocking):** if the record's original
  content sha256 matches neither the entry's current sha nor any version on
  the node, WARN — the user may be re-identifying the wrong file (manual files
  legitimately drift, so this cannot hard-fail; H12).
- **Collision guard:** if another entry in the federation already carries the
  original id (fan-out check via Task 32), hard-fail — restoring would create
  two live claims to one identity.
- **Never implicit:** no other command may call this code path; keep the
  re-seed function unexported in `cmd` or guarded in `catalog` so scan/heal
  cannot reach it accidentally.
- SPEC §8: identity/lock/catalog return plain errors; boundary wraps tserr.

### internal/identity (consumed, possibly extended)

- **File:** `internal/identity/verify.go`
- **Action:** modify only if Task 30 lacks a single-call
  `VerifyID(record) (string, error)`
- **Purpose:** one canonical verify entry point shared with Task 50's tests.

## Implementation Checklist

- [ ] Three mutually-exclusive record sources; decode via owning packages.
- [ ] `sha256(canonical record) == id` verification; mismatch hard-fail.
- [ ] Advisory content-sha sanity WARN; federation-wide id collision
  hard-fail.
- [ ] Confirmation prompt + `--yes`; auth gate; WAL intent/done audit trail.
- [ ] Catalog entry keeps current sha/path/sync_mode, regains original
  id+genesis.
- [ ] Output documents the GH-3 residual-risk case when nothing can be
  restored.

## Testing Requirements

Against the Task 39 harness (stub backends only):

- **Receipt round-trip:** `put` → `get` (receipt written) → destroy + rebuild
  the stub catalog (re-mint ids) → `restore-identity --receipt` → entry carries
  the original id + genesis; WAL shows the restore op.
- **Lock round-trip:** same flow sourcing from a lock-v2 fixture entry.
- **Tampered record:** flip one byte of the record file → "does not
  self-certify", catalog untouched.
- **Wrong target:** record whose original sha matches nothing → WARN emitted,
  restore still possible; non-existent target path → clean error.
- **Collision:** original id still live on another member → hard-fail.
- **Auth + confirm:** wrong password / non-TTY without `--yes` → no mutation.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A catalog rebuilt from nothing regains original ids from either a pull
  receipt or a lock entry, end-to-end on the harness.
- Only records that self-certify are ever accepted; restoration is manual,
  confirmed, password-gated, and WAL-audited.
- An id can never be restored into two live entries.

## Related Proposal Sections

> **Identity recovery** (self-certifying: a record hashing to the claimed id
> proves itself): lock entries embed the full genesis record (every repo clone
> = off-node identity backup); every `vault get` writes a pull receipt …;
> manual `vault restore-identity` verifies sha256(record)==id and re-seeds a
> rebuilt catalog with the original ID. Never implicit.

> Residual risk (never-referenced file on a destroyed node) accepted — closed
> later by redundancy (GH issue).

## Notes & Considerations

- **Gotcha:** canonical encoding drift is the silent killer here — the test
  suite must include a frozen byte-fixture genesis record + its known id, so
  any future encoder change fails loudly instead of stranding old receipts.
- **Gotcha:** receipts are user-owned files; treat their contents as untrusted
  input (Block 5 will fuzz this parser — keep decode strict).
- **For Next Task:** Task 49 (`track` manual ingest) is the path that *mints*
  identities for hand-dropped files; together with this command it closes the
  identity lifecycle.
- **Prev:** [task-47-fed-membership-cli](./task-47-fed-membership-cli.md) ·
  **Next:** [task-49-track-manual-ingest](./task-49-track-manual-ingest.md)
