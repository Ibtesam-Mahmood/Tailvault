# Task 12: `tailvault track <glob>` — append an include rule and report newly matched files

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** CLI surface (`tailvault track <glob>` — "add include rule(s) to tailvault.toml"); Detailed Design — `[rules]` block · **Block:** 1 — MVP · **Estimated Effort:** 0.5 day · **Dependencies:** Task 03 (provides `internal/config` parse/write of `tailvault.toml`), Task 05 (provides the rule engine that evaluates include/exclude/min_size against the tree) · **Type:** Implementation

## Summary

`tailvault track <glob>` is the ergonomic way to start managing a file class without hand-editing TOML. It appends one (or more) `include` glob(s) to the `[rules]` block of `tailvault.toml`, de-duplicating against existing includes and validating that each glob is well-formed, then re-runs the rule engine over the working tree and reports which existing files now match as vault-managed.

The command must be **idempotent**: tracking a glob that is already present is a no-op on the file (it neither duplicates the entry nor reorders the list) and simply reports current matches. It must also be **safe**: an invalid glob is rejected before the config is touched, so a typo never corrupts `tailvault.toml`.

The "newly tracked files" report closes the loop for the user — after `tailvault track "**/*.stl"` they immediately see the STLs in the tree that just became vault-managed, which is the cue to run `tailvault push`. This task does not push or hash anything; it only edits config and reports rule-engine matches.

## Context

### Related packages
- `internal/config` (Task 03) — `Load()`/`Save()` for `tailvault.toml`; the `Config.Rules.Include []string` field. This task mutates and re-serialises that struct, preserving the canonical field/section ordering Task 03 defines.
- `internal/rules` (Task 05) — `Engine.Match(path)` / a tree-walk that returns the set of managed files given the merged rules. Used to compute the "now matches" report.
- `cmd/tailvault/track.go` — the Cobra command (stubbed in Task 02).

### Prerequisites
- [ ] Task 03 merged: `tailvault.toml` round-trips losslessly (write preserves unrelated sections like `[storage]` and `[[rules.overrides]]`).
- [ ] Task 05 merged: rule engine can enumerate matching files under the repo root.

## Changes Required

**File:** `internal/config/track.go`
**Action:** Create.
**Purpose:** Pure helpers to validate a glob and add it to the include list with de-dup.

```go
package config

// ValidateGlob returns an error if pattern is not a usable doublestar glob.
func ValidateGlob(pattern string) error {
    if strings.TrimSpace(pattern) == "" {
        return fmt.Errorf("empty glob")
    }
    if _, err := doublestar.Match(pattern, "x"); err != nil { // syntax probe
        return fmt.Errorf("invalid glob %q: %w", pattern, err)
    }
    return nil
}

// AddInclude appends pattern to c.Rules.Include if absent.
// Returns added=false when the pattern was already present (idempotent).
func (c *Config) AddInclude(pattern string) (added bool) {
    for _, g := range c.Rules.Include {
        if g == pattern {
            return false
        }
    }
    c.Rules.Include = append(c.Rules.Include, pattern)
    return true
}
```

Implementation Notes:
- Validate **all** patterns first, then mutate — partial application must not happen if any glob is bad (exit 2, config bucket).
- De-dup is exact-string; do not attempt glob-equivalence normalisation.

Key Considerations:
- Preserve existing include ordering; append only. Task 24's lock merge driver and human diffs both benefit from stable ordering.

---

**File:** `cmd/tailvault/track.go`
**Action:** Modify (replace stub).
**Purpose:** Load config, validate + add glob(s), save, then run the rule engine and print the matches.

```go
// tailvault track <glob>...
cfg, err := config.Load(repoRoot)        // exit 2 on missing/bad config
for _, g := range args {
    if err := config.ValidateGlob(g); err != nil { return tserr.Config(err) }
}
changed := false
for _, g := range args { changed = cfg.AddInclude(g) || changed }
if changed { config.Save(repoRoot, cfg) }

eng := rules.New(cfg.Rules)
matches := eng.WalkMatches(repoRoot)     // []string of managed files now
// print: "tracking <glob>" (or "already tracked"); then matched files
```

Implementation Notes:
- Accept multiple globs (`track <glob>...`) — the proposal says "include rule(s)".
- Report format: one header line per glob (`tracking **/*.stl` or `already tracked: **/*.stl`), then a sorted list of currently-matching files under a `matches:` heading. Keep it plain and greppable.
- Only call `config.Save` when something actually changed.

Key Considerations:
- The match report uses the **full merged rules** (size threshold + all includes + excludes + overrides), not just the new glob — a file matches only if it also clears `min_size` and isn't excluded. State this so users aren't surprised a sub-5MB `.stl` doesn't appear.

## Implementation Checklist
- [ ] `ValidateGlob` rejects empty/malformed patterns.
- [ ] `AddInclude` is idempotent and append-only; returns `added` correctly.
- [ ] Command validates all globs before mutating; bad glob → exit 2, config untouched.
- [ ] `config.Save` only on actual change; unrelated TOML sections preserved.
- [ ] Match report enumerates via the rule engine and is sorted.
- [ ] Multiple globs supported in one invocation.

## Testing Requirements

Go table tests: `internal/config/track_test.go` (pure helpers) + a command-level test using a temp repo dir fixture.

| Case | Setup | Expect |
|---|---|---|
| Adds include rule | config with empty include; `track "**/*.pdf"` | `Include == ["**/*.pdf"]`; `added==true`; saved |
| Idempotent on repeat | include already `["**/*.pdf"]`; `track "**/*.pdf"` | `added==false`; include unchanged; no needless reorder |
| Invalid glob rejected | `track "[bad"` | error; config struct unmodified |
| Newly-tracked files reported | temp tree with `a.pdf` (≥min_size), `b.txt`; `track "**/*.pdf"` | report lists `a.pdf`, not `b.txt` |
| Sub-threshold not reported | `small.pdf` < min_size present | `small.pdf` absent from matches |
| Multiple globs | `track "**/*.pdf" "**/*.stl"` | both appended; both validated before any mutation |
| Excluded file stays out | `exclude=["drafts/**"]`, `drafts/x.pdf` present | not in matches |

Fixtures/stubs:
- A temp repo dir with a minimal `tailvault.toml` and a few files of varying sizes/extensions (write real bytes ≥ `min_size` for the "should match" files).
- No backend needed (the **stub Backend from Task 09** and the **Task 08 tailscale fixture** are not exercised — `track` is offline and never contacts a node).

## Validation Checklist
- [ ] `go build ./...`, `go test ./...`, `go vet ./...` pass.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to `CHANGELOG.md` in the same commit (per CONTRIBUTING.md).

## Acceptance Criteria
- `tailvault track <glob>` appends a de-duplicated, validated include rule to `tailvault.toml`.
- Re-tracking the same glob is a no-op on config and still reports current matches.
- An invalid glob fails (exit 2) without modifying config.
- The command reports which existing files now match via the rule engine.
- VERSION + CHANGELOG bumped in the same commit.

## Related Proposal Sections
- **CLI surface** — "`tailvault track <glob>` — add include rule(s) to tailvault.toml".
- **Detailed Design — `[rules]`** — `include = ["**/*.pdf", "**/*.stl", …]`, `exclude`, `min_size`, overrides; "files >= this are vault-managed".

## Notes & Considerations
- **Gotcha:** matching is the **intersection** of size + include − exclude + override precedence; `track` only edits `include`, so a tracked-but-tiny file legitimately won't appear in the report.
- **Gotcha:** preserve TOML comments/sections on `Save` — that is Task 03's responsibility, but a careless re-serialise here would wipe `[[rules.overrides]]`. Test for it.
- **For Next Task:** Task 13 (`status`) consumes the same rule engine to enumerate managed files, then diffs them against `tailvault.lock`.
- Prev: [task-11-interactive-setup.md](./task-11-interactive-setup.md) · Next: [task-13-status.md](./task-13-status.md)
