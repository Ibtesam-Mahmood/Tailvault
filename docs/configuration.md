# Configuration

Tailvault has two config files: a committed project config (`tailvault.toml`) and
an uncommitted node registry (`locations.toml`). For the schemas' normative
definition see [`SPEC.md`](../SPEC.md); this is the practical guide.

---

## Project config — `tailvault.toml` (committed)

Written by `tailvault init`. Decides what is vault-managed and where, with **no**
node addresses or secrets (those resolve at runtime against `locations.toml`).

```toml
version = 1

[storage]
location = "node-a"      # name resolved via ~/.config/tailvault/locations.toml
subpath  = "subdir"     # optional child folder under the location's base_path

[rules]
min_size    = "5MB"                       # files >= this are vault-managed
include     = ["**/*.pdf", "**/*.stl", "**/*.3mf", "**/*.pptx"]
exclude     = ["**/*.tmp", "drafts/**"]   # exclude WINS over include
history     = false                       # default: no version history (anti-bloat)
auto_delete = true                        # default: prune from storage on git delete

# Per-pattern overrides; FIRST match wins.
[[rules.overrides]]
match    = "masters/**"
history  = true
preserve = true                           # never auto-delete
```

**Rule evaluation (normative):** a file is vault-managed when it does *not* match
any `exclude` glob **and** (it matches an `include` glob **or** its size
`>= min_size`). Size suffixes are **binary** (`MB = 1024²`); `"5MB"` = 5 242 880
bytes. See [`SPEC.md §1, §7`](../SPEC.md).

---

## Node registry — `locations.toml` (NOT committed)

Lives at `~/.config/tailvault/locations.toml`; carries node addresses and the SSH
login user. Written interactively by `tailvault setup` / `tailvault location add`.

```toml
[locations.node-a]
node      = "node-a.example-net.ts.net"  # MagicDNS or 100.x IP
base_path = "/mnt/ssd/tailvault"            # on a USB3 SSD, not the boot SD
backend   = "ssh"                           # local | ssh | taildrive (taildrive planned)
user      = "user"

[locations.home]                            # a LOCAL store — base_path only, no node
base_path = "/Users/you/.tailvault/stores/home"
backend   = "local"
```

`node` is prefilled from `tailscale status --json` peer enumeration (local session
only). `--node` is always available as a manual fallback. A **local** location
(`backend = "local"`) has no node/user/share — just a `base_path` directory on
this machine; it is created by the default `tailvault setup` (use
`tailvault setup --remote` to register a tailnet node). Local stores are
standalone — they cannot join a federation.

---

## Client-side state directories

| Path | Holds | Override |
| --- | --- | --- |
| `~/.config/tailvault/` | `locations.toml` (node registry) | `$XDG_CONFIG_HOME` |
| `~/.tailvault/` | pull receipts, federation cache, `update-check.json` | `$TAILVAULT_HOME` |

Neither is committed; both are removable via `tailvault update --uninstall` (see
[`install.md`](./install.md#uninstalling)).
