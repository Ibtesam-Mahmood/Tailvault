# Changelog

All notable changes to Tailvault are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/) and versions follow
[Semantic Versioning](https://semver.org/). The [`VERSION`](./VERSION) file is the
**single source of truth**; every task bumps it by `+0.0.1` and adds a matching
`## v<version>` heading here in the same commit.

## v0.0.34 — 2026-06-10

Phase 3 — init/revert exit-code wrapping (task-18/21 fix, R-C).

- `init` (toml write/stat failures) and `revert` (corrupt-lock) now return
  `tserr.ConfigErr` / TV-CFG-01 (exit 2) instead of a generic exit 1. Tests added for
  `init --location` and revert corrupt-lock.

## v0.0.33 — 2026-06-10

Phase 3 — status ManagedFiles pointer-aware size (task-13 fix, R-B C2).

- `status.ManagedFiles` now sizes via `status.ContentSize` (pointer-aware) instead of
  raw `os.Stat`, so a min_size-only file that is currently a clean pointer (~60-byte
  text) is no longer mis-dropped from the managed set during the pre-pull window. Same
  root cause as the v0.0.31 push fix, one layer up. Test `TestManagedFiles_MinSizeOnlyPointer`.

## v0.0.32 — 2026-06-10

Phase 3 — pull corrupt-vs-missing message (task-15 fix, R-B).

- A corrupt/mismatched blob on pull now returns a TV-OBJ-01 error whose cause names
  corruption and whose fix points at `tailvault verify`/re-store, distinct from the
  "missing object" message (still exit 5, still no overwrite).

## v0.0.31 — 2026-06-10

Phase 3 — push records real content size (task-14 fix, R-B).

- Added `status.ContentSize` (pointer-aware: uses `pointer.Decode().Size` when the working
  file is a clean pointer, else `os.Stat`); `push` now sources both `rules.Evaluate` and
  `Entry.Size` from it. Fixes a clean-pointer file (dedup branch) recording the ~60-byte
  pointer text length instead of the real content size (SPEC §2).

## v0.0.30 — 2026-06-10

Phase 3 — lock merge driver (task-24, `internal/lock/merge.go`).

- Added `lock.Merge`, a per-path union 3-way merge (newest `pushed_at` wins on a SHA
  conflict, deterministic tiebreak, `versions[]` unioned, canonical byte-identical
  output), exposed as the hidden `__merge-lock` command and registered as a git merge
  driver by `init`. Covered by a real `git merge` integration test.

## v0.0.29 — 2026-06-10

Phase 3 — `verify` command (task-23, `internal/verify`).

- Added `tailvault verify`: re-hashes blobs and cross-checks the lock — detects corruption
  (digest ≠ key), missing objects (lock SHA absent on the node), and reports orphans;
  history versions included.

## v0.0.28 — 2026-06-10

Phase 3 — `revert` command (task-21, `internal/revert`).

- Added `tailvault revert <path> <sha>`: repoints a history-on file to a recorded prior
  version and stages the lock. History-off / unknown-sha / unknown-path → typed errors;
  already-current is a no-op; a missing blob → TV-OBJ-01 (exit 5); `versions[]` left
  unchanged.

## v0.0.27 — 2026-06-10

Phase 3 — history (task-20, `internal/history` + push hook).

- Added optional per-file history: stable content-independent `PathID`/`RefKey`,
  `AppendVersion` (newest-first, dedup-head) and `ReadVersions`. Wired into `push.Run`
  at the task-20 seam — with history on, a content change appends to `refs/<path-id>`
  and `versions[]` instead of marking the superseded SHA for GC; GC keeps all history
  versions.

## v0.0.26 — 2026-06-10

Phase 3 — git hooks (task-19, `internal/hooks`).

- Added `InstallHooks`: installs pre-push / post-merge / post-checkout hooks (honouring
  `core.hooksPath`), embedding an absolute binary path, forwarding the pre-push exit code,
  idempotent, and warning on a pre-existing foreign hook.

## v0.0.25 — 2026-06-10

Phase 3 — `init` command (task-18, `internal/cli/init.go`).

- Implemented `tailvault init`: writes a `config.Default()` `tailvault.toml`,
  `.gitattributes` filter wiring, git config (filter + the `__merge-lock` merge driver),
  and installs hooks — idempotent, preserving any existing config. Not-a-git-repo
  → `tserr.ConfigErr` (exit 2).

## v0.0.24 — 2026-06-10

Phase 3 — clean/smudge filter (task-17, `internal/filter`).

- Added the git filter engine: `Clean` (byte→pointer, node-free so `git add` works
  offline) and `Smudge` (pointer→bytes, integrity-checked against the SHA). Missing blob
  → TV-OBJ-01 (exit 5); integrity mismatch → exit 5 with no bytes emitted. Hidden
  `filter-clean` / `filter-smudge` commands.

## v0.0.23 — 2026-06-10

Phase 3 — garbage collection (task-16, `internal/gc`).

- Added the mark-and-sweep GC: a pure `PlanSweep`/keep-set core (`BuildKeepSet`,
  `BuildPreserveSet`) and `Sweep`, with a branch-union keep-set assembled via gitglue,
  plus the `gc [--dry-run]` command. History versions and preserved files survive;
  cross-branch references are kept.

## v0.0.22 — 2026-06-10

Phase 2 — config-error wrapping + objMissing fix (WS-B follow-up).

- Config/lock load+parse failures now wrap to `tserr.ConfigErr` (TV-CFG-01, exit 2) at
  the command boundary (`loadConfig`/`loadLockOrEmpty`); leaf packages stay plain-error.
  Pinned by `TestStatus_BadConfig_IsTVCFGExit2`. Removed the interim local TV-CFG helpers
  in favour of the canonical `tserr.ConfigErr` (team-lead mandate).
- Fixed the `ObjMissing` error to strip the `objects/` key prefix (qa-review nit).

## v0.0.21 — 2026-06-10

Phase 2 — pull (task-15, `internal/pull`).

- Added `pull.Run(ctx, root, lk, Deps)` — integrity-checked pull that verifies each
  blob's SHA against the lock before materializing, hard-failing on a missing/mismatched
  object rather than silently succeeding.

## v0.0.20 — 2026-06-10

Phase 2 — push (task-14, `internal/push`).

- Added `push.Run(ctx, root, cfg, lk, Deps, Options)` — the critical-path push with
  fully injectable `Deps` (Backend, Preflight, Whois, GitIdentity, Now), preflight-first
  so an unreachable node fails before any partial write. Leaves a `TODO(task-20, WS-C)`
  seam for history-on version append.

## v0.0.19 — 2026-06-10

Phase 2 — Taildrive backend (task-22, `internal/backend/taildrive.go`).

- Added the `Taildrive` backend (`NewTaildrive(root)`) as a second `Backend` impl
  alongside SSH; passes the shared `RunContract` test.

## v0.0.18 — 2026-06-10

Phase 2 — status (task-13, `internal/status`).

- Added a pure `Classify` + `ScanTree`/`ManagedFiles` and the `status` command
  (`--check-blobs` to probe blob presence on the node).

## v0.0.17 — 2026-06-10

Phase 2 — setup + interactive node discovery (task-11, `internal/setup`).

- Added the `setup` command and interactive `location add` flow: `OnlinePeers` discovery
  over the tailscale fixture, a `Prompter` interface with a stdlib `StdinPrompter`. See
  DEVIATIONS (stdlib prompter chosen over an unlisted TUI dependency).

## v0.0.16 — 2026-06-10

Phase 2 — location registry (task-10, `internal/locations`).

- Added a user-level TOML location registry (XDG) with per-backend validation and a
  reachability `Check` (injected ping), plus the `location add` / `location ls` commands.

## v0.0.15 — 2026-06-10

Phase 2 — `track` command (task-12).

- Implemented `tailvault track <glob>...`: validate-all-before-mutate, append-only
  idempotent `config.AddInclude` + `ValidateGlob`, write-on-change, and an offline
  tree-walk that reports managed files via the rule engine (never contacts a node).
  Routes bad config through `tserr.ConfigErr`/TV-CFG-01 (exit 2).

## v0.0.14 — 2026-06-10

Phase 1 — lock read helpers (pulled into M1 to unblock WS-C).

- Added `lock.Parse`, `lock.Find`, and `lock.ReferencedSHAs` (`internal/lock/query.go`)
  — read-side helpers that gc/verify/revert (WS-C) build on. Updated the SPEC §8 lock row
  to match. Implemented (not stubbed) per team-lead.

## v0.0.13 — 2026-06-10

Phase 1 — config defaults + frozen Go API names (task-01/03 refinement, pulled into M1).

- Added `config.Default() Config`, the canonical zero-config baseline.
- Froze the public Go API names in SPEC §8 (`lock.Lock`/`lock.Entry`, the error-layering
  rule) so WS-B/WS-C build against stable identifiers — resolves the earlier
  `lock.File`-vs-`lock.Lock` naming inconsistency.

## v0.0.12 — 2026-06-10

Phase 1 — config error code (task-12 prereq, pulled into M1).

- Added `tserr.ConfigErr(cause, err)` / `TV-CFG-01` (exit bucket 2) so malformed config
  fails with the spec'd exit code 2 instead of the generic 1. Required by the upcoming
  command wiring (track, init, revert, status, push, pull).

## v0.0.11 — 2026-06-10

Phase 1 — storage backend (task-09, `internal/backend`).

- Added the `Backend` interface (`Stat`/`Get`/`Put`/`Delete`/`List`) with the SSH
  implementation (preflight ping → TV-NODE-01; perm/space → TV-NODE-02; missing object
  → TV-OBJ-01) and `FSBackend`, the in-tree stub all workstreams' tests use. Includes
  `HashObject`, an `ErrNotExist` sentinel, and an exported `RunContract` test helper.
  `Stat` of an absent key returns `Meta{Exists:false}, nil` (existence-as-data, for
  content-addressed dedup); only `Get` of a missing key errors. See DEVIATIONS.

## v0.0.10 — 2026-06-10

Phase 1 — Tailscale wrapper (task-08, `internal/tailscale`).

- Added a thin wrapper over the local `tailscale` CLI: `Client.Status` (peers sorted,
  MagicDNS dots trimmed; missing daemon → TV-NET-01, not-running → TV-NET-02), `Ping`,
  and `Whois`. Exec seam via a `Runner` interface; committed
  `testdata/status.json` fixture so tests need no real node.

## v0.0.9 — 2026-06-10

Phase 1 — pointer files (task-06, `internal/pointer`).

- Added the 4-line pointer format (`tailvault.v1` magic, `key SP value`) with
  `Encode`, `Decode` (strict reject of malformed input), and `IsPointer` sniffing.
  Round-trip and rejection tests.

## v0.0.8 — 2026-06-10

Phase 1 — rule engine (task-05, `internal/rules`).

- Added `rules.Evaluate(cfg, path, size) Decision{Managed,History,Preserve}` —
  `min_size` + include/exclude globs with first-match override precedence over a
  slash-normalized repo-relative path. Tests cover size boundary, include/exclude,
  overrides, and first-match ordering.

## v0.0.7 — 2026-06-10

Phase 1 — `tailvault.lock` state (task-04, `internal/lock`).

- Added `lock.Lock`/`Entry` with `Load`, `Canonicalize` (bytewise path sort, fixed
  field order, `versions` newest-first, RFC3339 UTC `pushed_at`), `Write`, `Upsert`,
  and `Remove`. Tests cover byte-stability, UTC normalization, versions ordering, and
  upsert/remove.

## v0.0.6 — 2026-06-10

Phase 1 — `tailvault.toml` config (task-03, `internal/config`).

- Added `config.Config` (storage + rules incl. per-pattern overrides), `Load`,
  `Validate`, and `Write`, plus `ParseSize` with **binary** units (`5MB` = 5242880;
  IEC synonyms accepted). Table-driven tests for round-trip, size vectors, and
  validation errors.
- Resolves SPEC §7 size-unit binding to binary (see DEVIATIONS).

## v0.0.5 — 2026-06-10

Phase 1 — structured error model (task-07, `internal/tserr`).

- Added `tserr.Error` with stable codes (`TV-NET-01/02`, `TV-NODE-01/02`, `TV-OBJ-01`),
  cause/fix rendering, `Unwrap()`, and `ExitCode()` mapped to buckets (NET→3, NODE→4,
  OBJ→5, default→2). Constructors per condition plus `ExitCodeFor(err)` (nil→0,
  untyped→1, typed→bucket).
- Wired into the CLI: `cli.Execute()` now returns `error`; `main` prints to stderr and
  exits with `tserr.ExitCodeFor(err)`. Commands return typed errors from `RunE`.

## v0.0.4 — 2026-06-10

Phase 0 — Go module + Cobra CLI skeleton (task-02).

- Scaffolded the Go module `github.com/Ibtesam-Mahmood/tailvault` (go 1.26) with
  Cobra v1.8.0: `cmd/tailvault/main.go` entry point, `internal/cli/root.go`
  (`Execute() int`), and one stub command per CLI verb (`setup`, `init`,
  `location add|ls`, `track`, `status`, `push`, `pull`, `gc`, `verify`, `revert`).
- `--version` embeds the `VERSION` file at build time via ldflags
  (`internal/version`); added a `Makefile` (build/test/vet/fmt) and a table-driven
  `root_test.go`.

## v0.0.3 — 2026-06-10

Phase 0 — spec freeze (task-01).

- Added **`SPEC.md`**, the normative frozen contract for Blocks 1–2: `tailvault.toml`
  fields/defaults/validation and rule-eval order (§1); `tailvault.lock` entry fields
  and canonical ordering (§2); the 4-line pointer format (§3); `locations.toml`
  schema and storage layout (§4); the error catalogue (`TV-NET/NODE/OBJ/CFG`) mapped
  to exit buckets 0/2/3/4/5 (§5); resolved open questions Q1–Q10 (§6); and the
  size-unit binding (decimal MB, binary MiB) (§7).
- `CLAUDE.md` planned-structure section now references `SPEC.md`.

## v0.0.2 — 2026-06-10

Spec refinement (no code yet).

- Renamed the repo-committed config/state files to `tailvault.toml` /
  `tailvault.lock` (from `vault.*`) across `proposal.md`, `DESIGN.md`, `CLAUDE.md`,
  and the task backlog.
- Specified **interactive setup + node discovery from the local Tailscale
  session** (`tailscale status --json`, pick-list + manual fallback, no Tailscale
  login or stored credentials); API/OAuth discovery is opt-in and deferred to
  Future. Folded into Phase 1 (`task-01`) and recorded the decision in `issue-01`.
- Specified a **structured error model**: typed conditions with stable codes
  (`TV-NET-*`, `TV-NODE-*`, `TV-OBJ-*`) + bucketed exit codes, preflight-first so
  an unreachable node fails clearly and leaves no partial state. Folded into
  Phase 2 (`task-02`); error-code catalogue added to the Phase 0 freeze.

## v0.0.1 — 2026-06-10

Project bootstrap.

- Imported the frozen design from the planning workspace: `proposal.md` and
  `DESIGN.md`.
- Established project structure, the versioning system (starting at `0.0.1`),
  and the task / issue / PR workflow — see [`CONTRIBUTING.md`](./CONTRIBUTING.md).
- Added project guidance in [`CLAUDE.md`](./CLAUDE.md).
- Seeded the phased implementation backlog (Phases 0–9) as local task files in
  [`tasks/`](./tasks/), with `.github` `Task` / `Issue` templates ready for when
  the backlog is mirrored to GitHub issues (not yet filed — repo is local-only).
