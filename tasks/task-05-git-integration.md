# Task 05 — Git integration

**Phase:** 5 · **Label:** `Task`, `phase-5` · **Est:** 2 d · **GitHub:** #_TBD_

## Goal

Wire Tailvault into git itself so ordinary `push`/`pull` carry the blobs.

## Acceptance criteria

- [ ] `clean` filter replaces a managed file with a pointer file on stage;
      `smudge` restores real bytes on checkout. Pointer format per Task 00.
- [ ] `tailvault init` registers the filter in `.gitattributes` and installs
      hooks: `pre-push` (upload before push completes), `post-merge` &
      `post-checkout` (fetch needed blobs).
- [ ] A green `git push` guarantees blobs landed on the node (pre-push hard-fails
      otherwise).
- [ ] Tests: clean/smudge round-trip; hook scripts invoke the CLI correctly.

## Notes

- Completes the **MVP** (Phases 0–5): `init/track/status/push/pull/gc` over SSH.

## References

- `proposal.md` — Phase 5; git filter/hooks; Q6 (eager fetch on checkout).
