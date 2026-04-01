---
name: dangerous-claude-firewall
description: Sandboxed Claude Code environment with network firewall protection. Run Claude in dangerous mode while restricting outbound connections to approved domains.
---

> **CRITICAL:** This skill enables Claude Code to run with `--dangerously-skip-permissions` inside a Docker container with network firewall protection. The firewall restricts all outbound connections to pre-approved domains. Interactive approval system allows adding domains on-demand.

# Dangerous Claude with Firewall

## Overview

This is a sandboxed environment for running Claude Code in "dangerous mode" (no permission prompts) with network-level security controls. All outbound connections are restricted by iptables firewall rules to a configurable allowlist.

**Security model:**
- Claude can execute commands without asking (dangerous mode)
- Network firewall restricts what external services Claude can reach
- Interactive approval system for adding new domains
- Credentials mounted read-only, never exposed in container

## Environment Details

**Container capabilities:**
- `NET_ADMIN` - Required for iptables firewall management
- `NET_RAW` - Required for network packet filtering

**Firewall implementation:**
- **Tool**: Go allowlist-proxy (HTTP CONNECT proxy) + iptables egress enforcement
- **Config**: `/home/claude/allowed-domains.txt.enc` (AES-256-GCM encrypted, bind-mounted from host)
- **Logging**: `/home/claude/logs/proxy.log` (ALLOWED/BLOCKED entries per request)
- **Hot-reload**: Send SIGHUP to allowlist-proxy or run `sandclaude reload-firewall` from host

## Pre-approved Domains

The firewall comes with these domains pre-configured:

**Claude Code & Development:**
- `api.anthropic.com` - Claude API
- `registry.npmjs.org` - npm packages
- `registry.yarnpkg.com` - Yarn packages
- `npm.pkg.github.com` - GitHub npm registry
- GitHub API/web/git (all IP ranges from api.github.com/meta)
- `marketplace.visualstudio.com` - VS Code extensions
- `vscode.blob.core.windows.net` - VS Code assets
- `update.code.visualstudio.com` - VS Code updates
- `statsig.anthropic.com`, `statsig.com` - Feature flags
- `sentry.io` - Error tracking

**Go Development:**
- `proxy.golang.org` - Go module proxy
- `sum.golang.org` - Go checksum database
- `go.googlesource.com` - Go source repos
- `golang.org` - Go official site

**CDNs:**
- `cdn.jsdelivr.net`
- `unpkg.com`

**Container Registries:**
- `registry.hub.docker.com`
- `ghcr.io`
- `quay.io`
- `pkgs.dev.azure.com`

**Always allowed (not in config file):**
- DNS (port 53)
- SSH (port 22)
- Localhost
- Host Docker network

## Firewall Management

### Adding Domains

When Claude Code needs to access a blocked domain, edit the allowlist and reload:

**Method 1: Hot-reload from host (container stays running)**
```bash
# Edit the plaintext allowlist
vim allowlist-proxy/allowed-domains.txt
# Encrypt and SIGHUP the proxy
./sandclaude reload-firewall [project]
```

**Method 2: Restart the container**
```bash
# Edit the plaintext allowlist
vim allowlist-proxy/allowed-domains.txt
# Re-encrypt
./sandclaude reload-firewall
# Restart
./sandclaude start [project]
```

### Viewing the Proxy Log
```bash
# From host
./sandclaude firewall-monitor [project]
# Inside container
tail -f ~/logs/proxy.log
```

## Firewall Technical Details

**Initialization:** Runs automatically in `entrypoint.sh` on container start.

**Architecture:**
1. Encrypted `allowed-domains.txt.enc` (bind-mounted from host) → decrypted in-memory by allowlist-proxy
2. allowlist-proxy listens on `127.0.0.1:3128` — validates each CONNECT/HTTP request by domain
3. iptables OUTPUT chain → allow only `proxyuser` (the proxy's UID) for direct TCP; all others REJECT
4. All traffic must flow through the proxy; ALLOWED/BLOCKED logged to `~/logs/proxy.log`

**Hot-reload:**
- Write new `.enc` file to host (via `sandclaude reload-firewall`)
- Bind mount means container sees it immediately
- SIGHUP to allowlist-proxy atomically swaps the in-memory domain set

**Performance:**
- O(1) domain lookups (hash map)
- No DNS resolution — domain-level matching with subdomain support
- No restart required to update allowlist

## Credential Handling

**Rule: Credentials NEVER enter the container filesystem in readable form.**

The `sandclaude` wrapper mounts:
- `~/.claude` → `/home/claude/.claude` (Claude credentials)
- `~/.claude.json` → `/home/claude/.claude.json` (session state)
- `~/.config/gh` → `/home/claude/.config/gh:ro` (GitHub CLI, read-only)
- `$GH_TOKEN` environment variable (if `gh auth token` succeeds)

All credential mounts are handled by the Docker runtime. They exist in the container but are never copied, logged, or written elsewhere.

**Never ask Claude to:**
- Read credential files directly
- Echo or display tokens
- Copy credentials to other locations
- Commit credentials to git
- Send credentials over network

If Claude needs authenticated access:
- GitHub: Use `gh` CLI (already authenticated via mount)
- npm: Use `npm login` interactively (persists to mounted `~/.claude` if needed)
- Other services: Mount credentials read-only or use env vars

## Docker-in-Docker (DinD)

When `DIND_ENABLED=1` is set in the environment, an inner Docker daemon is running at `unix:///var/run/dind/docker.sock`. `DOCKER_HOST` is already exported to point there.

**Key facts:**
- All `docker` commands you run go to the **inner daemon**, not the host
- Inner containers sit on `172.18.0.0/16` bridge network
- `~/.docker/config.json` injects `HTTP_PROXY` and `HTTPS_PROXY` into every inner container automatically — **no Dockerfile or compose env changes needed**
- The allowlist proxy listens on `0.0.0.0:3128`, reachable from inner containers via their bridge gateway (`172.18.0.1` for the default DinD bridge; compose networks get addresses in `172.18.0.0/15`)
- The allowlist proxy forwards to mitmproxy — inner containers must trust the mitmproxy CA cert. The `~/bin/docker` wrapper automatically injects the CA cert into any image built via `docker build` or `docker compose build` via the **cert-injector** sidecar. In most cases this is transparent. However, if an inner container produces SSL errors like `509 Certificate Verify Failed` or similar TLS errors, the mitmproxy CA was likely not injected correctly — check `/usr/local/share/ca-certificates/mitmproxy-ca.crt` inside the container and re-run `update-ca-certificates` if missing
- iptables allows `172.18.0.0/15` in the OUTPUT chain so the proxy can respond to inner container connections
- Inner containers are destroyed when sandclaude exits

**Running inner containers:**
```bash
# Build and start a Rails app
docker build -t myapp .
docker run -d -p 3000:3000 --name rails myapp

# Shell into it
docker exec -it rails bash

# Logs
docker logs rails -f

# Port 3000 is accessible on the user's host machine at http://localhost:3000
# (because the outer sandclaude container has -p 3000:3000)
```

**Inner container network access:**
- Inner containers go through the same allowlist proxy as everything else
- If an inner container needs a domain not in the allowlist, add it:
  ```bash
  echo 'rubygems.org' >> .firewall/allowed-domains.txt
  kill -HUP $(pgrep allowlist-proxy)
  ```
- Common domains to add for Rails: `rubygems.org`, `index.rubygems.org`
- Common domains to add for Django/Python: `pypi.org`, `files.pythonhosted.org`

**Multi-container apps:**
```bash
# Containers can talk to each other by name when on the same docker network
docker network create app-net
docker run -d --network app-net --name postgres postgres:16
docker run -d --network app-net --name redis redis:7
docker run -d --network app-net -p 3000:3000 --name web myapp
```

**Verify inner daemon:**
```bash
docker info          # Shows inner daemon info
docker ps            # Lists only inner containers
echo $DOCKER_HOST    # unix:///var/run/dind/docker.sock
```

**Storage driver issues:**
If image builds fail with overlay2 errors, the `DIND_STORAGE_DRIVER=vfs` fallback works universally (slower but compatible). Check `cat .firewall/dockerd.log` for errors.

## GitHub Issue Monitoring Mode

When GitHub issue monitoring is enabled during `sandclaude init`, the container automatically monitors a GitHub repository for new issues and works on them autonomously.

### Detecting Issue Monitoring Mode

Check if you're running in issue monitoring mode by looking for these environment variables:

```bash
# Inside the container
echo $GITHUB_REPO                      # e.g., "owner/repo"
echo $GITHUB_ISSUE_LABELS              # e.g., "bug,enhancement" (empty = all labels)
echo $GITHUB_ISSUE_SINCE               # e.g., "02-05-2025" (MM-DD-YYYY format, empty = all time)
echo $GITHUB_CLOSE_NON_REPRODUCIBLE    # "1" = close issues that can't be reproduced
```

**If `GITHUB_REPO` is set, you are in issue monitoring mode.**

The launcher (`/home/claude/launcher.py`) polls for new issues matching the configured labels and date range every 60 seconds. When an unassigned issue is found:

1. The monitor self-assigns the issue to the current GitHub user
2. Waits 2-10 seconds (random) to verify assignment still holds
3. Checks if the issue is still assigned to the current user (prevents race conditions with other sandclaude instances or manual reassignment)
4. If still assigned, begins working on the issue
5. After completing work, immediately checks for the next issue (no 60-second wait)

This prevents multiple sandclaude instances from working on the same issue simultaneously.

### Non-Reproducible Issue Handling

If `GITHUB_CLOSE_NON_REPRODUCIBLE=1` is set, you are authorized to close issues that cannot be reproduced after thorough investigation. This should only be done after exhaustive efforts to reproduce the issue.

### Issue Monitoring Workflow

When an issue is assigned to you in monitoring mode:

1. **Understand the issue requirements**
   - Read the full issue description and comments
   - Identify acceptance criteria
   - Ask clarifying questions via issue comments if needed

2. **Create a feature branch**
   - Branch from `main` (or the default branch)
   - Use descriptive branch names: `fix/issue-123-memory-leak` or `feat/issue-456-user-dashboard`

3. **Implement the solution**
   - Write clean, maintainable code
   - Follow the project's existing code style and patterns
   - Keep changes focused on the issue at hand

4. **Test thoroughly**
   - Write comprehensive unit tests covering the changes
   - Write integration tests if the change affects multiple components
   - **For web applications**: Use Chromium (via Selenium) to test UI changes visually
     ```python
     from selenium import webdriver
     options = webdriver.ChromeOptions()
     options.add_argument('--headless')
     driver = webdriver.Chrome(options=options)
     driver.get('http://localhost:3000/feature')
     # Test UI behavior, take screenshots, validate elements
     driver.save_screenshot('/tmp/test-result.png')
     ```
   - Run all existing tests to ensure no regressions: `npm test`, `pytest`, `go test ./...`, etc.
   - Test edge cases and error conditions

5. **Follow trunk-based development practices**
   - Keep PRs small: **ideally 0-300 lines changed** (excluding tests and generated code)
   - If implementation exceeds ~300 lines:
     - Break into multiple smaller PRs with logical boundaries
     - Create downstream feature branches (e.g., `feat/issue-123-part-1`, `feat/issue-123-part-2`)
     - Each PR should be independently reviewable and testable
     - Open PRs in sequence, with later PRs depending on earlier ones
   - Commit frequently with clear, descriptive messages

6. **Create a comprehensive pull request**

   Use `gh pr create` with a detailed description:

   ```bash
   gh pr create --title "Fix: Memory leak in WebSocket connection pool (#123)" --body "$(cat <<'EOF'
   ## Summary
   Fixes #123

   This PR addresses the memory leak in the WebSocket connection pool by:
   - Implementing proper connection cleanup on disconnect
   - Adding a TTL-based eviction policy for stale connections
   - Introducing connection lifecycle logging for debugging

   ## Changes
   - `src/websocket/pool.go`: Added `Close()` method and TTL tracking
   - `src/websocket/connection.go`: Added cleanup hooks
   - `tests/websocket_test.go`: Added unit tests for cleanup logic

   ## Testing
   - **Unit tests**: Added 8 new tests covering cleanup, TTL, and edge cases
   - **Integration test**: Verified connection pool behavior under load (1000 concurrent connections)
   - **Manual testing**: Ran application for 6 hours under realistic load, monitored memory usage (stable at ~50MB, down from 500MB+)
   - **Chromium UI test**: Verified WebSocket reconnection UI works correctly (screenshot attached)

   **Test results:**
   \`\`\`
   $ go test ./src/websocket -v
   === RUN   TestConnectionPoolCleanup
   --- PASS: TestConnectionPoolCleanup (0.05s)
   === RUN   TestConnectionTTL
   --- PASS: TestConnectionTTL (0.12s)
   ...
   PASS
   ok      github.com/org/repo/src/websocket    1.234s
   \`\`\`

   ## Documentation
   - Updated `/docs/architecture/websocket.md` with cleanup behavior
   - Added inline comments explaining TTL logic
   - See WebSocket best practices: https://docs.example.com/websocket-pooling

   ## Screenshots (if UI changes)
   ![Connection UI](https://user-images.githubusercontent.com/.../screenshot.png)

   ---
   🤖 Generated with [Claude Code](https://claude.com/claude-code)

   Co-Authored-By: Claude <noreply@anthropic.com>
   EOF
   )"
   ```

   **PR requirements:**
   - Link to the original issue with `Fixes #123` or `Closes #123`
   - Summarize **what** changed and **why**
   - List all modified files with brief explanations
   - Include test results (pass/fail counts, coverage if available)
   - Describe **how you tested it**:
     - Unit tests: what they cover
     - Integration tests: what scenarios were tested
     - Manual testing: steps taken, duration, observations
     - Chromium tests: for UI changes, include screenshots or describe interactions
   - Link to relevant documentation (architecture docs, API docs, external references)
   - Include screenshots or GIFs for UI changes
   - **Always** add Co-Authored-By: `Claude <noreply@anthropic.com>`

7. **Keep PRs focused and small**
   - **Target: 0-300 lines changed** (excluding auto-generated code, lockfiles, test fixtures)
   - If exceeding 300 lines, assess whether the change can be split:
     - Refactoring first, feature second
     - Backend API first, frontend second
     - Core logic first, peripheral features second
   - Create dependent PRs that build on each other
   - Label dependent PRs clearly: "Part 1/3", "Part 2/3", etc.

8. **Add comprehensive tests**
   - Every PR must include tests unless it's purely documentation
   - Aim for high code coverage of new/changed code (80%+ where feasible)
   - Test matrix should cover:
     - Happy path (expected behavior)
     - Edge cases (empty inputs, boundary values, null/undefined)
     - Error handling (network failures, invalid inputs, race conditions)
     - Integration points (API contracts, database interactions)
   - For UI: Use Chromium to verify visual behavior and interactions

9. **Request review**
   - After opening the PR, comment on the original issue with the PR link
   - If the PR addresses only part of the issue, explain what remains

10. **Respond to review feedback**
    - Address reviewer comments promptly
    - Push additional commits or amend as needed
    - Re-test after making changes

### Example: Working on an Issue

```bash
# 1. Verify monitoring mode is active
echo $GITHUB_REPO  # Should output: owner/repo

# 2. Issue assigned: "Fix login button not responding on mobile"
gh issue view 789

# 3. Create branch
git checkout -b fix/issue-789-mobile-login-button

# 4. Implement fix
# Edit src/components/LoginButton.jsx (23 lines changed)

# 5. Write tests
# Add tests/LoginButton.test.jsx (45 lines)

# 6. Test with Chromium
python3 << 'EOF'
from selenium import webdriver
from selenium.webdriver.common.by import By

options = webdriver.ChromeOptions()
options.add_argument('--headless')
options.add_argument('--no-sandbox')
driver = webdriver.Chrome(options=options)

# Test mobile viewport
driver.set_window_size(375, 667)
driver.get('http://localhost:3000/login')

# Click login button
button = driver.find_element(By.ID, 'login-button')
button.click()

# Verify behavior
assert driver.current_url.endswith('/dashboard'), "Login failed"
driver.save_screenshot('/tmp/mobile-login-test.png')
driver.quit()
print("✓ Mobile login test passed")
EOF

# 7. Run full test suite
npm test

# 8. Commit changes
git add .
git commit -m "Fix: Ensure login button responds to touch events on mobile

- Added touch event handlers to LoginButton component
- Fixed button z-index issue blocking touch events
- Added Chromium test for mobile viewport interaction

Fixes #789"

# 9. Push and create PR
git push -u origin fix/issue-789-mobile-login-button

gh pr create --title "Fix: Login button not responding on mobile (#789)" --body "$(cat <<'EOF'
## Summary
Fixes #789

The login button was not responding to touch events on mobile devices due to a CSS z-index issue and missing touch event handlers.

## Changes
- `src/components/LoginButton.jsx`: Added touch event listeners
- `src/styles/LoginButton.css`: Fixed z-index stacking
- `tests/LoginButton.test.jsx`: Added unit tests for touch events

## Testing
- **Unit tests**: 5 new tests covering touch/click handlers (all passing)
- **Chromium mobile test**: Verified button interaction at 375x667 viewport (screenshot attached)
- **Manual testing**: Tested on iPhone 12 simulator and physical Android device (Pixel 5)

**Test output:**
\`\`\`
PASS tests/LoginButton.test.jsx
  ✓ handles touch events (45ms)
  ✓ handles click events (23ms)
  ✓ prevents double-submit (67ms)
  ...
Test Suites: 1 passed, 1 total
Tests:       5 passed, 5 total
\`\`\`

## Screenshots
![Mobile login test](file:///tmp/mobile-login-test.png)

---
🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"

# 10. Comment on issue
gh issue comment 789 --body "PR opened: #890"
```

### Breaking Up Large Changes

If your implementation is approaching 300+ lines:

```bash
# Example: Large feature needs 600 lines
# Break into 2 PRs:

# Part 1: Backend API (250 lines)
git checkout -b feat/issue-456-user-dashboard-api
# Implement API endpoints, tests
git commit -m "feat: Add user dashboard API endpoints (Part 1/2)"
git push -u origin feat/issue-456-user-dashboard-api
gh pr create --title "feat: User dashboard API endpoints (#456, Part 1/2)"

# Part 2: Frontend UI (300 lines)
git checkout -b feat/issue-456-user-dashboard-ui
# Implement UI components, tests
git commit -m "feat: Add user dashboard UI components (Part 2/2)"
git push -u origin feat/issue-456-user-dashboard-ui
gh pr create --title "feat: User dashboard UI components (#456, Part 2/2)" \
  --body "Depends on #<PR-NUMBER-PART-1>"
```

### Handling Non-Reproducible Issues

If `GITHUB_CLOSE_NON_REPRODUCIBLE=1` is set and you cannot reproduce an issue after exhaustive investigation, you may close it with comprehensive documentation of your reproduction attempts.

**Requirements before closing as non-reproducible:**

1. **Attempt reproduction in multiple ways** (minimum 3-5 different approaches):
   - Follow the exact steps described in the issue
   - Try variations of the steps (different inputs, edge cases)
   - Test in different environments (if applicable: different browsers, OS, configurations)
   - Test with different data sets
   - Use Chromium for UI-related issues to visually verify behavior

2. **Document every reproduction attempt thoroughly**:
   ```bash
   gh issue comment 123 --body "$(cat <<'EOF'
   ## Reproduction Investigation

   I attempted to reproduce this issue across multiple scenarios but was unable to replicate the reported behavior.

   ### Environment
   - OS: Ubuntu 24.04 (Linux 6.x)
   - Application version: v2.3.1 (commit: abc123)
   - Browser: Chromium 120.0 (for UI testing)
   - Node.js: v22.1.0
   - Database: PostgreSQL 16.2

   ### Reproduction Attempts

   #### Attempt 1: Exact steps from issue description
   **Steps:**
   1. Navigated to /dashboard
   2. Clicked "Export Data" button
   3. Selected CSV format
   4. Clicked "Download"

   **Expected (per issue):** Export fails with 500 error
   **Actual result:** Export succeeded, CSV downloaded successfully (2.3 MB, 1000 rows)
   **Evidence:** Screenshot attached, logs show successful export
   ```bash
   [2025-01-15 10:23:45] INFO: Export initiated user_id=789
   [2025-01-15 10:23:46] INFO: Generated CSV size=2.3MB rows=1000
   [2025-01-15 10:23:46] INFO: Export completed successfully
   ```

   #### Attempt 2: Large dataset (edge case)
   **Steps:**
   1. Created test account with 10,000 records (10x normal size)
   2. Attempted export of all records
   3. Monitored memory usage and response times

   **Expected (per issue):** 500 error or timeout
   **Actual result:** Export succeeded after 8 seconds, CSV generated successfully
   **Evidence:** Memory peaked at 250MB (within limits), no errors in logs

   #### Attempt 3: Multiple concurrent exports
   **Steps:**
   1. Opened 5 browser tabs simultaneously
   2. Initiated export from each tab at the same time
   3. Monitored server load and database connections

   **Expected (per issue):** Server crash or 500 error
   **Actual result:** All 5 exports completed successfully (8-12 seconds each)
   **Evidence:** Server load peaked at 45% CPU, no errors

   #### Attempt 4: Different browsers
   **Steps:**
   1. Tested in Chromium (headless)
   2. Tested in Firefox (via Selenium)
   3. Tested API endpoint directly with curl

   **Actual results:** All methods succeeded without errors

   #### Attempt 5: Code inspection
   **Analysis:**
   - Reviewed export controller code (`app/controllers/export_controller.rb:45-89`)
   - Checked error handling paths - all properly wrapped with try/catch
   - Verified database query optimization - uses pagination and streaming
   - Examined recent commits - no changes to export functionality in past 3 months

   **Findings:** Code appears robust with proper error handling and resource management

   ### Testing Code
   I wrote automated tests to verify export functionality:
   \`\`\`python
   from selenium import webdriver
   from selenium.webdriver.common.by import By
   import time

   options = webdriver.ChromeOptions()
   options.add_argument('--headless')
   driver = webdriver.Chrome(options=options)

   # Test export
   driver.get('http://localhost:3000/dashboard')
   driver.find_element(By.ID, 'export-btn').click()
   driver.find_element(By.ID, 'format-csv').click()
   driver.find_element(By.ID, 'download-btn').click()

   # Wait for download
   time.sleep(5)

   # Verify no error messages
   errors = driver.find_elements(By.CLASS_NAME, 'error-message')
   assert len(errors) == 0, "Found error messages on page"
   print("✓ Export completed without errors")
   driver.quit()
   \`\`\`

   **Test results:** All tests passed (0 failures)

   ### Conclusion
   After 5 comprehensive reproduction attempts across different scenarios, environments, and data sets, I was unable to reproduce the reported issue. The export functionality appears to be working correctly in all tested configurations.

   ### Possible Explanations
   - Issue may have been fixed in a recent deployment (v2.3.0 → v2.3.1)
   - Issue may be specific to a particular configuration not tested (e.g., specific browser version, network conditions, or data characteristics)
   - Issue may have been intermittent and no longer occurring

   ### Recommendation
   Closing this issue as non-reproducible. If the issue reoccurs, please reopen with:
   - Exact browser/client version
   - Screenshot or video of the error
   - Network tab showing the failing request
   - Any relevant console errors
   - Sample data that triggers the issue (if applicable)

   ---
   🤖 Generated with [Claude Code](https://claude.com/claude-code)

   Co-Authored-By: Claude <noreply@anthropic.com>
   EOF
   )"
   ```

3. **Close the issue with appropriate label:**
   ```bash
   gh issue close 123 --reason "not planned" --comment "Closing as non-reproducible after exhaustive testing. See detailed reproduction attempts above. Please reopen if you can provide additional reproduction steps."
   ```

4. **Only close if:**
   - You've attempted reproduction at least 3-5 different ways
   - You've documented every attempt with evidence (logs, screenshots, test results)
   - You've inspected the relevant code for potential issues
   - You've tested edge cases and variations
   - `GITHUB_CLOSE_NON_REPRODUCIBLE=1` is set

5. **Never close as non-reproducible if:**
   - You haven't tried multiple reproduction approaches
   - The issue describes a plausible bug (even if you can't reproduce it)
   - You haven't documented your attempts comprehensively
   - The issue is recent (< 7 days old) - give reporter time to provide more details
   - The issue has active discussion or additional context being provided

### Issue Monitoring Best Practices

- **Communicate clearly**: Comment on issues to provide status updates
- **Test thoroughly**: Use all available testing tools (unit, integration, Chromium)
- **Document changes**: Update relevant docs in the same PR
- **Keep PRs atomic**: Each PR should be a complete, reviewable unit of work
- **Follow conventions**: Match the project's coding style, commit message format, and PR template
- **Link everything**: Always reference the issue number in commits and PRs
- **Document reproduction attempts**: If closing as non-reproducible, provide exhaustive documentation
- **Always add Co-Authored-By**: Include `Co-Authored-By: Claude <noreply@anthropic.com>` in all comments and PRs

## When to Use This Skill

**Use this environment when:**
- Running untrusted or experimental code
- Allowing Claude full command execution without prompts
- Testing integrations that need network access
- Building/testing Node.js or Go projects
- Need both freedom and network-level safety
- Running in GitHub issue monitoring mode to autonomously work on issues

**Do NOT use when:**
- You need full unrestricted internet (defeats the purpose)
- Working with services not in the allowlist (add them first)
- Production deployments (this is a development sandbox)

## Common Workflows

### Starting a new project
```bash
# From host
./sandclaude start

# Claude can now:
# - Install npm/yarn packages (allowlist includes registries)
# - Clone from GitHub (allowlist includes GitHub)
# - Run builds and tests
# - Access Claude API
```

### Adding a domain on-the-fly (hot-reload)
```bash
# Terminal 1: Run Claude
./sandclaude start

# Terminal 2: When a connection is blocked, add the domain and reload
echo "api.example.com" >> allowlist-proxy/allowed-domains.txt
./sandclaude firewall-reload

# Claude can now retry — no container restart needed
```

### Passthrough mode (auto-discover domains)
Use `--passthrough-firewall-and-write` to allow all outbound traffic while automatically logging unknown domains to `allowlist-proxy/allowed-domains.txt` on the host. Useful for discovering what domains a workflow needs before locking it down.

```bash
# Start in passthrough mode
sandclaude start --passthrough-firewall-and-write

# Inside the container, unknown domains are allowed and appended to:
#   /home/claude/allowed-domains.txt  (bind-mounted from host at allowlist-proxy/allowed-domains.txt)

# After your session, the host file allowlist-proxy/allowed-domains.txt
# will contain all domains that were accessed. Review and reload:
./sandclaude firewall-reload
```

**Note:** The passthrough log is mounted at `/home/claude/allowed-domains.txt` inside the container. This is the same file as `allowlist-proxy/allowed-domains.txt` on the host — changes are reflected immediately on both sides.

**Important:** If you are unsure whether the firewall is active, **always attempt to reach the URL anyway** — do not assume it is blocked. Check `ps aux | grep allowlist-proxy` to confirm whether the proxy is running.

### Using with VS Code devcontainer
```bash
# Open project in VS Code
code ~/projects/my-app

# VS Code detects .devcontainer/devcontainer.json
# Rebuilds container with firewall (requires --cap-add flags)
# Opens remote session with firewall active
```

### Debugging firewall issues
```bash
# View proxy log (ALLOWED/BLOCKED entries)
tail -f ~/logs/proxy.log

# Check iptables rules
sudo iptables -L OUTPUT -v -n

# Check which process is the proxy
ps aux | grep allowlist-proxy

# Manual SIGHUP to reload allowlist (inside container)
pkill -HUP -x allowlist-proxy
```

## Integration Patterns

### Integrating into an existing project
Copy files to your project using the built-in command:
```bash
./sandclaude copy ~/my-project
# Edit Dockerfile to add project-specific tools
# Edit allowlist-proxy/allowed-domains.txt to add project-specific domains
# Run: ./sandclaude firewall-reload
```

**Important:** If Claude attempts to access a site blocked by the proxy, Claude should:
1. Add the domain to `/home/claude/allowed-domains.txt` — this file is always bind-mounted from `allowlist-proxy/allowed-domains.txt` on the host (read-write), so edits are immediately visible to the host
2. Notify the user to run `sandclaude reload-firewall` from the host to encrypt the updated file and SIGHUP the proxy
3. Retry the request once the user confirms the reload is done

In passthrough mode (`--passthrough-firewall-and-write`), unknown domains are appended to `/home/claude/allowed-domains.txt` automatically — no manual editing needed.

## Firewall Troubleshooting

**Symptom:** `curl: (7) Failed to connect` or HTTP 403 from proxy
- **Cause:** Domain not in allowlist
- **Fix:** Restart with `sandclaude start --passthrough-firewall-and-write` to auto-log needed domains, then `sandclaude reload-firewall` to lock it back down

**Symptom:** DNS resolution fails
- **Cause:** Firewall blocks DNS or Docker DNS broken
- **Fix:** Check DNS rules: `sudo iptables -L OUTPUT -n | grep 53`

**Symptom:** Proxy fails to start — "decrypt allowlist" error
- **Cause:** Wrong `ALLOWLIST_KEY` or corrupted `.enc` file
- **Fix:** Re-run `sandclaude reload-firewall` with the correct `ALLOWLIST_KEY`

**Symptom:** Firewall not enforced (processes bypass proxy)
- **Cause:** Container missing `NET_ADMIN` capability
- **Fix:** Ensure `--cap-add=NET_ADMIN --cap-add=NET_RAW` in docker run args

**Symptom:** All connections blocked including allowed domains
- **Cause:** iptables rules not applied or proxy not running
- **Fix:** Check `sudo iptables -L OUTPUT -n` and `ps aux | grep allowlist-proxy`

## Architecture Decisions

**Why application-level proxy instead of iptables/ipset?**
- Domain-level matching (not IP-level) — handles CDNs and cloud services correctly
- Hot-reloadable allowlist without touching iptables
- Readable ALLOWED/BLOCKED log per request
- No DNS pre-resolution needed

**Why encrypt the allowlist?**
- Allowlist reflects permitted capabilities — shouldn't be trivially editable inside the container
- AES-256-GCM provides both confidentiality and integrity (tamper detection)
- Key stays on host; container only has the ciphertext + key via env var at runtime

**Why iptables on top of the proxy?**
- Prevents processes from bypassing the proxy via direct TCP (env var bypass, etc.)
- Only `proxyuser` (the proxy UID) can make direct outbound TCP connections

## Extending the Firewall

**Adding domains:**
```bash
# On host
echo "api.stripe.com" >> allowlist-proxy/allowed-domains.txt
./sandclaude firewall-reload
```

**Allowing all traffic (disable firewall):**
```bash
sudo iptables -P OUTPUT ACCEPT
sudo iptables -F OUTPUT
```

## Known Limitations

1. **No wildcard domain support** - Must add each subdomain explicitly
2. **No per-process rules** - Firewall applies to entire container
3. **Requires privileged capabilities** - NET_ADMIN and NET_RAW needed
4. **Key in env var** - ALLOWLIST_KEY visible in process list inside container

## Security Considerations

**What the firewall protects against:**
- Accidental data exfiltration to unauthorized domains
- Supply chain attacks (malicious npm packages calling home)
- Unintended API calls during development
- Credential leakage via HTTP to unknown endpoints

**What the firewall does NOT protect against:**
- Attacks using allowed domains (e.g., GitHub gists, npm packages)
- Local privilege escalation (container runs as non-root but has sudo)
- Attacks via mounted volumes (workspace is read-write)
- DNS tunneling (DNS is unrestricted for resolution)

**Additional hardening (not implemented):**
- Drop `sudo` access entirely (requires manual firewall init)
- Read-only workspace mount (breaks most development workflows)
- Restrict DNS to specific nameservers
- Rate limit allowed domains
- Network namespace isolation

This is a **development sandbox**, not a production security boundary. Use for local experimentation, not for running untrusted production workloads.
