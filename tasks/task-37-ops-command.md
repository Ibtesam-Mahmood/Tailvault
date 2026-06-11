# Task 37: `tailvault ops` — List Pending/Failed WAL Ops & Client-Driven Retry

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Per-node WAL" (pending/failed surfacing, `ops`/`ops retry`), "CLI surface (v2 additions)"; Part II task breakdown → 3.11 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 29 (`wal` — `Pending`, lifecycle, idempotency), Task 31 (`fed` — roster + reachability for the cross-member sweep), Task 32 (`tserr` TV-FED codes for boundary mapping) · **Type:** Implementation

## Summary

The WAL's intent→execute→done lifecycle means a crash, a dropped connection,
or a failed step leaves **pending or failed ops** sitting in some node's
journal. The design surfaces them passively (any later command that pings the
node reports them) — `tailvault ops` is the active counterpart: it sweeps the
WALs of all **reachable** federation members, lists pending and failed ops
with their age, type, actor, blob refs, and per-blob dependency relationships
("op B waits on op A — same blob"), and `tailvault ops retry` re-executes
them **client-driven over SSH** — nodes never execute anything themselves;
the client replays the op's remaining steps idempotently (op-id dedupe makes
a double-retry harmless).

Not every op is mechanically retryable: an op whose preconditions are gone
(source bytes missing, destination removed, chain anchor broken) is flagged
**unresolvable — needs physical fixing**, with the diagnosis printed (what's
missing, on which node) so the operator can intervene over SSH. `ops` never
silently drops an op: every journal entry that is not `done` appears in the
listing until it is completed, failed-and-retried, or explicitly resolved by
a human.

Listing tolerates partial reachability (it reports per-member "unreachable —
ops unknown" rows rather than failing — its scope is "whoever answers"), but
`ops retry` of a specific op requires its home node, hard-failing with the
standard node-unreachable error otherwise. Per §8 layering, the engine logic
returns plain errors; the command maps to `tserr`.

## Context

### Related packages

- `cmd/tailvault` — **`ops` + `ops retry` subcommands created here.**
- `internal/ops` — **created here**: sweep/inspect/retry engine.
- `internal/wal` (29) — `Read`/`Pending`/`MarkDone`/`MarkFailed`.
- `internal/fed` (31) — roster + `Probe` for the member sweep.
- `internal/backend` (09) — per-member transport.
- Consumers: gc (Task 36) points operators here; Block 4 mutating commands
  produce the ops this lists.

### Prerequisites

- [ ] Tasks 29, 31, 32 merged.
- [ ] SPEC v2 §10 op types + state markers re-read; the retry semantics of
  each op type must follow the lifecycle exactly.

## Changes Required

### internal/ops/ops.go

- **File:** `internal/ops/ops.go`
- **Action:** create
- **Purpose:** the sweep + dependency model + retry executor.

```go
package ops

// PendingOp is one not-done WAL entry, enriched for display.
type PendingOp struct {
	Member   string
	Rec      wal.Rec // entry + state (intent | failed)
	Age      time.Duration
	WaitsOn  []string // op ids ahead of it on a shared blob (per-blob ordering)
	Verdict  Verdict
	Diagnosis string // human text for Unresolvable
}

type Verdict int

const (
	Retryable    Verdict = iota // preconditions hold; replay remaining steps
	Unresolvable                // preconditions gone; needs physical fixing
)

// Sweep reads every reachable member's WAL (chain-verified) and returns all
// pending/failed ops + the reachability accounting. Unreachable members are
// reported, not fatal.
func Sweep(ctx context.Context, roster fed.Roster, q MemberWAL,
	probe Probe) ([]PendingOp, fed.Reach, error)

// Executor replays one op's remaining steps for its op type. Implementations
// are registered per op type (ingest, move, delete, sync_mode, gc, roster,
// scan); Block 4 registers more. Idempotent: already-completed steps are
// detected (op-id dedupe, Stat-before-write) and skipped.
type Executor interface {
	Diagnose(ctx context.Context, op PendingOp) (Verdict, string, error)
	Retry(ctx context.Context, op PendingOp) error // ends in MarkDone/MarkFailed
}

// Retry diagnoses then re-runs a single op on its home member.
func Retry(ctx context.Context, op PendingOp, ex Executor) error
```

Implementation Notes:

- **Per-blob dependency display:** within one member's WAL, pending intents
  sharing a blob ref are ordered by seq; `WaitsOn` lists the earlier op ids.
  This is *display + retry-ordering* only — there is no general dependency
  DAG (R2); cross-blob ops are independent.
- **Retry order:** `ops retry --all` retries within each blob's chain in seq
  order and refuses to retry an op whose `WaitsOn` predecessor is still
  pending (retry the head first).
- **Diagnose before retry, always:** a retry that would half-execute against
  missing preconditions is exactly the partial-failure class the WAL exists
  to prevent. Verdicts: e.g. a `move` whose source object is gone from disk
  and absent at the destination → Unresolvable ("blob <short-id> bytes lost —
  restore from a clone, then ops retry").
- A chain-verification failure on any member's WAL during `Sweep` is reported
  for that member as `TV-FED-03`-class (and its ops withheld — a tampered
  journal must not drive retries) while other members still list.
- Executors for Block 3 op types (`ingest`, `move` from scan, `delete`,
  `scan`, `gc`) are thin: each replays the standard ordering (bytes →
  catalog → done) using the same engine code paths Tasks 33/34/36 use — do
  not duplicate execution logic; factor seams in those packages where needed.

### cmd/tailvault/ops.go

- **File:** `cmd/tailvault/ops.go`
- **Action:** create
- **Purpose:** the Cobra commands.

```go
// tailvault ops [list] [--member <name>] [--json]
//   table: MEMBER  OP-ID(short)  TYPE  STATE  AGE  BLOBS(short ids)  WAITS-ON  VERDICT
//   trailing rows for unreachable members: "pi-2: unreachable — ops unknown"
// tailvault ops retry (<op-id> | --all) [--member <name>]
//   per-op result lines; exit non-zero if any retry failed or was refused
```

Implementation Notes:

- `ops list` exits 0 even with pending ops shown (it is an inspection
  command); a `--fail-pending` flag (exit 1 when anything is pending) serves
  scripts/CI. Record the choice in `EDGE-CASES.md`.
- `ops retry <op-id>` accepts the 12-hex short op-id prefix; ambiguity errors
  out listing matches.
- Mutating-op auth (per-node password, D9) is enforced when Block 4's
  `vault passwd` lands; leave the auth seam (an `Authenticator` parameter
  defaulting to allow) visibly in place with a TODO citing task 4.7.

## Implementation Checklist

- [ ] `Sweep` across reachable members with chain verification + per-member
  unreachable reporting.
- [ ] `WaitsOn` per-blob ordering computation.
- [ ] `Executor` registry + Block 3 op-type executors reusing engine code.
- [ ] Diagnose→Retry flow; Unresolvable flagging with diagnosis text.
- [ ] `ops [list]` table + `--json`; `ops retry <id>|--all` with
  predecessor-first ordering.

## Testing Requirements

`internal/ops/*_test.go` + command tests (multi-member `FSBackend` fixtures):

- **Listing:** pending + failed ops across two stub members appear with
  correct member/type/state/age; a third down member shows the unreachable
  row; exit 0.
- **Dependency display:** two intents on one blob → second `WaitsOn` first;
  ops on different blobs → no edges.
- **Retry success:** crash-injected ingest (intent without done) → retry
  completes bytes→catalog→done; WAL shows exactly one op id; catalog correct.
- **Retry idempotency:** retry of an op whose execution actually completed
  (marker write lost) detects completion and just marks done — no double
  side effects.
- **Refused retry:** retrying an op with a pending `WaitsOn` predecessor is
  refused with a pointer to the head op.
- **Unresolvable:** move op with source bytes deleted → `Unresolvable` +
  diagnosis; retry refuses.
- **Tampered member:** chain-broken WAL on one member → its ops withheld +
  TV-FED-03 report; other member unaffected.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Every not-done WAL entry on every reachable member is visible in
  `ops list`, with per-blob dependency edges.
- `ops retry` re-runs ops idempotently, in per-blob order, client-driven —
  no node-side execution of any kind.
- Unresolvable ops are flagged with an actionable diagnosis, never silently
  dropped or blindly re-executed.
- Down members degrade the listing, not the command.

## Related Proposal Sections

> Pending/failed ops surface on any later command that pings the node;
> `tailvault ops` lists, `ops retry` re-runs; unresolvable ops are flagged
> for physical fixing. Blocking is **per-blob ordering** only (no general
> dependency DAG).

> Ops are idempotent with unique ids (retry-safe, dedupe).

## Notes & Considerations

- **Gotcha:** retry must replay through the *same* engine code as the
  original op — a parallel "retry implementation" will drift and corrupt the
  write-ahead ordering.
- **Gotcha:** never retry against a chain-broken WAL; tamper evidence first
  (Block 5 ships the chain-repair tooling).
- **For Next Task:** Task 38's verify cites `ops` in its repair guidance
  ("pending op detected — run `tailvault ops`"); Block 4's mutating commands
  register their executors into the same registry.
- **Prev:** [task-36-gc-federation](./task-36-gc-federation.md) ·
  **Next:** [task-38-verify-3way](./task-38-verify-3way.md)
