# Task 31: internal/fed — Federation Roster, Client State Caches & Reachability Accounting

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Resolution & reachability" (client state caches, reachability scoping), "Membership: join / leave / evict" (roster storage); Part II task breakdown → 3.5 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 27 (SPEC v2 §13 roster, §14 cache format), Task 28 (`catalog.Federation`/`Member` types) · **Type:** Implementation

## Summary

The federation has no server and no global state: the roster lives in **each
member's catalog** `[federation]` section, mirrored into clients'
`locations.toml` awareness and per-federation caches. This task ships
`internal/fed`: parsing/merging rosters read from member catalogs, mirroring
roster knowledge into the user-level registry, **client state caches**
(advisory current + previous federation snapshots under
`~/.tailvault/cache/fed-<fed_id>/`), and **reachability accounting** — the
bookkeeping of exactly which members answered a fan-out, which every v2 error
decision and remote view depends on.

Caches are **advisory, never authoritative** (D26): they exist to distinguish
"was here before, offline now" from "never existed" (sharpening partial-view
errors), to show last-known state when members are offline, and to detect
roster changes between sessions. Live pings always win — no code path may
treat a cache hit as proof of current state.

Reachability accounting implements the substrate for per-operation scoping
(D27): there is no global "federation online"; each command needs only the
members its scope touches. This package answers "of the members I needed, who
answered?" — the resolution engine (Task 32) and gc gate (Task 36) consume
that answer. Roster *mutation* commands (`fed join/leave/evict`) are Block 4;
here we only read, merge, and account. Plain errors only (§8 layering).

## Context

### Related packages

- `internal/fed` — **created here.**
- `internal/catalog` (Task 28) — `Federation`/`Member` are the wire types;
  this package adds behavior.
- `internal/backend` (Task 09) + `internal/locations` (Task 10) — fetch member
  catalogs; mirror roster into the registry.
- Downstream: resolution engine (Task 32), gc all-members gate (Task 36),
  ops listing (Task 37), Block 4 `fed *` commands.

### Prerequisites

- [ ] Task 27 merged; §13 (roster, member status lifecycle, `fed_id`) and §14
  (cache layout, rotation rule) frozen.
- [ ] Task 28 merged.

## Changes Required

### internal/fed/roster.go

- **File:** `internal/fed/roster.go`
- **Action:** create
- **Purpose:** roster type + parse/merge from member catalogs.

```go
package fed

// Roster is the merged federation view (SPEC v2 §13).
type Roster struct {
	FedID   string
	Members []catalog.Member // sorted by name, byte-wise ascending
}

// FromCatalog lifts a single catalog's [federation] section.
func FromCatalog(c *catalog.Catalog) (Roster, error) // errors on empty fed_id

// Merge combines rosters read from several members' catalogs into one view:
// same fed_id required (mismatch = error), member rows unioned by name, and
// on conflicting rows the newest joined_at/status wins. Deterministic output.
func Merge(rosters ...Roster) (Roster, error)

func (r Roster) Active() []catalog.Member // status == "active" only
func (r Roster) Find(name string) (catalog.Member, bool)
```

Implementation Notes:

- Members whose status is `left`/`evicted` stay in the merged roster (history
  feeds WARN messages, D28) but are excluded from `Active()` — fan-out and the
  gc gate operate on `Active()`.
- A fed_id mismatch across catalogs is a hard error (two different federations
  were mixed) — never silently pick one.
- Mirroring into `locations.toml`: add a small helper that reports roster
  members missing from the user's registry (so commands can hint
  `tailvault location add <name>`); do **not** auto-write registry entries —
  the registry holds user-confirmed addresses/credentials.

### internal/fed/cache.go

- **File:** `internal/fed/cache.go`
- **Action:** create
- **Purpose:** advisory client state caches per SPEC v2 §14.

```go
// Snapshot is one cached federation state (SPEC v2 §14).
type Snapshot struct {
	FedID   string          `toml:"fed_id"`
	TakenAt time.Time       `toml:"taken_at"`
	Roster  Roster          `toml:"-"` // serialized per §14
	Members []MemberSummary `toml:"member"`
}

// MemberSummary is the per-member catalog digest a client keeps.
type MemberSummary struct {
	Name      string    `toml:"name"`
	Reachable bool      `toml:"reachable"`
	LastSeen  time.Time `toml:"last_seen"`
	FileIDs   []string  `toml:"file_ids"` // ids the member reported holding
}

// Cache manages ~/.tailvault/cache/fed-<fed_id>/{current,previous}.toml.
type Cache struct{ Dir string } // Dir injectable for tests

func (c *Cache) Load() (current, previous *Snapshot, err error) // missing files → nils, no error
// Record rotates current→previous and writes snap as current (atomic writes).
func (c *Cache) Record(snap Snapshot) error
// WasKnown reports whether id appeared in current or previous — the
// "was here before, offline now" vs "never existed" signal (advisory).
func (c *Cache) WasKnown(id string) (member string, known bool)
```

### internal/fed/reach.go

- **File:** `internal/fed/reach.go`
- **Action:** create
- **Purpose:** reachability accounting for per-operation scoping.

```go
// Reach records, for one operation, which required members answered.
type Reach struct {
	Required    []string // member names the op's scope touches (D27)
	Answered    []string
	Unreachable []string
}

// Probe pings each required member (injected prober — tailscale.Ping or a
// backend Stat seam) and returns the accounting. Probes run concurrently;
// a per-member timeout bounds the slowest member.
func Probe(ctx context.Context, members []catalog.Member,
	probe func(ctx context.Context, m catalog.Member) error) Reach

func (r Reach) AllAnswered() bool
func (r Reach) Partial() bool // ≥1 unreachable
```

Implementation Notes:

- `Reach` is attached to **every** remote view downstream (Task 32's results,
  `vault ls/stat` output in Block 4) — keep it a plain serializable value.
- The prober is injected so tests simulate down members with a stub; no real
  Tailscale calls in tests, ever.
- This package never decides success/failure from reachability — that mapping
  (TV-FED vs TV-OBJ) belongs to the resolution engine (Task 32).

## Implementation Checklist

- [ ] `Roster` + `FromCatalog` + deterministic `Merge` (fed_id gate, newest
  wins, union by name) + `Active`/`Find`.
- [ ] Registry-mirror helper reporting unregistered roster members.
- [ ] `Cache` with current/previous rotation, atomic writes, `WasKnown`.
- [ ] `Reach` + concurrent `Probe` with injected prober.

## Testing Requirements

`internal/fed/*_test.go`:

- **Merge:** disjoint rosters union; conflicting member rows → newest wins;
  fed_id mismatch errors; output deterministic regardless of input order.
- **Status lifecycle:** left/evicted members excluded from `Active()` but
  present in the merged roster.
- **Cache rotation:** `Record` twice → first snapshot is `previous.toml`;
  `Load` on empty dir → nils without error; writes are atomic (no tmp debris).
- **WasKnown:** id in previous-only snapshot still reports known; unknown id
  reports false.
- **Probe:** stub prober with mixed pass/fail → correct
  `Answered`/`Unreachable` partition; `AllAnswered`/`Partial` flags; context
  cancellation respected.

Fixtures: `t.TempDir()` cache dirs; catalogs built from the §9/§13 sample.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Rosters read from multiple member catalogs merge deterministically; mixed
  fed_ids hard-error.
- Clients persist current + previous snapshots per §14; rotation and
  `WasKnown` behave as specified; nothing treats the cache as authoritative.
- `Probe` produces accurate reachability accounting against stub probers,
  including down-member simulation.

## Related Proposal Sections

> **Client state caches** (advisory, never authoritative): every reading
> client persists current + previous federation snapshots
> (`~/.tailvault/cache/fed-<id>/`) — used to distinguish "was here, now
> offline" from "never existed" … Live pings always win.

> **Per-operation reachability scoping** — no global online requirement. …
> Roster lives in each member's catalog `[federation]` section, mirrored in
> clients' `locations.toml` + caches.

## Notes & Considerations

- **Gotcha:** never let `WasKnown` upgrade an advisory cache hit into an
  authoritative claim — it only *colors error messages* downstream ("last seen
  on pi-2 at <t>"); the error class is decided by live reachability alone.
- **Gotcha:** `Merge` newest-wins needs a tiebreak (member name, then status
  rank) to stay deterministic when timestamps collide.
- **For Next Task:** Task 32 composes `Roster.Active()` + `Probe` + per-member
  catalog queries into the resolution engine, and is where TV-FED errors are
  finally minted.
- **Prev:** [task-30-identity](./task-30-identity.md) ·
  **Next:** [task-32-resolution-engine](./task-32-resolution-engine.md)
