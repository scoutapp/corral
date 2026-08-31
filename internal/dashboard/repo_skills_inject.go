package dashboard

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/config"
)

// corralContextMarker delimits the corral-managed agent context appended to a
// workspace CLAUDE.md, so it's re-writable without clobbering the repo's own.
const corralContextMarker = "<!-- corral:agent-context -->"

// injectRepoAssets writes a project's effective skills and agent context into
// the workspace at create time, so a sandbox checkout carries them:
//   - each skill  → <workspace>/.corral/skills/<name>/SKILL.md  (the container
//     already mounts .corral/skills/* into ~/.claude/skills). The set is the
//     repo's EffectiveSkills — global auto-add skills plus the repo's own, minus
//     any the repo opted out of.
//   - the context → <workspace>/CLAUDE.md, appended under a corral marker (or
//     created if absent), so Claude discovers it via cwd without clobbering a
//     repo's own committed CLAUDE.md.
//
// Best-effort: a hiccup here must not fail project creation (the project is
// already cloned). Multiple repos' skills are merged by name (last wins on a
// clash — rare, and deterministic by repo order).
func (d *dashboardServer) injectRepoAssets(workspace string, repoIDs []string) {
	s, err := d.getStore()
	if err != nil {
		return
	}
	svc := automations.New(s)

	var contextParts []string
	for _, repoID := range repoIDs {
		if repoID == "" {
			continue
		}
		// Skills → .corral/skills/<name>/SKILL.md. EffectiveSkills resolves the
		// full set for this repo: global auto-adds (minus this repo's opt-outs,
		// plus its opt-ins) with the repo's own skills overriding a global of the
		// same name.
		if skills, err := svc.EffectiveSkills(repoID); err == nil {
			for _, sk := range skills {
				writeSkillFile(workspace, sk.Name, sk.Content)
			}
		}
		// Agent context → collected, appended to CLAUDE.md below.
		if ctx, err := svc.RepoAgentContext(repoID); err == nil {
			if c := strings.TrimSpace(ctx); c != "" {
				contextParts = append(contextParts, c)
			}
		}
	}

	if len(contextParts) > 0 {
		writeAgentContext(workspace, strings.Join(contextParts, "\n\n"))
	}
}

// writeSkillFile writes one skill's SKILL.md under the workspace skills dir. The
// name is validated at save time (validSkillName), so it's a safe dir component.
func writeSkillFile(workspace, name, content string) {
	dir := filepath.Join(workspace, ".corral", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		config.Debugf("inject skill %q: mkdir: %v", name, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		config.Debugf("inject skill %q: write: %v", name, err)
	}
}

// writeAgentContext appends the corral-managed context to <workspace>/CLAUDE.md
// under a marker, replacing any prior corral block, and preserving the repo's own
// content above it. Creates the file if absent.
func writeAgentContext(workspace, context string) {
	path := filepath.Join(workspace, "CLAUDE.md")
	block := corralContextMarker + "\n# Corral context\n\n" + context + "\n"

	existing := ""
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	}

	var out string
	if idx := strings.Index(existing, corralContextMarker); idx >= 0 {
		// Replace everything from the marker onward (our block is always last).
		out = strings.TrimRight(existing[:idx], "\n")
		if out != "" {
			out += "\n\n"
		}
		out += block
	} else if existing != "" {
		out = strings.TrimRight(existing, "\n") + "\n\n" + block
	} else {
		out = block
	}

	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		config.Debugf("inject agent context: write CLAUDE.md: %v", err)
	}
}
