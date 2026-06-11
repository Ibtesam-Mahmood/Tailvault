# Task 53: WAL Chain-Verify Tooling — `tailvault wal verify`

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Per-node WAL" (hash-chained, tamper-evident); "Security & transport" ("WAL hash-chaining gives tamper-evident history"); "Part II task breakdown" → Block 5 ("WAL chain-verify tooling") · **Block:** 5 — Security analysis & hardening · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 29 (`internal/wal` — append/read + the existing `hash-chain verify` primitive), Task 38 (3-way verify — integration point), Task 51 (threat model — names the tampering adversary this discharges) · **Type:** Implementation

## Summary

The hash-chained WAL (D17) is only tamper-**evident** if something actually
walks the chain. This task ships the user-facing tooling: `tailvault wal
verify [--location <name>]` walks a node's entire WAL chain, recomputes each
entry's link to its predecessor, and reports **clean / tampered / truncated /
forked** — with the exact position (op id + index) where the chain breaks.
It also wires a chain **spot-check** into the existing 3-way verify (task 38)
so routine `tailvault verify` runs catch gross tampering without paying the
full-walk cost, and emits concrete **recovery guidance** when a break is found.

Per the hard-fail rule: a broken chain is a non-zero exit (integrity bucket,
exit 5) with a structured error — never a warning that scrolls past. Detection
is the whole product here; tailvault never auto-"repairs" a tampered chain
(that would be a silent success over corrupted history).

## Context

### Related packages

- `internal/wal` (task 29) — **modified here.** Already owns entry hashing and
  a chain-verify primitive; this task adds the full-walk report type
  (break classification + position) and a bounded spot-check helper.
- `cmd/tailvault` — **modified here.** New `wal verify` subcommand.
- Verify command (task 38) — **modified here.** Calls the spot-check and folds
  the result into the 3-way report.
- `internal/tserr` — chain-break error code (under `TV-OBJ-*` / the v2 codes
  frozen in task 27's SPEC; exit bucket 5).

### Prerequisites

- [ ] Task 29's entry format is frozen: each entry embeds the hash of the
      previous entry (SPEC v2, task 27).
- [ ] Decide with the maintainer: standalone `wal verify` subcommand (default
      assumption here) vs a `verify --wal` flag — the engine is identical.

## Changes Required

### internal/wal/chainverify.go

- **File:** `internal/wal/chainverify.go`
- **Action:** create (extending task 29's primitive)
- **Purpose:** full-walk verification with break classification.

```go
// ChainReport is the result of walking a node's WAL chain.
type ChainReport struct {
    Entries  int
    Status   ChainStatus // Clean | Tampered | Truncated | Forked
    BreakAt  int         // index of first bad link (-1 if clean)
    BreakOp  string      // op id at the break, "" if clean/truncated-at-tail
    Detail   string      // human cause, e.g. "entry 41 prev-hash mismatch"
}

// VerifyChain walks every entry oldest→newest, recomputing prev-hash links.
func VerifyChain(r Reader) (ChainReport, error)
// SpotCheck verifies the head entry + n randomly sampled links (cheap probe
// for routine verify runs).
func SpotCheck(r Reader, n int) (ChainReport, error)
```

Classification rules:

- **Tampered** — entry i's recorded prev-hash ≠ recomputed hash of entry i-1
  (an entry was edited in place, or a middle segment replaced).
- **Truncated** — the chain is internally consistent but ends earlier than an
  independent reference expects: compare the head against the catalog's
  last-op pointer and (when available) the client cache's last-seen head
  (task 31). A consistent-but-shorter chain alone cannot prove truncation —
  say so in the report when no reference is available.
- **Forked** — two entries claim the same predecessor (duplicate index /
  prev-hash), i.e. history was rewritten from a point and both lineages are
  present.

### cmd/tailvault — `wal verify`

- **Action:** create. `tailvault wal verify [--location <name>] [--json]`.
- Preflight the node (existing rules), stream the WAL via the backend, run
  `VerifyChain`, print the report. Clean → exit 0; any break → structured
  error, exit 5. `--json` for scripting/CI.

### Task-38 verify integration

- **Action:** modify. The 3-way verify (lock ↔ catalog ↔ disk) gains a fourth
  cheap leg: `SpotCheck(r, k)` per member (k small, e.g. 16 links + head).
  A spot-check failure upgrades the verify result to failed and tells the
  user to run the full `wal verify`.

### Recovery guidance (printed on any break — detection without a next step
is half a tool)

- Broken chain means node-side history is untrustworthy from `BreakAt`
  onward — **do not** run `gc` or trust `moved_to` forwarding from this node
  until resolved.
- Cross-check surviving truth: committed locks in repo clones and pull
  receipts hold genesis records (D24) — `vault restore-identity` can re-seed
  a rebuilt catalog; `vault scan` re-reconciles disk reality.
- If tampering is suspected (vs disk corruption), treat the node as
  compromised per `docs/threat-model.md`; rotating the vault password and
  reviewing SSH access comes before any repair.
- The honest fallback: archive the broken WAL, start a fresh chain via scan +
  restore-identity, accepting loss of op history (not of blobs or identity).

## Implementation Checklist

- [ ] `VerifyChain` walks the full chain; `SpotCheck` samples head + n links.
- [ ] Tampered / Truncated / Forked classified with break index + op id.
- [ ] Truncation cross-references catalog last-op + client cache head, and is
      honest when no reference exists.
- [ ] `tailvault wal verify` subcommand with `--location`, `--json`; break →
      exit 5 structured error.
- [ ] Spot-check folded into task-38 verify; failure upgrades the run.
- [ ] Recovery guidance printed on every non-clean report.
- [ ] Edge cases met along the way appended to `EDGE-CASES.md`.

## Testing Requirements

`internal/wal/chainverify_test.go` + CLI tests against the stub/multi-node
harness (task 39) — stub-only, no real nodes:

- **Clean chain:** N appended entries → `Clean`, `BreakAt == -1`.
- **Tamper:** flip one byte in a middle entry's payload → `Tampered` at the
  exact index; tamper the *last* entry → detected via head recompute.
- **Truncation:** drop the last k entries with a catalog last-op pointer ahead
  of the head → `Truncated`; without any reference → report states
  truncation is unprovable.
- **Fork:** append two entries sharing one predecessor → `Forked`.
- **SpotCheck:** detects a head tamper always; full-walk catches what a small
  sample may miss (test sets sample = all to be deterministic).
- **CLI:** broken chain → exit 5 + recovery text; clean → exit 0; `--json`
  round-trips the report.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A single flipped byte anywhere in a node's WAL is detected with the exact
  break position and op id; forks and (referenced) truncations are
  distinguished from in-place tampering.
- Routine 3-way verify catches a tampered head via the spot-check with
  negligible added cost.
- Every non-clean result hard-fails (exit 5) and prints actionable recovery
  guidance; tailvault never auto-rewrites a broken chain.

## Related Proposal Sections

> Every storage node keeps its own hash-chained WAL (each entry embeds the
> hash of the previous entry → tamper-evident, no consensus needed).

> **D17.** ADOPT the one cheap idea from that world: **hash-chain the WAL** —
> any tampering with history is detectable on read (tamper-EVIDENT, free, no
> consensus). Feeds Block 5's security analysis.

## Notes & Considerations

- **Gotcha:** stream the walk — a long-lived node's WAL can be large; never
  load it whole into memory (same rule as backend `Get`).
- **Gotcha:** tamper-evident ≠ tamper-proof: a node-disk attacker can rewrite
  the *entire* chain self-consistently. Cross-references (catalog pointer,
  client cache heads, receipts) are the only defense — the report must credit
  them, and the threat model (task 51) already scopes this honestly.
- **For Next Task:** task 54 fuzzes the WAL/catalog/genesis parsers this tool
  feeds hostile bytes through.
- **Prev:** [task-52-auth-adversarial-review](./task-52-auth-adversarial-review.md) ·
  **Next:** [task-54-fuzz-vuln-ci](./task-54-fuzz-vuln-ci.md)
