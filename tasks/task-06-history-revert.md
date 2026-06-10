# Task 06 — History & revert (opt-in)

**Phase:** 6 · **Label:** `Task`, `phase-6` · **Est:** 1.5 d · **GitHub:** #_TBD_

## Goal

Opt-in version history for files that want it, plus `revert`.

## Acceptance criteria

- [ ] `history = true` (per rule/file) keeps prior shas in `refs/<path-id>` on the
      node instead of the single-current-ref default.
- [ ] History-on blobs are exempt from auto-delete of superseded versions.
- [ ] `tailvault revert <path> <sha>` repoints a file to an older blob and updates
      `vault.lock`.
- [ ] Tests: enable history, push three versions, revert to the first.

## References

- `proposal.md` — Phase 6; history model; storage `refs/<path-id>`.
- Independent of Phase 7; slots in after Phase 5.
