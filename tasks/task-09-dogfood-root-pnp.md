# Task 09 — Dogfood on root-pnp

**Phase:** 9 · **Label:** `Task`, `phase-9` · **Est:** 1 d · **GitHub:** #_TBD_

## Goal

Prove Tailvault on the motivating case: migrate `root-pnp`'s ~1.1 GB of blobs off
git history onto the home Tailscale node.

## Acceptance criteria

- [ ] Migrate `root-pnp` behind a branch: `init`, `track` the large globs
      (`**/*.pdf`, `**/*.stl`, `**/*.3mf`, `**/*.pptx`), push blobs to the node.
- [ ] A **fresh clone** is lean (pointers only) and `pull` restores real bytes.
- [ ] Push/pull verified reliable across a few edit/delete cycles; GC reclaims
      space for deleted files.
- [ ] Document the migration steps and rollback (remove filter/hooks → restore
      real files → plain git).

## References

- `proposal.md` — Phase 9; phased adoption / rollback safety.
- The whole MVP + hardening must be in place first.
