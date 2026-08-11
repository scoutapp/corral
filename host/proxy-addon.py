"""
mitmproxy addon for credential injection in corral

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

import asyncio
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
        self._creds_mtime: Optional[float] = None
        self._watch_task: Optional[asyncio.Task] = None

    def load(self, loader):
        """Load configuration options"""
        loader.add_option(
            name="credentials_file",
            typespec=str,
            default="~/.config/corral/proxy-credentials.json",
            help="Path to JSON file containing credentials to inject",
        )

    def configure(self, updates):
        """Called when configuration changes"""
        if "credentials_file" in updates:
            self._load_credentials()

    def running(self):
        """Called once the proxy is up. Start watching the credentials file so
        updates take effect live — no mitmweb/corral restart needed. This is
        what makes 'set credentials on the fly' work: the dashboard/CLI just
        rewrites the file and this loop picks it up within ~1s."""
        if self._watch_task is None:
            self._watch_task = asyncio.ensure_future(self._watch_credentials())

    async def _watch_credentials(self):
        """Poll the credentials file mtime and reload when it changes. The initial
        load happens via configure(); this only fires on subsequent edits, since
        _load_credentials records the mtime it read."""
        while True:
            await asyncio.sleep(1.0)
            try:
                creds_file = Path(ctx.options.credentials_file).expanduser()
                mtime = creds_file.stat().st_mtime if creds_file.exists() else None
                if mtime != self._creds_mtime:
                    self.logger.info("credentials file changed — reloading")
                    self._load_credentials()
                    # _load_credentials sets _creds_mtime on success; if the file
                    # was removed, record that so we don't reload every tick.
                    if mtime is None:
                        self._creds_mtime = None
            except Exception as e:
                self.logger.debug(f"credentials watch tick failed: {e}")

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
            # Remember mtime so the watch loop doesn't treat this load as a change.
            self._creds_mtime = creds_file.stat().st_mtime

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
