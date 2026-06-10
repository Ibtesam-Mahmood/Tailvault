# Changelog

All notable changes to Tailvault are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/) and versions follow
[Semantic Versioning](https://semver.org/). The [`VERSION`](./VERSION) file is the
**single source of truth**; every task bumps it by `+0.0.1` and adds a matching
`## v<version>` heading here in the same commit.

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
