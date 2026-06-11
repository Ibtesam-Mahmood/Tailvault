# Task 56: Edge-Case Design — Consume `EDGE-CASES.md`, Cut the Final Backlog

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Edge-case discipline"; "Part II task breakdown" → Block 7 ("Designed only after Blocks 3–6, consuming `EDGE-CASES.md`; its own task set at that point") · **Block:** 6 — Edge-case handling · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Blocks 3–5 complete (tasks 27–55); `EDGE-CASES.md` non-empty (the running log seeded in task 27 and appended throughout Blocks 3–5). Block 7 (dogfood, tasks 57–59 + 26) runs **after** this block as the final validation — late entries it produces feed a future triage iteration, not this one · **Type:** Design

## Summary

This is the deliberately deferred design task (D31): this block was **not**
designed up front, because the edge cases worth handling can only be known
after the layers beneath exist and have been built, tested, and
security-analyzed. Its input is `EDGE-CASES.md` — the running log every
dev/QA appended to while building Blocks 3–5 (what was chosen, what was
punted, what worked). Its
output is a **reviewed edge-case backlog**: the log triaged and categorized,
a new set of granular `task-NN-*.md` implementation files cut for the cases
worth fixing, matching GitHub issues filed, and the remainder explicitly
punted with written rationale.

**Acceptance is the backlog, not code.** No edge case is fixed in this PR —
fixes land through the newly cut tasks, each its own branch/PR per the
standard workflow. The one hard rule for triage: anything that violates a
core invariant (hard-fail / never-silent-success, serverless, single-home,
no-own-crypto) is never puntable — it must become a task.

## Context

### Related packages

- `EDGE-CASES.md` — **consumed (triage-stamped) here.** Created in task 27;
  appended by every Block 3–5 task. The log stays open afterwards: Block 7's
  dogfood (tasks 57–59, 26) appends late entries for a future iteration.
- `tasks/task-NN-*.md` — **created here:** the new edge-case implementation
  task files, numbered **60+** (57–59 are taken by the dogfood block).
- `tasks/README.md` — **modified here:** the edge-case task table listing the
  new tasks, deps, and types.
- GitHub issues — one filed per cut task (`Task` label) per
  `CONTRIBUTING.md`; punted-but-tracked items filed as `Issue` (design
  gap/deferral) where they deserve a future hook.

### Prerequisites

- [ ] Tasks 51–55 merged (Block 5 security analysis closed; its findings
      register may itself contribute entries).
- [ ] `EDGE-CASES.md` is non-empty. If it is empty or thin, **stop** — that
      means the D31 discipline failed during Blocks 3–5; raise it with the
      maintainer rather than inventing edge cases from the armchair.
- [ ] Skim open `Issue`-labeled GitHub issues (GH-1..4 + any filed since) so
      the new backlog cross-links rather than duplicates them.

## Changes Required

### 1. Triage pass over `EDGE-CASES.md`

- **Action:** read every entry; normalize each into
  `{id, description, discovered-in (task/block), severity, category, status}`.
- Categories (adjust to what the log actually contains, but start here):
  - **Invariant-threatening** — silent success, data loss, lock/catalog/disk
    divergence, GC over-deletion. Never puntable.
  - **Partial-view / concurrency** — fan-out races, pending-intent
    interactions, mid-op member loss beyond what tasks 29/32/36 covered.
  - **Crash/recovery** — WAL lifecycle gaps, heal/verify blind spots.
  - **UX / error legibility** — confusing errors, missing `--json`, prompts
    in scripts.
  - **Performance/scale** — huge roots, slow links, Pi throughput.
  - **Already handled** — logged during a build, fixed by a later task;
    record where, close.
- Severity: does it break an invariant / lose data (must-fix) → degrade a
  guarantee's legibility (should-fix) → annoy (may-punt).

### 2. Cut the new task set

- **Action:** for every must-fix and accepted should-fix, write a granular
  `tasks/task-NN-<slug>.md` in the house format (template: task-09 for code
  tasks; one task ≈ one PR ≈ one branch `phase-N/<slug>`). Each must carry:
  the originating `EDGE-CASES.md` entry id(s), full acceptance criteria, the
  standard Validation Checklist (build/test/vet/gofmt + VERSION/CHANGELOG
  bump), and Prev/Next links forming the Block 7 chain.
- Keep tasks small and independent where possible; note deps explicitly.
- File a matching GitHub issue per task (`Closes #N` wiring per
  CONTRIBUTING.md), labeled for Block 7.

### 3. Punt list with rationale

- **Action:** every remaining entry gets an explicit disposition recorded in
  a closing "Triage outcome" section of `EDGE-CASES.md` (or a
  `docs/edge-case-triage.md` if the log is huge): **punted** + why (cost vs
  likelihood, mitigated by documented workaround, superseded by GH-3/GH-4
  style future work), or **rejected** + why it isn't actually a problem.
  A punt with no rationale is not a punt — it's a dropped ball.
- Punts that deserve a future hook become `Issue`-labeled GitHub issues.

### 4. tasks/README.md

- **Action:** add the Block 7 table (numbers, slugs, types, deps) and the
  closing note that this completes the plan's final block.

## Implementation Checklist

- [ ] Every `EDGE-CASES.md` entry triaged — zero entries without a
      disposition (task / punt / rejected / already-handled).
- [ ] No invariant-threatening entry is punted.
- [ ] New task files cut in the house format, numbered 60+, with Prev/Next
      chain and per-task acceptance criteria + originating entry ids.
- [ ] Matching GitHub issues filed (`Task` for cut work, `Issue` for tracked
      punts).
- [ ] `tasks/README.md` gains the Block 7 table.
- [ ] Triage outcome recorded; maintainer review of the backlog obtained
      (this is the "reviewed" in "reviewed backlog").

## Testing Requirements

A design task — no code, no new automated tests. Rigor checks instead:

- **Coverage check (scripted if convenient):** every entry id in
  `EDGE-CASES.md` appears exactly once in the triage outcome; every cut task
  cites ≥ 1 entry id; every must-fix maps to a task.
- **Format check:** each new task file contains the required sections and the
  verbatim Validation Checklist items (a `grep` over `tasks/task-6*`
  suffices).
- **Issue cross-check:** each cut task names its GH issue number; no
  duplicate of an existing GH-1..4 item.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A **reviewed edge-case backlog** exists: every logged edge case has exactly
  one written disposition; the maintainer has signed off on the cut/punt
  split.
- No entry that threatens hard-fail/never-silent-success, serverless,
  single-home, or no-own-crypto is punted.
- The cut tasks are executable standalone (an agent can pick one up without
  re-reading this task or the log) and are mirrored as GitHub issues.
- `tasks/README.md` reflects the new edge-case tasks; `EDGE-CASES.md` carries
  the triage outcome stamp (seeded task 27 → fed Blocks 3–5 → triaged here →
  stays open for Block 7 dogfood entries).

## Related Proposal Sections

> **Edge-case discipline.** `EDGE-CASES.md` is a running log: every dev/QA
> appends edge cases discovered while building Blocks 3–6 (what was chosen,
> punted, or worked). Block 7's design consumes that log — it is deliberately
> designed only after the layers beneath exist.

> **D31.** Block 7 — edge-case handling added as the FINAL block, after
> dogfood; designed only after the layers beneath exist. Discipline starting
> NOW: maintain a running `EDGE-CASES.md` log … Block 7's design consumes
> that log.

## Notes & Considerations

- **Gotcha:** the temptation here is to fix "quick ones" inline — don't.
  Mixed design+fix PRs blur review and break the one-task-one-PR rule; even a
  one-liner goes through a cut task.
- **Gotcha:** dispositions must cite evidence from the log ("hit twice during
  dogfood" beats "seems unlikely") — the whole point of deferring this design
  was to decide from experience, not speculation.
- **For Next Task:** the tasks cut *here* (60+) land first; then Block 7
  dogfood (57 → 58 → 59 → 26) validates the whole system and closes the plan.
- **Prev:** [task-55-privacy-audit-ssh-hardening](./task-55-privacy-audit-ssh-hardening.md) ·
  **Next:** [task-57-dogfood-config-matrix](./task-57-dogfood-config-matrix.md)
