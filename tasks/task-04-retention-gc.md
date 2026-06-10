# Task 04 — Retention + GC

**Phase:** 4 · **Label:** `Task`, `phase-4` · **Est:** 2 d · **GitHub:** #_TBD_

## Goal

Delete detection and garbage collection that match the bloat-averse defaults.

## Acceptance criteria

- [ ] Detect files removed from git and, when `auto_delete = true`, mark their
      blobs for deletion on the node.
- [ ] `preserve` flag (per file) exempts a blob from auto-delete.
- [ ] Per-branch GC: a blob is only pruned when unreferenced across all relevant
      branches (mark-on-push, explicit sweep).
- [ ] `tailvault gc [--dry-run]` prunes unreferenced blobs per retention policy;
      dry-run reports what *would* be deleted.
- [ ] Tests cover: delete → GC, `preserve` survives GC, shared blob not pruned
      while still referenced.

## References

- `proposal.md` — Phase 4; retention model; Q4 (GC trigger).
- `DESIGN.md` — retention / auto-delete / preserve.
