# Tailvault

![version](https://img.shields.io/badge/version-0.0.112-1f6feb)
![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)
![Tailscale](https://img.shields.io/badge/Tailscale-native-242424?logo=tailscale&logoColor=white)
![git](https://img.shields.io/badge/git--native-filters%20%2B%20hooks-F05032?logo=git&logoColor=white)
![platform](https://img.shields.io/badge/platform-linux%20%7C%20macOS-555)
![status](https://img.shields.io/badge/status-active%20development-orange)

**A Tailscale-native, content-addressed large-file store that syncs with
`git push` / `git pull`.**

`tailvault` keeps large binary files **out of git history** while keeping them in
lockstep with your normal git workflow. The real bytes live in a content-addressed
folder on a **Tailscale node** (e.g. a home Raspberry Pi with a USB3 SSD); the git
repo carries only small **pointer files** and a committed **lock file**. It is a
clean, bloat-averse alternative to Git LFS — **not a wrapper** — built on
git-native filters and hooks.

The headline guarantees:

- **History is off by default** (one current ref per file) to prevent repo bloat —
  but diffs and deletes are still tracked, and history is opt-in per file.
- **Deletes propagate.** A file removed from git is auto-deleted from storage
  unless you mark it `preserve`.
- **No silent success.** Every command that needs the node runs a preflight and
  **hard-fails loudly** if the node is down or an expected blob is missing — a
  green `git push` means the bytes actually landed on storage.

> **Status — v0.0.111, under active phased development.** The CLI is implemented
> and test-covered through Blocks 0–4 (core workflow + multi-node federation), and
> tagged releases ship prebuilt, checksummed binaries via Homebrew, a shell
> installer, `go install`, and `tailvault update`. Design is frozen in
> [`SPEC.md`](./SPEC.md). Personal project, pre-1.0 — installable, no stability
> commitment yet.

---

## How it works at a glance

Your laptop clone stays lean: git holds only tiny **pointer files** + a committed
**`tailvault.lock`**. The real bytes live content-addressed on a Tailscale
**storage node**, reached over your tailnet — no cloud, no public exposure, no
API keys. Clean/smudge **filters** swap bytes ⇄ pointers in the working tree;
git **hooks** move the real bytes on `push` / `pull`.

```mermaid
flowchart LR
    subgraph laptop["💻 Laptop clone (lean)"]
        wt["working tree<br/>real bytes"]
        repo["git repo<br/>pointers + tailvault.lock"]
        wt <-- "clean / smudge filter" --> repo
    end
    subgraph node["🗄️ Storage node (e.g. Pi + USB3 SSD)"]
        store["content store<br/>objects/&lt;sha256&gt;"]
        wal["WAL<br/>(hash-chained)"]
        cat["catalog.toml"]
        auth["argon2id passwd"]
        store --- wal --> cat
        auth -. "gates mutations" .- store
    end
    repo == "git push / pull (hooks)<br/>🔒 over tailnet · no public exposure" ==> store

    style laptop fill:#eef6ff,stroke:#1f6feb
    style node fill:#eefbf0,stroke:#2da44e
```

### Two modes

```mermaid
flowchart TB
    A["tailvault"] --> R["📁 Repo-managed mode<br/><i>Git-LFS-style</i>"]
    A --> V["🌐 Vault / federation mode<br/><i>checkout-free</i>"]
    R --> R1["files live in the git working tree"]
    R --> R2["filters + push/pull hooks move bytes"]
    V --> V1["files live only on storage nodes"]
    V --> V2["put / get / ls / mv / rm across nodes"]
    style R fill:#eef6ff,stroke:#1f6feb
    style V fill:#fff7e6,stroke:#d29922
```

### Data flow — repo-managed mode

```mermaid
sequenceDiagram
    autonumber
    actor U as You
    participant G as git
    participant TV as tailvault<br/>(filters + hooks)
    participant N as Storage node

    U->>G: git add big.pdf
    G->>TV: clean filter
    TV-->>G: 4-line pointer<br/>(bytes set aside)
    U->>G: git push
    G->>TV: pre-push hook
    TV->>N: preflight (status → ping/stat)
    alt node down or blob missing
        TV-->>U: ❌ hard-fail loudly<br/>(no partial upload, lock unadvanced)
    else node reachable
        TV->>N: upload new blobs by sha256
        TV-->>G: update tailvault.lock
    end
    U->>G: git pull / checkout
    G->>TV: smudge filter
    TV->>N: fetch blob by sha256
    TV-->>U: ✅ real bytes restored
    U->>G: git rm big.pdf → push
    TV->>N: auto-delete blob (unless preserve)
```

A **green `git push` means the bytes actually landed** — there is no silent
success. Identity is content-independent: a file's **ID** is the `sha256` of its
genesis record, so **moves change the path, never the ID**, and every clone's
lock is an off-node identity backup.

### Federation

```mermaid
flowchart TB
    C["💻 Client"] -- "read fans out" --> NA & NB & NC
    subgraph fed["One logical tree (federation)"]
        NA["🗄️ node-a<br/><i>home</i>"]
        NB["🗄️ node-b"]
        NC["🗄️ node-c (offline)"]
    end
    NA -. "found at non-home member<br/>→ ✅ + WARN (run heal)" .-> NB
    NC -- "can't prove absence<br/>→ exit 6, never a false miss" --- C
    style NC fill:#fdecea,stroke:#cf222e,stroke-dasharray:4 4
    style fed fill:#f6f8fa,stroke:#8b949e
```

Reads are **reachability-aware**: a genuinely missing file is exit `5`, but an
unprovable absence behind an offline member is exit `6` — never a false miss.
Destructive all-member ops (`gc`) **refuse** to run while any member is offline.

---

## Install

**Homebrew is the recommended path** — pick **one** channel; they all resolve to
the same signed, checksummed GitHub Release, so install and update are the same
action. **This repo is private**, so installs need GitHub auth (`gh auth login`,
or a read-only token).

```sh
export HOMEBREW_GITHUB_API_TOKEN=$(gh auth token)   # private-repo auth; omit when public
brew install Ibtesam-Mahmood/tap/tailvault
tailvault --version
```

Update with `brew upgrade tailvault`; uninstall with `brew uninstall tailvault`.

<details>
<summary><b>Prefer not to use Homebrew?</b> You only need one of these instead.</summary>

```sh
# shell installer (servers / CI / no Homebrew)
GITHUB_TOKEN=$(gh auth token) sh -c "$(curl -fsSL https://raw.githubusercontent.com/Ibtesam-Mahmood/tailvault/main/install.sh)"

# go install (Go devs)
go env -w GOPRIVATE=github.com/Ibtesam-Mahmood/*
go install github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault@latest
```

Then `tailvault update` self-updates; `tailvault update --uninstall` removes it.
</details>

📖 **Full install, update, uninstall, requirements, and how to share with others
(grant access + auth) → [`docs/install.md`](./docs/install.md).**

---

## Quick start (repo-managed mode)

```sh
tailvault setup                  # create a local storage location (or: setup --remote for a node)
tailvault init                   # write tailvault.toml + .gitattributes + hooks
tailvault track "**/*.pdf"       # track large files by rule or path
git add . && git commit -m "add big assets"
git push                         # preflight + upload blobs + update lock
tailvault status                 # local-only / pushed / drifted / orphaned
```

📖 **Vault/federation quick start and the full command reference →
[`docs/commands.md`](./docs/commands.md).**

---

## Documentation

| Doc | What it is |
| --- | --- |
| [`docs/how-it-works.md`](./docs/how-it-works.md) | The two modes, moving parts, data flow, identity, federation, features, use cases. |
| [`docs/install.md`](./docs/install.md) | Install / update / uninstall / **share**, all channels, requirements. |
| [`docs/commands.md`](./docs/commands.md) | Quick starts + full command reference. |
| [`docs/configuration.md`](./docs/configuration.md) | `tailvault.toml` + `locations.toml` + state dirs. |
| [`docs/troubleshooting.md`](./docs/troubleshooting.md) | Error/exit codes + caveats & known issues. |
| [`docs/distribution.md`](./docs/distribution.md) | Release pipeline + maintainer setup (private). |
| [`docs/public-release.md`](./docs/public-release.md) | The go-public runbook. |
| [`SPEC.md`](./SPEC.md) | **Normative frozen contract** — file schemas, federation formats, error catalogue. |
| [`DESIGN.md`](./DESIGN.md) | Authoritative design dump: schemas, retention model, rejected options. |
| [`proposal.md`](./proposal.md) | Formal proposal: problem, architecture, phased plan. |
| [`CONTRIBUTING.md`](./CONTRIBUTING.md) | Workflow: versioning, task/issue tracking, PR conventions. |
| [`CHANGELOG.md`](./CHANGELOG.md) | Version history (`VERSION` is the single source of truth). |
| [`tasks/`](./tasks/) | Phased implementation backlog (mirrors GitHub issues). |

---

## Requirements (in brief)

**Tailscale** (logged in, daemon running, local session only) on the client and a
**storage node** with the **SSH backend** + a writable `base_path` on a real disk;
**git** for repo-managed mode; **Go 1.26+** only to build from source. Full detail
in [`docs/install.md`](./docs/install.md#requirements).

---

## Contributing

Work is tracked as GitHub issues **and** mirrored in [`tasks/`](./tasks/), in
**blocks of PRs**: one task ≈ one branch (`phase-N/<slug>`) ≈ one PR that
`Closes #N`, bumps [`VERSION`](./VERSION) by `+0.0.1`, and adds a matching
`## v<version>` entry to [`CHANGELOG.md`](./CHANGELOG.md) in the same commit.
`main` stays green.

```sh
make build       # build with embedded version → bin/tailvault
make test        # go test ./...
make vet         # go vet ./...
make fmt         # gofmt -l .
```

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) and [`CLAUDE.md`](./CLAUDE.md) for the
full workflow and locked decisions.
