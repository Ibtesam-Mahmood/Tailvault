# Task 03 — Core engine

**Phase:** 3 · **Label:** `Task`, `phase-3` · **Est:** 3 d · **GitHub:** #_TBD_

## Goal

The working sync core: `track`, `status`, `push`, `pull`.

## Acceptance criteria

- [ ] `tailvault track <glob>` adds include rule(s) to `tailvault.toml`.
- [ ] `tailvault status` reports each managed file as
      local-only / pushed / drifted / orphaned.
- [ ] `tailvault push` hashes, uploads diffs (dedup by sha256), updates
      `tailvault.lock` (with pusher identity + timestamp); **fails loudly** if the
      node is down or an upload fails — a green push guarantees bytes landed.
- [ ] `tailvault pull` fetches blobs the current tree/branch needs.
- [ ] Integration test: round-trip a file laptop → node → fresh clone.

## References

- `proposal.md` — Phase 3; CLI surface; hard-fail semantics.
- Depends on Task 01 (config/lock/rules) + Task 02 (backend).
