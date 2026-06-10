# Issue 01 — Interactive setup & node discovery (design addendum)

**Type:** `Issue` (design addendum) · **Label:** `Issue`, `phase-1`, `phase-2`
**GitHub:** #_TBD_ · **Slots into:** Phase 1–2

## What's unresolved

Today's `DESIGN.md` only specifies a hand-edited `~/.config/tailvault/locations.toml`.
We want a friendlier "attach to a project" experience without breaking the
"tailvault carries almost no auth code" principle.

## Proposed layered model (least → most privilege)

1. **Manual entry (default).** `tailvault setup` / `tailvault location add --interactive`
   wizard prompts for node name, MagicDNS/IP, base path, backend; writes
   `locations.toml`. No Tailscale auth involved. (Go prompt lib: `huh`/`survey`.)
2. **Local-daemon discovery (optional, no credentials).** Read the machine's
   existing session via `tailscale status --json` to offer a pick-list of tailnet
   nodes instead of manual typing. No separate login, no stored secrets.
3. **API/OAuth login (opt-in, stores a token).** Only this enumerates the tailnet
   from Tailscale's control server. Gated behind `tailvault login` / `--use-api`;
   token stored user-level (OS keychain), never in the repo. **Off by default.**

Plus `tailvault config set <key> <val>` for non-interactive `vault.toml` edits
(complements `init` / `track`).

## Why it's safe

Defaults need nothing from Tailscale auth; discovery only reads an
already-authenticated local daemon; credential storage is strictly opt-in. The
minimal-auth design holds.

## Next step

Fold tiers 1–2 into Phase 1–2 acceptance criteria; keep tier 3 as a separate
opt-in task. Capture the schema impact (none for tiers 1–2) when Task 00 freezes
`locations.toml`.

## Origin

Feasibility check from a maintainer `/btw` question, 2026-06-10.
