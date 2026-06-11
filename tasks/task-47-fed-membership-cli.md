# Task 47: `fed init|join|leave|evict|status` — Membership CLI

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Membership: join / leave / evict", "Resolution & reachability" (per-op scoping, client caches), "CLI surface (v2 additions)" · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 2 ideal eng-days · **Dependencies:** Task 28 (`internal/catalog` `[federation]` roster section), Task 29 (`internal/wal` pending ops), Task 31 (`internal/fed` roster parse/merge + caches), Task 32 (resolution/reachability), Task 35 (lock v2 — leave WARNs surface through pull/lock history), Task 37 (`ops` retry for pending roster ops), Task 46 (auth gate for `evict`) · **Type:** Implementation

## Summary

The `fed` command group manages federation membership. The roster lives in each
member's catalog `[federation]` section (no central registry — serverless),
mirrored into clients' `locations.toml` and advisory caches. `fed init` creates
a federation around one existing vault location (mints the federation id,
writes the initial roster). `fed join <location>` is a **client-driven WAL op
applied to every member's roster**: reachable members get the update
immediately; unreachable members get a **pending op** queued against them
(D6/D27) that any later contact — or `tailvault ops retry` — applies. There is
no "federation down": join succeeds the moment the joiner and at least the
sponsoring member record it, with the rest converging asynchronously.

`fed leave` is a **clean detach, never blocked-until-empty** (D28): the
leaver's files simply drop out of the federated tree. Every git repo or sync
that referenced those files learns through the state change — pull emits the
WARNING "home left the federation — repush to a new location or resync from a
moved copy" via the lock-v2 machinery (Task 35) and committed lock history;
remote readers learn via fan-out + cache diffing (D26). The leaver's disk is
**untouched**: its data is no longer federated, not deleted.

A node that dies *without* leaving just looks offline forever, so `fed evict
<member>` exists: a manual, **password-gated** (D9 — it is destructive to the
roster) declaration that a dead member has departed. It applies the same roster
removal as leave, minus the leaver's own participation. `fed status` is the
read-only dashboard: roster, live per-member reachability from a fresh fan-out,
and cache-based `last seen <when>` for members that don't answer.

## Context

### Related packages

- `cmd/tailvault` — **created here:** the `fed` command group.
- `internal/fed` (Task 31) — roster types, merge, federation id, caches.
- `internal/catalog` (Task 28) — `[federation]` section read/write.
- `internal/wal` (Task 29) — roster-update intents, pending ops for
  unreachable members.
- `internal/resolve` (Task 32) — fan-out reachability for `status`.
- `internal/auth` (Task 46) — gate on `evict` (and on `join`'s writes to
  *other* members' rosters — see Notes).

### Prerequisites

- [ ] Tasks 27–32, 35, 37, 46 merged.
- [ ] SPEC v2 roster record shape confirmed (member name, node, base_path ref,
  joined_at, status: active|left|evicted, op id of the change).

## Changes Required

### cmd/tailvault/fed.go

- **File:** `cmd/tailvault/fed.go`
- **Action:** create
- **Purpose:** the five subcommands.

```go
// tailvault fed init <location>            // mint fed id; roster = {self}
// tailvault fed join <location> [--via m]  // add member to every roster
// tailvault fed leave <location>           // clean detach (self)
// tailvault fed evict <member>             // declare a dead member departed
// tailvault fed status                     // roster + reachability + last-seen
```

Implementation Notes:

- **init:** WAL-logged on the founding node; federation id minted per SPEC v2
  (e.g. sha256 of the init op record — confirm with Task 27); client cache
  seeded; `locations.toml` annotated with the fed id.
- **join:** sequence = WAL intent on the joiner ("joining fed F") → for each
  existing member, reachable: roster-add op applied (WAL intent/done on that
  member); unreachable: **pending op recorded** (per Task 29's queued-op shape)
  so the roster-add applies on next contact or `ops retry`. The joiner's own
  catalog gains the full roster snapshot. Partial application is the designed
  state, not an error — report exactly who got the update and who is pending.
- **leave:** the inverse fan-out, initiated by the leaving member's owner.
  Mark self `left` in own catalog (kept — it documents the detach), push
  roster-removal to reachable members + pending ops for the rest. **No data is
  deleted.** Print the repo-facing consequence loudly: files homed here drop
  from the tree; referencing repos will WARN on next pull (Task 35 already
  renders that WARN — assert the integration, don't reimplement).
- **evict:** password-gated via `auth.Gate` against the member you run it
  *through* (the surviving member whose roster you are mutating); applies
  roster-removal with `status: evicted` + the evictor's whois stamp across
  reachable members, pending for the rest. Refuses to evict a member that
  answers a live ping ("member is reachable — use `fed leave` on it instead").
- **status:** read-only, no password. Columns: member, node, state, live
  reachability (fresh fan-out), `last seen` from caches for non-answerers,
  pending roster ops outstanding against each member. `--json` for scripts.
- Roster merge conflicts (two members with divergent rosters after partition)
  resolve via Task 31's merge rules (op-id/timestamp based) — `status` shows a
  divergence note when members disagree, with `ops`/retry as the remedy.
- SPEC §8 layering throughout; partial-view reads in `status` are fine (it is
  a view, clearly annotated), but `evict` requires its write targets reachable
  or queued explicitly.

## Implementation Checklist

- [ ] `fed init` — fed id mint, roster seed, cache + locations annotation.
- [ ] `fed join` — fan-out roster add; pending ops for unreachable members;
  precise applied/pending report.
- [ ] `fed leave` — clean detach; no deletion; pending ops; WARN integration
  with Task 35 asserted.
- [ ] `fed evict` — password-gated; refuses live members; evicted status +
  stamp.
- [ ] `fed status` — roster + live reachability + cache last-seen + pending-op
  counts; read-only.
- [ ] Roster divergence surfaced, never silently merged away.

## Testing Requirements

Against the Task 39 harness (stub backends only):

- **init/join happy path:** 3 members all up → identical rosters everywhere;
  caches updated.
- **join with a member down:** pending op recorded; bring the member up →
  `ops retry` (and also mere next-contact) converges its roster.
- **leave:** files of the leaver disappear from `vault ls`; a stubbed repo
  lock referencing them produces the Task 35 WARN on pull; leaver's stub disk
  byte-identical before/after.
- **evict:** dead member removed with `evicted` status; evict of a live member
  refused; wrong password → roster untouched everywhere.
- **status:** mixed up/down roster renders live + `last seen` rows; divergent
  rosters (fault-injected) flagged.
- **Idempotence:** re-running join/leave/evict with the same op id is a no-op.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- All five subcommands work on the harness with any subset of members down;
  roster convergence is achieved via pending ops without any coordinator.
- Leave never deletes data and never blocks on the member being "empty";
  referencing repos get the repush/resync WARNING through the existing lock
  machinery.
- Evict is password-gated, refuses reachable members, and is the only way to
  retire a dead node.
- `fed status` distinguishes live, cached-last-seen, and never-seen members.

## Related Proposal Sections

> Roster lives in each member's catalog `[federation]` section … `fed join` =
> client-driven WAL op on every member (pending for unreachable ones).

> **Leave = clean detach** (not blocked-until-empty): the leaver's files drop
> out of the federated tree; every repo/sync referencing them gets a WARNING …
> The leaver's disk is untouched.

> `fed evict <member>` (manual, password-gated) declares a dead node departed —
> the only way to distinguish "crashed forever" from "gone".

## Notes & Considerations

- **Gotcha:** join writes to *other* members' catalogs are mutations of those
  nodes — D9 says mutating remote ops are gated. Resolution per SPEC v2
  (Task 27): roster ops are gated with the password of the member being
  written (the joiner proves it to each). Follow whatever Task 27 froze; if it
  exempted roster-adds, cite that section explicitly in the code comment.
- **Gotcha:** evicting a member that later comes back must not corrupt the
  federation — its catalog still claims membership; `status`/resolution treat
  it as `evicted` (roster wins) and direct the user to re-`join` it cleanly.
- **For Next Task:** Task 48 (`restore-identity`) covers the other disaster
  path — a member whose *catalog* died rather than the member itself.
- **Prev:** [task-46-vault-passwd-auth](./task-46-vault-passwd-auth.md) ·
  **Next:** [task-48-restore-identity](./task-48-restore-identity.md)
