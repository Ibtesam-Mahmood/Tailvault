#!/bin/sh
# tailvault installer — downloads a released binary and installs it on PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/Ibtesam-Mahmood/tailvault/main/install.sh | sh
#
# While the repo is PRIVATE, asset download needs auth. The script uses the
# GitHub CLI (`gh`) if it is installed and authenticated; otherwise it falls
# back to the GitHub API with a token from $GITHUB_TOKEN / $GH_TOKEN:
#
#   GITHUB_TOKEN=ghp_xxx sh -c "$(curl -fsSL .../install.sh)"
#
# Once the repo is public, neither gh nor a token is required.
#
# Env knobs:
#   TAILVAULT_VERSION   install a specific tag (e.g. v0.0.106); default: latest
#   TAILVAULT_BIN_DIR   install dir; default: /usr/local/bin (falls back to
#                       ~/.local/bin if the former is not writable)
set -eu

OWNER="Ibtesam-Mahmood"
REPO="tailvault"
API="https://api.github.com/repos/${OWNER}/${REPO}"

log()  { printf 'tailvault-install: %s\n' "$1" >&2; }
die()  { log "error: $1"; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# --- detect platform --------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux)  os=linux ;;
  darwin) os=darwin ;;
  *) die "unsupported OS: $os (use 'go install' or build from source)" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac
log "detected ${os}/${arch}"

token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
use_gh=0
if have gh && gh auth status >/dev/null 2>&1; then
  use_gh=1
fi

# --- resolve version --------------------------------------------------------
version="${TAILVAULT_VERSION:-}"
if [ -z "$version" ]; then
  if [ "$use_gh" -eq 1 ]; then
    version=$(gh release view --repo "${OWNER}/${REPO}" --json tagName -q .tagName)
  else
    auth_hdr=""
    [ -n "$token" ] && auth_hdr="-H Authorization: Bearer ${token}"
    # shellcheck disable=SC2086
    version=$(curl -fsSL $auth_hdr "${API}/releases/latest" \
      | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
  fi
  [ -n "$version" ] || die "could not determine latest version (private repo? set GITHUB_TOKEN or run 'gh auth login')"
fi
log "version ${version}"

ver_noV=${version#v}
asset="tailvault_${ver_noV}_${os}_${arch}.tar.gz"

# --- download archive + checksums -------------------------------------------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

log "downloading ${asset}"
if [ "$use_gh" -eq 1 ]; then
  gh release download "$version" --repo "${OWNER}/${REPO}" \
    --pattern "$asset" --pattern "checksums.txt" --dir "$tmp" --clobber
else
  base="https://github.com/${OWNER}/${REPO}/releases/download/${version}"
  auth_hdr=""
  [ -n "$token" ] && auth_hdr="-H Authorization: Bearer ${token}"
  # shellcheck disable=SC2086
  curl -fL $auth_hdr -o "${tmp}/${asset}" "${base}/${asset}" \
    || die "download failed (private repo without a token?)"
  # shellcheck disable=SC2086
  curl -fsSL $auth_hdr -o "${tmp}/checksums.txt" "${base}/checksums.txt" || true
fi

# --- verify checksum --------------------------------------------------------
if [ -f "${tmp}/checksums.txt" ]; then
  expected=$(grep " ${asset}\$" "${tmp}/checksums.txt" | awk '{print $1}' | head -n1)
  if [ -n "$expected" ]; then
    if have sha256sum; then
      actual=$(sha256sum "${tmp}/${asset}" | awk '{print $1}')
    elif have shasum; then
      actual=$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')
    else
      actual=""
      log "no sha256 tool found; skipping checksum verification"
    fi
    if [ -n "$actual" ] && [ "$actual" != "$expected" ]; then
      die "checksum mismatch for ${asset}"
    fi
    [ -n "$actual" ] && log "verifying checksum ✓"
  fi
else
  log "checksums.txt unavailable; skipping verification"
fi

# --- extract + install ------------------------------------------------------
tar -xzf "${tmp}/${asset}" -C "$tmp"
[ -f "${tmp}/tailvault" ] || die "archive did not contain a tailvault binary"
chmod 0755 "${tmp}/tailvault"

bindir="${TAILVAULT_BIN_DIR:-/usr/local/bin}"
install_to() { # $1 = dir
  if [ -w "$1" ] || { [ ! -e "$1" ] && mkdir -p "$1" 2>/dev/null; }; then
    mv "${tmp}/tailvault" "$1/tailvault"; return 0
  fi
  return 1
}
if install_to "$bindir"; then :;
elif [ "$bindir" = "/usr/local/bin" ] && have sudo; then
  log "installing to ${bindir} (needs sudo)"
  sudo mv "${tmp}/tailvault" "${bindir}/tailvault"
else
  bindir="${HOME}/.local/bin"
  mkdir -p "$bindir"
  mv "${tmp}/tailvault" "${bindir}/tailvault"
  case ":${PATH}:" in
    *":${bindir}:"*) ;;
    *) log "note: ${bindir} is not on your PATH — add it to use 'tailvault'";;
  esac
fi

log "installed → ${bindir}/tailvault"
log "run 'tailvault --version' to confirm"
