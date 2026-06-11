# Task 52: Adversarial Review of All Auth Paths

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Security & transport" (per-node password, argon2id, reads ride tailnet ACL + SSH); "Part II task breakdown" → Block 5 ("password/perms audit … whois-spoofing assumptions … adversarial review of auth paths") · **Block:** 5 — Security analysis & hardening · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 51 (threat model — defines the boundaries under attack), Task 46 (`vault passwd` + argon2id enforcement), Task 47 (`fed evict`, the other password-gated op), Task 29 (WAL op ids / idempotence) · **Type:** Analysis

## Summary

Attack the shipped auth implementation the way a compromised tailnet device or
a buggy client would (the adversaries from task 51), **fix what breaks**, and
document the residual risks that are accepted by design. The auth model (D9) is
deliberately thin: SSH access is the outer boundary, the per-node argon2id
password gates only mutating tailvault ops, and `tailscale whois` stamps are
trusted identity — this review verifies the thin model is implemented
*correctly* and that its known limits are *written down*, not discovered later.

Unlike task 51, this task **does change code**: every confirmed weakness in the
verification flow, parameters, or permissions is fixed in this PR (or, if
large, filed as a blocking issue with the maintainer's sign-off). Hard-fail
discipline applies: an auth check that cannot complete must reject, never
fall open.

## Context

### Related packages

- `internal/backend` (task 09/40) — the SSH primitives a bypassing client
  would call directly.
- Auth enforcement + password hash file handling (task 46), `fed evict`
  (task 47), WAL op-id dedupe (task 29) — the code under attack.
- `docs/threat-model.md` (task 51) — **modified here:** findings register
  updated; residual-risks section appended.

### Prerequisites

- [ ] Task 51 merged — attack scenarios are traced to its adversary list.
- [ ] Task 50's integration harness available (multi-node stubs from task 39)
      for reproducing attacks as tests.

## Changes Required

### Review matrix — each path attacked, finding recorded, fix or accept

1. **Node-side password verification flow (task 46).** Trace every mutating
   op (`mv`, `rm`, sync-mode change, remote `gc`, `evict`): is the argon2id
   verification performed against the node's hash file for **every** op, with
   no cached "already verified" state a second op could ride? Does a missing
   or unreadable hash file **reject** (closed-fail) rather than skip the gate?
   Is the comparison constant-time (the argon2id library's verify, not a
   hand-rolled string compare — never roll our own crypto)?
2. **argon2id parameters.** Check the configured time/memory/parallelism and
   salt length against current OWASP/RFC 9106 guidance (e.g. ≥ 19 MiB memory,
   t ≥ 2, 16-byte random salt, 32-byte tag). Underpowered params are a fix,
   not a note. Confirm the salt is per-node random, never static.
3. **Hash-file permissions.** The password hash file must be `0600`, owned by
   the vault SSH user, with a `0700` parent dir; creation must apply perms
   atomically (no world-readable window between create and chmod — write with
   `O_CREAT` mode 0600 / `os.WriteFile(.., 0600)` + verify). Add a startup/
   verify-time permission check that **warns loudly** on drift.
4. **Bypass via raw backend primitives.** A client with SSH access can skip
   the CLI and run `rm`/`mv`/`dd` against `objects/` directly — no tailvault
   password stops that. **Document** (threat model residual risks) that SSH
   access itself is the outer authorization boundary (D9): the password is a
   safety interlock against accidental/scripted mutation, not a defense
   against a hostile SSH-holder. Verify nothing in the code or docs claims
   otherwise; point at task 55's SSH-hardening guide for narrowing SSH itself.
5. **Replay of WAL ops.** Can a captured intent record be re-appended to
   replay a mutation (e.g. a second `rm`)? Verify op-id uniqueness + dedupe
   (task 29) makes re-execution a no-op, and that a *new* op id replaying old
   args still passes through the password gate. Record the analysis either way.
6. **whois-spoofing assumptions.** `pusher` stamps and any auth-adjacent use
   of `tailscale whois` trust the local daemon. Confirm whois output is used
   for **attribution only**, never as an authorization input; if any code path
   branches on whois identity for access decisions, that is a finding to fix.

### docs/threat-model.md — residual risks

- **Action:** modify. Append a "Residual risks (accepted)" subsection: raw-SSH
  bypass (outer boundary by design), node-disk attacker rewrites hash file +
  WAL (tamper-evident only, → task 53), no password recovery (H8: reset =
  SSH/physical access, by design), whois trusted within the tailnet.

## Implementation Checklist

- [ ] All six attack paths executed against shipped code; finding per path.
- [ ] Verification flow closed-fails on missing/unreadable hash file.
- [ ] argon2id params meet RFC 9106 / OWASP minimums; per-node random salt.
- [ ] Hash file `0600` + owner enforced at creation; drift check added.
- [ ] Replay analysis written; op-id dedupe covered by a test.
- [ ] whois used for attribution only — verified, with a grep-able note.
- [ ] Residual risks documented in `docs/threat-model.md`; security edge cases
      appended to `EDGE-CASES.md`.

## Testing Requirements

All tests run against **stub backends / the multi-node harness (task 39)** —
no real nodes, per the stub-only rule:

- **Closed-fail:** delete/chmod-0000 the stub node's hash file → every
  mutating op rejects with the auth error code; reads unaffected.
- **No gate bypass:** each mutating op invoked without a password → rejected;
  with wrong password → rejected; correct → proceeds. (Extends task 50's
  auth cases with the negative-space variants.)
- **Replay:** re-submit an already-done op id → no second execution (WAL
  dedupe); same args under a fresh op id → password gate still enforced.
- **Perms:** hash-file creation path asserts resulting mode `0600`; the
  world-readable-window case covered by asserting the create mode, not a
  post-hoc chmod.
- **Params:** unit test pins the argon2id parameters so a future downgrade
  fails the build.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Every attack path has a written verdict: **fixed** (with the fix in this PR),
  **already safe** (with the test proving it), or **accepted residual risk**
  (documented in the threat model). No path is left unexamined.
- Auth never fails open: every error branch in verification rejects.
- argon2id parameters are pinned by test and meet published guidance.
- The raw-SSH-bypass boundary is documented in `docs/threat-model.md`, not
  merely understood.

## Related Proposal Sections

> Mutating remote ops (mv, rm, sync-mode change, remote gc, evict) require a
> **per-node password** … stored as an **argon2id hash** on the node (no
> recovery — reset requires SSH/physical access). Reads ride tailnet ACL +
> SSH alone.

> **H8** ◐ password reset = SSH/physical only (user accepted); Block 5 covers
> hash-file perms, WAL tampering (mitigated by D17 hash-chain), whois spoofing.

## Notes & Considerations

- **Gotcha:** "fix anything found" does not mean redesign — the thin D9 model
  is intentional; this task hardens its implementation, not its philosophy.
  Confirm with the maintainer before any model-level change.
- **Gotcha:** the password may legitimately be identical across nodes (D9);
  don't flag that as a finding.
- **For Next Task:** task 53 builds the tooling for the one tampering vector
  this review can only document — node-side WAL history rewrites.
- **Prev:** [task-51-threat-model](./task-51-threat-model.md) ·
  **Next:** [task-53-wal-chain-verify-tooling](./task-53-wal-chain-verify-tooling.md)
