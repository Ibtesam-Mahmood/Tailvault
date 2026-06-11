# Task 55: Privacy Audit + SSH Hardening Guide

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Security & transport"; "Part II task breakdown" → Block 5 ("SSH hardening guide; privacy audit (catalogs/receipts leak filenames)") · **Block:** 5 — Security analysis & hardening · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 51 (threat model — asset inventory this audit deepens), Task 52 (auth review — established SSH as the outer boundary the hardening guide narrows), Tasks 28/31/35/42 (catalog, caches, lock v2 genesis records, receipts — the metadata carriers under audit) · **Type:** Documentation

## Summary

tailvault's *contents* ride encrypted transports, but its *metadata* is chatty
by design: catalogs enumerate every filename and logical path on a node,
pull receipts and client caches replicate filenames onto every reading
machine, and lock-embedded genesis records put **original filenames into git
history permanently**. This task audits exactly what metadata is visible to
whom, where each copy rests, and what file permissions guard it — then closes
the loop on task 52's finding that SSH is the real outer boundary by writing
`docs/ssh-hardening.md`, the operator's guide to narrowing that boundary.

Two deliverables: a "Metadata & privacy" section added to
`docs/threat-model.md` (keeping one security document of record), and the new
`docs/ssh-hardening.md`. Permission fixes found during the audit (e.g. a
world-readable cache dir) are small and land in this PR; anything larger is
filed and routed per the threat model's findings register.

## Context

### Related packages

- `docs/threat-model.md` (task 51) — **modified here:** "Metadata & privacy"
  section appended.
- `docs/ssh-hardening.md` — **created here.**
- Audited (read, possibly perm-fixed): catalog writes (28), client caches
  `~/.tailvault/cache/fed-<id>/` (31), receipts `~/.tailvault/receipts/`
  (42), lock v2 genesis embedding (35), `locations.toml` (10), WAL entries
  (29 — op args carry paths too).

### Prerequisites

- [ ] Tasks 51–52 merged: the asset inventory and the SSH-as-boundary verdict
      are the frame this fills in.
- [ ] Read the shipped code for every write path above — the audit documents
      actual modes (`0600`? umask-dependent?), not intended ones.

## Changes Required

### docs/threat-model.md — "Metadata & privacy" section

- **Action:** modify (append section).
- **Purpose:** a visibility matrix + at-rest map.

**Visibility matrix** — rows = metadata carriers, columns = who can read:

| Carrier | Contains | Rests on | Readable by | Mode |
|---|---|---|---|---|
| Catalog | every filename, logical path, sync_mode, timestamps, IDs | node disk | anyone with SSH to the node; node-disk holder | *(audit)* |
| WAL | op args incl. paths, genesis records, pusher identity | node disk | same as catalog | *(audit)* |
| Lock v2 | genesis records: **original filename + path, origin node, pusher** | every repo clone + full git history | anyone with repo access — forever (history is immutable) | n/a (git) |
| Pull receipts | genesis record per fetched file | every reading client, `~/.tailvault/receipts/` | local users per mode | *(audit)* |
| Fed caches | roster, catalog summaries (filenames again), reachability | every reading client, `~/.tailvault/cache/fed-<id>/` | local users per mode | *(audit)* |
| `locations.toml` | node names, base paths, SSH users | client config dir | local users per mode | *(audit)* |
| Pointer files | sha256 + size + location name | repo + history | repo readers | n/a (git) |

Required call-outs:

- **The git-history leak is permanent:** lock-embedded genesis records (D24)
  trade privacy for identity recovery — renaming or removing a file later
  never removes its original name from history. Anyone given repo access
  learns the names of everything ever vaulted. State this as an accepted,
  documented trade-off; the only mitigation is repo-access control.
- **Client-side spread:** every `vault get` and every federated read leaves
  filename metadata on that client (receipts, caches). Document the cleanup
  story (what deleting `~/.tailvault/cache`/`receipts` costs: D26 caches are
  advisory; receipts are an identity backup — deleting them is safe but
  weakens recovery).
- **Permissions:** node-side catalog/WAL and client-side receipts/caches/
  `locations.toml` should be `0600` files in `0700` dirs. Audit each write
  path; fix umask-dependent modes in this PR; add the modes to the matrix.

### docs/ssh-hardening.md

- **Action:** create.
- **Purpose:** operator guidance for the outer boundary. tailvault itself
  changes nothing here (never roll our own — SSH and Tailscale own this
  layer); the guide makes the recommended posture concrete:

1. **Key-only auth:** `PasswordAuthentication no`, `PubkeyAuthentication yes`
   on the node's sshd; tailvault's per-node password is *not* an SSH
   credential and never substitutes for keys.
2. **Per-vault user:** run the vault under a dedicated low-privilege user
   owning only `base_path`; clients SSH as that user (the `user` field in
   `locations.toml`). Blast radius of a compromised client = the vault, not
   the node.
3. **Restricted commands — consideration, not a default:** a `command=`
   forced command / restricted shell whitelisting the helper commands the
   backend issues (`cat`, `dd`, `stat`, `sha256sum`, `mkdir`, `mv`, `rm`,
   `ls`/`find`, rsync server mode for task-44 moves). Document the exact
   command inventory and the trade-off honestly: it meaningfully narrows the
   raw-SSH bypass from task 52, but is brittle across tailvault upgrades —
   recommend only for hostile-ish tailnets, with the caveat that it must be
   updated in lockstep with the backend.
4. **Tailscale SSH option:** using `tailscale ssh` moves authn/authz into
   tailnet ACLs (`ssh` action rules, check mode) — no node-side key
   management, identity = tailnet identity (consistent with the whois trust
   in task 51). Document setup and the trade-offs vs plain OpenSSH.
5. **Baseline:** tailnet ACLs restricting which devices may reach node:22 at
   all — the cheapest, strongest control; show a minimal ACL snippet.

## Implementation Checklist

- [ ] Visibility matrix complete, with **actual audited modes** per carrier.
- [ ] Permanent git-history leak documented as an explicit trade-off.
- [ ] Client-side metadata spread + cleanup story documented.
- [ ] Permission gaps found in write paths fixed (0600/0700) in this PR.
- [ ] `docs/ssh-hardening.md` covers key-only auth, per-vault user,
      restricted-command consideration (with the exact helper-command
      inventory), Tailscale SSH, and ACL baseline.
- [ ] Findings register in the threat model updated; privacy edge cases
      appended to `EDGE-CASES.md`.

## Testing Requirements

Documentation task with small perm fixes; tests scoped to the fixes:

- For each write path whose mode was corrected: a unit test asserting the
  created file's mode is `0600` (and parent `0700` where created by us) —
  same pattern as task 52's hash-file perm test. Stub-only, `t.TempDir()`.
- Verify the restricted-command inventory against the shipped SSH backend by
  grepping the actual remote command strings (`internal/backend`) — the doc's
  whitelist must match the code, with a comment in the backend pointing back
  at the doc so drift gets caught in review.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Every metadata carrier from the threat model's asset list appears in the
  visibility matrix with audited at-rest location, audience, and file mode;
  no carrier's mode is "unknown".
- The lock/genesis git-history exposure is documented as permanent and
  accepted — a reader can decide whether vaulting sensitive filenames is
  acceptable for them *before* it's irreversible.
- `docs/ssh-hardening.md` exists; its restricted-command inventory matches
  the real backend command set; Tailscale SSH and per-vault-user setups are
  actionable as written.
- Any perm fix ships with its regression test.

## Related Proposal Sections

> **Security & transport.** Reuse built primitives only — never roll our own
> crypto/transport. … Block 5 is a dedicated security analysis (… SSH
> hardening, **privacy audit of catalogs/receipts** …).

> **D24.** (a) lock entries … embed the full genesis record, making every repo
> clone an off-node identity backup; (b) every `vault get` writes a local pull
> receipt (`~/.tailvault/receipts/<id>.toml`) with the genesis record …

## Notes & Considerations

- **Gotcha:** the identity-recovery design (D24) and privacy pull in opposite
  directions — this audit documents the tension; it does not relitigate D24.
  Any proposal to redact genesis records goes to the maintainer, not this PR.
- **Gotcha:** restricted SSH commands can break the backend silently on
  upgrade — that's why it's a documented *consideration* with a maintenance
  warning, never a silently-recommended default.
- **For Next Task:** Block 5 closes here; Block 6 (task 56) triages the
  edge-case log, then Block 7 dogfood (57–59, 26) runs the hardened system —
  including the proposal's "live security checks" against this guide.
- **Prev:** [task-54-fuzz-vuln-ci](./task-54-fuzz-vuln-ci.md) ·
  **Next:** [task-56-edge-case-design](./task-56-edge-case-design.md)
