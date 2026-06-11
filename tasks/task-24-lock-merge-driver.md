# Task 24: `tailvault.lock` merge driver — per-path union merge

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Risk Assessment (*"`tailvault.lock` merge conflicts … Custom git merge driver (per-path union)"*); Open Questions Q3 (per-path union; single active writer early); Implementation Plan → Phase 8 (lock-merge driver) · **Block:** 2 — Hardening & extras · **Type:** Integration · **Estimated Effort:** 0.75 ideal eng-day · **Dependencies:** Task 04 (provides the **canonical** `tailvault.lock` form — stable entry ordering + serialization — so unions diff and re-serialize cleanly), Task 18 (provides `tailvault init`, which is where the driver gets registered: `.gitattributes` + `git config`)

## Summary

`tailvault.lock` is committed to the repo, so two branches that both push will
produce conflicting locks on merge. A naive 3-way text merge garbles a TOML list
of `[[entry]]` tables. This task adds a **custom git merge driver** that merges
the lock semantically: a **per-path union** of entries keyed by `path`.

Rules:

- An entry whose `path` exists on only one side (ours/theirs) is **kept** as-is
  (disjoint edits union cleanly).
- An entry whose `path` exists on both sides with the **same `sha256`** is a
  no-op (identical) — keep one copy.
- An entry whose `path` exists on both sides with **differing `sha256`** is a
  genuine conflict — resolve **deterministically**: **newest `pushed_at` wins**
  (with a documented tiebreak), or, if you prefer strictness, emit a real
  conflict marker. Default to newest-`pushed_at`-wins for a clean automatic
  merge under the proposal's "single active writer early" assumption.

`tailvault init` registers it so git uses it automatically: add
`tailvault.lock merge=tailvault` to `.gitattributes` and set
`merge.tailvault.driver` in git config.

## Context

### Related packages

- `cmd/tailvault/merge_lock.go` (or a hidden `tailvault __merge-lock` subcommand)
  — **created here.** The driver entry point git invokes with the `%O %A %B`
  files (base / ours / theirs).
- `internal/lock` (Task 04) — **edited/extended here.** Add `Merge(base, ours,
  theirs Lock) (Lock, error)` and reuse the canonical writer so the result
  re-serializes identically regardless of input ordering.
- `cmd/tailvault/init.go` (Task 18) — **edited here.** Register the driver:
  `.gitattributes` line + `git config merge.tailvault.*`.
- `internal/gitglue` (if present) — wrapper to run `git config`.

```mermaid
sequenceDiagram
    participant G as git merge
    participant D as tailvault __merge-lock %O %A %B
    participant M as lock.Merge
    G->>D: base=%O ours=%A theirs=%B
    D->>M: parse all three locks
    M->>M: union entries by path
    M->>M: same path + same sha -> keep one
    M->>M: same path + diff sha -> newest pushed_at wins
    M-->>D: canonical merged lock
    D->>G: write result to %A; exit 0 (or 1 if unresolved)
```

### Prerequisites

- [ ] Task 04 merged: canonical lock parse/write (stable ordering).
- [ ] Task 18 merged: `init` writes `.gitattributes` + installs hooks (so there's
      a place to also write the merge config).

## Changes Required

### internal/lock/merge.go

- **File:** `internal/lock/merge.go`
- **Action:** create
- **Purpose:** the pure 3-way union merge over parsed locks.

```go
// Merge performs a per-path union 3-way merge. base may be empty (Lock{}).
// Returns the merged lock and whether any path needed conflict resolution.
func Merge(base, ours, theirs Lock) (merged Lock, err error) {
	byPath := map[string]Entry{} // accumulate union, ours-then-theirs
	for _, e := range ours.Entries   { byPath[e.Path] = e }
	for _, e := range theirs.Entries {
		o, both := byPath[e.Path]
		switch {
		case !both:
			byPath[e.Path] = e          // disjoint -> union
		case o.SHA256 == e.SHA256:
			// identical -> keep (prefer the one with richer versions[])
		default:
			byPath[e.Path] = resolve(o, e) // differing sha -> deterministic winner
		}
	}
	// emit entries in canonical (path-sorted) order via the Task 04 writer
}

// resolve: newest pushed_at wins; tiebreak on lexicographically greater sha256
// for total determinism. For history-on entries, union versions[] newest-first.
func resolve(a, b Entry) Entry { /* ... */ }
```

Notes:

- **Determinism is the contract:** the same three inputs must always produce the
  same output on every machine — hence the `pushed_at` + sha tiebreak, never
  wall-clock or map-iteration order. Always re-serialize through the canonical
  writer so ordering is path-sorted.
- **History-on entries:** when both sides differ, the winner's current `sha256`
  is chosen by `pushed_at`, but `versions[]` should be **unioned** (newest-first,
  deduped) so no historical blob reference is lost — keeps GC's keep-set and
  `revert` correct after a merge.
- `base` is informational (helps distinguish add-vs-modify) but the union rule
  doesn't strictly need it; accept and ignore gracefully if empty.

### cmd/tailvault/merge_lock.go

- **File:** `cmd/tailvault/merge_lock.go`
- **Action:** create
- **Purpose:** the driver git calls. Hidden command taking three file paths.

```go
// Registered as: tailvault __merge-lock %O %A %B
// %O base, %A ours (also the OUTPUT file git reads back), %B theirs.
&cobra.Command{
	Use:    "__merge-lock <base> <ours> <theirs>",
	Hidden: true,
	Args:   cobra.ExactArgs(3),
	RunE: func(_ *cobra.Command, a []string) error {
		base, _  := lock.LoadFile(a[0]) // missing base => empty
		ours, err := lock.LoadFile(a[1]); /* handle */
		theirs, err := lock.LoadFile(a[2]); /* handle */
		merged, err := lock.Merge(base, ours, theirs)
		if err != nil { return err }            // exit 1 -> git marks conflict
		return lock.WriteFile(a[1], merged)     // write result back into %A
	},
}
```

### cmd/tailvault/init.go — register the driver

- **File:** `cmd/tailvault/init.go` (Task 18)
- **Action:** edit
- **Purpose:** make git use the driver automatically.

- Append to `.gitattributes` (idempotently):
  ```
  tailvault.lock merge=tailvault
  ```
- Set git config (repo-local) in `init`:
  ```
  git config merge.tailvault.name   "tailvault lock per-path union merge"
  git config merge.tailvault.driver "tailvault __merge-lock %O %A %B"
  ```

Key Considerations:

- The driver binary is `tailvault` itself (a hidden subcommand) so there's
  nothing extra to install — `init` just points git at the current binary.
- Exit `0` writes a resolved `%A` git accepts; exit non-zero leaves the conflict
  for the user (use this only if you choose the strict "surface conflict" policy
  for differing-sha — the default is auto-resolve).
- Be idempotent: re-running `init` must not duplicate the `.gitattributes` line
  or the git config.

## Implementation Checklist

- [ ] `lock.Merge` per-path union with deterministic differing-sha resolution.
- [ ] History-on `versions[]` unioned (newest-first, deduped) on merge.
- [ ] Hidden `__merge-lock <base> <ours> <theirs>` command writes back to `%A`.
- [ ] Result always re-serialized via the canonical (Task 04) writer.
- [ ] `init` appends `.gitattributes` line idempotently.
- [ ] `init` sets `merge.tailvault.name` + `merge.tailvault.driver`.

## Testing Requirements

`internal/lock/merge_test.go` — pure, no git needed for the merge logic:

| Case | ours | theirs | Expect |
|---|---|---|---|
| Disjoint paths union | entry `a` | entry `b` | merged has both `a` and `b` |
| Same path, same sha | `a@X` | `a@X` | one `a@X`, no conflict |
| Same path, diff sha | `a@X` pushed earlier | `a@Y` pushed later | merged `a@Y` (newest `pushed_at`) |
| Tiebreak | `a@X` same `pushed_at` | `a@Y` same | deterministic (greater sha wins), stable across runs |
| History union | `a` versions `[X]` | `a` versions `[Y,Z]` | winner's `versions` = union newest-first deduped |
| Canonical output | entries given out of order | — | output is path-sorted, byte-identical to a fresh canonical write |

Plus one **git-integration** test (build the binary, `git init` a temp repo,
register the driver via `init`, create two branches each editing the lock for
disjoint paths, `git merge` → assert the merged lock contains both entries and
the merge succeeded without manual resolution).

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Two branches editing the lock for **disjoint paths** merge cleanly into a union
  with no manual conflict resolution.
- A **same-path, differing-sha** divergence resolves deterministically by the
  documented rule (newest `pushed_at`, sha tiebreak), identically on every run.
- `tailvault init` registers the driver (`.gitattributes` + git config)
  idempotently, and git invokes it on a real merge.

## Related Proposal Sections

> **Risk Assessment:** `tailvault.lock` merge conflicts between clients … Custom
> git merge driver (per-path union); single-writer in practice early on (Open Q2).

> **Q3 — `tailvault.lock` conflict policy.** … ship a per-path union merge driver;
> assume single active writer early.

## Notes & Considerations

- **Gotcha:** the result MUST go through the canonical writer or you reintroduce
  ordering-only conflicts on the *next* merge.
- **Gotcha:** don't lose history — union `versions[]`, don't pick one side's list
  wholesale, or GC/`revert` can later find a referenced blob missing.
- **For Next Task:** Task 25's integration suite should exercise this driver as
  part of multi-branch scenarios.
- **Prev:** [task-23-verify](./task-23-verify.md) ·
  **Next:** [task-25-tests-docs-ci](./task-25-tests-docs-ci.md)
