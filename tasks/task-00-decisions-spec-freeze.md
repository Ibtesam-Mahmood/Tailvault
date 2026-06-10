# Task 00 — Decisions & spec freeze

**Phase:** 0 · **Label:** `Task`, `phase-0` · **Est:** 0.5 d · **GitHub:** #_TBD_

## Goal

Resolve the open questions from `proposal.md` and freeze the three schemas so
implementation can begin without churn.

## Acceptance criteria

- [ ] Open questions Q1–Q8 each have a recorded decision (default to the
      proposal's recommendations unless the maintainer overrides):
  - Q1 Language → **Go**
  - Q2 First backend → **SSH**
  - Q3 Lock-merge → per-path union merge driver; assume single writer early
  - Q4 GC trigger → mark-on-push, explicit `gc` sweep
  - Q5 Default `min_size` → **5 MB**
  - Q6 Pointer resolution → eager fetch on `post-checkout`
  - Q7 Identity stamp → `tailscale whois`, fall back to git
  - Q8 Scope → **MVP first** (Phases 0–5), then iterate
  - Q9 Node discovery → read the **local Tailscale session** (`tailscale status
    --json`) for a pick-list + manual fallback; **no API login** in v1 (opt-in,
    Future). See [`issue-01-interactive-setup-node-discovery.md`](./issue-01-interactive-setup-node-discovery.md).
  - Q10 Error model → typed conditions with stable codes (`TV-NET/NODE/OBJ-*`) +
    bucketed exit codes; preflight-first so node-down leaves no partial state.
- [ ] `tailvault.toml` schema frozen (fields, defaults, validation rules).
- [ ] `tailvault.lock` schema frozen (entry fields, ordering, canonical form).
- [ ] Pointer file format frozen (line format, version tag).
- [ ] Error-code catalogue frozen (code → cause → fix → exit code).
- [ ] `locations.toml` schema frozen (incl. fields filled by discovery).
- [ ] Schemas captured as a short `SPEC.md` (or a `DESIGN.md` addendum).

## References

- `proposal.md` — Open Questions section + recommendations.
- `DESIGN.md` — config/lock/pointer schemas.
