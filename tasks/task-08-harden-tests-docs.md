# Task 08 — Harden, tests & docs

**Phase:** 8 · **Label:** `Task`, `phase-8` · **Est:** 3 d · **GitHub:** #_TBD_

## Goal

Make Tailvault robust enough to trust with real data.

## Acceptance criteria

- [ ] `tailvault verify` re-hashes stored blobs and reports corruption / missing.
- [ ] Custom per-path **union** merge driver for `tailvault.lock` (Q3) so concurrent
      pushes don't clobber each other; installed by `init`.
- [ ] Unit + integration test coverage across config/lock/rules/backend/engine/gc.
- [ ] User docs: install, `init`, `track`, day-to-day push/pull, recovery.
- [ ] `go vet` + `gofmt` clean; CI workflow runs build + test on push.

## References

- `proposal.md` — Phase 8; Q3 (lock-merge); integrity checks.
