# Sandclaude Quick Reference

## First Time Setup

```bash
# Clone into your project's .devcontainer directory
cd ~/my-project
git clone https://github.com/scoutapp/sandclaude.git .devcontainer
cd .devcontainer

# Build the Go binary (required)
go build -o sandclaude main.go
```

## Commands

```bash
./sandclaude init                 # Setup ./project/ config in this directory
./sandclaude start                # Start Claude + proxy
./sandclaude list                 # Show project configuration
./sandclaude remove               # Remove ./project/ directory
./sandclaude shell                # Debug shell in container
./sandclaude reload-firewall      # Encrypt allowlist + SIGHUP proxy
./sandclaude firewall-monitor     # Tail logs/proxy.log
./sandclaude copy <target>        # Copy to .devcontainer/ in target
./sandclaude rebuild              # Rebuild Docker image
./sandclaude help                 # Show help
```

## Setup

```bash
./sandclaude init

# Prompts for:
#   - Enable GitHub monitoring? (optional)
#   - Pass AWS credentials? (optional, reads from ~/.aws)
#   - Enable credential proxy? (optional)
#   - Workspace directory (defaults to parent directory)

# Then encrypt the allowlist (required):
export ALLOWLIST_KEY=<your-passphrase>
./sandclaude reload-firewall
```

## Start Claude

```bash
export ALLOWLIST_KEY=<your-passphrase>
./sandclaude start

# Automatically:
#   - Starts proxy if configured during init
#   - Starts Docker container with firewall
#   - Runs Claude Code in dangerous mode
#   - Starts GitHub monitoring if enabled
```

## Add to Existing Project

```bash
# Copy command (creates .devcontainer/)
./sandclaude copy ~/my-project
code ~/my-project  # Click "Reopen in Container"
```

## Verify Skill Loading

Inside container:
```bash
ls /home/claude/.claude/skills/sandclaude.md
claude --prompt "What firewall domains are allowed?"
```

## Firewall Management

```bash
firewall-helper.sh list                    # List allowed domains
firewall-helper.sh add example.com         # Add domain
firewall-helper.sh remove example.com      # Remove domain
firewall-helper.sh monitor                 # Interactive approval
```

## Project Structure After Copy

```
~/my-project/
├── .devcontainer/
│   ├── devcontainer.json       # VS Code config
│   ├── Dockerfile              # Container image
│   ├── init-firewall.sh        # Firewall setup
│   ├── firewall-helper.sh      # Domain management
│   ├── launcher.py             # GitHub + Claude launcher
│   ├── requirements.txt        # Python deps
│   └── skill/
│       └── SKILL.md           # Claude's knowledge
└── .gitignore                  # Credential protection
```

## Configuration Location

```
.devcontainer/          (or wherever sandclaude lives)
└── project/
    ├── proxy-credentials.json  (mitmproxy credential injection, gitignored)
    └── config/
        ├── repo                (optional: GitHub owner/repo)
        ├── aws_enabled         (optional: if AWS env vars enabled)
        ├── workspace
        └── metadata.json
```

All of `project/` is gitignored — credentials and config stay local.

## Optional Integrations

**GitHub Issue Monitoring:**
- Uses `gh auth token` on the host — token passed as `GH_TOKEN` env var
- Checks every 60 seconds for new unassigned issues
- Auto-assigns issues to bot

**AWS Credentials:**
- Reads `~/.aws/credentials` on the host
- Passes `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION` as env vars
- `~/.aws` is never volume-mounted

**Credential Proxy:**
- Hides real credentials from Claude using mitmproxy
- Claude uses dummy credentials, proxy injects real ones
- Enable during init — proxy starts automatically with `sandclaude start`
- Proxy logs written to `logs/mitm.log`
- Configure in `./project/proxy-credentials.json`:
```json
{
  "api.example.com": {
    "header": "X-API-Key",
    "value": "your-real-key"
  }
}
```

## Troubleshooting

**Connection refused / 403:**
```bash
echo 'example.com' >> allowlist-proxy/allowed-domains.txt
./sandclaude reload-firewall
```

**Skill not loading:**
```bash
ls /home/claude/.claude/skills/sandclaude.md
# Should exist and be readable
```

**Firewall not working:**
```bash
sudo /usr/local/bin/init-firewall.sh  # Reload
sudo iptables -L -v -n                # Check rules
```

**GitHub not monitoring:**
```bash
# Check if gh CLI is authenticated
gh auth status

# Check if repo is set
echo $GITHUB_REPO

# Check logs in container for errors
```
