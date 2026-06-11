# Task 57: Dogfood — guided config-matrix manual tests (local mock first)

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II task breakdown → Block 7 (Dogfood); Testing Strategy → Manual / acceptance · **Block:** 7 — Dogfood · **Type:** Acceptance · **Estimated Effort:** 1 ideal eng-day (guided sessions) · **Dependencies:** Blocks 3–6 complete (tasks 27–56). Uses the **local mock rig created here**; no real hardware required for this task.

## Summary

A guided manual-testing session: the AI assistant directs, the maintainer runs
the commands and reports output. The goal is to validate the system across the
**configuration matrix** — every meaningful combination of config knobs — on a
**local mock rig** (a generated test git repo with generated files + a
localhost "vault node"), so configuration bugs are flushed out cheaply before
any real hardware is involved (Task 26).

This task also **creates the reusable dogfood rig**: a script that conjures a
throwaway test repo (mixed file types/sizes straddling `min_size`), a local
vault directory served as an SSH-localhost or FS-path location, and a second
"member" directory for federation configs. Tasks 58, 59, and 26's dry-runs
reuse this rig — it is the repo-wide answer to "local mock tests with a test
repo and files."

Every surprise gets a dated entry in `EDGE-CASES.md`.

## Context

### Related packages

- `scripts/dogfood-rig.sh` — **created here.** Builds the local mock: temp git
  repo + generated files (sizes 1 KB–20 MB, types pdf/stl/bin/txt), localhost
  vault dir(s), optional second federation member dir. Idempotent; `--clean`.
- `docs/dogfood-matrix.md` — **created here.** The matrix checklist + recorded
  results.
- `EDGE-CASES.md` — appended throughout.

### Prerequisites

- [ ] Blocks 3–6 merged; binary built from main.
- [ ] SSH-to-localhost works (`ssh localhost true`) for the ssh-backend rows;
      FS/taildrive-style rows use a plain local path.
- [ ] Maintainer available for a guided session (AI directs, human runs).

## Changes Required

### scripts/dogfood-rig.sh — the local mock rig

- **File:** `scripts/dogfood-rig.sh`
- **Action:** create
- **Purpose:** one command to stand up/tear down the mock environment all
  dogfood tasks share.

Behavior: create `$(mktemp -d)/repo` (git-initialized, commit history with
generated files), `…/vault-a` and `…/vault-b` (vault roots), print exports for
the session. Generated files must straddle the default 5 MB `min_size` and
cover include/exclude glob shapes.

### docs/dogfood-matrix.md — the matrix + results

- **File:** `docs/dogfood-matrix.md`
- **Action:** create
- **Purpose:** checklist with one row per config combination; results recorded
  in place. Minimum rows:

| Dimension | Variants to cover |
| --- | --- |
| Backend | ssh (localhost) · taildrive-style (local path) |
| `min_size` | default 5MB · "1MB" · "10MiB" (binary-unit check) |
| Rules | include-only · include+exclude · `[[rules.overrides]]` first-match |
| History | off (default) · on for one pattern + `revert` round-trip |
| Retention | `auto_delete=true` default · `preserve=true` override |
| Ignore | `.tailvaultignore` patterns · `!re-include` · `track` override |
| Sync mode | git · manual · git↔manual flip via sync-mode command |
| Federation | single member · two local members (vault-a + vault-b) |
| Auth | password set · password unset (mutating remote op must fail closed per spec) |

Each row: setup commands, the probe commands, expected vs observed, pass/fail.

## Implementation Checklist

- [ ] `dogfood-rig.sh` builds and tears down the mock rig reproducibly.
- [ ] Every matrix row executed in a guided session; results recorded.
- [ ] Failures triaged: real bug → fix task/GH issue filed; surprise →
      `EDGE-CASES.md` entry.
- [ ] Matrix doc committed with the evidence filled in.

## Testing Requirements

This task IS manual testing; its artifacts must still be checked:

- `dogfood-rig.sh` runs clean on a fresh machine (shellcheck clean).
- A scripted smoke wrapper (`scripts/dogfood-matrix-smoke.sh`, optional)
  re-runs the 3–4 highest-value rows non-interactively so CI keeps them green.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- The rig script exists, is reusable by Tasks 58/59/26, and is documented.
- Every matrix row has a recorded pass (or a filed issue + edge-case entry).
- Binary size units verified ("5MB" = 5,242,880; "MiB" synonym).
- Password-unset row fails closed for mutating remote ops.

## Related Proposal Sections

> **Block 7 — Dogfood.** Per-command guided acceptance; automated demo-project
> tests by dev/QA; manual tests across configs and routes before the real use
> case.

## Notes & Considerations

- **Gotcha:** keep the rig's generated files deterministic (seeded) so re-runs
  compare cleanly.
- **For Next Task:** Task 58 reuses the rig to walk every command route.
- **Prev:** [task-56-edge-case-design](./task-56-edge-case-design.md) ·
  **Next:** [task-58-dogfood-route-walkthroughs](./task-58-dogfood-route-walkthroughs.md)
