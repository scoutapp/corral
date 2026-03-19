# Sandclaude Quick Reference

## Commands

```bash
bash sandclaude init [project]       # Setup project with credentials
bash sandclaude start [project]      # Start Claude + GitHub monitoring
bash sandclaude copy <target>        # Copy to .devcontainer/ in target
bash sandclaude list                 # List all projects
bash sandclaude shell [project]      # Debug shell
bash sandclaude rebuild              # Rebuild Docker image
```

## Setup New Project

```bash
bash sandclaude init myapp
# Prompts for:
#   - Enable GitHub monitoring? (optional)
#   - Mount AWS credentials? (optional)
#   - Workspace directory
```

## Add to Existing Project

```bash
# Method 1: Copy command (creates .devcontainer/)
bash sandclaude copy ~/my-project
code ~/my-project

# Method 2: Clone directly
cd ~/my-project
git clone https://github.com/yourname/sandclaude .devcontainer
rm -rf .devcontainer/.git
code .
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
