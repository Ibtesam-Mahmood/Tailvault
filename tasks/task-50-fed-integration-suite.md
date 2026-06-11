# Task 50: Block 4 Integration Suite

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Part II task breakdown" 4.11; Testing Strategy → "Integration (v2 — multi-node harness)" · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 2 ideal eng-days · **Dependencies:** Task 39 (multi-node test harness), Tasks 40–49 (every Block 4 surface under test), Task 36 (federated gc, exercised against pending ops), Task 37 (`ops` retry) · **Type:** Testing

## Summary

Block 4 shipped ten user-facing surfaces in as many PRs, each with its own
focused tests. This task is the cross-cutting suite that runs **every remote
command against the Task 39 multi-node harness** (N stub backends, simulated
down members, fault injection) and asserts the federation-level invariants
that no single task's tests can: auth is enforced uniformly, partial views
never lie, WAL state converges after failures, and identities survive the
round-trips the design promises. **No real Tailscale node is ever touched** —
stub-only is a hard invariant; anything needing real hardware belongs to
Block 6 dogfood.

The suite is organized as scenario files, one per command family, plus a
cross-cutting auth matrix and a failure/retry matrix. Each scenario follows the
same shape: build an N-member federation on the harness, seed files via the
real ingestion paths (`vault init` bootstrap, `track`, `vault put` — not by
poking catalogs directly), run the command under test through the real Cobra
entry points (output + exit codes are part of the contract), then assert
catalog/WAL/disk three-way consistency on every member via the Task 38
verifier.

This suite is also Block 4's gate: it closes the block, feeds discovered
oddities into `EDGE-CASES.md` (D31 discipline — Block 7 consumes that log),
and hands Block 5 a known-green baseline to attack.

## Context

### Related packages

- `internal/fedtest` (Task 39) — harness: N stub members, down-member
  switches, fault injection hooks, in-memory auth verifier seam (Task 46).
- `test/integration` (or the layout Task 39 established) — **created here:**
  the Block 4 scenario files.
- Every `cmd/tailvault` Block 4 command — exercised end-to-end.
- `internal/verify` (Task 38) — the 3-way consistency oracle each scenario
  ends with.

### Prerequisites

- [ ] Tasks 39–49 merged; Block 3 suite (Task 39) green on `main`.
- [ ] `EDGE-CASES.md` exists at repo root (create if Block 3 has not).

## Changes Required

### test/integration/block4_*_test.go

- **File:** `test/integration/block4_ls_stat_test.go`,
  `block4_get_test.go`, `block4_put_test.go`, `block4_mv_test.go`,
  `block4_rm_syncmode_test.go`, `block4_fed_membership_test.go`,
  `block4_identity_test.go`, `block4_track_test.go`,
  `block4_auth_matrix_test.go`, `block4_failure_retry_test.go`
- **Action:** create
- **Purpose:** the scenarios below. Shared setup lives in one
  `block4_helpers_test.go` (federation builder, seeded fixture set, output
  capture, exit-code assert).

Scenario coverage (normative list — each bullet is at least one test):

- **Happy paths, every command:** `ls`, `stat` (path + ID), `get`, `put`,
  `mv` (intra + cross), `rm`, `sync-mode`, `passwd`, `track`,
  `restore-identity`, `fed init|join|leave|evict|status`, `ops retry` — each
  run through the CLI entry point on a 3-member federation, ending in a clean
  3-way verify on all members.
- **Auth matrix (cross-cutting):** every mutating command × {no password
  configured, wrong password, correct password} → mutations rejected leave
  **zero** WAL entries and zero byte changes (disk snapshot compare); every
  read command runs with no password anywhere. The gated set is asserted to
  equal the SPEC v2 list — a new ungated mutation fails this suite.
- **Conflict modes:** `put` onto an existing logical path × `--on-conflict=
  copy|rename|stop` + non-TTY-without-flag hard-fail; `mv` destination
  collision reusing the same matrix.
- **Leave/evict flows:** leave drops the member's files from `ls`; a seeded
  repo lock referencing them yields the Task 35 repush/resync WARNING on pull;
  leaver's stub disk byte-identical. Evict of a downed member converges
  rosters (pending ops applied on revival of others); evict of a live member
  refused; wrong-password evict untouched everywhere.
- **Get-receipt round-trip:** `put` → `get` → receipt exists and
  self-certifies (`sha256(record) == id` recomputed in-test).
- **Restore-identity round-trip:** destroy + rebuild a member's catalog/WAL →
  `restore-identity` from (a) the pull receipt and (b) a lock-v2 entry → the
  original ID is live again; tampered record rejected.
- **mv with destination down:** preflight-down → clean fail, no intents;
  mid-transfer down (fault injection) → pending intents on both ends visible
  in `ops`, gc skips the in-flight blob, member revived → `ops retry`
  completes the move exactly once, `moved_to` forwarder resolves while the
  dest is down again.
- **Partial-view discipline:** with one member down, `ls`/`stat` show cached
  last-seen rows; a missing-file lookup is TV-FED (exit 6) not TV-OBJ; with
  all members up it is TV-OBJ (exit 5); `gc` hard-fails unless all members
  answer.
- **Track + scan interplay:** hand-drop into a stub tree → `track` → edit →
  `scan` absorbs; `.tailvaultignore` exact-path override.

Implementation Notes:

- Drive commands through the root Cobra command with injected
  stdin/stdout/stderr — exit codes and user-visible text (WARN lines, "last
  seen" markers) are asserted, since they are the product.
- Seed only via real commands; hand-edited catalogs are allowed exclusively in
  destruction/fault scenarios where the point is a broken node.
- Keep the suite parallel-safe (`t.Parallel()` per scenario; one harness
  federation per test) and fast enough for `go test ./...` on every PR — no
  build tags hiding it.
- Every surprising behavior found while writing these tests gets an
  `EDGE-CASES.md` entry in the same PR (what was chosen / punted / worked).

## Implementation Checklist

- [ ] Shared federation builder + fixture seeding helpers.
- [ ] One scenario file per command family (list above), all green.
- [ ] Auth matrix with gated-set == SPEC v2 list assertion.
- [ ] Failure/retry matrix (mv mid-transfer, put crash points, pending-op
  convergence via `ops retry`).
- [ ] Every scenario ends in a 3-way verify on every member.
- [ ] `EDGE-CASES.md` entries for anything discovered.
- [ ] Zero real-network, real-SSH, or real-Tailscale usage (enforced: harness
  refuses to construct non-stub backends).

## Testing Requirements

This task **is** the tests. Meta-requirements:

- The suite runs in plain `go test ./...` with no environment setup, no
  network, no `tailscale` binary on PATH (assert the harness never shells out
  to it).
- Failure messages identify member, command, and the violated invariant —
  these tests are Block 5/6's debugging map.
- Add a `-run` -friendly naming convention (`TestBlock4_<Family>_<Case>`)
  documented in the helpers file header.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Every Block 4 command has a green happy-path scenario plus its
  rejected-auth, conflict, and (where applicable) down-member/retry scenarios
  on the multi-node harness.
- The auth matrix proves the gated set matches SPEC v2 exactly; the
  partial-view scenarios prove TV-FED (exit 6) vs TV-OBJ (exit 5) never blur.
- Both identity round-trips (receipt, lock) pass after a full catalog
  destruction.
- The suite is stub-only, parallel-safe, and runs on every PR with no real
  node — Block 4 is closed and Block 5 inherits a green baseline.

## Related Proposal Sections

> 4.11 Block 4 integration suite (remote ops, auth, conflicts, leave/evict).

> **Auth:** mutating remote ops rejected without password; reads unaffected.
> **Identity:** restore-identity round-trip from a lock entry and a receipt.
> **Membership:** join with a member down (pending op applied later); leave
> detaches + warns referencing repos; evict of a dead member.

> `EDGE-CASES.md` is a running log: every dev/QA appends edge cases discovered
> while building Blocks 3–6 … Block 7's design consumes that log.

## Notes & Considerations

- **Gotcha:** scenario tests that seed via real commands are slower but catch
  the integration bugs this task exists for — resist the shortcut of writing
  catalogs directly except in destruction scenarios.
- **Gotcha:** the disk-snapshot compare in the auth matrix must include WAL
  files — a rejected op that still appended an intent is exactly the bug class
  being hunted.
- **For Next Task:** Block 5 begins with the threat model; this suite's auth
  matrix and failure scenarios are its first adversarial targets.
- **Prev:** [task-49-track-manual-ingest](./task-49-track-manual-ingest.md) ·
  **Next:** [task-51-threat-model](./task-51-threat-model.md)
