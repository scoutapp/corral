#!/usr/bin/env bash
#
# sandclaude remote installer — downloads prebuilt binaries, no clone, no Go.
#
#   curl -fsSL https://raw.githubusercontent.com/scoutapp/sandclaude/main/scripts/install.sh | bash
#
# Installs:
#   - the sandclaude binary  -> $SANDCLAUDE_PREFIX (default /usr/local/bin, else ~/.local/bin)
#   - the runtime asset bundle -> $SANDCLAUDE_HOME/assets (default ~/.sandclaude/assets)
#
# The binary needs the asset bundle (sandbox/ Docker build context + host/
# proxy-addon.py) at runtime, so we fetch BOTH the platform archive and
# sandclaude-assets.tar.gz from the same GitHub Release.
#
# Env overrides:
#   SANDCLAUDE_VERSION   release tag to install (default: latest), e.g. v0.1.0
#   SANDCLAUDE_PREFIX    dir on $PATH for the binary  (default: /usr/local/bin if writable, else ~/.local/bin)
#   SANDCLAUDE_HOME      per-user data dir for assets  (default: ~/.sandclaude)
#   SANDCLAUDE_REPO      owner/name to fetch from      (default: scoutapp/sandclaude)
#
set -euo pipefail

REPO="${SANDCLAUDE_REPO:-scoutapp/sandclaude}"
HOME_DIR="${SANDCLAUDE_HOME:-$HOME/.sandclaude}"
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
    *) die "unsupported OS '$os' (sandclaude ships darwin and linux builds)" ;;
  esac
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) die "unsupported arch '$arch' (sandclaude ships amd64 and arm64)" ;;
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
  if [ -n "${SANDCLAUDE_VERSION:-}" ]; then
    printf '%s' "$SANDCLAUDE_VERSION"
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
  if [ -n "${SANDCLAUDE_PREFIX:-}" ]; then printf '%s' "$SANDCLAUDE_PREFIX"; return; fi
  if [ -w /usr/local/bin ] 2>/dev/null; then printf '/usr/local/bin'; return; fi
  printf '%s' "$HOME/.local/bin"
}

main() {
  local platform version prefix tmp base
  platform="$(detect_platform)"
  version="$(resolve_version)"
  [ -n "$version" ] || die "could not resolve a release version (set SANDCLAUDE_VERSION)"
  prefix="$(resolve_prefix)"

  info "sandclaude $version ($platform)"
  base="https://github.com/$REPO/releases/download/$version"

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  local archive="sandclaude_${platform}.tar.gz"
  info "downloading $archive"
  download "$base/$archive" "$tmp/$archive" || die "failed to download $archive (does $version have a $platform build?)"

  info "downloading asset bundle"
  download "$base/sandclaude-assets.tar.gz" "$tmp/assets.tar.gz" || die "failed to download sandclaude-assets.tar.gz"

  # Best-effort checksum verification (checksums.txt lists all release artifacts).
  if download "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
    if command -v shasum >/dev/null 2>&1 || command -v sha256sum >/dev/null 2>&1; then
      info "verifying checksums"
      ( cd "$tmp" && grep -E "  ($archive|sandclaude-assets.tar.gz)$" checksums.txt > want.txt \
        && { command -v sha256sum >/dev/null 2>&1 && sha256sum -c want.txt || shasum -a 256 -c want.txt; } ) \
        || die "checksum verification failed"
    fi
  fi

  info "extracting binary"
  tar xzf "$tmp/$archive" -C "$tmp"
  [ -f "$tmp/sandclaude" ] || die "archive did not contain a 'sandclaude' binary"
  chmod +x "$tmp/sandclaude"

  info "installing binary to $prefix/sandclaude"
  mkdir -p "$prefix" 2>/dev/null || true
  if [ -w "$prefix" ]; then
    install -m 0755 "$tmp/sandclaude" "$prefix/sandclaude"
  else
    info "$prefix not writable; using sudo"
    sudo install -m 0755 "$tmp/sandclaude" "$prefix/sandclaude"
  fi

  info "installing asset bundle to $ASSETS_DIR"
  # Fresh bundle each install: assets/{sandbox,host} are fully replaced.
  rm -rf "$ASSETS_DIR/sandbox" "$ASSETS_DIR/host"
  mkdir -p "$ASSETS_DIR"
  tar xzf "$tmp/assets.tar.gz" -C "$ASSETS_DIR"

  ensure_mitmproxy

  echo
  echo "✅ Installed sandclaude $version"
  echo "   binary : $prefix/sandclaude"
  echo "   assets : $ASSETS_DIR"
  echo
  if ! command -v sandclaude >/dev/null 2>&1; then
    echo "Note: '$prefix' is not on your \$PATH. Add it, e.g.:"
    echo "   export PATH=\"$prefix:\$PATH\""
    echo
  fi
  echo "Next: cd ~/your-project && sandclaude init && sandclaude start"
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
  err "sandclaude needs mitmproxy for 'sandclaude start' — install it and re-run if start fails"
}

main "$@"
