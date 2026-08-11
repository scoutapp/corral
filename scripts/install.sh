#!/usr/bin/env bash
#
# corral remote installer — downloads prebuilt binaries, no clone, no Go.
#
#   curl -fsSL https://raw.githubusercontent.com/scoutapp/corral/main/scripts/install.sh | bash
#
# Installs:
#   - the corral binary  -> $CORRAL_PREFIX (default /usr/local/bin, else ~/.local/bin)
#   - the runtime asset bundle -> $CORRAL_HOME/assets (default ~/.corral/assets)
#
# The binary needs the asset bundle (sandbox/ Docker build context + host/
# proxy-addon.py) at runtime, so we fetch BOTH the platform archive and
# corral-assets.tar.gz from the same GitHub Release.
#
# Env overrides:
#   CORRAL_VERSION   release tag to install (default: latest), e.g. v0.1.0
#   CORRAL_PREFIX    dir on $PATH for the binary  (default: /usr/local/bin if writable, else ~/.local/bin)
#   CORRAL_HOME      per-user data dir for assets  (default: ~/.corral)
#   CORRAL_REPO      owner/name to fetch from      (default: scoutapp/corral)
#
set -euo pipefail

REPO="${CORRAL_REPO:-scoutapp/corral}"
HOME_DIR="${CORRAL_HOME:-$HOME/.corral}"
ASSETS_DIR="$HOME_DIR/assets"

info() { printf '==> %s\n' "$*"; }
err()  { printf 'Error: %s\n' "$*" >&2; }
die()  { err "$*"; exit 1; }

# --- Detect OS / arch, mapped to the release archive naming --------------------
detect_platform() {
  local os arch
  os="$(uname -s)"; arch="$(uname -m)"
  case "$os" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *) die "unsupported OS '$os' (corral ships darwin and linux builds)" ;;
  esac
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) die "unsupported arch '$arch' (corral ships amd64 and arm64)" ;;
  esac
  printf '%s_%s' "$os" "$arch"
}

# --- Pick a download tool ------------------------------------------------------
download() { # download <url> <dest>
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    die "need curl or wget to download"
  fi
}

# --- Resolve the release tag (latest unless pinned) ----------------------------
resolve_version() {
  if [ -n "${CORRAL_VERSION:-}" ]; then
    printf '%s' "$CORRAL_VERSION"
    return
  fi
  # Follow the /releases/latest redirect to read the tag without needing jq.
  local url="https://github.com/$REPO/releases/latest" location
  if command -v curl >/dev/null 2>&1; then
    location="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$url")"
  else
    location="$(wget -qSO /dev/null "$url" 2>&1 | awk '/Location:/ {print $2}' | tail -1)"
  fi
  # …/releases/tag/vX.Y.Z -> vX.Y.Z
  printf '%s' "${location##*/}"
}

# --- Choose an install prefix that's writable ---------------------------------
resolve_prefix() {
  if [ -n "${CORRAL_PREFIX:-}" ]; then printf '%s' "$CORRAL_PREFIX"; return; fi
  if [ -w /usr/local/bin ] 2>/dev/null; then printf '/usr/local/bin'; return; fi
  printf '%s' "$HOME/.local/bin"
}

main() {
  local platform version prefix tmp base
  platform="$(detect_platform)"
  version="$(resolve_version)"
  [ -n "$version" ] || die "could not resolve a release version (set CORRAL_VERSION)"
  prefix="$(resolve_prefix)"

  info "corral $version ($platform)"
  base="https://github.com/$REPO/releases/download/$version"

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  local archive="corral_${platform}.tar.gz"
  info "downloading $archive"
  download "$base/$archive" "$tmp/$archive" || die "failed to download $archive (does $version have a $platform build?)"

  info "downloading asset bundle"
  download "$base/corral-assets.tar.gz" "$tmp/assets.tar.gz" || die "failed to download corral-assets.tar.gz"

  # Best-effort checksum verification (checksums.txt lists all release artifacts).
  if download "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
    if command -v shasum >/dev/null 2>&1 || command -v sha256sum >/dev/null 2>&1; then
      info "verifying checksums"
      ( cd "$tmp" && grep -E "  ($archive|corral-assets.tar.gz)$" checksums.txt > want.txt \
        && { command -v sha256sum >/dev/null 2>&1 && sha256sum -c want.txt || shasum -a 256 -c want.txt; } ) \
        || die "checksum verification failed"
    fi
  fi

  info "extracting binary"
  tar xzf "$tmp/$archive" -C "$tmp"
  [ -f "$tmp/corral" ] || die "archive did not contain a 'corral' binary"
  chmod +x "$tmp/corral"

  info "installing binary to $prefix/corral"
  mkdir -p "$prefix" 2>/dev/null || true
  if [ -w "$prefix" ]; then
    install -m 0755 "$tmp/corral" "$prefix/corral"
  else
    info "$prefix not writable; using sudo"
    sudo install -m 0755 "$tmp/corral" "$prefix/corral"
  fi

  info "installing asset bundle to $ASSETS_DIR"
  # Fresh bundle each install: assets/{sandbox,host} are fully replaced.
  rm -rf "$ASSETS_DIR/sandbox" "$ASSETS_DIR/host"
  mkdir -p "$ASSETS_DIR"
  tar xzf "$tmp/assets.tar.gz" -C "$ASSETS_DIR"

  ensure_mitmproxy
  ensure_tmux

  echo
  echo "✅ Installed corral $version"
  echo "   binary : $prefix/corral"
  echo "   assets : $ASSETS_DIR"
  echo
  if ! command -v corral >/dev/null 2>&1; then
    echo "Note: '$prefix' is not on your \$PATH. Add it, e.g.:"
    echo "   export PATH=\"$prefix:\$PATH\""
    echo
  fi
  echo "Next: cd ~/your-project && corral init && corral start"
}

# mitmproxy (mitmweb) is a runtime dependency the binary can't bundle. Install it
# the same cross-platform way the from-clone installer does.
ensure_mitmproxy() {
  command -v mitmweb >/dev/null 2>&1 && return 0
  info "installing mitmproxy (mitmweb credential proxy)"
  local os; os="$(uname -s)"
  case "$os" in
    Darwin)
      if command -v brew >/dev/null 2>&1; then brew install mitmproxy && return 0; fi
      err "Homebrew not found; install mitmproxy manually: brew install mitmproxy" ;;
    Linux)
      local sudo=""; [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1 && sudo="sudo"
      if command -v apt-get >/dev/null 2>&1; then
        $sudo apt-get update -qq && $sudo apt-get install -y -qq mitmproxy && return 0
      fi
      err "apt-get not found; install mitmproxy manually (see https://docs.mitmproxy.org)" ;;
  esac
  err "corral needs mitmproxy for 'corral start' — install it and re-run if start fails"
}

# tmux is a HOST dependency: corral runs the interactive container inside a
# host tmux session (so it survives detach/reattach and the dashboard can attach
# to it). Install it the same cross-platform way as mitmproxy.
ensure_tmux() {
  command -v tmux >/dev/null 2>&1 && return 0
  info "installing tmux (host terminal multiplexer)"
  local os; os="$(uname -s)"
  case "$os" in
    Darwin)
      if command -v brew >/dev/null 2>&1; then brew install tmux && return 0; fi
      err "Homebrew not found; install tmux manually: brew install tmux" ;;
    Linux)
      local sudo=""; [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1 && sudo="sudo"
      if command -v apt-get >/dev/null 2>&1; then
        $sudo apt-get update -qq && $sudo apt-get install -y -qq tmux && return 0
      fi
      err "apt-get not found; install tmux manually (e.g. your distro's package manager)" ;;
  esac
  err "corral needs tmux for 'corral start' — install it and re-run if start fails"
}

main "$@"
