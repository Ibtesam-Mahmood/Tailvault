# Configuration

Example Project has two config files: a committed project config (`example-project.toml`) and
an uncommitted node registry (`locations.toml`). For the schemas' normative
definition see [`SPEC.md`](../SPEC.md); this is the practical guide.

---

## Project config — `example-project.toml` (committed)

Written by `example-project init`. Decides what is vault-managed and where, with **no**
node addresses or secrets (those resolve at runtime against `locations.toml`).

```toml
version = 1

[storage]
location = "node-a"      # name resolved via ~/.config/example-project/locations.toml
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

Lives at `~/.config/example-project/locations.toml`; carries node addresses and the SSH
login user. Written interactively by `example-project setup` / `example-project location add`.

```toml
[locations.node-a]
node      = "node-a.example-net.ts.net"  # MagicDNS or 100.x IP
base_path = "/mnt/ssd/example-project"            # on a USB3 SSD, not the boot SD
backend   = "ssh"                           # ssh | taildrive (taildrive planned)
user      = "user"
```

`node` is prefilled from `tailscale status --json` peer enumeration (local session
only). `--node` is always available as a manual fallback.

---

## Client-side state directories

| Path | Holds | Override |
| --- | --- | --- |
| `~/.config/example-project/` | `locations.toml` (node registry) | `$XDG_CONFIG_HOME` |
| `~/.example-project/` | pull receipts, federation cache, `update-check.json` | `$EXAMPLE_PROJECT_HOME` |

Neither is committed; both are removable via `example-project update --uninstall` (see
[`install.md`](./install.md#uninstalling)).
