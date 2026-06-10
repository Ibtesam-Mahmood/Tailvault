# Contributing & workflow

This is a small, single-maintainer project built in disciplined, traceable steps.
The rules below keep history clean and progress legible.

## Versioning

- [`VERSION`](./VERSION) is the **single source of truth** for the project
  version. It starts at `0.0.1` and stays in the `0.0.x` range until the first
  feature-complete MVP.
- **Every task bumps `VERSION` by exactly `+0.0.1`** (patch) and adds a matching
  `## v<version>` entry to the top of [`CHANGELOG.md`](./CHANGELOG.md) **in the
  same commit**.
- Never hardcode the version elsewhere. Once Go code exists, it will read
  `VERSION` (embedded at build time) rather than duplicating the string.
- Re-read `VERSION` before committing; if a push is rejected because someone
  else bumped it, rebase and re-apply your `+0.0.1` on top.
- Releases are tagged `v<version>` (e.g. `git tag v0.0.1`) at meaningful points.

## Tracking work: GitHub issues + local task files

Work is tracked in **two mirrored places**:

1. **GitHub issues** — the live backlog and status. Two labels:
   - **`Task`** — implementation work (a thing to build).
   - **`Issue`** — a design gap, deferral, or known limitation to revisit.
   Phase labels (`phase-0` … `phase-9`) group issues into their block.
2. **Local task files** in [`tasks/`](./tasks/) — `task-NN-<slug>.md`. These
   carry the durable detail that doesn't belong in a terse issue: acceptance
   criteria, schema snippets, reference images (drop them in
   [`tasks/assets/`](./tasks/assets/)), and notes. Each task file links to its
   GitHub issue and vice-versa.

[`tasks/README.md`](./tasks/README.md) holds the phase → block map and the
dependency/critical path.

## Working in blocks of PRs

- Work proceeds in **phase blocks** (see `tasks/README.md`). A block is one or
  more tightly-scoped PRs that together complete a phase.
- **One task ≈ one branch ≈ one PR.** Keep PRs small and reviewable.
- Branch naming: `phase-N/<short-slug>` (e.g. `phase-1/config-parse`).
- A PR must reference the issue it closes (`Closes #N`) and bump the version +
  changelog as above.
- `main` stays green and buildable. The bootstrap scaffold lands directly on
  `main`; all subsequent feature work goes through PRs.

## Commit messages

```
tailvault: <task summary> [<Task|Issue>] (#<issue>)      # task commit
tailvault: v<version> — <consolidation summary> (#<issue>) # version/merge commit
```

## Pre-PR checklist (applies once Go code exists)

- `go build ./...` succeeds.
- `go test ./...` passes.
- `go vet ./...` and `gofmt -l .` are clean.
- `VERSION` bumped `+0.0.1` and `CHANGELOG.md` has the matching entry.
