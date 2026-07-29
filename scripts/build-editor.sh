#!/usr/bin/env bash
#
# build-editor.sh — build the self-contained CodeMirror 6 bundle for the
# sandclaude dashboard.
#
# CodeMirror 6 is ES-modules-only; the dashboard frontend is plain (no-build)
# vanilla JS behind a strict CSP. This script bundles editor-src/editor.js into
# a single minified IIFE at internal/dashboard/webui/static/codemirror.bundle.js
# exposing window.SandclaudeEditor.
#
# Requires Node 20+ (esbuild / CM6). If you use nvm:
#   export NVM_DIR="$HOME/.nvm" && . "$NVM_DIR/nvm.sh" && nvm use 20
#
set -euo pipefail

# Resolve repo root relative to this script so it works from any cwd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SRC_DIR="$REPO_ROOT/internal/dashboard/webui/editor-src"
OUT_FILE="$REPO_ROOT/internal/dashboard/webui/static/codemirror.bundle.js"

cd "$SRC_DIR"

echo "==> Installing dependencies (npm install) in $SRC_DIR"
npm install

echo "==> Building bundle (npm run build)"
npm run build

echo ""
echo "==> Bundle written to:"
echo "    $OUT_FILE"
if [ -f "$OUT_FILE" ]; then
  SIZE="$(wc -c < "$OUT_FILE" | tr -d ' ')"
  echo "    size: $SIZE bytes"
fi
