# CLAUDE.md — Tailvault

Guidance for working in this repository. Keep changes small, traceable, and
aligned with the frozen design.

## What Tailvault is

A Tailscale-aware, content-addressed large-file storage CLI that syncs with
`git push` / `git pull`. Real bytes live in a content-addressed folder on a
Tailscale node; the repo carries only small pointer files and a `tailvault.lock`.
A **clean** alternative to Git LFS (not a wrapper) using git-native
filter/hooks. History is **off by default** (single current ref per file),
diffs/deletes are still tracked, auto-delete is **on** by default, all retention
is opt-out per file. Hard-fails on a down node or missing blob — never a silent
success.

## Status

**Bootstrap (v0.0.1).** Design is frozen; no Go code yet. The build is executed
in phased PR blocks (Phases 0–9). This repo graduated from a planning workspace —
unlike that workspace, **writing implementation code here is expected** once a
phase calls for it.

## Authoritative docs (read these first)

- [`proposal.md`](./proposal.md) — formal proposal: architecture, CLI surface,
  9-phase plan, effort estimates, open questions with recommendations.
- [`DESIGN.md`](./DESIGN.md) — the golden design dump: `tailvault.toml` / `tailvault.lock`
  / pointer schemas, retention model, Tailscale leverage, rejected options.
- [`CONTRIBUTING.md`](./CONTRIBUTING.md) — versioning, task/issue/PR workflow.
- [`tasks/README.md`](./tasks/README.md) — phase → block map and critical path.

## Versioning (hard rule)

- [`VERSION`](./VERSION) is the **single source of truth**. Starts at `0.0.1`.
- Every task bumps it `+0.0.1` (patch) and adds a `## v<version>` entry to the
  top of [`CHANGELOG.md`](./CHANGELOG.md) in the **same commit**. Re-read
  `VERSION` before committing; rebase if a push is rejected.
- Never hardcode the version in source — embed `VERSION` at build time.

## Workflow

- Work is tracked as **GitHub issues** (`Task` = build work, `Issue` = design
  gap/deferral; `phase-N` labels group blocks) **and** mirrored as local files in
  [`tasks/`](./tasks/) that carry the durable detail (acceptance criteria,
  schemas, reference images in `tasks/assets/`).
- Work in **blocks of PRs**: one task ≈ one branch (`phase-N/<slug>`) ≈ one PR.
  PRs `Closes #N`, bump version + changelog, and keep `main` green.
- The bootstrap scaffold lands on `main`; feature work goes through PRs.

## Planned structure (Go — not yet created)

The four file schemas, the error catalogue, and the resolved Open Questions are
frozen in [`SPEC.md`](./SPEC.md) — the normative contract every implementation
task cites. Per `proposal.md`, the implementation will be a Go + Cobra CLI:

```
cmd/tailvault/main.go      # entry point
internal/config/           # tailvault.toml parse/validate
internal/lock/             # tailvault.lock parse/write + merge driver
internal/tserr/            # structured error codes (TV-NET/NODE/OBJ) + exit map
internal/rules/            # min_size + include/exclude glob engine
internal/backend/          # Backend interface + ssh, taildrive impls
internal/pointer/          # pointer file format round-trip
internal/filter/           # clean/smudge driver
internal/hooks/            # git hook callouts
internal/gc/               # mark-and-sweep per-branch GC
internal/gitglue/          # git command wrappers
internal/tailscale/        # status/ping/whois wrappers
```

Build/test (once code exists): `go build ./...`, `go test ./...`,
`go vet ./...`, `gofmt -l .`.

## Locked decisions (from proposal §"Open Questions" recommendations)

Language **Go**; first backend **SSH** (Taildrive later); default `min_size`
**5 MB**; identity via `tailscale whois` (fall back to git); MVP first
(SSH; `init/track/status/push/pull/gc`; no history, no Taildrive) then iterate.
Confirm with the maintainer before deviating.
