# Tailvault Public Release Plan

The forward plan for taking Tailvault from a **private** repo to a **public**
project. The distribution machinery ([`distribution.md`](./distribution.md)) was
deliberately built so this transition is a **permissions flip plus polish**, not
a re-platform. Nothing about the build/release pipeline changes.

---

## What actually changes vs. the private phase

The only material difference is **auth on asset download disappears**. Every
channel that needed a token now works anonymously:

| Channel | Private phase | Public phase |
|---|---|---|
| Homebrew | needs `gh`/`HOMEBREW_GITHUB_API_TOKEN` | anonymous `brew install` |
| `install.sh` | needs `gh` or `GITHUB_TOKEN` | anonymous `curl \| sh` |
| `go install` | `GOPRIVATE` + git `insteadOf` | nothing — just works |
| `tailvault update` | `GITHUB_TOKEN` | no token needed |

The scripts and the self-updater already treat the token as **optional** (they
only attach `Authorization` when a token is present), so no code changes are
required for them to work publicly — the token branches simply go unused.

---

## Maintainer: the go-public runbook

1. **Pre-flight audit (do this before flipping):**
   - Scrub git history for secrets (tokens, `.env`, node SSH keys, tailnet
     names). Use `git log -p`, `gitleaks`, or `trufflehog`.
   - Confirm no internal hostnames / private tailnet identifiers are baked into
     committed test fixtures or docs.
   - Verify `LICENSE` exists and the GoReleaser `brews.license` value matches it.
   - Ensure `SPEC.md`/`DESIGN.md`/`proposal.md` don't leak anything you don't
     want public (they're design docs — usually fine, but check).

2. **Flip repo visibility to public:**
   - `tailvault` repo → Settings → General → Danger Zone → **Change visibility →
     Public**.
   - **`homebrew-tap` repo → also make public.** A public formula pointing at
     private assets is the one broken state; flip both together.

3. **Simplify the tap token (optional cleanup):**
   - Public tap + public assets means `HOMEBREW_TAP_TOKEN` only needs to push the
     formula to the (now public) tap; you can keep it as-is or rotate to a
     narrower token. No formula edits needed — the URLs are unchanged.

4. **Publish the canonical install one-liners** (README + landing):
   ```sh
   brew install Ibtesam-Mahmood/tailvault          # or a renamed tap/org
   curl -fsSL https://raw.githubusercontent.com/Ibtesam-Mahmood/tailvault/main/install.sh | sh
   go install github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault@latest
   ```

5. **Cut the first public release** exactly as before:
   `git tag vX.Y.Z && git push origin vX.Y.Z`. Nothing about the pipeline changes.

6. **Announce** with a version pinned in the docs and a checksum/cosign
   verification snippet (see below) for security-conscious adopters.

### Optional scaling steps once public
- **Homebrew core / a vanity tap org** (`brew install tailvault` without the
  owner prefix) once there's adoption.
- **Linux packages**: GoReleaser can also emit `.deb`/`.rpm` (nfpm) and push to
  an apt/yum repo or Cloudsmith.
- **Scoop/winget** manifests for Windows users.
- **Docker image** of the CLI for CI use.
- **`tailvault.dev`** docs site (the README already reads as a user guide).

---

## User experience (public)

### Install
```console
$ brew install Ibtesam-Mahmood/tailvault
🍺  tailvault 0.0.106

# or, no Homebrew:
$ curl -fsSL https://raw.githubusercontent.com/Ibtesam-Mahmood/tailvault/main/install.sh | sh
tailvault-install: installed → /usr/local/bin/tailvault

# or, Go devs:
$ go install github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault@latest
```

### Update
```console
$ brew upgrade tailvault            # homebrew
$ tailvault update                  # built-in, any install method
$ curl -fsSL .../install.sh | sh    # installer re-run
```
Passive nudge (cached, ~daily) on long-lived commands:
```console
$ tailvault status
... output ...
⬆ tailvault 0.0.107 is available (you have 0.0.106). Run `tailvault update`.
```

### Verify a download (security-conscious users)
```console
# checksums
$ curl -fsSLO https://github.com/Ibtesam-Mahmood/tailvault/releases/download/v0.0.106/checksums.txt
$ sha256sum -c checksums.txt --ignore-missing

# cosign signature (keyless, GitHub OIDC identity)
$ cosign verify-blob \
    --certificate checksums.txt.pem \
    --signature   checksums.txt.sig \
    --certificate-identity-regexp 'https://github.com/Ibtesam-Mahmood/tailvault' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    checksums.txt
```

### Uninstall
```console
$ brew uninstall tailvault
$ tailvault update --uninstall      # binary + client state, with a confirm
$ curl -fsSL .../uninstall.sh | sh  # TAILVAULT_PURGE=1 to also drop state dirs
```

---

## Versioning & compatibility going public

- `VERSION` remains the single source of truth; tags remain `v$(cat VERSION)`.
- Tailvault is pre-1.0 (`0.0.x`); document that minor bumps may change behavior
  until a `1.0.0` stability commitment. Consider adopting a deprecation policy
  and a `CHANGELOG.md` "Breaking" section once external users exist.
- On-node formats are **schema v2** and frozen; a public release must not change
  the byte-stable WAL/identity serializations without a deliberate, documented
  migration (today there is intentionally none).

---

## Rollback / contingency

- Going public is reversible (flip visibility back) **but** anything cloned or
  cached while public is out — treat the secret-history audit (step 1) as
  irreversible-once-public. Do it thoroughly the first time.
- A bad release is handled the same public or private: publish a higher patch;
  `tailvault update` / `brew upgrade` rolls everyone forward. Avoid deleting
  published tags/releases (breaks `go install` checksums in the module proxy).
