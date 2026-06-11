# Implementation backlog — 59 tasks in 7 blocks

Tailvault's [`proposal.md`](../proposal.md) is broken into **59 granular,
standalone tasks** grouped into **7 blocks**. Each task is one PR. Files are
zero-padded `task-NN-<slug>.md` and carry full context (an agent can execute one
without re-reading the proposal). Reference images go in [`assets/`](./assets/).

**Blocks 1–2 (tasks 01–25) are SHIPPED** (PR #1, v0.0.43). Blocks 3–7 implement
proposal Part II (federation); task numbering continues at 27, with the
pre-existing task-26 (dogfood) rewritten in place as Block 6 — it **runs after
Block 5** despite its lower number.

See [`../CONTRIBUTING.md`](../CONTRIBUTING.md) for the versioning / PR workflow.

## Block 1 — MVP (proposal Phases 0–5): usable SSH-backed tool wired into git ✅ shipped

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

## Block 2 — Hardening & extras (proposal Phases 6–8) ✅ shipped

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 20 | [history-refs](./task-20-history-refs.md) — opt-in `history`, `refs/<path-id>`, GC-exempt | Implementation | 14, 16 |
| 21 | [revert](./task-21-revert.md) — `revert <path> <sha>` | Implementation | 20 |
| 22 | [taildrive-backend](./task-22-taildrive-backend.md) — mounted-path backend + selection | Implementation | 09, 10 |
| 23 | [verify](./task-23-verify.md) — re-hash stored blobs, report corrupt/missing | Implementation | 09, 04 |
| 24 | [lock-merge-driver](./task-24-lock-merge-driver.md) — per-path union merge driver, installed by `init` | Integration | 04, 18 |
| 25 | [tests-docs-ci](./task-25-tests-docs-ci.md) — integration suite, user docs, CI workflow | Testing | Block 1 |

## Block 3 — Vault catalog + federation core (proposal Part II)

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 27 | [spec-v2-freeze](./task-27-spec-v2-freeze.md) — SPEC v2: catalog/WAL/genesis/receipt/ignore/roster/cache schemas, TV-FED codes, password file; creates EDGE-CASES.md | Foundation | Blocks 1–2 |
| 28 | [catalog](./task-28-catalog.md) — `internal/catalog` parse/write/atomic-update | Foundation | 27 |
| 29 | [wal](./task-29-wal.md) — `internal/wal` hash-chained intent log, WAL-as-lock, pruning | Foundation | 27 |
| 30 | [identity](./task-30-identity.md) — genesis records, file IDs, pull receipts | Foundation | 27 |
| 31 | [fed-roster-caches](./task-31-fed-roster-caches.md) — `internal/fed` roster + client state caches | Implementation | 27, 28 |
| 32 | [resolution-engine](./task-32-resolution-engine.md) — fan-out, `moved_to` forwarding, partial-view semantics | Implementation | 29, 30, 31 |
| 33 | [vault-init-bootstrap](./task-33-vault-init-bootstrap.md) — bootstrap ingestion, `.tailvaultignore`, WAL-resumable | Implementation | 28, 29, 30 |
| 34 | [vault-scan](./task-34-vault-scan.md) — disk↔catalog reconcile, catch-up WAL, edited-vs-corrupt | Implementation | 28, 29, 30 |
| 35 | [lock-v2-heal](./task-35-lock-v2-heal.md) — lock schema v2 (id+genesis), pull WARN, `heal` | Integration | 30, 32 |
| 36 | [gc-federation](./task-36-gc-federation.md) — pending-intent skip, git-only scoping, all-members gate | Integration | 29, 31, 32 |
| 37 | [ops-command](./task-37-ops-command.md) — `ops list`/`retry` for pending/failed WAL ops | Implementation | 29, 31 |
| 38 | [verify-3way](./task-38-verify-3way.md) — lock↔catalog↔disk verify + freshness | Integration | 28, 34, 35 |
| 39 | [fed-test-harness](./task-39-fed-test-harness.md) — multi-node stub harness + Block 3 suite | Testing | 27–38 |

## Block 4 — Remote interaction CLI + ingestion + moves (proposal Part II)

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 40 | [remote-hash-shortcircuit](./task-40-remote-hash-shortcircuit.md) — remote sha256 (DEV-C1 / GH-2); Block 4 prerequisite | Implementation | Blocks 1–2 |
| 41 | [vault-ls-stat](./task-41-vault-ls-stat.md) — browse logical tree + IDs + reachability | Implementation | 32, 40 |
| 42 | [vault-get-receipts](./task-42-vault-get-receipts.md) — download + pull receipts | Implementation | 32, 40 |
| 43 | [vault-put](./task-43-vault-put.md) — remote ingest, `--on-conflict`, vault-copy-is-original | Implementation | 29, 30, 46 |
| 44 | [vault-mv](./task-44-vault-mv.md) — intra/cross-location moves over SSH/rsync, `moved_to` | Implementation | 29, 32, 46 |
| 45 | [vault-rm-syncmode](./task-45-vault-rm-syncmode.md) — explicit delete + sync-mode mgmt | Implementation | 29, 46 |
| 46 | [vault-passwd-auth](./task-46-vault-passwd-auth.md) — per-node argon2id password + enforcement | Implementation | 27 |
| 47 | [fed-membership-cli](./task-47-fed-membership-cli.md) — `fed init/join/leave/evict/status` | Implementation | 31, 32, 46 |
| 48 | [restore-identity](./task-48-restore-identity.md) — manual identity recovery from receipts/locks | Implementation | 30, 28 |
| 49 | [track-manual-ingest](./task-49-track-manual-ingest.md) — register manually-dropped files | Implementation | 29, 30, 34 |
| 50 | [fed-integration-suite](./task-50-fed-integration-suite.md) — Block 4 suite on the harness | Testing | 39, 40–49 |

## Block 5 — Security analysis & hardening (proposal Part II)

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 51 | [threat-model](./task-51-threat-model.md) — assets, boundaries, adversaries, assumptions | Analysis | Blocks 3–4 |
| 52 | [auth-adversarial-review](./task-52-auth-adversarial-review.md) — attack the auth paths, fix findings | Analysis | 51 |
| 53 | [wal-chain-verify-tooling](./task-53-wal-chain-verify-tooling.md) — chain tamper detection tooling | Implementation | 51 |
| 54 | [fuzz-vuln-ci](./task-54-fuzz-vuln-ci.md) — fuzz new parsers; govulncheck in CI | Testing | 51 |
| 55 | [privacy-audit-ssh-hardening](./task-55-privacy-audit-ssh-hardening.md) — metadata-leak audit + SSH hardening doc | Documentation | 51 |

## Block 6 — Edge-case handling (designed post-implementation)

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 56 | [edge-case-design](./task-56-edge-case-design.md) — triage EDGE-CASES.md (fed by Blocks 3–5), design + cut the edge-case task set (numbered 60+) | Design | Blocks 3–5 |

## Block 7 — Dogfood (FINAL block: manual validation of the entire system + the real use case)

Guided manual sessions (AI directs, maintainer runs), local-mock-first via the
dogfood rig (generated test repo + files + localhost vaults, created in task
57), real hardware only at the end. **Each task is mirrored 1:1 as its own
GitHub issue.**

| # | Task | Type | Deps |
| --- | --- | --- | --- |
| 57 | [dogfood-config-matrix](./task-57-dogfood-config-matrix.md) — build the local mock rig; guided tests across the full config matrix | Acceptance | Blocks 3–6 |
| 58 | [dogfood-route-walkthroughs](./task-58-dogfood-route-walkthroughs.md) — guided walkthrough of every CLI route on the rig | Acceptance | 57 |
| 59 | [dogfood-failure-recovery-drills](./task-59-dogfood-failure-recovery-drills.md) — guided failure injection + recovery drills on the rig | Acceptance | 58 |
| 26 | [dogfood-root-pnp](./task-26-dogfood-root-pnp.md) — the real use case: `root-pnp` migration + 2-node federation walkthrough on real hardware; runbook + rollback | Acceptance | 59 (real hardware) |

## Critical path & parallelism

- **Blocks 1–2 critical path (done):** 01 → 02 → 09 → 14 → 17 → 19 → 25.
- **Part II critical path:** 27 → 29 → 32 → 35 → 39 → 50 → 51 → 56 → 57 → 58
  → 59 → 26.
- **Fan-out after 27:** 28 / 29 / 30 are independent; 31 follows 28; 40 and 46
  can land any time after Blocks 1–2 / task 27 respectively.
- **Block 4** commands (41–49) are mutually independent once their Block 3
  deps and 40/46 are in; 50 runs last. **Block 5** tasks 52–55 are independent
  after task 51 — whose STEP 0 is the maintainer manually running **Claude's
  security review** and committing the artifacts. **Block 7** is strictly
  sequential (mock rig → routes → drills → real hardware) and closes the plan.
- **Local mock testing:** tasks 39/50 (automated, stub multi-node harness +
  demo test repo) and tasks 57–59 (manual, dogfood rig) are the two layers of
  the no-real-hardware test story; task 26 is the only task requiring real
  nodes.
