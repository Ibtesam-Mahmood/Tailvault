# Task 32: Resolution Engine — Fan-Out, moved_to Forwarding & Partial-View Semantics

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Resolution & reachability" (fan-out, error semantics); Part II task breakdown → 3.6 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 28 (`catalog` queries), Task 29 (`wal` — `moved_to` records, pending-move detection), Task 30 (`identity` — ids, short form), Task 31 (`fed` — roster, `Probe`, caches) · **Type:** Implementation

## Summary

Resolution answers the federation's central question: **given a file ID (or
logical path), where does it live right now — and what may I conclude from
who answered?** The engine fans out over the roster's active members, asks
each (via its catalog over the backend) what it holds, follows `moved_to`
forwarding pointers (the source node's move WAL/catalog record doubles as a
pointer that finds files whose new home is currently offline), and classifies
the outcome under partial-view semantics. There is no global "online/offline":
state is whatever the members you ping report, and partial views are
first-class.

The error semantics are frozen (SPEC v2 §15) and this task implements them
exactly: found at the recorded home → success; found at a **different**
member → success + WARN ("run `tailvault heal`"); not found among reachable
members with **≥1 member unreachable** → `TV-FED-01` partial-view hard-fail
("cannot prove absence"); not found with **all** members reachable and no
pending move → `TV-OBJ-01` missing. Every result carries reachability
metadata (`fed.Reach`), and client caches color messages ("last seen on
pi-2") without ever changing the error class.

This is also where the `TV-FED-*` codes become real: the task extends
`internal/tserr` with `FedPartialView`/`FedNeedAllMembers`/`FedChainBroken`
constants, constructors, and **exit bucket 6**, per §15. The engine itself
(an `internal/fed` resolver) returns typed results + plain errors; commands
map to `tserr` at the boundary per the §8 layering rule.

## Context

### Related packages

- `internal/fed` — **extended here** (`resolve.go`).
- `internal/tserr` (Task 07) — **extended here**: `TV-FED-01/02/03`, exit 6.
- `internal/wal` (Task 29) — pending intents + `moved_to` op records.
- `internal/catalog` (Task 28), `internal/identity` (Task 30),
  `internal/backend` (Task 09).
- Downstream: `heal` (Task 35), gc gate (Task 36), `vault ls|stat|get`
  (Block 4).

### Prerequisites

- [ ] Tasks 28–31 merged.
- [ ] SPEC v2 §15 semantics table re-read; the four outcome classes are
  normative, no fifth class may be invented.

## Changes Required

### internal/tserr/tserr.go

- **File:** `internal/tserr/tserr.go`
- **Action:** modify
- **Purpose:** the v2 error codes + exit bucket.

```go
const (
	FedPartialView    Code = "TV-FED-01" // not found among reachable; ≥1 unreachable
	FedNeedAllMembers Code = "TV-FED-02" // op requires ALL members; ≥1 unreachable
	FedChainBroken    Code = "TV-FED-03" // WAL hash-chain verification failed
)

func FedPartialViewErr(id string, unreachable []string, err error) *Error
func FedNeedAllMembersErr(op string, unreachable []string, err error) *Error
func FedChainBrokenErr(node string, err error) *Error
// ExitCodeFor: TV-FED-* → 6 (new bucket; SPEC v2 §15).
```

### internal/fed/resolve.go

- **File:** `internal/fed/resolve.go`
- **Action:** create
- **Purpose:** the engine.

```go
package fed

// MemberView is what one member reports for a query (its catalog answer).
type MemberView struct {
	Member  string
	File    catalog.File // zero unless Found
	Found   bool
	MovedTo string // forwarding pointer: member name from a moved_to record
}

// Outcome classifies a resolution per SPEC v2 §15.
type Outcome int

const (
	FoundAtHome      Outcome = iota // success
	FoundElsewhere                  // success + WARN: run `tailvault heal`
	PartialView                     // → TV-FED-01 at the boundary
	Missing                         // → TV-OBJ-01 at the boundary
)

// Result is the full resolution answer; Reach metadata is ALWAYS populated.
type Result struct {
	Outcome  Outcome
	View     MemberView // winning view for Found* outcomes
	Home     string     // recorded home (from lock/catalog hint), if any
	Reach    Reach
	LastSeen *MemberSummary // advisory cache color (may be nil)
}

// Querier fetches one member's catalog + pending WAL state over its backend.
type Querier interface {
	Query(ctx context.Context, m catalog.Member, id string) (MemberView, error)
}

// Resolver fans out a query over the roster's active members.
type Resolver struct {
	Roster  Roster
	Q       Querier
	Probe   func(ctx context.Context, m catalog.Member) error
	Cache   *Cache // advisory; may be nil
}

// Resolve looks up a file id (homeHint = recorded home member, "" if unknown).
func (r *Resolver) Resolve(ctx context.Context, id, homeHint string) (Result, error)
```

Implementation Notes:

- **Fan-out order:** probe + query the home hint first (the overwhelmingly
  common success path costs one member); on miss or no hint, query remaining
  active members concurrently. `Reach.Required` is all active members for an
  unhinted/miss case — resolution's scope is the whole roster (D27,
  ls/search class).
- **moved_to forwarding:** a member that no longer holds the file but has a
  `moved_to` record (move WAL entry / catalog tombstone) reports `MovedTo`.
  The engine follows the pointer: if the destination answers and holds the
  file → `FoundElsewhere` (or `FoundAtHome` if the destination IS the recorded
  home). If the destination is **unreachable**, the forwarding pointer still
  proves the file existed and moved → classify as `PartialView` with the
  pointer named in the message (the file is findable later, not missing).
  Follow at most a small bounded chain (e.g. 4 hops) and error on cycles.
- **Pending-move check:** before declaring `Missing`, consult pending WAL
  intents (`wal.Pending` per member) — a pending move op on the id means
  "in flight", which is `PartialView`-class, never `Missing` (§15: "no
  pending move" is a condition of TV-OBJ).
- **Cache coloring:** when `PartialView`, ask `Cache.WasKnown(id)` and attach
  `LastSeen` so the boundary can print "last seen on pi-2 at <t>" — the
  Outcome is unaffected.
- The engine returns `Result` + plain error; the **command boundary** maps
  `PartialView` → `tserr.FedPartialViewErr` (exit 6) and `Missing` →
  `tserr.ObjMissingErr` (exit 5). A `wal.ErrChainBroken` bubbling from a
  Querier maps to `FedChainBrokenErr`.
- Serverless: members are queried by reading their catalogs/WALs over the
  backend — no member executes anything.

## Implementation Checklist

- [ ] `tserr`: `TV-FED-01/02/03` consts + constructors + exit bucket 6.
- [ ] `MemberView`/`Outcome`/`Result`/`Querier`/`Resolver` per the sketch.
- [ ] Home-hint-first fan-out, then concurrent remaining members.
- [ ] `moved_to` following (bounded hops, cycle guard, offline-destination →
  PartialView).
- [ ] Pending-move consultation before `Missing`.
- [ ] Reachability metadata on every `Result`; cache coloring on PartialView.

## Testing Requirements

`internal/fed/resolve_test.go` (stub `Querier` + stub prober — no real nodes):

- **Four outcomes:** found-at-home; found-elsewhere (WARN class); not-found
  with one down member → `PartialView`; not-found with all reachable and no
  pending move → `Missing`.
- **moved_to:** source forwards to a reachable destination → `FoundElsewhere`;
  destination down → `PartialView` naming the pointer; 2-hop chain resolves;
  cycle → error, not a hang.
- **Pending move:** in-flight move intent on the id blocks a `Missing`
  classification.
- **Reach metadata:** every result's `Answered`/`Unreachable` matches the
  stub setup exactly.
- **Cache color:** id present in previous snapshot → `LastSeen` populated on
  PartialView; absent → nil; Outcome identical either way.
- **tserr mapping:** `ExitCodeFor` returns 6 for all three TV-FED codes;
  constructors carry the unreachable-member list into the message.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- The four §15 outcome classes are produced under exactly their specified
  conditions; no resolution path can report `Missing` while any required
  member is unreachable or a move is pending.
- `moved_to` finds files whose new home is offline (as PartialView, never
  Missing) and resolves multi-hop moves.
- Every `Result` carries reachability metadata; caches color but never decide.
- `TV-FED-*` codes exist with exit bucket 6.

## Related Proposal Sections

> **Fan-out**: ping members, each reports what it has; the source's `moved_to`
> WAL/catalog record doubles as a forwarding pointer (finds files whose new
> home is currently offline).

> found at recorded home → success; found at a different member → success +
> WARN (run `heal`); not found among reachable with ≥1 member unreachable →
> TV-FED partial-view hard-fail ("cannot prove absence"); not found with all
> reachable and no pending move → TV-OBJ missing. Every remote view carries
> reachability metadata.

## Notes & Considerations

- **Gotcha:** "cannot prove absence" is the safety property — a down member
  must *never* degrade into a silent `Missing`. Bias every ambiguous branch
  toward `PartialView`.
- **Gotcha:** keep fan-out timeouts bounded; one hung member must not stall
  resolution forever (it becomes Unreachable after its probe timeout).
- **For Next Task:** Task 35's `heal` consumes `FoundElsewhere` results to
  rewrite lock entries; Task 36's gc gate uses `Reach.AllAnswered` +
  `FedNeedAllMembersErr`.
- Append any newly discovered outcome-classification edge case to
  `EDGE-CASES.md` — this is exactly the area Block 7 will mine.
- **Prev:** [task-31-fed-roster-caches](./task-31-fed-roster-caches.md) ·
  **Next:** [task-33-vault-init-bootstrap](./task-33-vault-init-bootstrap.md)
