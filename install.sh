#!/usr/bin/env bash
#
# install.sh — build sandclaude and install it as a global CLI.
#
# Installs:
#   - the sandclaude binary  -> $SANDCLAUDE_PREFIX (default /usr/local/bin)
#   - the asset bundle       -> $SANDCLAUDE_HOME/assets (default ~/.sandclaude/assets)
#
# The asset bundle is the Docker build context + support files the installed
# binary needs at runtime (Dockerfile, entrypoint.sh, launcher.py, proxy-addon.py,
# allowlist-proxy/, bin/, .claude/). Per-project state lives in each project's
# ./.sandclaude/ directory, created by `sandclaude init`.
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
go build -o "$REPO_DIR/sandclaude" "$REPO_DIR/main.go"

echo "==> Installing binary to $PREFIX/sandclaude"
if [ -w "$PREFIX" ]; then
  install -m 0755 "$REPO_DIR/sandclaude" "$PREFIX/sandclaude"
else
  echo "    $PREFIX is not writable; using sudo (set SANDCLAUDE_PREFIX=\$HOME/.local/bin to avoid sudo)"
  sudo install -m 0755 "$REPO_DIR/sandclaude" "$PREFIX/sandclaude"
fi

echo "==> Syncing asset bundle to $ASSETS_DIR"
mkdir -p "$ASSETS_DIR"

# The files/dirs the Docker build context and runtime mounts require.
ASSET_ITEMS=(
  Dockerfile
  entrypoint.sh
  launcher.py
  proxy-addon.py
  allowlist-proxy
  bin
  .claude
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
