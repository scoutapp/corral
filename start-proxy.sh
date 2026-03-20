#!/bin/bash
# Start mitmproxy with credential injection addon

set -euo pipefail

CREDENTIALS_FILE="${SANDCLAUDE_PROXY_CREDS:-$HOME/.config/sandclaude/proxy-credentials.json}"
PROXY_PORT="${SANDCLAUDE_PROXY_PORT:-8080}"
ADDON_SCRIPT="/usr/local/bin/proxy-addon.py"

echo "Starting mitmproxy credential injection proxy..."
echo "Port: $PROXY_PORT"
echo "Credentials file: $CREDENTIALS_FILE"

if [ ! -f "$CREDENTIALS_FILE" ]; then
    echo ""
    echo "⚠️  Warning: Credentials file not found at $CREDENTIALS_FILE"
    echo ""
    echo "Create it with this format:"
    echo '{'
    echo '  "api.anthropic.com": {'
    echo '    "header": "X-API-Key",'
    echo '    "value": "sk-ant-your-real-key"'
    echo '  },'
    echo '  "api.github.com": {'
    echo '    "header": "Authorization",'
    echo '    "value": "Bearer ghp_your_real_token"'
    echo '  }'
    echo '}'
    echo ""
    echo "Press Ctrl+C to exit or wait to start proxy anyway..."
    sleep 3
fi

# Start mitmproxy with web interface
exec mitm

web \
    --listen-port "$PROXY_PORT" \
    --set credentials_file="$CREDENTIALS_FILE" \
    --ssl-insecure \
    -s "$ADDON_SCRIPT"
