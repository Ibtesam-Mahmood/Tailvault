# Implementation backlog

Tailvault is built in **10 phases (0–9)**. Each phase is a **block** of one or
more PRs that together complete it. These local files mirror the GitHub issues
and carry the durable detail (acceptance criteria, schemas, reference images in
[`assets/`](./assets/)) that doesn't fit in a terse issue.

See [`../proposal.md`](../proposal.md) for the full rationale and
[`../CONTRIBUTING.md`](../CONTRIBUTING.md) for the workflow.

## Phase → block map

| Phase | File | Block goal | Est. |
| --- | --- | --- | --- |
| 0 | [`task-00-decisions-spec-freeze.md`](./task-00-decisions-spec-freeze.md) | Resolve open questions; freeze `tailvault.toml` / `tailvault.lock` / pointer schemas. | 0.5 d |
| 1 | [`task-01-foundation.md`](./task-01-foundation.md) | Go module + Cobra CLI; config/lock parse/write; rule engine. | 2 d |
| 2 | [`task-02-backend-ssh.md`](./task-02-backend-ssh.md) | Backend interface; SSH impl; Tailscale liveness checks. | 2 d |
| 3 | [`task-03-core-engine.md`](./task-03-core-engine.md) | `track`, `status`, `push` (upload/dedup/lock), `pull`. | 3 d |
| 4 | [`task-04-retention-gc.md`](./task-04-retention-gc.md) | Delete detection, `auto_delete`, `preserve`, per-branch GC. | 2 d |
| 5 | [`task-05-git-integration.md`](./task-05-git-integration.md) | `clean`/`smudge` filter + pointer format; git hooks. | 2 d |
| 6 | [`task-06-history-revert.md`](./task-06-history-revert.md) | Opt-in `history`; `refs/<path-id>`; `revert`. | 1.5 d |
| 7 | [`task-07-taildrive-backend.md`](./task-07-taildrive-backend.md) | Mounted-path backend; backend selection from config. | 1 d |
| 8 | [`task-08-harden-tests-docs.md`](./task-08-harden-tests-docs.md) | `verify`, lock-merge driver, unit/integration tests, docs. | 3 d |
| 9 | [`task-09-dogfood-root-pnp.md`](./task-09-dogfood-root-pnp.md) | Real migration of `root-pnp`; verify lean clone + reliable sync. | 1 d |

## Critical path & MVP

- **MVP** = Phases 0–5 + light tests (SSH only; `init/track/status/push/pull/gc`;
  no history, no Taildrive). ~10 ideal eng-days.
- **Full v1** = all phases. ~18 ideal eng-days.
- Phases are mostly sequential. Phase 7 (Taildrive) and Phase 6 (history) are
  independent of each other and can slot in after the core engine (Phase 3) and
  git integration (Phase 5) respectively.

## Conventions

- Each task file: `task-NN-<slug>.md`, links its GitHub issue once filed.
- Drop reference images / mockups in [`assets/`](./assets/) and link them from the
  relevant task file.
