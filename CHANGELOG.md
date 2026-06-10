# Changelog

All notable changes to Tailvault are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/) and versions follow
[Semantic Versioning](https://semver.org/). The [`VERSION`](./VERSION) file is the
**single source of truth**; every task bumps it by `+0.0.1` and adds a matching
`## v<version>` heading here in the same commit.

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
