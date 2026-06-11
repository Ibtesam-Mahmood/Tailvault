# Task 36: GC Federation-Awareness — Pending-Intent Skip, git-Only Scope & All-Members Gate

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "GC under federation"; "Per-node WAL" (D13), "Resolution & reachability" (per-operation scoping: gc → all members); Part II task breakdown → 3.10 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 16 (`internal/gc` v1 mark-and-sweep), Task 29 (`wal.Pending` — D13 skip), Task 28 (`catalog` — sync_mode scoping), Task 31 (`fed.Roster`/`Probe` — the all-members gate), Task 32 (`tserr.FedNeedAllMembersErr`) · **Type:** Implementation

## Summary

Deletion is the operation that can never tolerate ambiguity, so under
federation `gc` grows three hard rules. **(1) Scope — git objects only
(D14):** only blobs whose catalog entry has `sync_mode = "git"` are ever GC
candidates. Manual files (and any unknown future sync mode) are NEVER
collected — they are deleted solely by explicit user action (`rm`/move).
**(2) Pending-intent skip (D13):** gc consults the home node's WAL; any blob
referenced by a pending intent is skipped for that run — an in-flight move or
ingest must not race a delete. **(3) All-members gate (D27/R3):** gc's scope
is *all references*, so it hard-fails with `TV-FED-02` (exit 6) unless ALL
active federation members answered the preflight — a delete may be justified
only by a complete view. This is the per-operation scoping rule applied, not
a special case.

The v1 per-branch mark-and-sweep (keep-set = union of every branch lock's
referenced shas, `preserve` exempt, `--dry-run`) survives intact; this task
wraps it in the three federation gates and extends marking to be
catalog-aware: the candidate set is intersected with the home catalog's
git-mode object set before the keep-set subtraction, and each surviving
candidate is checked against `wal.Pending` immediately before its delete is
journaled. The sweep itself becomes a WAL-journaled op (op type `gc` with
the doomed blob refs), so a crashed sweep is visible and resumable like every
other mutation.

Non-federated vaults (no catalog/roster) keep the exact v1 behavior — the
gates engage only when the location is federated. Hard-fail, never silent
success, throughout.

## Context

### Related packages

- `internal/gc` — **modified here.**
- `internal/wal` (29) — `Pending` skip + journaling the sweep.
- `internal/catalog` (28) — `sync_mode` scoping.
- `internal/fed` (31–32) — roster, `Probe`, `Reach.AllAnswered`,
  `FedNeedAllMembersErr`.
- `cmd/tailvault` — `gc` command flags/help updated.

### Prerequisites

- [ ] Tasks 28, 29, 31, 32 merged; Task 16's gc green.
- [ ] Re-read SPEC v2 §15 (`TV-FED-02`) and §10 (`gc` op type).

## Changes Required

### internal/gc/gc.go

- **File:** `internal/gc/gc.go`
- **Action:** modify
- **Purpose:** the three gates around the v1 sweep.

```go
// FedContext carries the federation inputs; nil ⇒ non-federated vault,
// v1 behavior unchanged.
type FedContext struct {
	Roster fed.Roster
	Probe  func(ctx context.Context, m catalog.Member) error
	Cat    *catalog.Catalog // home node's catalog
	Log    *wal.Log         // home node's WAL
}

// Plan computes the doomed set. Federated path:
//  1. Gate: fed.Probe over Roster.Active(); !AllAnswered → plain
//     ErrNeedAllMembers (boundary → tserr.FedNeedAllMembersErr, exit 6).
//     NOTHING is computed past a failed gate.
//  2. Candidates: objects whose catalog entry has SyncMode == "git" ONLY.
//     Manual/unknown modes and objects absent from the catalog are excluded.
//  3. Keep-set: union of referenced shas across ALL members' branch locks
//     reachable in step 1 (v1 logic, widened to the federation's references).
//  4. Skip: drop any candidate whose file id (or sha) appears in
//     wal.Pending — record skips in the plan for reporting (D13).
func Plan(ctx context.Context, fctx *FedContext, /* v1 args */) (PlanResult, error)

// Sweep executes a plan: append a gc intent (blob refs = doomed ids) →
// delete objects → update catalog → mark done. Re-checks Pending per blob
// at execution time (plan-to-sweep races) and re-skips on conflict.
func Sweep(ctx context.Context, fctx *FedContext, p PlanResult, dryRun bool) (Report, error)
```

Implementation Notes:

- **Gate order matters:** reachability gate → candidate scoping → keep-set →
  pending skip. A failed gate must abort before any marking so a partial
  view can never shape a doomed set.
- **`sync_mode` is allow-list, not deny-list:** candidacy requires literally
  `"git"`; anything else (manual, future `s3`/`watch`, missing entry) is
  excluded. This makes new sync modes safe by default (D15).
- The sweep's own WAL intent makes gc participate in WAL-as-lock: a pending
  gc intent on a blob blocks a concurrent move on it, and vice versa.
- `--dry-run` runs the full plan (including gates — a dry run that lies about
  reachability is worthless) and prints doomed/kept/skipped with reasons.
- Report skipped-pending blobs explicitly: "skipped 3 blobs with in-flight
  ops — re-run gc after `tailvault ops` clears them".

### cmd/tailvault/gc.go

- **File:** `cmd/tailvault/gc.go`
- **Action:** modify
- **Purpose:** boundary mapping + UX. `ErrNeedAllMembers` →
  `tserr.FedNeedAllMembersErr("gc", unreachable, err)` listing the members
  that did not answer; help text documents the three federation rules.

## Implementation Checklist

- [ ] `FedContext` seam; nil ⇒ byte-for-byte v1 behavior.
- [ ] All-members gate before any marking; `TV-FED-02` at the boundary.
- [ ] Candidate scoping to `sync_mode == "git"` (allow-list).
- [ ] Pending-intent skip at plan time AND re-check at sweep time.
- [ ] Sweep journaled as a WAL `gc` op (intent → delete → catalog → done).
- [ ] `--dry-run` exercises gates and prints doomed/kept/skipped+reasons.

## Testing Requirements

`internal/gc/*_test.go` (multi-stub-member fixtures via `FSBackend`s; no real
node):

- **All-members gate:** one down member → gc fails `ErrNeedAllMembers`
  before computing candidates (assert no plan side effects); all answering →
  proceeds.
- **git-only scope:** vault holding git + manual + `sync_mode="s3"` objects —
  only unreferenced git objects are doomed; manual/unknown survive even when
  referenced by nothing.
- **Pending skip:** blob with a pending move intent is skipped and reported;
  after `MarkDone` a re-run collects it.
- **Plan/sweep race:** intent appears between Plan and Sweep → blob re-skipped
  at sweep time.
- **Journaled sweep:** crash injected after the gc intent → pending gc op
  visible in the WAL; re-run/ops-retry completes; no half-deleted state
  invisible to inspection.
- **Non-federated:** nil `FedContext` reproduces the existing v1 gc test
  results unchanged.
- **Keep-set regression:** v1 per-branch protection (branch B's blob survives
  branch A's delete) still holds under the federated path.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- gc never deletes anything unless every active federation member answered
  (`TV-FED-02` otherwise) — verified under down-member simulation.
- Manual and unknown-sync-mode files are categorically un-collectable.
- Blobs with pending WAL intents are skipped at plan AND sweep time.
- Non-federated vaults see zero behavior change.

## Related Proposal Sections

> Only `sync_mode = git` objects are ever GC candidates — manual files are
> deleted solely by explicit user action. gc skips any blob with a pending
> WAL intent, and hard-fails unless ALL members answered (deletes never
> tolerate partial views).

> … gc → all members (its scope is all references — R3 survives as a
> consequence of this rule, not as a special case).

## Notes & Considerations

- **Gotcha:** the keep-set under federation includes references held by
  *other members'* repos/locks where discoverable; when in doubt a sha stays.
  Deletion bias is always "keep".
- **Gotcha:** do not "optimize" the dry-run to skip the reachability gate —
  operators rehearse with `--dry-run` and must see the same gate failures the
  real run would hit.
- **For Next Task:** Task 37's `ops` command surfaces the pending intents
  that gc skipped, closing the operator loop ("clear ops, re-run gc").
- Record any keep-set ambiguity discovered here in `EDGE-CASES.md` — deletion
  edge cases are Block 7's highest-value input.
- **Prev:** [task-35-lock-v2-heal](./task-35-lock-v2-heal.md) ·
  **Next:** [task-37-ops-command](./task-37-ops-command.md)
