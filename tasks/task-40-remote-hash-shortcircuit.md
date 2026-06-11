# Task 40: Remote sha256 Short-Circuit (GH-2 / DEV-C1)

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Part II task breakdown" 4.1; Future → GH-2 (DEV-C1, promoted to Block 4 prerequisite) · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 0.5 ideal eng-day · **Dependencies:** Task 09 (`Backend` interface + SSH impl), Task 22 (taildrive backend), Task 23 (`verify`, the first consumer) · **Type:** Implementation

## Summary

Today, hashing a stored blob means streaming the whole object back over the
tailnet and running sha256 locally — at ~1 GB per blob that turns `verify` (and
every future remote read that wants an integrity check) into a full re-download.
This was accepted during Blocks 1–2 as deviation **DEV-C1** and filed as
**GH-2**; the existing code comment in `internal/backend` anticipates the fix.
Because every Block 4 remote command (`vault ls|stat|get|put|mv|rm`) needs cheap
remote integrity answers, this task is the **Block 4 prerequisite and lands
first** — it has no dependency on Block 3 and may land before or in parallel
with it.

The fix is a new `HashObject` method on the `Backend` interface: the SSH backend
runs `sha256sum` **on the node** and ships back only the 64-hex digest; the
taildrive backend hashes the file through its local mount (the bytes are already
local-ish — no remote helper exists on a passive share); the `FSBackend` stub
implements it so every downstream engine test keeps running without a real node.
`verify` (Task 23) is updated to prefer `HashObject` and only falls back to
stream-and-hash when a backend cannot answer.

Per SPEC §8 error layering, the backend returns the already-established typed
conditions at this layer (`TV-NODE-01/02`, `TV-OBJ-01` — `internal/backend` is
the historical exception that wraps `tserr` directly, established in Task 09);
no new error codes are introduced.

## Context

### Related packages

- `internal/backend` — **modified here.** `Backend` interface gains
  `HashObject`; `ssh`, `taildrive`, and `stub` impls all implement it; the
  contract test grows a hash leg.
- `internal/tserr` (Task 07) — existing codes only.
- `cmd/tailvault` `verify` (Task 23) — switched to the short-circuit.
- Tasks 41–45 — every Block 4 remote command consumes `HashObject`.

### Prerequisites

- [ ] Blocks 1–2 merged (`Backend`, ssh + taildrive + stub impls, `verify`).
- [ ] Locate the anticipating comment in `internal/backend` (DEV-C1) and remove
  it as part of this change.

## Changes Required

### internal/backend/backend.go

- **File:** `internal/backend/backend.go`
- **Action:** modify
- **Purpose:** extend the interface with the hash short-circuit.

```go
// Backend is "a path that can hold objects/ and refs/."
type Backend interface {
	Stat(ctx context.Context, key string) (Meta, error)
	Get(ctx context.Context, key string, w io.Writer) error
	Put(ctx context.Context, key string, r io.Reader) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	// HashObject returns the sha256 hex digest of the stored object without
	// streaming its bytes back to the caller. Missing key -> TV-OBJ-01.
	HashObject(ctx context.Context, key string) (string, error)
}
```

Implementation Notes:

- Extending the interface (vs an optional `Hasher` side-interface) is deliberate:
  every shipped backend can answer, and Block 4 commands must not carry a
  "maybe the backend can hash" branch. The compile break flushes any
  out-of-tree impls immediately.

### internal/backend/ssh.go

- **File:** `internal/backend/ssh.go`
- **Action:** modify
- **Purpose:** remote `sha256sum` over the existing SSH runner seam.

```go
func (s *SSH) HashObject(ctx context.Context, key string) (string, error) {
	// preflight Ping -> TV-NODE-01
	// ssh ... `sha256sum -- P` -> parse first 64 hex chars
	// missing file (exit status / stderr) -> TV-OBJ-01
}
```

Implementation Notes:

- Reuse the same exit-status/stderr classification as `Get`: unreachable →
  `TV-NODE-01`, permission → `TV-NODE-02`, no such file → `TV-OBJ-01`.
- Validate the output strictly (exactly 64 lowercase hex before the first
  space); any other shape is an error, never a silent success.
- `sha256sum` is already in the assumed POSIX helper set from Task 09.

### internal/backend/taildrive.go

- **File:** `internal/backend/taildrive.go`
- **Action:** modify
- **Purpose:** hash via the local mount with `crypto/sha256` + streaming
  `io.Copy` — the mount **is** the locality; there is nothing remote to shell
  into on a passive share.

### internal/backend/stub.go

- **File:** `internal/backend/stub.go`
- **Action:** modify
- **Purpose:** `FSBackend.HashObject` hashes the file under `Root` with the same
  semantics (missing → `TV-OBJ-01`), keeping the stub a faithful double for the
  multi-node harness (Task 39) and the Block 4 suite (Task 50).

### internal/backend/contract_test.go

- **File:** `internal/backend/contract_test.go`
- **Action:** modify
- **Purpose:** `RunContract` gains: Put bytes → `HashObject` returns the known
  digest; `HashObject` of an absent key → `TV-OBJ-01`.

### cmd/tailvault verify path

- **File:** wherever Task 23 wired `verify` (e.g. `cmd/tailvault/verify.go`)
- **Action:** modify
- **Purpose:** replace stream-and-hash with `HashObject`; remove the DEV-C1
  comment. Behavior (corrupt/missing reporting) is unchanged — only the
  transfer cost drops.

## Implementation Checklist

- [ ] `HashObject` added to the `Backend` interface.
- [ ] SSH impl via remote `sha256sum` with strict output parsing + error mapping.
- [ ] Taildrive impl via local-mount streaming hash.
- [ ] `FSBackend` impl; `RunContract` extended.
- [ ] `verify` switched to the short-circuit; DEV-C1 comment removed.

## Testing Requirements

`internal/backend/*_test.go`:

- **Contract (FSBackend):** Put known bytes → `HashObject` == precomputed digest.
- **Missing key:** `HashObject` of an absent key → `*tserr.Error` `TV-OBJ-01`.
- **SSH unit (faked runner):** well-formed `sha256sum` output parses; truncated
  / garbage output errors; permission stderr → `TV-NODE-02`; ping failure →
  `TV-NODE-01`.
- **verify:** with a counting stub, `verify` of N stored blobs performs N
  `HashObject` calls and **zero** `Get` calls.

Fixtures: `t.TempDir()`; faked exec runner. Stub-only — no real node, ever.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- All three backends implement `HashObject` and pass the extended `RunContract`.
- `verify` over N blobs streams zero blob bytes (counting-stub assertion).
- Missing key → `TV-OBJ-01`; unreachable node → `TV-NODE-01`; no new codes.
- The DEV-C1 anticipating comment is gone; GH-2 can be closed by this PR.

## Related Proposal Sections

> 4.1 Remote sha256 short-circuit (existing deviation DEV-C1 — prerequisite,
> lands first).

> **GH-2** — DEV-C1: remote sha256 short-circuit for `verify`/remote reads
> (accepted deviation; **promoted to Block 4 prerequisite**, task 4.1).

> // ssh: stream over `ssh user@node` (cat / dd / sha256sum remote-side helpers
> via stdin).

## Notes & Considerations

- **Gotcha:** `sha256sum` output format differs subtly across coreutils/busybox
  (` ` vs ` *` separator) — parse only the leading 64 hex chars, reject anything
  else.
- **Gotcha:** never fall back to silently streaming the blob when `sha256sum`
  errors on the SSH backend — that hides the node being misconfigured.
  Hard-fail, never silent success.
- **For Next Task:** Task 41 (`vault ls|stat`) and every later Block 4 command
  assume `HashObject` exists for cheap remote integrity/freshness answers.
- **Prev:** [task-39-fed-test-harness](./task-39-fed-test-harness.md) ·
  **Next:** [task-41-vault-ls-stat](./task-41-vault-ls-stat.md)
