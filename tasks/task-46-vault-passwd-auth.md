# Task 46: `vault passwd` + argon2id Auth Enforcement

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Security & transport" (D9 per-node password, argon2id, reads exempt); "Part II task breakdown" 4.7 · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 27 (SPEC v2: password hash file format + auth error code), Task 29 (`internal/wal` — passwd change is itself a WAL op), Task 43 (the `auth.Gate` seam + first call sites) · **Type:** Implementation

## Summary

Mutating remote operations — `vault put/mv/rm`, `vault sync-mode`, `fed evict`,
remote `gc` — require a **per-node password** (D9). The password may be the same
across nodes (user's choice), but each node stores and checks its **own**
secret: an **argon2id hash** in the node's vault metadata (file path + encoding
frozen by SPEC v2, e.g. `<base_path>/meta/auth.toml` with parameters + salt +
hash). There is **no recovery flow**: a forgotten password is reset only by SSH
or physical access to the node (delete/rewrite the hash file). Reads
(`ls/stat/get`, `fed status`) are exempt — they ride tailnet ACL + Tailscale SSH
alone.

Verification happens **on the node**, never on the client: the client invokes a
node-side verifier over SSH (a hidden `tailvault node verify-passwd` subcommand
reading the password from stdin), so the stored hash never leaves the node and
the candidate password transits only the WireGuard+SSH channel. We never roll
our own crypto or transport (D8): argon2id comes from `golang.org/x/crypto/
argon2` (an accepted dependency), randomness from `crypto/rand`, comparison via
`crypto/subtle`, and the channel security is entirely Tailscale's.

This task ships `tailvault vault passwd <location>` (set/change the node's
password — itself a mutating, WAL-logged op gated on the *old* password when one
exists), the `internal/auth` package that replaces the stub gate Tasks 43–45
have been calling, and the enforcement audit: every mutating remote code path
calls `auth.Gate`, every read path provably does not.

## Context

### Related packages

- `internal/auth` — **completed here** (seam introduced in Task 43): hash
  file encode/decode, argon2id derive/verify, client `Gate`, node-side
  verifier.
- `cmd/tailvault` — `vault passwd` subcommand + hidden `node verify-passwd`.
- `internal/wal` (Task 29) — passwd-change op lifecycle.
- `internal/tserr` (Task 07) — gains the SPEC v2 auth code (e.g.
  `TV-AUTH-01 — password required/incorrect`) + its exit bucket.
- Tasks 43/44/45/47, Task 36 (remote gc) — call sites audited.

### Prerequisites

- [ ] SPEC v2 (Task 27) frozen: hash file path/format, argon2id parameters
  (time/memory/threads — SPEC-pinned so all nodes agree), auth error code +
  exit bucket.
- [ ] Task 43 merged (the `auth.Gate` call-site seam exists).
- [ ] Decision recorded: nodes that host vaults have the `tailvault` binary
  installed (required for node-side verification; already true for any node
  initialized via `vault init`).

## Changes Required

### internal/auth/auth.go

- **File:** `internal/auth/auth.go`
- **Action:** create (replacing the Task 43 stub internals)
- **Purpose:** the whole auth surface.

```go
package auth

// HashFile is the parsed node-side secret (SPEC v2 format).
type HashFile struct {
	Version int
	Time    uint32 // argon2id params — SPEC-pinned defaults
	MemoryKB uint32
	Threads uint8
	Salt    []byte
	Hash    []byte
}

func Derive(password []byte, salt []byte, p Params) []byte // x/crypto/argon2.IDKey
func Verify(hf HashFile, password []byte) bool             // subtle.ConstantTimeCompare
func WriteHashFile(path string, hf HashFile) error          // 0600, temp+rename
func LoadHashFile(path string) (HashFile, bool, error)      // ok=false: no password set

// Gate prompts (or reads TAILVAULT_PASSWORD / --password-file) and runs the
// node-side verifier over the backend's SSH channel. Plain error up; the
// command boundary wraps the SPEC v2 auth code per SPEC §8.
func Gate(ctx context.Context, node NodeSpec) error
```

Implementation Notes:

- **No password set** on a node = mutations are **refused** with a clear
  "no vault password set — run `tailvault vault passwd <location>`" error.
  Defaulting to open would silently weaken D9.
- Password input: TTY prompt with no echo (`golang.org/x/term`, transitively
  acceptable via x/crypto's module family — confirm; otherwise read raw);
  `TAILVAULT_PASSWORD` env and `--password-file` for scripts/tests. Never a
  bare `--password` argv flag (visible in `ps`).
- Zero password bytes in memory after use (best effort); never log them.
- The harness/stub path: `internal/auth` exposes a `Verifier` seam so the
  Task 39 stub backend verifies against an in-memory hash file — Task 50's
  auth-rejection tests need no SSH.

### cmd/tailvault/vault_passwd.go

- **File:** `cmd/tailvault/vault_passwd.go`
- **Action:** create
- **Purpose:** `tailvault vault passwd <location>` — set or change.

```go
// First set: prompt new password twice -> Derive -> write hash file via SSH
//   (temp+rename, 0600), WAL intent/done around it.
// Change: Gate with the OLD password first, then as above.
```

### cmd/tailvault/node_verify.go

- **File:** `cmd/tailvault/node_verify.go`
- **Action:** create
- **Purpose:** hidden `tailvault node verify-passwd --vault <base_path>` —
  runs **on the node** via SSH, reads the candidate from stdin, loads the local
  hash file, exits 0/non-zero. The hash never leaves the node.

### Enforcement audit (modifications)

- **Files:** `cmd/tailvault/vault_put.go`, `vault_mv.go`, `vault_rm.go`,
  `vault_syncmode.go`, the remote-gc path (Task 36), `fed evict` (Task 47 when
  it lands).
- **Action:** modify/verify
- **Purpose:** every mutating remote op calls `auth.Gate` **before** its WAL
  intent; `ls/stat/get/scan-read/fed status` provably never import the prompt
  path. Add a package-level test that walks the command tree asserting the
  gated set matches the SPEC v2 list exactly.

## Implementation Checklist

- [ ] argon2id derive/verify with SPEC-pinned params; constant-time compare.
- [ ] Hash file load/write, `0600`, temp+rename, atomic.
- [ ] `vault passwd` set + change (change gated on old password), WAL-logged.
- [ ] Node-side `node verify-passwd` over stdin; hash never leaves the node.
- [ ] `Gate` with TTY prompt / env / `--password-file`; no argv password.
- [ ] No-password-set ⇒ mutations refused with actionable error.
- [ ] Auth error code + exit bucket wired into `tserr` per SPEC v2.
- [ ] Enforcement audit test (gated set == SPEC v2 list; reads exempt).

## Testing Requirements

`internal/auth/*_test.go` + harness tests:

- **Round-trip:** Derive→Verify accepts the right password, rejects a wrong
  one and a truncated hash file; params honored.
- **Hash file:** write→load round-trip; permission bits `0600`; corrupt file →
  clean error, never a false accept.
- **passwd flows:** first set; change with correct old password; change with
  wrong old password rejected, file unchanged.
- **Gate sources:** env var and `--password-file` paths; non-TTY with neither →
  hard-fail before any network mutation.
- **Enforcement:** harness run of each mutating command with wrong/absent
  password → auth error, zero WAL entries, zero bytes moved; `ls/stat/get`
  succeed with no password configured anywhere.
- **No-recovery doc check:** the error text for a wrong password mentions the
  SSH/physical reset path.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Every mutating remote op is gated; every read op is not — proven by the
  audit test, not by convention.
- Verification runs on the node; the hash file never transits the network;
  the password transits only the SSH channel.
- argon2id via `golang.org/x/crypto` with SPEC-pinned parameters; no
  hand-rolled crypto anywhere in the diff.
- No recovery path exists; reset requires node access and is documented in the
  error text.

## Related Proposal Sections

> Mutating remote ops (mv, rm, sync-mode change, remote gc, evict) require a
> **per-node password** (may be identical across nodes), stored as an
> **argon2id hash** on the node (no recovery — reset requires SSH/physical
> access). Reads ride tailnet ACL + SSH alone.

> **Reuse built primitives only** — never roll our own crypto/transport.
> Tailscale WireGuard + SSH provide encryption, identity (whois), key exchange.

## Notes & Considerations

- **Gotcha:** argon2id parameters must come from the **hash file** on verify
  (not the client's defaults), or a future param bump breaks old nodes; pin
  defaults in SPEC v2 but always verify with stored params.
- **Gotcha:** the node-side verifier requires the `tailvault` binary on the
  node — fail with a precise "tailvault not installed on node" error, never
  fall back to client-side verification (that would ship the hash to the
  client).
- **Gotcha:** Block 5 will adversarially review this exact surface
  (perms, timing, prompt handling) — keep the package small and boring.
- **For Next Task:** Task 47 (`fed` membership) gates `evict` through this
  package.
- **Prev:** [task-45-vault-rm-syncmode](./task-45-vault-rm-syncmode.md) ·
  **Next:** [task-47-fed-membership-cli](./task-47-fed-membership-cli.md)
