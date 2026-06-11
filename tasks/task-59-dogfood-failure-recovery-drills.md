# Task 59: Dogfood — guided failure & recovery drills

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → Resolution & reachability, Operation journal, Membership; Risk Assessment (v2 rows) · **Block:** 7 — Dogfood · **Type:** Acceptance · **Estimated Effort:** 1 ideal eng-day (guided sessions) · **Dependencies:** Task 58 (all routes proven on the happy path).

## Summary

A guided manual session (AI directs, maintainer runs) that **deliberately
breaks** the system on the local mock rig and proves every failure is loud,
legible, and recoverable — the hard-fail/never-silent-success invariant under
real misuse rather than test harnesses. Where Task 58 proved the routes work,
this task proves they **fail correctly** and that every recovery tool
(`ops retry`, `heal`, `vault scan`, `restore-identity`, `wal verify`, rollback)
actually rescues the state it claims to.

Every drill records: how the failure was injected, the exact error (code +
exit bucket must match SPEC), and the recovery commands that returned the
system to a verified-clean state (`verify` 3-way passes after each drill).
Results land in `docs/dogfood-drills.md`; surprises in `EDGE-CASES.md`.

## Context

### Related packages

- `scripts/dogfood-rig.sh` (Task 57) — reused; drills mutate rig state.
- `docs/dogfood-drills.md` — **created here.**
- `EDGE-CASES.md` — appended throughout.

### Prerequisites

- [ ] Tasks 57–58 complete and green on the rig.
- [ ] Two-member local federation rig up (vault-a + vault-b).

## Changes Required

### docs/dogfood-drills.md — drill checklist (inject → observe → recover)

**Drill group 1 — node down (the founding guarantee):**
make vault-a unreachable (revoke ssh / rename dir) → `push` fails `TV-NODE`,
refs unmoved, no partial upload · `vault get` of an a-homed file fails
`TV-FED` partial-view (exit 6) NOT `TV-OBJ` · `vault ls` shows last-seen from
the client cache · restore vault-a → everything green again.

**Drill group 2 — interrupted operations (WAL):**
kill a `vault put`/`mv` mid-flight (Ctrl-C between intent and done-mark) →
next command flags the pending op · `ops list` shows it · second op on the
same blob blocks ("op in flight") while an op on another blob proceeds ·
`ops retry` completes it idempotently · `wal verify` clean after.

**Drill group 3 — corruption & tamper:**
truncate a stored blob → `verify` reports corrupt (not "edited") · hand-edit a
manual file → `verify`/`scan` reports edited (not corrupt), re-hash updates
catalog · hand-edit a WAL entry → `wal verify` reports tamper + position ·
delete a catalog → rebuild via `scan` + `restore-identity` from a receipt and
from a lock entry (IDs preserved, self-certification check shown failing for a
forged record).

**Drill group 4 — auth & membership:**
wrong password 3× → mutating op rejected every time, readable error, reads
unaffected · `fed leave` vault-b while a repo references its files → pull
WARNS with the repush/resync message; repush to vault-a; `heal`; clean ·
`fed evict` a "dead" member (vault-b dir removed) → roster shrinks, warnings
fire, no data invented.

**Drill group 5 — git-side recovery:**
conflicting lock on two branches → merge driver resolves per-path union ·
full rollback runbook on the rig repo → plain git, clean status, real bytes.

Each drill: inject / expected (SPEC citation: error code + exit bucket) /
observed / recovery / post-`verify` clean.

## Implementation Checklist

- [ ] All five drill groups executed; every error code + exit bucket matched
      SPEC exactly.
- [ ] Every drill ends in a verified-clean state (3-way `verify` passes).
- [ ] Mismatches → GH issues; surprises → `EDGE-CASES.md`.
- [ ] Drill doc committed with full evidence.

## Testing Requirements

Manual by design. The highest-value injections (node-down push, mid-flight
kill, blob truncation) already have automated twins in Tasks 39/50 — confirm
the manual observations match the suite's assertions; divergence is itself a
finding.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- No drill produces a silent success, a wrong error code, or an unrecoverable
  state.
- TV-FED vs TV-OBJ distinction observed exactly as specced under a down member.
- WAL-as-lock blocking, idempotent retry, and tamper detection all witnessed
  first-hand.
- Recovery tooling returns every drill to verified-clean.

## Related Proposal Sections

> **Resolution & error semantics.** …not found among reachable with ≥1 member
> unreachable → TV-FED partial-view hard-fail; not found, all reachable, no
> pending move → TV-OBJ missing.

> **Operation journal.** …append intent → receipt → execute → confirm → mark
> done… pending/failed ops surface on any future command.

## Notes & Considerations

- **Gotcha:** snapshot the rig (`cp -a`) before each drill group so a drill
  that goes sideways doesn't poison the next one.
- **For Next Task:** Task 26 repeats the critical subset of these drills on
  real hardware — the rig evidence here tells it which ones matter most.
- **Prev:** [task-58-dogfood-route-walkthroughs](./task-58-dogfood-route-walkthroughs.md) ·
  **Next:** [task-26-dogfood-root-pnp](./task-26-dogfood-root-pnp.md)
