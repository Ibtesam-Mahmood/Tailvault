# Error codes, exit codes & known issues

How Example Project reports failure, and the rough edges to expect from a pre-1.0 build.

---

## Error codes & exit codes

Failures are **structured**: a stable code, a one-line cause, a concrete fix, and a
**bucketed exit code**. The `pre-push` hook surfaces the same code so a failed push
reads obviously rather than as a generic git error.

| Code | Meaning | Exit |
| --- | --- | --- |
| `TV-CFG-*` | Bad `example-project.toml`, unknown/missing location, unparseable pointer or lock | 2 |
| `TV-AUTH-01` | Password missing/rejected on a mutating remote op (refused before any work) | 2 |
| `TV-NET-01` | `tailscaled` not reachable / `tailscale` not in PATH | 3 |
| `TV-NET-02` | Not logged into the tailnet (`tailscale up`) | 3 |
| `TV-NODE-01` | Storage node offline/unreachable | 4 |
| `TV-NODE-02` | Node reachable but `base_path` not writable | 4 |
| `TV-OBJ-01` | Expected blob missing on the node (genuinely absent) | 5 |
| `TV-FED-01` | Partial view — not found among reachable members **and** ≥1 member offline (can't prove absence) | 6 |
| `TV-FED-02` | An all-members op (gc) ran with ≥1 member unreachable | 6 |
| `TV-FED-03` | WAL hash-chain verification failed (tamper/corruption) | 6 |
| `TV-FED-04` | ID-collision on `restore-identity` (id already live on a member) | 6 |

Exit buckets: `0` success · `1` unclassified · `2` config/precondition/auth ·
`3` Tailscale down · `4` node unreachable · `5` integrity/missing blob ·
`6` federation/partial-view. See [`SPEC.md §5, §15`](../SPEC.md).

---

## Caveats & known issues

- **Pre-1.0, personal project.** The CLI is implemented and test-covered through
  Blocks 0–4 and ships tagged, installable releases, but there is no 1.0 stability
  commitment; expect rough edges and format churn only within the frozen v2
  contract.
- **SSH backend only (today).** The **Taildrive** backend is designed but not yet
  the shipped path — use the `ssh` backend.
- **Single active writer assumed (early).** Lock conflicts use a per-path union
  merge driver, but true multi-writer safety needs a create-exclusive backend
  `Put`; concurrent writers from multiple machines aren't fully hardened yet.
- **No password recovery.** The node password (argon2id) has **no recovery path** —
  resetting it requires SSH/physical access to rewrite `meta/auth/passwd`. Reads
  are never gated, so losing the password doesn't lock you out of `get`/`ls`/`stat`.
- **No v1 migration.** On-node formats are schema v2; a reader rejects any other
  version with a config error. Old test vaults are recreated, not migrated.
- **Eager smudge.** Checkout fetches all needed blobs eagerly (v1 behavior);
  lazy/partial checkout is a planned later option — large trees pull everything the
  branch references.
- **Destructive ops gate on full reachability.** `gc` refuses while any federation
  member is unreachable (by design — deletes never tolerate a partial view); bring
  all members online to run it.
- **Tailscale local-session only.** Node discovery uses the local,
  already-authenticated daemon (`tailscale status --json`) — there's no API login
  or stored-credential fallback if the daemon can't enumerate peers (use `--node`
  to enter an address manually).
