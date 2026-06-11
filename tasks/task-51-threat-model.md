# Task 51: Threat Model — `docs/threat-model.md`

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Security & transport"; "Part II task breakdown" → Block 5 · **Block:** 5 — Security analysis & hardening · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Blocks 3–4 complete (tasks 27–50 merged) — the threat model describes the system as **built**, not as planned · **Type:** Analysis

## Summary

Write `docs/threat-model.md`: the single document that names what tailvault
protects, who it protects it from, and — just as importantly — what it
**explicitly does not defend against**. Every later Block 5 task (52–55) hangs
its findings off this model: the adversarial auth review (52) attacks the
boundaries named here, the WAL chain tooling (53) addresses the tampering
adversary named here, the fuzzing (54) covers the parser attack surfaces
enumerated here, and the privacy audit (55) covers the metadata assets
enumerated here.

This is an **analysis/documentation** task, not a hardening task: findings that
require code changes are recorded as explicit items and handed to tasks 52–55
(or to the edge-case log) — nothing is silently fixed inline.

**Block 5 starts with a human-in-the-loop step:** before any analysis, the
maintainer runs **Claude's automated security review** over the repo and
commits the artifacts (see Prerequisites → STEP 0). The threat model is then
written *against* those artifacts — every automated finding gets a disposition
in the findings register, and tasks 52–55 inherit both the model and the raw
review as inputs.

## Context

### Related packages

- `docs/threat-model.md` — **created here.** The deliverable.
- No code changes. The document cites `SPEC.md` (v1 §1–§8 + the v2 freeze from
  task 27) for every asset's schema and on-disk location.
- `EDGE-CASES.md` (task 27) — security-relevant edge cases discovered while
  writing the model are appended here per the Block 7 discipline (D31).

### Prerequisites

- [ ] **STEP 0 (mandatory, before any analysis): the maintainer manually runs
      Claude's security review** (`/security-review` in Claude Code, run over
      the full repo at current main) **and hands the artifacts over** — the
      findings report is committed under `docs/security/claude-review-<date>.md`
      (raw output + the maintainer's notes). This task and tasks 52–55 use
      those artifacts as a primary input: every finding in them must appear in
      the threat model's findings register (confirmed, refuted-with-reason, or
      routed to 52–55). Do not start the threat model without them.
- [ ] Blocks 3–4 merged: catalog (28), WAL (29), identity/receipts (30),
      federation roster + caches (31), auth (46), membership (47) all exist as
      shipped code — read the real implementations, not the proposal.
- [ ] SPEC v2 (task 27) frozen — schemas, `TV-FED-*` codes, password hash file
      format are normative.

## Changes Required

### docs/threat-model.md

- **File:** `docs/threat-model.md`
- **Action:** create
- **Purpose:** the complete threat model, structured as the sections below.

**1. Assets** — what an attacker would want, where each rests, who writes it:

- **Blobs** (`objects/<sha256>` on node disk) — the actual file contents.
- **Catalogs** (node disk; task 28) — full filename/path/metadata inventory.
- **WALs** (node disk; task 29) — hash-chained operation history, incl.
  genesis records.
- **Locks** (`tailvault.lock`, committed to every repo clone; task 35) —
  embed full genesis records: every clone is an off-node copy of identity
  metadata.
- **Passwords** (argon2id hash file on node; task 46) — gate mutating
  remote ops.
- **Pull receipts** (`~/.tailvault/receipts/<id>.toml` on client machines;
  task 42) — genesis records + filenames at rest on every reading client.
- Also: client federation caches (`~/.tailvault/cache/fed-<id>/`, task 31)
  and `locations.toml`.

**2. Trust boundaries** — three concentric zones, each crossing named:

- **The tailnet** — Tailscale WireGuard + ACLs are the outer wall; anything
  inside it can reach node SSH.
- **Node disk** — physical/OS access to a storage node = full read/write of
  blobs, catalog, WAL, password hash.
- **Client machine** — holds receipts, caches, `locations.toml`, SSH keys,
  and working-tree bytes.

**3. Adversaries** — capability-scoped, in increasing strength:

- **Stolen node disk** (offline attacker): reads everything at rest —
  tailvault stores blobs **unencrypted**; state this plainly and name
  full-disk encryption as the user's mitigation.
- **Compromised tailnet device** (a machine inside the WireGuard boundary):
  can SSH to nodes per ACLs, read everything readable over SSH, attempt
  mutating ops (password-gated), spoof nothing at the WireGuard layer but
  inherit whatever identity `tailscale whois` reports for it.
- **Malicious or buggy client** (legit tailvault user, hostile/broken
  binary): can append garbage WAL entries on nodes it can auth to, write
  corrupt catalogs, feed malformed input to every parser (→ task 54), and
  lie in lock files committed to repos.
- **Out of scope** (say so explicitly): Tailscale control-plane compromise,
  WireGuard/SSH cryptographic breaks, multi-tenant/Byzantine tailnets (D17:
  single-owner trusted network).

**4. Attack surfaces per command** — a table: every v1 + v2 command
(`push/pull/gc/verify`, `vault ls|stat|get|put|mv|rm|scan|passwd|
restore-identity`, `fed init|join|leave|evict|status`, `ops`, `heal`,
`wal verify`) × (what it reads, what it mutates, what auth gates it, what a
hostile peer/input could do to it). Parser entry points (catalog, WAL,
genesis, receipts, `.tailvaultignore`, lock, pointer) are flagged as the
fuzz targets for task 54.

**5. Explicit assumptions** (the things we consciously trust, per D8/D9/D17):

- Tailscale WireGuard provides transport encryption + peer authenticity;
  SSH provides channel auth; `tailscale whois` is trusted for identity
  stamps. **Never roll our own crypto** — tailvault adds no crypto of its
  own beyond sha256 content addressing and argon2id password hashing.
- Single-owner tailnet: members are trusted not-Byzantine; the WAL
  hash-chain is tamper-**evident**, not tamper-**proof** (a node-disk
  attacker can rewrite the whole chain — detection relies on cross-checks,
  task 53).
- SSH access to a node IS authorization for reads (D9): the password gates
  only mutating *tailvault* ops, not raw SSH.

**6. Findings register** — numbered list of gaps found while modeling, each
tagged with the owning task (52–55) or "accepted residual risk" with
rationale.

## Implementation Checklist

- [ ] All seven asset classes documented with at-rest location + schema cite.
- [ ] Three trust boundaries + crossings drawn (a mermaid diagram is fine).
- [ ] Three adversaries scoped; out-of-scope adversaries explicitly listed.
- [ ] Per-command attack-surface table covers the full v1+v2 CLI surface.
- [ ] Assumptions section states the no-own-crypto rule and the
      tamper-evident-not-proof caveat verbatim.
- [ ] Findings register routes every gap to task 52/53/54/55 or accepts it
      with rationale; security edge cases appended to `EDGE-CASES.md`.

## Testing Requirements

No code, so no new tests. Verification is review-based:

- Cross-check the asset list against `SPEC.md` (every schema in §1–§4 and the
  v2 freeze appears as an asset or is justified absent).
- Cross-check the command table against `cmd/tailvault` — every registered
  Cobra command has a row.
- The findings register is non-empty or the document argues why (an empty
  register on a first threat model is a red flag, not a pass).

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- `docs/threat-model.md` exists with all six sections; every claim about an
  on-disk location/permission was verified against the shipped code, not the
  proposal.
- Tasks 52–55 can each point at the section of this document they discharge.
- Out-of-scope adversaries and accepted residual risks are explicit — the
  document never implies protection it doesn't provide (the doc equivalent of
  "never a silent success").

## Related Proposal Sections

> **Security & transport.** Reuse built primitives only — never roll our own
> crypto/transport. Tailscale WireGuard + SSH provide encryption, identity
> (whois), key exchange. … Block 5 is a dedicated security analysis (threat
> model, perms, chain-verify tooling, whois assumptions, SSH hardening, privacy
> audit of catalogs/receipts, govulncheck CI, parser fuzzing).

> **D17.** A tailnet is a small, single-owner, trusted network … hash-chain the
> WAL — tampering with history is detectable on read (tamper-EVIDENT, free, no
> consensus).

## Notes & Considerations

- **Gotcha:** model the system **as shipped** — re-read the actual perms and
  paths in tasks 28–31/42/46 code; a threat model of the proposal is fiction.
- **Gotcha:** resist fixing things inline. This task's output is the map;
  tasks 52–55 do the fighting.
- **For Next Task:** task 52 attacks the auth boundary this document draws
  (password gate, SSH-as-outer-boundary, whois trust).
- **Prev:** [task-50-fed-integration-suite](./task-50-fed-integration-suite.md) ·
  **Next:** [task-52-auth-adversarial-review](./task-52-auth-adversarial-review.md)
