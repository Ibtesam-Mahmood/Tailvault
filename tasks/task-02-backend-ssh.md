# Task 02 — Backend interface + SSH

**Phase:** 2 · **Label:** `Task`, `phase-2` · **Est:** 2 d · **GitHub:** #_TBD_

## Goal

A pluggable `Backend` interface with a working SSH implementation and Tailscale
liveness checks.

## Acceptance criteria

- [ ] `internal/backend` defines `Backend` (put/get/exists/delete/list by sha256).
- [ ] SSH backend streams blobs over `ssh user@node` (Tailscale SSH for
      transport/auth); content-addressed paths `objects/<sha256>`.
- [ ] `internal/tailscale` wraps `tailscale status` / `ping` / `whois`.
- [ ] Liveness check: **hard-fail** with a clear error when the node is
      unreachable before any push.
- [ ] `internal/tserr`: typed conditions with stable codes (`TV-NET-*` Tailscale
      down/logged-out, `TV-NODE-*` node unreachable/path not writable, `TV-OBJ-*`
      missing/corrupt blob), each carrying cause + a concrete fix, mapped to
      bucketed exit codes (2 config, 3 network, 4 node, 5 integrity).
- [ ] **Preflight-first**: every command needing the node checks reachability and
      aborts before any partial work, so a node-down failure leaves no partial
      upload and an unadvanced lock.
- [ ] `tailvault location ls` lists registered locations with live reachability.
- [ ] `tailvault location add` writes `~/.config/tailvault/locations.toml`.
- [ ] Tests with a stub/loopback backend.

## References

- `proposal.md` — Phase 2; Tailscale leverage points.
- `DESIGN.md` — storage layout; locations registry.
