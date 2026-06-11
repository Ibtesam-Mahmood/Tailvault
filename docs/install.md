# Installing, updating, uninstalling & sharing Tailvault

Everything about getting Tailvault onto a machine — for yourself and for others —
**while the repo is private**. Tailvault builds to a single static binary with the
version embedded at build time from [`VERSION`](../VERSION).

Related: [`distribution.md`](./distribution.md) (the release pipeline + maintainer
setup) and [`public-release.md`](./public-release.md) (the go-public plan, where
all the auth friction below disappears).

> **Private-repo note.** While this repo is private, Homebrew, the shell
> installer, and `tailvault update` need GitHub auth: either run `gh auth login`,
> or export a read-only token as `GITHUB_TOKEN`. Once public, no auth is needed.

---

## Requirements

- **Tailscale** installed and logged in on both the client and the storage node
  (`tailscale` in `PATH`, daemon running). Tailvault uses the **local,
  already-authenticated session only** — no Tailscale API login, no stored
  credentials.
- A **storage node** on your tailnet with:
  - **SSH backend** (the supported backend today): SSH access for the configured
    user and a writable `base_path` (use a real disk such as a USB3 SSD, **not** a
    Pi's boot SD card). *Taildrive backend is planned.*
  - The `tailvault` binary is **not** required on the node for the SSH backend —
    node-side helpers are invoked over SSH.
- **git** for repo-managed mode.
- **Go 1.26+** only if building from source / using `go install`.

---

## Install

Pick one channel. All resolve to the same signed, checksummed GitHub Release, so
**install and update are the same action.**

### Homebrew (recommended)

```sh
export HOMEBREW_GITHUB_API_TOKEN=$(gh auth token)   # private-repo auth
brew install Ibtesam-Mahmood/tap/tailvault
# update:    brew upgrade tailvault
# uninstall: brew uninstall tailvault
```

Add the `HOMEBREW_GITHUB_API_TOKEN` line to your shell rc so `brew upgrade` keeps
working.

### Shell installer

Detects OS/arch, downloads the matching archive, verifies its checksum, installs
to `/usr/local/bin` (falling back to `~/.local/bin`).

```sh
GITHUB_TOKEN=$(gh auth token) sh -c "$(curl -fsSL https://raw.githubusercontent.com/Ibtesam-Mahmood/tailvault/main/install.sh)"
# pin a version:  TAILVAULT_VERSION=v0.0.106 GITHUB_TOKEN=… sh -c "$(curl -fsSL …/install.sh)"
```

### `go install`

```sh
go env -w GOPRIVATE=github.com/Ibtesam-Mahmood/*
git config --global url."git@github.com:".insteadOf "https://github.com/"
go install github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault@latest
```

`go install` ignores the Makefile's `-ldflags`, but `tailvault` reads its module
build info as a fallback, so `tailvault --version` reports the real tag for
`@vX.Y.Z`/`@latest` installs (only an untagged local checkout shows `dev`).

### From source

```sh
git clone https://github.com/Ibtesam-Mahmood/tailvault.git
cd tailvault
make build                                          # → ./bin/tailvault (embeds VERSION)
sudo install -m 0755 bin/tailvault /usr/local/bin/tailvault
# …or user-local:  install -m 0755 bin/tailvault ~/.local/bin/tailvault
```

`make build` runs:

```sh
go build -ldflags "-X github.com/Ibtesam-Mahmood/tailvault/internal/version.Version=$(cat VERSION)" \
  -o bin/tailvault ./cmd/tailvault
```

### Verify

```sh
tailvault --version
tailvault --help
```

---

## Updating

The simplest path — works regardless of how you installed:

```sh
tailvault update            # download latest, verify checksum, replace in place
tailvault update --check    # just report whether a newer release exists
tailvault update --version v0.0.106   # install a specific release (pin/downgrade)
```

`tailvault update` verifies the download's SHA-256 against the release
`checksums.txt` before replacing the binary, and aborts (leaving the current
binary intact) on any mismatch. In-place self-update is unsupported on Windows —
reinstall via the installer there. Long-lived commands (`status`, `pull`) print a
cached, best-effort "update available" nudge; silence it with
`TAILVAULT_NO_UPDATE_CHECK=1`.

Per-channel alternatives:

```sh
brew upgrade tailvault                                   # Homebrew
curl -fsSL …/install.sh | sh                             # shell installer (re-run)
go install github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault@latest   # go
cd tailvault && git pull && make build                   # from source
```

On-node formats are **schema v2** with **no v1-migration machinery** — a reader
requires `version = 2` and rejects anything else. Updates within the v2 line
preserve on-disk formats; the byte-stable WAL/identity serializations are frozen
so a dependency bump can't silently break an existing chain.

---

## Uninstalling

The built-in path removes the binary plus client-side state (it confirms first and
lists exactly what it will delete):

```sh
tailvault update --uninstall            # binary + ~/.config/tailvault + ~/.tailvault
brew uninstall tailvault                # if installed via Homebrew
curl -fsSL …/uninstall.sh | sh          # TAILVAULT_PURGE=1 to also drop state dirs
```

Or by hand:

```sh
# 1. Remove the binary
sudo rm /usr/local/bin/tailvault        # or ~/.local/bin/tailvault

# 2. Remove user-level config & client-side state (optional)
rm -rf ~/.config/tailvault              # locations.toml (node registry)
rm -rf ~/.tailvault                     # pull receipts + federation cache

# 3. Per-repo: stop git from using the filters/hooks
#    Edit .gitattributes to drop the tailvault filter lines, remove the installed
#    hooks from .git/hooks (pre-push, etc.), then delete tailvault.toml / .lock.
```

> Uninstalling the client does **not** touch the bytes on your storage node — the
> content store under `<base_path>/<subpath>/` is left intact. Delete it manually
> on the node if you want the storage reclaimed.

---

## Sharing with others (private repo)

A private repo means collaborators can't install anonymously — they need read
access **plus** auth. Two parts:

### 1. Grant access to *both* repos (read/"pull" is enough)

```sh
gh repo add-collaborator Ibtesam-Mahmood/tailvault     <username> --permission pull
gh repo add-collaborator Ibtesam-Mahmood/homebrew-tap  <username> --permission pull
```

Both matter: `tailvault` holds the release assets; `homebrew-tap` holds the
formula. The invitee accepts the emailed invite.

### 2. They authenticate (once, on their machine)

```sh
gh auth login        # GitHub CLI → browser; easiest
```

…or a fine-grained PAT with **read-only Contents** on those repos, exported as
`GITHUB_TOKEN`.

### 3. They install

Same commands as [Install](#install) — e.g.:

```sh
export HOMEBREW_GITHUB_API_TOKEN=$(gh auth token)
brew install Ibtesam-Mahmood/tap/tailvault
```

After that, `tailvault update` / `brew upgrade` work for them too.

### The friction-free option: go public

Every "needs a token" caveat above exists **only** because the repo is private.
Make both `tailvault` and `homebrew-tap` public and sharing collapses to a single
anonymous line:

```sh
brew install Ibtesam-Mahmood/tap/tailvault
# or:  curl -fsSL https://raw.githubusercontent.com/Ibtesam-Mahmood/tailvault/main/install.sh | sh
```

Before going public you must add a `LICENSE` (the project currently ships with
none — "all rights reserved" means nobody may legally use it) and run the
secret-history audit. Full runbook: [`public-release.md`](./public-release.md).
