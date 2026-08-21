package dashboard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/convstore"
	"github.com/scoutapp/corral/internal/repos"
)

// draftBootGuidance drafts the `worker.boot_guidance` prompt for a repo, GROUNDED
// IN EVIDENCE from what we actually learned booting it: the repo's captured boot
// conversations (worker/project runs — their timings, what got reused vs rebuilt,
// what hung/corrupted/OOM'd) plus a fresh checkout to read the real stack. The
// result (streamed, then a final "draft" frame) is the recommended repo-specific
// recipe the user reviews and saves as this repo's override in the Prompts
// section. Two turns like the other drafters: research, then emit only the text.
func (d *dashboardServer) draftBootGuidance(ctx context.Context, claudeBin, repoID, extra string, send func(chatServerMsg) error) {
	repoName := repoID
	if r, err := repos.Get(repoID); err == nil && r.Name != "" {
		repoName = r.Name
	}
	_ = send(chatServerMsg{Type: "text", Text: "Reviewing what we learned booting " + repoName + "…\n"})

	evidence := d.bootEvidence(repoID)

	// Fresh temp checkout so claude can read the actual stack (compose, Gemfile/
	// package.json, Dockerfile, Procfile, language version).
	checkout := ""
	tmpRoot := filepath.Join(config.CorralHome(), "tmp")
	_ = os.MkdirAll(tmpRoot, 0o755)
	if tmp, terr := os.MkdirTemp(tmpRoot, "boot-guidance-*"); terr == nil {
		defer os.RemoveAll(tmp)
		checkout = filepath.Join(tmp, "repo")
		_ = send(chatServerMsg{Type: "text", Text: "Checking out the repo to read its stack…\n"})
		if cerr := repos.CloneLocal(repoID, checkout, ""); cerr != nil {
			_ = send(chatServerMsg{Type: "text", Text: "(checkout unavailable; drafting from evidence only)\n"})
			checkout = ""
		}
	}

	// Turn 1 — research, grounded in the evidence.
	research := "You are drafting REPO-SPECIFIC guidance appended to a worker that boots this app in a Corral " +
		"sandbox, so the DinD baseline (a snapshot of the inner-docker data root capturing IMAGES and NAMED " +
		"VOLUMES, but NOT a container's writable layer) makes the NEXT boot as fast as possible.\n\n" +
		"GOAL of the guidance: a reused boot should only START containers + boot the app — not reinstall deps, " +
		"re-migrate the DB, or re-run build/asset/warmup work. Every slow reusable step must land in a NAMED VOLUME " +
		"or a committed image so the snapshot captures it.\n\n" +
		"EVIDENCE — what we actually observed booting THIS repo (timings, reuse vs rebuild, failures):\n" +
		evidence + "\n\n"
	if strings.TrimSpace(extra) != "" {
		research += "Additional operator notes to incorporate: " + strings.TrimSpace(extra) + "\n\n"
	}
	if checkout != "" {
		research += "Read this checkout: find the datastore(s), dependency manager (Gemfile/package.json/…), any " +
			"docker-compose / Dockerfile / Procfile, the language + version, and the boot command. Briefly summarize " +
			"the concrete stack. Do NOT write the final guidance yet."
	} else {
		research += "From the evidence, summarize the stack and the slow steps. Do NOT write the final guidance yet."
	}
	sessionID, canceled := d.runChatTurn(ctx, claudeBin, checkout, chatDefaultTools, research, "", send)
	if canceled {
		return
	}

	// Turn 2 — emit ONLY the finished guidance text.
	_ = send(chatServerMsg{Type: "text", Text: "\nWriting the guidance…\n"})
	format := "Now output ONLY the finished guidance — plain text, no preamble, no code fence around the whole " +
		"thing, no explanation. It is appended verbatim to a worker's prompt. Make it CONCRETE and specific to THIS " +
		"app: exact deterministic named-volume names (e.g. <app>-deps, <app>-db), the exact commands to run the " +
		"datastore on a persistent volume + migrate once, whether/how to install deps into a volume, whether to " +
		"`docker commit` a prepared image and what to name it, which per-boot build caches to persist, and any " +
		"pitfalls we hit (proxy/telemetry hangs, image corruption, OOM). Keep it tight and actionable — a worker " +
		"will follow it literally to make reuse fast."
	var buf strings.Builder
	collect := func(m chatServerMsg) error {
		if m.Type == "text" {
			buf.WriteString(m.Text)
		}
		if m.Type == "error" {
			return send(m)
		}
		return nil
	}
	if _, canceled = d.runChatTurn(ctx, claudeBin, checkout, chatDefaultTools, format, sessionID, collect); canceled {
		return
	}
	_ = send(chatServerMsg{Type: "draft", Result: strings.TrimSpace(buf.String())})
}

// bootEvidence summarizes a repo's captured boot conversations into a compact
// evidence block for the drafting prompt. Returns a friendly note when nothing's
// captured yet (drafting still works from the checkout + general principles).
func (d *dashboardServer) bootEvidence(repoID string) string {
	cs, err := d.getConvStore()
	if err != nil {
		return "(no captured conversations available)"
	}
	page, err := cs.List(convstore.ListQuery{Repo: repoID, Limit: 6})
	if err != nil || len(page.Conversations) == 0 {
		return "(no prior boot conversations captured for this repo yet — draft from the codebase + general principles)"
	}
	var b strings.Builder
	for _, c := range page.Conversations {
		msgs, _ := cs.Messages(c.ID, "")
		b.WriteString(fmt.Sprintf("\n### %s conversation #%d (%s, %d msgs)\n", c.OriginKind, c.ID, c.Status, c.MessageCount))
		b.WriteString(bootFindings(msgs))
	}
	out := b.String()
	if len(out) > 12000 { // keep the most recent evidence within a sane budget
		out = out[len(out)-12000:]
	}
	return out
}

// bootFindings extracts outcome-bearing lines (assistant text + tool results
// mentioning timing / reuse / errors) from a conversation, capped.
func bootFindings(msgs []convstore.MessageRow) string {
	var lines []string
	for _, m := range msgs {
		t := strings.TrimSpace(m.Text)
		if t == "" {
			t = strings.TrimSpace(m.ToolResult)
		}
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if strings.Contains(low, "step ") || strings.Contains(low, "reuse") || strings.Contains(low, "cache") ||
			strings.Contains(low, "bundle") || strings.Contains(low, "migrat") || strings.Contains(low, "corrupt") ||
			strings.Contains(low, "hang") || strings.Contains(low, "oom") || strings.Contains(low, "timing") ||
			strings.Contains(low, "launch:") || strings.Contains(low, "volume") || strings.Contains(low, "docker") ||
			strings.Contains(low, "seconds") || strings.Contains(low, "min") {
			if len(t) > 400 {
				t = t[:400] + "…"
			}
			lines = append(lines, "- "+t)
		}
	}
	s := strings.Join(lines, "\n")
	if len(s) > 3500 {
		s = s[len(s)-3500:]
	}
	if s == "" {
		return "(no outcome/timing lines found)\n"
	}
	return s + "\n"
}
