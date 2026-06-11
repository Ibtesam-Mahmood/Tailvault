#!/bin/sh
# tailvault uninstaller — removes the binary and (optionally) client-side state.
#
#   curl -fsSL https://raw.githubusercontent.com/Ibtesam-Mahmood/tailvault/main/uninstall.sh | sh
#
# Equivalent to `tailvault update --uninstall` for setups installed via
# install.sh. It removes ONLY:
#   - the tailvault binary on PATH
#   - ~/.config/tailvault   (node registry; honors $XDG_CONFIG_HOME)
#   - ~/.tailvault          (pull receipts + federation cache; honors $TAILVAULT_HOME)
#
# It NEVER touches storage-node bytes or per-repo tailvault.toml/.lock — remove
# those manually per the README if desired.
#
# Env knobs:
#   TAILVAULT_PURGE=1   also remove the two state dirs (default: keep them)
#   TAILVAULT_BIN_DIR   look here first for the binary
set -eu

log() { printf 'tailvault-uninstall: %s\n' "$1" >&2; }

# --- remove the binary ------------------------------------------------------
bin=""
if command -v tailvault >/dev/null 2>&1; then
  bin=$(command -v tailvault)
elif [ -n "${TAILVAULT_BIN_DIR:-}" ] && [ -x "${TAILVAULT_BIN_DIR}/tailvault" ]; then
  bin="${TAILVAULT_BIN_DIR}/tailvault"
fi

if [ -n "$bin" ]; then
  if [ -w "$bin" ] || [ -w "$(dirname "$bin")" ]; then
    rm -f "$bin"
  elif command -v sudo >/dev/null 2>&1; then
    log "removing ${bin} (needs sudo)"
    sudo rm -f "$bin"
  else
    log "cannot remove ${bin} (no write access, no sudo)"
  fi
  log "removed binary ${bin}"
else
  log "no tailvault binary found on PATH"
fi

# --- optionally purge client state ------------------------------------------
if [ "${TAILVAULT_PURGE:-0}" = "1" ]; then
  cfg="${XDG_CONFIG_HOME:-${HOME}/.config}/tailvault"
  state="${TAILVAULT_HOME:-${HOME}/.tailvault}"
  for d in "$cfg" "$state"; do
    if [ -d "$d" ]; then
      rm -rf "$d"
      log "removed ${d}"
    fi
  done
else
  log "kept client state (~/.config/tailvault, ~/.tailvault); re-run with TAILVAULT_PURGE=1 to remove"
fi

log "done. Storage-node bytes and per-repo tailvault.toml/.lock are untouched."
