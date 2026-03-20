#!/bin/bash
# Start mitmproxy with credential injection addon

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CREDENTIALS_FILE="${SANDCLAUDE_PROXY_CREDS:-$HOME/.config/sandclaude/proxy-credentials.json}"
PROXY_PORT="${SANDCLAUDE_PROXY_PORT:-8080}"
ADDON_SCRIPT="$SCRIPT_DIR/proxy-addon.py"

echo "Starting mitmproxy credential injection proxy..."
echo "Port: $PROXY_PORT"
echo "Credentials file: $CREDENTIALS_FILE"
echo ""
echo "Configure credentials in $CREDENTIALS_FILE with format:"
echo '  {"api.example.com": {"header": "X-API-Key", "value": "secret"}}'
echo ""

# Start mitmproxy with web interface
exec mitmweb \
    --listen-port "$PROXY_PORT" \
    --set credentials_file="$CREDENTIALS_FILE" \
    --ssl-insecure \
    -s "$ADDON_SCRIPT"
