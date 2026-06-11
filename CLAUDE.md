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

**Under active phased development (v0.0.104).** Design is frozen in
[`SPEC.md`](./SPEC.md); the build runs in phased PR blocks (Phases 0–9).
Implementation through **Blocks 0–4** — core repo workflow (init/track/status/
push/pull/gc) plus multi-node federation, identity, WAL, auth, and recovery — is
in place and test-covered (including a real-CLI end-to-end suite). No tagged
stable release yet. Writing implementation code here is expected as each phase
calls for it; keep changes small and aligned with the frozen design.

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

## Structure (Go + Cobra CLI)

The four file schemas, the error catalogue, the federation contract, and the
resolved Open Questions are frozen in [`SPEC.md`](./SPEC.md) — the normative
contract every implementation task cites. The implemented layout:

```
cmd/tailvault/main.go      # entry point (maps errors → bucketed exit codes)
internal/cli/              # Cobra command tree (setup/init/track/push/pull/gc/
                           #   verify/heal + vault/fed/ops subcommands + filters)
internal/config/           # tailvault.toml parse/validate + size units
internal/lock/             # tailvault.lock parse/write + per-path union merge driver
internal/pointer/          # pointer file format round-trip
internal/rules/            # min_size + include/exclude glob engine
internal/tserr/            # structured error codes (TV-CFG/NET/NODE/OBJ/FED/AUTH) + exit map
internal/backend/          # Backend interface + ssh, taildrive impls
internal/filter/           # clean/smudge driver
internal/hooks/            # git hook callouts
internal/gitglue/          # git command wrappers
internal/tailscale/        # status/ping/whois wrappers
internal/gc/ history/      # retention: mark-and-sweep GC + version history
internal/push/ pull/ status/ revert/   # repo-side workflow
internal/ingest/           # bootstrap/scan/track + ReplayOp + ProjectCatalog (WAL→catalog)
internal/catalog/          # meta/catalog.toml (self-describing vault state)
internal/wal/              # hash-chained write-ahead log + state markers
internal/identity/         # genesis record + file-ID derivation + pull receipts
internal/fed/              # federation roster, reachability, client cache
internal/auth/             # argon2id password hash + mutating-op gate
internal/locations/ setup/ # locations.toml registry + interactive node setup
internal/ops/              # pending/failed WAL op listing + retry
internal/verify/           # blob re-hash + 3-way integrity checks
internal/version/          # build-time version string (embedded via -ldflags)
internal/fedtest/ integration/   # multi-node test harness + e2e suites
```

Build/test: `make build` (embeds `VERSION`), `go build ./...`, `go test ./...`,
`go vet ./...`, `gofmt -l .`.

## Locked decisions (from proposal §"Open Questions" recommendations)

Language **Go**; first backend **SSH** (Taildrive later); default `min_size`
**5 MB**; identity via `tailscale whois` (fall back to git); MVP first
(SSH; `init/track/status/push/pull/gc`; no history, no Taildrive) then iterate.
Confirm with the maintainer before deviating.
