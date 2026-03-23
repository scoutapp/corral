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
./sandclaude init [project]       # Setup project with credentials (prompts if no arg)
./sandclaude start [project]      # Start Claude + proxy (prompts if no arg)
./sandclaude list                 # List all projects
./sandclaude remove <project>     # Remove a project
./sandclaude shell [project]      # Debug shell
./sandclaude copy <target>        # Copy to .devcontainer/ in target
./sandclaude rebuild              # Rebuild Docker image
./sandclaude help                 # Show help
```

## Setup New Project

```bash
./sandclaude init myapp
# Or use current directory name:
./sandclaude init

# Prompts for:
#   - Project name (defaults to current directory)
#   - Enable GitHub monitoring? (optional)
#   - Mount AWS credentials? (optional)
#   - Enable credential proxy? (optional)
#   - Workspace directory (defaults to parent directory)
```

## Start Claude

```bash
./sandclaude start myapp
# Or use current directory name:
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
~/.config/sandclaude/projects/
└── myapp/
    └── config/
        ├── repo                (optional: GitHub owner/repo)
        ├── aws_enabled         (optional: if AWS mount enabled)
        ├── workspace
        └── metadata.json
```

## Optional Integrations

**GitHub Issue Monitoring:**
- Uses `gh` CLI (authenticated via host mount at `~/.config/gh`)
- Checks every 60 seconds for new unassigned issues
- Auto-assigns issues to bot
- Generates prompt with title/description
- Adds comment to GitHub tracking progress

**AWS Credentials:**
- Mounts `~/.aws` directory read-only
- Works with STS temporary credentials
- Supports all standard AWS credential methods

**Credential Proxy:**
- Hides real credentials from Claude using mitmproxy
- Claude uses dummy credentials, proxy injects real ones
- Prevents credential exfiltration
- Enable during init - proxy starts automatically with `sandclaude start`
- Proxy logs written to `mitm.log`
- Configure in `~/.config/sandclaude/proxy-credentials.json`:
```json
{
  "api.example.com": {
    "header": "X-API-Key",
    "value": "your-real-key"
  }
}
```

## Troubleshooting

**Connection refused:**
```bash
firewall-helper.sh monitor  # Approve domains interactively
# OR
firewall-helper.sh add domain.com
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
