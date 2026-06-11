# Task 58: Dogfood — guided walkthrough of every command route

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II task breakdown → Block 7 (Dogfood); CLI surface (Parts I + II) · **Block:** 7 — Dogfood · **Type:** Acceptance · **Estimated Effort:** 1 ideal eng-day (guided sessions) · **Dependencies:** Task 57 (the local mock rig + matrix baseline).

## Summary

A guided manual session (AI directs, maintainer runs) that walks **every CLI
route** end-to-end on the local mock rig from Task 57 — the complete surface,
not a sample. Where Task 57 varied *configuration*, this task varies *the
path taken through the tool*: every command, its major flags, and the
realistic sequences users will actually chain them in.

The deliverable is `docs/dogfood-routes.md`: one section per route group with
the exact commands, expected output, and observed result — which doubles as a
user-facing cookbook afterwards. Surprises go to `EDGE-CASES.md`; real bugs
get GH issues.

## Context

### Related packages

- `scripts/dogfood-rig.sh` (Task 57) — reused for every route.
- `docs/dogfood-routes.md` — **created here.**
- `EDGE-CASES.md` — appended throughout.

### Prerequisites

- [ ] Task 57 complete (rig works; matrix passed).
- [ ] Two-member local federation rig variant available (vault-a + vault-b).

## Changes Required

### docs/dogfood-routes.md — route checklist (one section per group)

**Route group 1 — repo lifecycle (git glue):**
`setup` (interactive) · `init` · `location add/ls` · `track` · `status` →
commit → `push` (direct + via pre-push hook) → fresh clone → `pull` (smudge +
post-checkout) → edit/rename/delete cycles → `status` drift states →
`gc --dry-run`/`gc` → `verify` (3-way) · history-on file `revert` round-trip ·
lock merge driver on a two-branch conflict.

**Route group 2 — vault remote interaction (no repo checkout):**
`vault init` (bootstrap, ignore file honored) · `vault ls`/`stat` (IDs, sync
modes, reachability) · `vault put` (all three `--on-conflict` modes) ·
`vault get` (receipt written; manual-file freshness reported) · `vault mv`
(intra-location, then cross-member; ID stable, path changes) · `vault rm` ·
sync-mode flip · `vault scan` after a hand-moved file · `track` manual-ingest.

**Route group 3 — federation membership:**
`fed init` · `fed join` (second local member) · `fed status` (roster +
reachability) · resolution after a mv (pull WARN → `heal` → clean) ·
`fed leave` (detach warnings in a referencing repo) · re-join.

**Route group 4 — maintenance:**
`ops list` (empty + with a manufactured pending op) · `ops retry` ·
`vault passwd` (set, change) · `restore-identity` from a receipt and from a
lock entry · `wal verify` clean run · `--version`, help texts, exit codes
spot-checked against the SPEC buckets (0/2/3/4/5/6).

Each route entry records: command(s), expected behavior (cited from SPEC /
task acceptance criteria), observed output, pass/fail.

## Implementation Checklist

- [ ] Every command in the v1 + v2 CLI surface exercised at least once.
- [ ] Every route group section completed with evidence.
- [ ] Exit-code spot-checks match SPEC buckets.
- [ ] Bugs → GH issues; surprises → `EDGE-CASES.md`.

## Testing Requirements

Manual by design; the scriptable spine (`scripts/dogfood-routes-smoke.sh`,
optional) re-runs route group 1 plus one put/get/mv loop non-interactively
against the rig for CI continuity.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- 100% of CLI commands walked; no route ends in undocumented behavior.
- `docs/dogfood-routes.md` committed with full evidence — usable as a cookbook.
- All found bugs filed; all surprises logged to `EDGE-CASES.md`.

## Related Proposal Sections

> **CLI surface (v2 additions).** `tailvault vault ls|stat|get|put|mv|rm|scan|
> passwd|restore-identity` · `tailvault fed init|join|leave|evict|status` ·
> `tailvault ops [list]|retry` · `tailvault heal`.

## Notes & Considerations

- **Gotcha:** run route group 2 from a directory that is NOT a tailvault repo
  — that's the point of checkout-free interaction.
- **For Next Task:** Task 59 deliberately breaks what this task proved works.
- **Prev:** [task-57-dogfood-config-matrix](./task-57-dogfood-config-matrix.md) ·
  **Next:** [task-59-dogfood-failure-recovery-drills](./task-59-dogfood-failure-recovery-drills.md)
