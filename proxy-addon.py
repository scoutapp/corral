"""
mitmproxy addon for credential injection in sandclaude

This addon intercepts HTTP requests to external services and injects
real credentials from secure storage, allowing Claude Code to use dummy
credentials that can't be exfiltrated.

Usage:
    mitmproxy -s proxy-addon.py --set credentials_file=/path/to/credentials.json

Credentials file format (JSON):
{
  "api.anthropic.com": {
    "header": "X-API-Key",
    "value": "sk-ant-real-key-here"
  },
  "api.github.com": {
    "header": "Authorization",
    "value": "token ghp_real_token_here"
  },
  "api.example.com": {
    "url_param": "api_key",
    "value": "secret123"
  }
}
"""

import json
import logging
from pathlib import Path
from typing import Dict, Optional
from mitmproxy import ctx, http


class CredentialInjector:
    """Injects real credentials into HTTP requests based on hostname"""

    def __init__(self):
        self.credentials: Dict[str, Dict[str, str]] = {}
        self.logger = logging.getLogger(__name__)

    def load(self, loader):
        """Load configuration options"""
        loader.add_option(
            name="credentials_file",
            typespec=str,
            default="~/.config/sandclaude/proxy-credentials.json",
            help="Path to JSON file containing credentials to inject",
        )

    def configure(self, updates):
        """Called when configuration changes"""
        if "credentials_file" in updates:
            self._load_credentials()

    def _load_credentials(self):
        """Load credentials from JSON file"""
        creds_file = Path(ctx.options.credentials_file).expanduser()

        if not creds_file.exists():
            self.logger.warning(f"Credentials file not found: {creds_file}")
            self.logger.info("Create file with format: {\"api.example.com\": {\"header\": \"X-API-Key\", \"value\": \"secret\"}}")
            return

        try:
            with open(creds_file, 'r') as f:
                self.credentials = json.load(f)

            # Log loaded hosts (not the actual credentials)
            hosts = list(self.credentials.keys())
            self.logger.info(f"Loaded credentials for {len(hosts)} hosts: {hosts}")
        except Exception as e:
            self.logger.error(f"Failed to load credentials: {e}")

    def request(self, flow: http.HTTPFlow):
        """Intercept requests and inject credentials"""
        host = flow.request.pretty_host

        if host in self.credentials:
            cred = self.credentials[host]
            value = cred.get("value")
            header_name = cred.get("header")
            url_param = cred.get("url_param")

            if header_name and value:
                flow.request.headers[header_name] = value
                self.logger.debug(f"Injected header credential for {host}")
            elif url_param and value:
                flow.request.query[url_param] = value
                self.logger.debug(f"Injected url_param credential for {host}")


addons = [CredentialInjector()]
