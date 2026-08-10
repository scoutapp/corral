#!/usr/bin/env bash
#
# install.sh — build sandclaude and install it as a global CLI.
#
# Installs:
#   - the sandclaude binary  -> $SANDCLAUDE_PREFIX (default /usr/local/bin)
#   - the asset bundle       -> $SANDCLAUDE_HOME/assets (default ~/.sandclaude/assets)
#
# The asset bundle is the Docker build context + support files the installed
# binary needs at runtime, organized by tier:
#   assets/sandbox/  — the sandbox image build context + runtime mounts
#                      (Dockerfile, entrypoint.sh, launcher.py, allowlist-proxy/,
#                       setup/, dind/, skills/)
#   assets/host/     — host-tier assets loaded by host processes (proxy-addon.py)
# Per-project state lives in each project's ./.sandclaude/ directory, created by
# `sandclaude init`.
#
# Env overrides:
#   SANDCLAUDE_PREFIX  directory on $PATH to install the binary  (default /usr/local/bin)
#   SANDCLAUDE_HOME    per-user data dir for assets + global creds (default ~/.sandclaude)
#
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
PREFIX="${SANDCLAUDE_PREFIX:-/usr/local/bin}"
HOME_DIR="${SANDCLAUDE_HOME:-$HOME/.sandclaude}"
ASSETS_DIR="$HOME_DIR/assets"

echo "==> Building sandclaude binary"
if ! command -v go >/dev/null 2>&1; then
  echo "Error: 'go' is not installed. Install Go 1.21+ and re-run." >&2
  exit 1
fi
go build -o "$REPO_DIR/sandclaude" "$REPO_DIR/cmd/sandclaude"

# The dashboard's Terminal tab is a self-contained PTY-over-WebSocket bridge built
# into the binary (see internal/dashboard/terminal.go) — it no longer needs the
# external `ttyd`. mitmproxy (mitmweb) is the one hard runtime dependency install.sh
# guards for.
#
# install_pkg <human-name> <brew-formula> <apt-package> — install a dependency
# using whatever package manager fits the platform:
#   macOS            -> Homebrew
#   Debian/Ubuntu    -> apt-get (with sudo if not root)
# Falls back to a loud error with per-platform instructions otherwise. Detects
# the OS via `uname -s` (and reports arch, for diagnostics).
install_pkg() {
  local name="$1" brew_formula="$2" apt_pkg="$3"
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"
  echo "    installing ${name} (os=${os} arch=${arch})…"
  case "$os" in
    Darwin)
      if command -v brew >/dev/null 2>&1; then
        brew install "$brew_formula"
        return $?
      fi
      echo "Error: Homebrew not found; cannot install ${name}." >&2
      echo "       Install Homebrew (https://brew.sh) then: brew install ${brew_formula}" >&2
      return 1
      ;;
    Linux)
      local sudo=""
      [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1 && sudo="sudo"
      if command -v apt-get >/dev/null 2>&1; then
        $sudo apt-get update -qq && $sudo apt-get install -y -qq "$apt_pkg"
        return $?
      fi
      echo "Error: apt-get not found; cannot auto-install ${name} on this Linux distro." >&2
      echo "       Install ${name} manually and re-run (package: ${apt_pkg})." >&2
      return 1
      ;;
    *)
      echo "Error: unsupported OS '${os}'; install ${name} manually and re-run." >&2
      return 1
      ;;
  esac
}

# mitmproxy provides `mitmweb`, the credential proxy every `sandclaude start`
# launches (see startProxy) — it terminates TLS to inject credentials and enforce
# the allowlist. The most fundamental runtime dependency, so guard it at install
# time. Package name is `mitmproxy` on both Homebrew and apt.
echo "==> Checking for mitmproxy (mitmweb credential proxy)"
if ! command -v mitmweb >/dev/null 2>&1; then
  echo "    mitmproxy not found"
  if ! install_pkg "mitmproxy" "mitmproxy" "mitmproxy"; then
    echo "Error: could not install mitmproxy automatically." >&2
    echo "       mitmweb is the credential proxy behind every 'sandclaude start'." >&2
    echo "         macOS:  brew install mitmproxy" >&2
    echo "         Linux:  apt-get install mitmproxy  (or see https://docs.mitmproxy.org)" >&2
    exit 1
  fi
fi

# tmux is a HOST dependency: sandclaude runs the interactive container inside a
# host tmux session (so it survives detach/reattach and the dashboard can attach
# to it). Same package name on Homebrew and apt.
echo "==> Checking for tmux (host terminal multiplexer)"
if ! command -v tmux >/dev/null 2>&1; then
  echo "    tmux not found"
  if ! install_pkg "tmux" "tmux" "tmux"; then
    echo "Error: could not install tmux automatically." >&2
    echo "       tmux hosts the interactive session behind every 'sandclaude start'." >&2
    echo "         macOS:  brew install tmux" >&2
    echo "         Linux:  apt-get install tmux" >&2
    exit 1
  fi
fi

echo "==> Installing binary to $PREFIX/sandclaude"
if [ -w "$PREFIX" ]; then
  install -m 0755 "$REPO_DIR/sandclaude" "$PREFIX/sandclaude"
else
  echo "    $PREFIX is not writable; using sudo (set SANDCLAUDE_PREFIX=\$HOME/.local/bin to avoid sudo)"
  sudo install -m 0755 "$REPO_DIR/sandclaude" "$PREFIX/sandclaude"
fi

echo "==> Syncing asset bundle to $ASSETS_DIR"
mkdir -p "$ASSETS_DIR"

# The two tier dirs the installed binary resolves at runtime: assets/sandbox
# (build context + mounts) and assets/host (host-loaded assets). Syncing the
# dirs wholesale keeps their internal structure (setup/, dind/, skills/, …).
ASSET_ITEMS=(
  sandbox
  host
)

if command -v rsync >/dev/null 2>&1; then
  # --delete keeps the installed bundle in lock-step with the checkout.
  rsync_args=(-a --delete)
  # Exclude the encrypted allowlist artifact — it's project-specific, seeded on init.
  rsync_args+=(--exclude 'allowed-domains.txt.enc')
  src_paths=()
  for item in "${ASSET_ITEMS[@]}"; do
    src_paths+=("$REPO_DIR/$item")
  done
  rsync "${rsync_args[@]}" "${src_paths[@]}" "$ASSETS_DIR/"
else
  # Fallback without rsync: clear and re-copy.
  for item in "${ASSET_ITEMS[@]}"; do
    rm -rf "${ASSETS_DIR:?}/$item"
    cp -R "$REPO_DIR/$item" "$ASSETS_DIR/$item"
  done
  rm -f "$ASSETS_DIR/allowlist-proxy/allowed-domains.txt.enc"
fi

echo ""
echo "✅ Installed:"
echo "   binary : $PREFIX/sandclaude"
echo "   assets : $ASSETS_DIR"
echo ""
echo "Next steps:"
echo "   cd ~/any-project"
echo "   sandclaude init"
echo "   sandclaude populate-proxy-credentials   # global creds (~/.sandclaude/proxy-credentials.json)"
echo "   sandclaude start"
echo ""
if ! command -v sandclaude >/dev/null 2>&1; then
  echo "Note: '$PREFIX' is not on your \$PATH. Add it, e.g.:"
  echo "   export PATH=\"$PREFIX:\$PATH\""
fi
