# Task 07 — Taildrive backend

**Phase:** 7 · **Label:** `Task`, `phase-7` · **Est:** 1 d · **GitHub:** #_TBD_

## Goal

A mounted-path backend so the storage node can run **only** Tailscale (no SSH).

## Acceptance criteria

- [ ] `internal/backend` gains a Taildrive (mounted-path) implementation behind
      the same `Backend` interface.
- [ ] Backend selection reads `backend = "ssh" | "taildrive"` from
      `locations.toml`.
- [ ] Same content-addressed layout (`objects/<sha256>`) over the mounted path.
- [ ] Tests against a local temp dir standing in for the mount.

## References

- `proposal.md` — Phase 7; backend abstraction.
- Independent of Phase 6; requires Task 02's `Backend` interface.
