# Task 42: `vault get` — Download + Pull Receipt

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "File identity — genesis-hash IDs" (identity recovery / pull receipts), "Resolution & reachability", "CLI surface (v2 additions)" · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 30 (`internal/identity` genesis records + receipt format), Task 32 (resolution engine), Task 28 (`internal/catalog`), Task 40 (`HashObject`), Task 41 (shared path-or-ID target parsing) · **Type:** Implementation

## Summary

`vault get` downloads a federated file to the local machine by **logical path
or file ID**, from any node, with no repo checkout. Resolution goes through the
Block 3 engine: found at the recorded home → download; found at another member →
download + WARN (heal available); not found with a member unreachable → TV-FED
partial-view hard-fail; not found with everyone answering → TV-OBJ missing.
Reads ride tailnet ACL + SSH — no password (D9).

Every successful `get` does two extra things. First, an **integrity check**:
the streamed bytes are hashed locally and compared against the catalog's
recorded sha256. For `sync_mode = git` files a mismatch is a hard integrity
failure; for `manual` files (editable in place, H12) the command instead reports
**hash freshness** — `verified against scan of <last_scanned>` when matching, or
a clear `content has changed since last scan <last_scanned> — run vault scan on
the node` notice when not, because drift on a manual file is legitimate, not
corruption.

Second, a **pull receipt** is written to `~/.tailvault/receipts/<id>.toml`
containing the file's full **genesis record** (D24). Because the genesis-hash ID
is self-certifying (`id = sha256(genesis record)`), every receipt is an off-node
identity backup: `vault restore-identity` (Task 48) can re-seed a rebuilt
catalog from it. Receipts are written atomically and idempotently overwritten on
re-download.

## Context

### Related packages

- `cmd/tailvault` — **created here:** `vault get` subcommand.
- `internal/identity` (Task 30) — genesis record struct, receipt
  encode/decode (`~/.tailvault/receipts/<id>.toml`, SPEC v2 format), ID verify.
- `internal/resolve` (Task 32) — lookup + moved_to forwarding + TV-FED errors.
- `internal/backend` (Tasks 09/40) — streaming `Get`; `HashObject` for a
  cheap pre-check of large blobs.
- `internal/catalog` (Task 28) — recorded sha256, `sync_mode`, `last_scanned`.

### Prerequisites

- [ ] Tasks 27–32 merged; receipt schema frozen in SPEC v2 (Task 27).
- [ ] Task 41 merged (shared target parsing helper).

## Changes Required

### cmd/tailvault/vault_get.go

- **File:** `cmd/tailvault/vault_get.go`
- **Action:** create
- **Purpose:** the command.

```go
// tailvault vault get <logical-path | id> [-o <local-path>]
// flags: -o/--output (default: basename into cwd), --force (overwrite local),
//        --no-receipt (debugging escape hatch), --json
func runVaultGet(cmd *cobra.Command, args []string) error {
	// 1. target := parseTarget(args[0])              // shared with Task 41
	// 2. rec, home, reach, err := resolve.Lookup(...) // TV-FED / TV-OBJ at boundary
	// 3. if found off-home: WARN "found at <member>; run `tailvault heal`"
	// 4. stream backend.Get(objects/<sha>) -> local *.tmp, hashing as it flows
	// 5. compare digest vs rec.SHA256 (git: hard-fail; manual: freshness report)
	// 6. atomic rename tmp -> destination
	// 7. identity.WriteReceipt(home receiptsDir, rec.Genesis)  // atomic
}
```

Implementation Notes:

- **Stream, never buffer:** hash with an `io.TeeReader` into the temp file —
  one pass, constant memory, fine at ~1 GB.
- **git-mode mismatch** → wrap as `tserr.ObjMissing`-family integrity error
  (`TV-OBJ`, exit 5) at the command boundary per SPEC §8; delete the temp file —
  a corrupt download must never land at the destination (hard-fail,
  never-silent-success).
- **manual-mode mismatch** is *not* an error: exit 0, file delivered, loud
  freshness notice with `last_scanned`. The downloaded bytes are what the node
  actually holds — that is the truth being fetched.
- **Local overwrite:** refuse to clobber an existing destination without
  `--force` (cheap local conflict guard; the put-side conflict machinery in
  Task 43 is for vault-side names).
- **Receipt:** `identity.WriteReceipt` (Task 30) handles temp+rename; the
  receipt embeds the full genesis record plus retrieval metadata (home, sha at
  download, timestamp) per SPEC v2. `--no-receipt` skips it but prints that it
  did.
- Optional fast pre-check: `HashObject` on the home before streaming lets a
  git-mode corrupt blob fail in milliseconds instead of after a 1 GB transfer.

### internal/identity (consumed, possibly extended)

- **File:** `internal/identity/receipt.go`
- **Action:** modify only if Task 30 left retrieval-metadata fields out
- **Purpose:** keep the receipt format owned by one package; `vault get` calls
  it, never hand-writes TOML.

## Implementation Checklist

- [ ] `vault get` by logical path and by ID prefix; `-o`, `--force`,
  `--no-receipt`, `--json`.
- [ ] Streaming download to temp + atomic rename; tee-hash integrity check.
- [ ] git-mode mismatch → integrity hard-fail, temp removed.
- [ ] manual-mode → freshness report wired to `last_scanned`.
- [ ] Off-home find → success + heal WARN; partial view → TV-FED exit 6.
- [ ] Pull receipt written atomically to `~/.tailvault/receipts/<id>.toml`.
- [ ] No password on any path (read-only op).

## Testing Requirements

Against the Task 39 harness (stub backends only):

- **Round-trip:** put bytes in a stub member's store + catalog → `get` by path
  and by short ID → bytes identical, receipt exists, receipt's genesis record
  re-hashes to the ID (self-certification asserted).
- **Corrupt git blob:** flip a byte in the stub object → `get` exits 5, no
  destination file, no stray temp file.
- **Edited manual file:** drift bytes after catalog write → exit 0 + freshness
  notice mentioning `last_scanned`.
- **Moved:** entry whose `moved_to` points elsewhere → fetched from new home,
  WARN emitted; new home down → TV-FED partial-view.
- **Local overwrite:** existing destination without `--force` → refused.
- **Receipt idempotence:** second `get` rewrites the receipt without error.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- `vault get <path|id>` delivers verified bytes with no repo checkout and no
  password.
- Every successful get leaves a valid, self-certifying receipt at
  `~/.tailvault/receipts/<id>.toml` that Task 48 can restore from.
- git/manual mismatch semantics differ exactly as specified (hard-fail vs
  freshness notice).
- Resolution semantics match the frozen table (home / off-home WARN / TV-FED /
  TV-OBJ).

## Related Proposal Sections

> every `vault get` writes a pull receipt (`~/.tailvault/receipts/<id>.toml`)
> with the genesis record; manual `vault restore-identity` … re-seeds a rebuilt
> catalog with the original ID.

> The ID is **not** the content hash: manual files are editable in place, so
> content sha drifts until a scan re-hashes … remote `get` of a manual file
> should report hash freshness.

> Found at recorded home → success; found at a different member → success +
> WARN (run `heal`) …

## Notes & Considerations

- **Gotcha:** write the receipt only **after** the destination rename succeeds —
  a receipt for a download that failed mid-rename would imply possession the
  user does not have.
- **Gotcha:** `~/.tailvault/receipts/` may not exist — create with `0700`
  (receipts leak filenames; Block 5's privacy audit covers this, don't make it
  worse).
- **For Next Task:** Task 43 (`vault put`) is the inverse flow and mints the
  genesis records these receipts carry.
- **Prev:** [task-41-vault-ls-stat](./task-41-vault-ls-stat.md) ·
  **Next:** [task-43-vault-put](./task-43-vault-put.md)
