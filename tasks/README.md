# Implementation backlog — 26 tasks in 3 blocks

Tailvault's [`proposal.md`](../proposal.md) is broken into **26 granular,
standalone tasks** grouped into **3 blocks**. Each task is one PR. Files are
zero-padded `task-NN-<slug>.md` and carry full context (an agent can execute one
without re-reading the proposal). Reference images go in [`assets/`](./assets/).

See [`../CONTRIBUTING.md`](../CONTRIBUTING.md) for the versioning / PR workflow.

## Block 1 — MVP (proposal Phases 0–5): usable SSH-backed tool wired into git

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 01 | [spec-freeze](./task-01-spec-freeze.md) — lock toml/lock/pointer + error-code + locations schemas | Foundation | — |
| 02 | [go-module-cli-skeleton](./task-02-go-module-cli-skeleton.md) — Cobra root, stub subcommands, `--version` from `VERSION` | Foundation | 01 |
| 03 | [config-parse](./task-03-config-parse.md) — `internal/config` `tailvault.toml` parse/validate/write | Foundation | 02 |
| 04 | [lock-parse](./task-04-lock-parse.md) — `internal/lock` `tailvault.lock` parse/write (canonical) | Foundation | 02 |
| 05 | [rule-engine](./task-05-rule-engine.md) — `internal/rules` min_size + globs + overrides | Foundation | 03 |
| 06 | [pointer-format](./task-06-pointer-format.md) — `internal/pointer` encode/decode round-trip | Foundation | 02 |
| 07 | [error-model](./task-07-error-model.md) — `internal/tserr` typed codes + exit buckets | Foundation | 02 |
| 08 | [tailscale-wrapper](./task-08-tailscale-wrapper.md) — `internal/tailscale` status/ping/whois | Implementation | 02, 07 |
| 09 | [backend-ssh](./task-09-backend-ssh.md) — `Backend` interface + SSH impl | Implementation | 07, 08 |
| 10 | [locations-registry](./task-10-locations-registry.md) — `locations.toml`; `location add`/`ls` | Implementation | 08, 07 |
| 11 | [interactive-setup](./task-11-interactive-setup.md) — `setup` pick-list from local session + manual fallback | Implementation | 10, 08 |
| 12 | [track](./task-12-track.md) — `track <glob>` writes config rule | Implementation | 03, 05 |
| 13 | [status](./task-13-status.md) — local-only/pushed/drifted/orphaned | Implementation | 04, 05, 09 |
| 14 | [push](./task-14-push.md) — hash/upload/dedup/lock, preflight hard-fail | Implementation | 09, 04, 05, 06, 07 |
| 15 | [pull](./task-15-pull.md) — fetch needed blobs + integrity | Implementation | 09, 04, 06 |
| 16 | [gc-retention](./task-16-gc-retention.md) — delete-detect, auto_delete, preserve, per-branch GC, `--dry-run` | Implementation | 14, 04 |
| 17 | [clean-smudge-filter](./task-17-clean-smudge-filter.md) — git filter driver | Integration | 06, 14, 15 |
| 18 | [init](./task-18-init.md) — write config, `.gitattributes`, register filter | Integration | 17, 03 |
| 19 | [git-hooks](./task-19-git-hooks.md) — pre-push / post-merge / post-checkout | Integration | 14, 15, 18 |

## Block 2 — Hardening & extras (proposal Phases 6–8)

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 20 | [history-refs](./task-20-history-refs.md) — opt-in `history`, `refs/<path-id>`, GC-exempt | Implementation | 14, 16 |
| 21 | [revert](./task-21-revert.md) — `revert <path> <sha>` | Implementation | 20 |
| 22 | [taildrive-backend](./task-22-taildrive-backend.md) — mounted-path backend + selection | Implementation | 09, 10 |
| 23 | [verify](./task-23-verify.md) — re-hash stored blobs, report corrupt/missing | Implementation | 09, 04 |
| 24 | [lock-merge-driver](./task-24-lock-merge-driver.md) — per-path union merge driver, installed by `init` | Integration | 04, 18 |
| 25 | [tests-docs-ci](./task-25-tests-docs-ci.md) — integration suite, user docs, CI workflow | Testing | Block 1 |

## Block 3 — Dogfood (proposal Phase 9)

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 26 | [dogfood-root-pnp](./task-26-dogfood-root-pnp.md) — migrate `root-pnp`; lean clone, reliable sync, GC reclaim, rollback doc | Acceptance | Block 1 (+23, 24) |

## Critical path & parallelism

- **Critical path:** 01 → 02 → 09 → 14 → 17 → 19 → 25 → 26.
- **Fan-out after 02:** 03 / 04 / 06 / 07 are independent; 05 follows 03.
- **MVP** ships after Block 1 (~10 ideal eng-days). Block 2 items (20–24) are
  mutually independent and can land in any order / in parallel.
