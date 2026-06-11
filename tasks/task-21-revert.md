# Task 21: `tailvault revert` — repoint a history-on file to an older blob

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Detailed Design → "Revert flow"; CLI surface (`tailvault revert <path> <sha>`); Detailed Design → history model (`refs/<path-id>`, `versions[]`) · **Block:** 2 — Hardening & extras · **Estimated Effort:** 0.5 ideal eng-day · **Dependencies:** Task 20 (provides the history layer: `refs/<path-id>` newest-first ref lists, `versions[]` on history-on lock entries, and the path-id hashing used to key both) · **Type:** Implementation

## Summary

`tailvault revert <path> <sha>` rewinds a **history-on** file to a prior
content version. It repoints the lock entry's current `sha256` to the chosen
`<sha>` (which must already be one of that entry's recorded `versions[]` /
`refs/<path-id>`), re-smudges the working file from the blob at
`objects/<sha>`, and stages the modified `tailvault.lock` so the change is
captured in the next commit.

Per the proposal: *"For a history-on file, `tailvault revert <path> <sha>`
repoints the lock entry's current `sha256` to the chosen prior version from
`refs/<path-id>`, re-smudges the working file, and commits the lock change.
History-off files have no prior versions to revert to (by design)."*

The command must fail clearly and early in the two error cases: the file is
**history-off** (no versions to revert to — by design), or `<sha>` is **not
among** the file's recorded versions. Neither case touches the working tree or
the lock.

## Context

### Related packages

- `cmd/tailvault/revert.go` — **created here.** Cobra command wiring.
- `internal/revert` (or a `Revert` function in `internal/lock` consumers) —
  **created here.** The pure repoint-and-resmudge logic.
- `internal/lock` (Task 04) — read the entry for `<path>`, mutate its current
  `sha256`, write the canonical lock back. `versions[]` ordering is preserved
  (revert does not reorder history — it only moves the *current* pointer).
- `internal/store` / `internal/backend` (Tasks 08/09) — `Get` the blob bytes
  for `<sha>` to re-materialize the working file.
- `internal/pointer` (Task 06) / `internal/filter` (Task 17) — re-smudge writes
  the real bytes back into the working tree at `<path>`.
- `internal/tserr` (Task 07) — typed errors for the two failure modes and for a
  missing blob (`TV-OBJ-01`).

```mermaid
sequenceDiagram
    participant U as user
    participant TV as tailvault revert
    participant L as tailvault.lock
    participant N as storage node
    U->>TV: revert <path> <sha>
    TV->>L: load entry for <path>
    alt history == false
        TV-->>U: error: file is history-off
    else <sha> not in versions[]
        TV-->>U: error: unknown sha for <path>
    else ok
        TV->>N: Get objects/<sha>
        TV->>TV: write real bytes to working <path>
        TV->>L: set entry.sha256 = <sha>; stage lock
        TV-->>U: reverted; commit to persist
    end
```

### Prerequisites

- [ ] Task 20 merged: history-on entries carry `versions[]` and `refs/<path-id>`
      is maintained on push.
- [ ] A lock with at least one history-on entry that has ≥2 versions (for tests).

## Changes Required

### cmd/tailvault/revert.go

- **File:** `cmd/tailvault/revert.go`
- **Action:** create
- **Purpose:** define `tailvault revert <path> <sha>`; exactly two positional
  args; resolve config/lock/backend, call the revert logic, print a concise
  result, and map errors through `tserr` exit codes.

```go
func newRevertCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revert <path> <sha>",
		Short: "Repoint a history-on file to an older stored version",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, sha := args[0], normalizeSHA(args[1])
			return revert.Run(cmd.Context(), revert.Options{Path: path, SHA: sha})
		},
	}
}
```

Notes:

- Accept a short-prefix `<sha>` only if it unambiguously matches one full
  version; otherwise require the full hex (document the choice; full-hex-only is
  the simplest safe default).
- The command preflights node reachability (shared preflight from Task 09)
  before any `Get`, so an offline node fails as `TV-NODE-01` not mid-write.

### internal/revert/revert.go

- **File:** `internal/revert/revert.go`
- **Action:** create
- **Purpose:** the pure flow — validate, fetch, rewrite working file, mutate +
  write lock, stage it.

```go
type Options struct {
	Path string
	SHA  string
}

func Run(ctx context.Context, opt Options) error {
	lk := lock.MustLoad()
	e, ok := lk.Entry(opt.Path)
	if !ok {
		return tserr.New(tserr.Config, "no vault-managed file at %q", opt.Path)
	}
	if !e.History {
		return tserr.New(tserr.Config,
			"%q is history-off: no prior versions to revert to (by design)", opt.Path)
	}
	if !contains(e.Versions, opt.SHA) {
		return tserr.New(tserr.Config,
			"sha %s is not a recorded version of %q", short(opt.SHA), opt.Path)
	}
	if e.SHA256 == opt.SHA {
		return nil // already at that version — no-op, exit 0
	}
	// fetch blob -> write real bytes to working tree at opt.Path
	// (TV-OBJ-01 if objects/<sha> missing on the node)
	// e.SHA256 = opt.SHA ; persist canonical lock ; git add tailvault.lock
}
```

Key Considerations:

- **Do not** drop or reorder `versions[]`: revert only moves the *current*
  `sha256`. The full history stays intact so you can revert forward again.
- Re-smudge by writing the decoded blob bytes directly (reuse the smudge path
  from Task 17) so the working file is the real content, not a pointer.
- Stage `tailvault.lock` (`git add`) but **do not auto-commit** — the proposal
  says "commits the lock change" via the normal flow; staging + a clear "commit
  to persist" message is the safe interpretation that keeps the user in control.
  (If Task 20 established auto-commit, match it; otherwise stage-only.)
- A missing blob for an otherwise-valid version is `TV-OBJ-01` (integrity), exit
  code `5` — not a config error.

## Implementation Checklist

- [ ] `revert <path> <sha>` command with `ExactArgs(2)`.
- [ ] History-off entry → clear typed error, no working-tree/lock change.
- [ ] `<sha>` not in `versions[]` → clear typed error, no change.
- [ ] Unknown `<path>` → clear typed error.
- [ ] Already-current `<sha>` → no-op, exit 0.
- [ ] Valid revert: fetch blob, rewrite working file with real bytes.
- [ ] Set `entry.sha256 = <sha>`, write canonical lock, stage it.
- [ ] Missing blob surfaces `TV-OBJ-01`.

## Testing Requirements

`internal/revert/revert_test.go` — reuse the **stub `Backend`** from Task 09
(in-memory `objects/<sha>` map) so no real node is needed.

| Case | Setup | Expect |
|---|---|---|
| Revert restores old sha + bytes | history-on entry, current=`B`, versions=`[B,A]`, blob `A` present | lock `sha256`==`A`; working file bytes == blob `A`; `versions` unchanged |
| History-off → error | entry with `history=false` | typed error, exit `2`; lock + working file untouched |
| Unknown sha → error | revert to a sha not in `versions[]` | typed error, exit `2`; no change |
| Unknown path → error | `<path>` not in lock | typed error |
| Already current → no-op | revert to the current sha | no error, no lock rewrite, exit `0` |
| Missing blob | version listed but `objects/A` absent in stub | `TV-OBJ-01`, exit `5` |

Assert on bytes (`Get` of the rewritten working file) and on the persisted lock
round-trip, not just return values.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Reverting a history-on file to a recorded prior sha leaves the lock's current
  `sha256` equal to that sha and the working file equal to that blob's bytes.
- A history-off file produces a clear, typed "no prior versions (by design)"
  error and changes nothing.
- A sha not among the file's versions produces a clear, typed error and changes
  nothing.

## Related Proposal Sections

> **Revert flow.** For a history-on file, `tailvault revert <path> <sha>`
> repoints the lock entry's current `sha256` to the chosen prior version from
> `refs/<path-id>`, re-smudges the working file, and commits the lock change.
> History-off files have no prior versions to revert to (by design).

> **CLI surface:** `tailvault revert <path> <sha>` — history-on files: repoint to
> an older blob.

## Notes & Considerations

- **Gotcha:** revert is the *only* reason a history-on entry's current `sha256`
  moves backward — keep it the single code path that does so, and never let it
  truncate `versions[]`.
- **Gotcha:** preflight before fetching, so an offline node never leaves a
  half-rewritten working file.
- **For Next Task:** Task 23 (`verify`) re-hashes the blobs that revert relies
  on; a corrupt version blob is the realistic failure revert must surface.
- **Prev:** [task-20-history-refs](./task-20-history-refs.md) ·
  **Next:** [task-22-taildrive-backend](./task-22-taildrive-backend.md)
