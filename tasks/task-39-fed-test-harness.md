# Task 39: Multi-Node Federation Test Harness + Block 3 Integration Suite

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II task breakdown → 3.13; Testing Strategy → "Integration (v2 — multi-node harness: N stub backends, simulated down members)" · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 2 ideal eng-days · **Dependencies:** Tasks 27–38 (everything in Block 3 — this task closes the block) · **Type:** Testing

## Summary

Block 3 built a federation out of catalogs, WALs, identities, resolution, and
gates — each unit-tested in isolation. This task builds the **multi-node
integration harness** that exercises them together: a `fedtest` package that
spins up N simulated federation members, each a `backend.FSBackend` over a
temp dir with its own catalog, WAL, and object store, wired into a shared
roster — plus first-class **down-member simulation** (any member can be
toggled unreachable so its prober fails and its backend errors like a dead
node). Tests must **NEVER use a real Tailscale node**: the harness is pure
stub + fixture, runs in plain `go test ./...`, and is the substrate Block 4's
suite (task 4.11) and the Block 5 security work reuse.

On top of the harness lands the **Block 3 integration suite**, mirroring the
proposal's v2 testing strategy: WAL lifecycle + crash-recovery at every step
boundary + tamper detection; WAL-as-lock races (concurrent ops on one blob
serialize, different blobs proceed, gc skips pending intents); resolution
through fan-out and through `moved_to` when the destination is down; the
TV-FED partial-view vs TV-OBJ missing distinction under every reachability
permutation; bootstrap honoring `.tailvaultignore`; and scan detecting a
manual edit (edited ≠ corrupt). Verify (Task 38) is the oracle most scenarios
end on.

The deliverable is as much an **API** as a test run: building a 3-member
federation with seeded files must be a few lines, because every future block
writes scenarios against this harness. Keep it boring, deterministic, and
fast (whole suite well under a minute).

## Context

### Related packages

- `internal/fedtest` — **created here**: harness (exported helpers, used from
  `_test.go` files across packages and by Blocks 4–5).
- `test/integration` (or the Task 25 suite's home — follow its layout) —
  **Block 3 scenario suite created here.**
- Consumes every Block 3 package: `catalog`, `wal`, `identity`, `fed`,
  `ingest`, `gc`, `ops`, `verify`, `lock`, plus `backend.FSBackend` (Task 09).

### Prerequisites

- [ ] Tasks 27–38 merged (the suite exercises all of them).
- [ ] Review how Task 25's integration suite is organized (build tags, dirs,
  CI wiring) and extend the same conventions rather than inventing new ones.

## Changes Required

### internal/fedtest/harness.go

- **File:** `internal/fedtest/harness.go`
- **Action:** create
- **Purpose:** the N-member simulated federation.

```go
package fedtest

// Member is one simulated federation member: an FSBackend rooted in a temp
// dir carrying objects/, meta/catalog.toml, meta/wal/.
type Member struct {
	Name    string
	Backend *backend.FSBackend
	Root    string // temp dir
	down    atomic.Bool
}

// SetDown toggles unreachability: the prober fails for this member and every
// Backend call errors like a dead node (preflight-failure shaped).
func (m *Member) SetDown(down bool)

// Fed is the harness: members + roster + the probe/querier seams that
// internal/fed components accept.
type Fed struct {
	Members []*Member
	Roster  fed.Roster
	CacheDir string // per-test ~/.tailvault stand-in (caches, receipts)
}

// New builds an N-member federation: fed_id minted, each member's catalog
// seeded with the [federation] roster, genesis WAL entries in place.
func New(t *testing.T, names ...string) *Fed

// Seed ingests a file into a member (full WAL lifecycle + catalog + genesis),
// returning its catalog.File (id included). sync_mode defaults to "manual";
// SeedGit seeds a git-mode object + matching lock entry.
func (f *Fed) Seed(t *testing.T, member, path string, content []byte) catalog.File
func (f *Fed) SeedGit(t *testing.T, member, path string, content []byte, lk *lock.Lock) catalog.File

// Probe / Querier return the seams Resolver/gc/ops expect, honoring SetDown.
func (f *Fed) Probe() func(ctx context.Context, m catalog.Member) error
func (f *Fed) Querier() fed.Querier

// Crash helpers: run a mutating op and stop it after a named step
// (after-intent | after-bytes | after-catalog), leaving real torn state.
func (f *Fed) CrashAfter(step string) /* fault-injection hook for ingest/scan/gc seams */

// Tamper flips bytes in a member's WAL entry k (chain-break fixture).
func (f *Fed) Tamper(t *testing.T, member string, k int)

// NewDemoRepo generates a throwaway git repo fixture: a real `git init` repo
// populated with generated files straddling min_size (some above, some
// below, nested dirs), tailvault.toml pointed at a harness member, and the
// filter/hooks installed. The harness's bridge between the federation world
// and the git-flow world; reused by the Block 7 dogfood demo tests (task 50).
func NewDemoRepo(t *testing.T, f *Fed, member string, opt DemoOpt) *DemoRepo

type DemoRepo struct {
	Dir   string // the repo working tree
	Files []string // generated paths with sizes
}
```

Implementation Notes:

- Down simulation must fail the same way a dead node fails through the real
  seams (prober error; backend ops erroring before any data moves) so
  production error-classification paths are what's exercised — not special
  test branches.
- Seeding goes through the **real** ingest/WAL code paths (Task 33's
  pipeline), not hand-written catalog files — fixtures that bypass production
  writers rot silently.
- Crash injection: prefer explicit fault-hook seams in `ingest`/`gc` (a
  `testHook func(step string)` already threadable) over goroutine murder —
  determinism beats realism here.
- Everything keys off `t.TempDir()`/`t.Cleanup`; zero global state; safe for
  `go test -race -parallel`.

### test/integration/block3_test.go (path per Task 25 conventions)

- **File:** `test/integration/block3_test.go` (split by area if large)
- **Action:** create
- **Purpose:** the Block 3 scenario suite. Required scenarios:

1. **WAL lifecycle:** ingest on member A → intent→done visible; entry bytes
   immutable across state change.
2. **Crash recovery:** `CrashAfter` each of after-intent / after-bytes /
   after-catalog → verify (Task 38) reports exactly one `PendingOpState`;
   `ops retry` completes; final state byte-identical to the uninterrupted
   run.
3. **Tamper:** `Tamper` a mid-chain entry → `wal.Read` fails `ErrChainBroken`;
   verify reports `ChainBroken`; `ops` withholds that member's ops.
4. **WAL-as-lock race:** two concurrent ops on one seeded blob → exactly one
   intent wins, the other gets op-in-flight; ops on different blobs both
   land (run under `-race`).
5. **gc gates:** pending intent → blob skipped; manual + unknown-sync-mode
   files never doomed; one member `SetDown(true)` → gc fails `TV-FED-02`
   before planning.
6. **Resolution — fan-out:** file moved (catalog rewritten via the move
   executor) → resolve finds it at the new member → `FoundElsewhere`.
7. **Resolution — moved_to with destination down:** move A→B recorded, then
   `B.SetDown(true)` → resolve via A's forwarding pointer → `PartialView`
   naming B (never `Missing`).
8. **TV-FED vs TV-OBJ:** absent id with all members up → `Missing`
   (TV-OBJ-01, exit 5); same query with any member down → `PartialView`
   (TV-FED-01, exit 6); cache from a prior snapshot colors the message.
9. **Bootstrap + ignore:** `vault init` over a fixture tree with
   `.tailvaultignore` → ignored files absent, everything else catalogued
   with self-certifying ids; interrupted bootstrap resumes.
10. **Scan manual edit:** seeded manual file edited on disk → scan reports
    `Edited`, absorbs it; byte-rot fixture (content changed, mtime+size
    restored) → verify `Corrupt`, scan `Suspect` — edited ≠ corrupt proven
    end-to-end.
11. **Heal:** moved file's repo lock healed to the new location; under a
    down member heal leaves the entry untouched.

## Implementation Checklist

- [ ] `fedtest.New/Seed/SeedGit/SetDown/Probe/Querier/CrashAfter/Tamper`.
- [ ] `NewDemoRepo` git-repo fixture (generated files straddling `min_size`,
  config + filter/hooks installed against a harness member).
- [ ] Down members fail through production seams, not test branches.
- [ ] Seeding via real ingest/WAL code paths.
- [ ] All 11 scenarios implemented; suite wired into the Task 25 CI job.
- [ ] Whole suite deterministic, `-race`-clean, no network, no real node.

## Testing Requirements

This task IS tests; the meta-requirements:

- `go test ./...` runs harness + suite with no env vars, no tags required
  for the stub path (mirroring Task 09's rule that only real-node tests hide
  behind guards — there are no real-node tests here at all).
- `go test -race ./...` clean (scenario 4 specifically exists for it).
- Each scenario asserts **terminal state** (catalog/WAL/lock bytes, verify
  findings, exit-code classes) — not just "no error".
- Harness API documented with package-level example
  (`ExampleNew_threeMembers`).

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A 3-member federation with seeded files + a down member is expressible in
  <10 lines of harness calls.
- All 11 scenarios pass, including every crash-step permutation and the
  TV-FED/TV-OBJ distinction under each reachability permutation.
- No test anywhere in the suite touches Tailscale, SSH, or the network.
- Blocks 4–5 can consume `internal/fedtest` without modification (exported,
  documented API).

## Related Proposal Sections

> 3.13 Multi-node integration harness (N stub backends, down-member
> simulation) + Block 3 suite.

> **Integration (v2 — multi-node harness: N stub backends, simulated down
> members):** WAL: intent→done lifecycle; crash between any two write steps
> is detected and repaired by verify/heal; hash-chain tamper detection; retry
> idempotence. WAL-as-lock: concurrent ops on one blob serialize; gc skips
> pending intents … Resolution: moved file found via fan-out and via
> `moved_to` when the new home is down; TV-FED partial-view vs TV-OBJ missing
> distinction. … manual edit detected by scan (edited ≠ corrupt).

## Notes & Considerations

- **Gotcha:** crash tests that "kill" goroutines are flaky by construction —
  use the explicit fault-hook seams and leave genuinely torn files on disk;
  that's the state verify must prove it catches.
- **Gotcha:** keep scenario fixtures small (KB files; `NewDemoRepo` files
  need only straddle a test-sized `min_size`) — the suite guards semantics,
  not throughput; a real-hardware pass is the dogfood block's (Block 7) job.
- **For Next Task:** Block 4 starts with the remote sha256 short-circuit
  (DEV-C1) and then drives every `vault *` remote command against this
  harness; `NewDemoRepo` is reused by the dogfood demo-test task (task 50);
  harvest any harness gaps into `EDGE-CASES.md` as you go.
- **Prev:** [task-38-verify-3way](./task-38-verify-3way.md) ·
  **Next:** [task-40-remote-hash-shortcircuit](./task-40-remote-hash-shortcircuit.md)
