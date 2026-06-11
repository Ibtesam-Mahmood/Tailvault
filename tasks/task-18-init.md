# Task 18: `tailvault init`

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** CLI surface (`tailvault init`); Distribution & Rollout ("`tailvault init` writes tailvault.toml, .gitattributes, and installs hooks"); Distribution & Rollout → Rollback ("the tool is additive … removing the filter/hooks … returns to plain git") · **Block:** 1 — MVP · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 17 (clean/smudge filter — provides the `filter-clean`/`filter-smudge` commands that `.gitattributes` + git config point at); Task 03 (config parse/write — provides the `tailvault.toml` writer + defaults) · **Type:** Integration

## Summary

`tailvault init` is the **non-interactive** repo bootstrap (the interactive
sibling is `setup`, Task 11). One command makes a plain git repo tailvault-ready:

1. Write a default `tailvault.toml` (via Task 03's writer) with the locked
   defaults — `min_size = "5MB"`, `history = false`, `auto_delete = true`, and
   empty `include`/`exclude` for the user to fill via `track`.
2. Register the filter driver: add the `filter=tailvault` line(s) to
   `.gitattributes` and set the git config
   `filter.tailvault.clean = "tailvault filter-clean %f"`,
   `filter.tailvault.smudge = "tailvault filter-smudge %f"`, and
   `filter.tailvault.required = true`.
3. Install the git hooks — delegating the hook **bodies** to Task 19's installer.

Every step is **idempotent**: re-running `init` is safe and converges (no
duplicate `.gitattributes` lines, no clobbered user edits to `tailvault.toml`,
hooks re-installed in place). This is what makes `init` safe to run on an existing
repo and re-run after upgrades.

## Context

### Related packages

- `cmd/tailvault/init.go` — **created here.** Orchestrates the three steps.
- `internal/config` (Task 03) — `WriteDefault(path)` / detect-and-skip if a
  `tailvault.toml` already exists.
- `internal/gitglue` (Task 02) — locate the repo root + `.git` dir; run `git
  config filter.tailvault.*`.
- `internal/hooks` (Task 19) — **`InstallHooks(repoRoot, binPath)`**; init calls
  it, hook bodies live there.
- `.gitattributes` (repo file) — gets the `filter=tailvault` line(s).

### Prerequisites

- [ ] Task 17 merged: `filter-clean` / `filter-smudge` commands exist to point at.
- [ ] Task 03 merged: `tailvault.toml` writer + locked defaults.
- [ ] Task 19's `InstallHooks` available (or stubbed) for the hook step.

## Changes Required

### cmd/tailvault/init.go

- **File:** `cmd/tailvault/init.go`
- **Action:** create
- **Purpose:** the `init` command: write config, register filter, install hooks —
  all idempotent.

```go
// init steps (each a no-op if already done):
// 1. if no tailvault.toml at repo root -> config.WriteDefault(); else leave it.
// 2. ensure .gitattributes contains a filter line for the default globs, e.g.
//      *.pdf  filter=tailvault -text
//    (append missing lines only; never duplicate an existing identical line).
// 3. git config filter.tailvault.clean/smudge/required (set, overwriting is fine
//    — values are deterministic).
// 4. hooks.InstallHooks(repoRoot, absSelfPath).
```

Notes:

- **`.gitattributes` idempotency:** read existing lines, append only those not
  already present (exact-line match). Default attribute lines cover the
  `tailvault.toml` `include` globs (or a sensible default set if empty); document
  that `track` (Task 12) also appends here.
- **git config** is set via `git config filter.tailvault.clean "tailvault
  filter-clean %f"` etc. Re-setting is inherently idempotent (overwrites with the
  same value).
- **`tailvault.toml`:** if present, **do not overwrite** — print "already
  initialized, leaving tailvault.toml" so a user's edits survive a re-run.
- Resolve the **absolute path to the running binary** (`os.Executable()`) to hand
  to `InstallHooks` so hooks invoke tailvault by absolute path (Task 19).

Key Considerations:

- Must run from anywhere inside the repo: resolve the repo root first; error with
  a typed config code (exit 2) if not in a git repo.
- `init` does **not** require a reachable node — it only writes local files; no
  preflight. (Contrast: `push`/`pull` preflight.)
- Keep filter registration and hook install **additive** so Rollback (remove
  filter/hooks → plain git) holds.

## Implementation Checklist

- [ ] Writes a default `tailvault.toml` with locked defaults if absent; leaves an
  existing one untouched.
- [ ] Appends `filter=tailvault` line(s) to `.gitattributes` without duplicating.
- [ ] Sets `filter.tailvault.clean`, `.smudge`, `.required` git config.
- [ ] Calls `hooks.InstallHooks(repoRoot, absBinPath)`.
- [ ] Fully idempotent: a second `init` changes nothing and exits 0.
- [ ] No node preflight (local-only command).

## Testing Requirements

`cmd/tailvault/init_test.go` — run against a `t.TempDir()` `git init` repo:

- **Fresh init:** after `init`, assert `tailvault.toml` exists with defaults,
  `.gitattributes` contains a `filter=tailvault` line, hook files exist (delegate
  assertion to Task 19's installer or check the files directly), and `git config
  --get filter.tailvault.clean` returns the expected command.
- **Idempotent re-run:** capture `.gitattributes` + `tailvault.toml` bytes after
  the first `init`; run `init` again; assert the bytes are unchanged (no
  duplicated attribute lines, config still set, hooks still present).
- **Existing config preserved:** pre-write a `tailvault.toml` with a custom
  `min_size`; `init`; assert it is **not** overwritten.
- **Not a git repo:** `init` in a non-repo dir → typed config error, exit 2.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A single `tailvault init` on a plain git repo produces `tailvault.toml`,
  `.gitattributes` filter lines, the three `filter.tailvault.*` git config keys,
  and installed hook files.
- Re-running `init` is a no-op (idempotent) and never duplicates lines or clobbers
  user config.
- Removing the filter config, `.gitattributes` lines, and hooks returns the repo
  to plain git (additive guarantee).

## Related Proposal Sections

> `tailvault init  # non-interactive: write tailvault.toml + .gitattributes,
> install hooks`

> **Install on a repo:** `tailvault init` writes `tailvault.toml`,
> `.gitattributes`, and installs hooks; `tailvault location add` registers the
> node.

> **Rollback:** the tool is additive — pointers + lock are just files; removing
> the filter/hooks and restoring real files from the vault returns to plain git.

## Notes & Considerations

- **Gotcha:** don't overwrite an existing `tailvault.toml` — re-init after the
  user added rules must not wipe them.
- **Gotcha:** `.gitattributes` lines must match exactly to avoid duplicates;
  trim/normalize before comparing.
- **For Next Task:** Task 19 owns the hook bodies and the `InstallHooks` function
  this command calls; keep the `(repoRoot, binPath)` signature stable.
- **Prev:** [task-17-clean-smudge-filter](./task-17-clean-smudge-filter.md) ·
  **Next:** [task-19-git-hooks](./task-19-git-hooks.md)
