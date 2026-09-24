package dashboard

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/convstore"
	"github.com/scoutapp/corral/internal/session"
)

// Sandbox conversation capture (host-pull). The sandbox's OWN interactive Claude
// (the Container tab) isn't on the host runChatTurn seam, so we can't tee it.
// Instead we PULL: the container's Claude Code writes its native session JSONL
// into the host's ~/.claude/projects (that dir is bind-mounted :rw at the same
// path, see internal/container/docker.go), so the host can simply read those
// files. This preserves one-directional trust completely — no inbound endpoint,
// no firewall hole, no token; the sandbox only ever writes files in its own
// mounted dirs, which it can already do.
//
// A background ticker tails each running project's session files (read since a
// tracked byte offset) and mirrors new records into the conversations DB under
// origin_kind="sandbox". ~2s lag is fine for a log/audit view.

// claudeProjectSlug mirrors Claude Code's session-dir naming: the absolute cwd
// with every non-alphanumeric character replaced by '-' (so /Users/x/.corral →
// -Users-x--corral). Session files live at ~/.claude/projects/<slug>/<sid>.jsonl.
var slugNonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

func claudeProjectSlug(workspace string) string {
	return slugNonAlnum.ReplaceAllString(workspace, "-")
}

// claudeProjectsDir is the host location Claude Code persists session transcripts
// to (shared with the container via the :rw bind mount).
func claudeProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// startSandboxConvTail launches the host-pull tailer: every ~2s it mirrors any
// new session-transcript lines into the conversations DB — from running sandboxes
// (the Container tab's Claude) AND from the host's own interactive `claude`
// terminals (the host-Claude terminal that replaces the bespoke ChatDock). Both
// write the same ~/.claude/projects/<slug>/<sid>.jsonl transcripts, so one tailer
// covers both; they differ only in which workspaces to sweep and how the resulting
// conversation is stamped (origin kind + conv-key prefix + parent resolver).
// Best-effort throughout — an unopenable convstore (e.g. a tag-less dev build) or
// a missing session dir just means nothing is captured.
func (d *dashboardServer) startSandboxConvTail() {
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		st := &sandboxTailState{offsets: map[string]int64{}, convIDs: map[string]int64{}}
		for {
			d.tailConversationsOnce(st)
			<-t.C
		}
	}()
}

// sandboxTailState is the tailer's in-memory bookkeeping: byte offset per
// transcript file, and the conversation id per (origin-scoped) Claude session id.
type sandboxTailState struct {
	offsets map[string]int64 // file path → bytes already consumed
	convIDs map[string]int64 // "<origin>:<claude session id>" → convstore conversation id
}

// convOriginProfile captures how a swept transcript's conversation is stamped —
// the one axis on which the sandbox pull and the host-terminal pull differ. The
// file-tail and line-ingest machinery is otherwise identical for both.
type convOriginProfile struct {
	kind      string                    // convstore OriginKind (e.g. "sandbox", "host-terminal")
	keyPrefix string                    // conv-key namespace so the same session id can't collide across origins
	parent    func(workspace string) int64 // resolves the spawning conversation id, or 0
}

// tailConversationsOnce does one sweep over both sandbox and host-Claude
// transcripts.
func (d *dashboardServer) tailConversationsOnce(st *sandboxTailState) {
	cs, err := d.getConvStore()
	if err != nil {
		return
	}
	dir := claudeProjectsDir()
	if dir == "" {
		return
	}
	reg, err := readRegistry()
	if err != nil {
		return
	}

	sandboxProfile := convOriginProfile{
		kind:      "sandbox",
		keyPrefix: "sandbox",
		parent:    d.sandboxParentConv,
	}
	// The host interactive `claude` runs with a DEDICATED CLAUDE_CONFIG_DIR
	// (hostClaudeProjectsDir), so its transcripts live in their own tree — cleanly
	// separated from the sandbox's ~/.claude/projects, no on-disk ambiguity.
	hostProfile := convOriginProfile{
		kind:      "host-terminal",
		keyPrefix: "host",
		parent:    d.sandboxParentConv, // same project-level parent linkage
	}

	for _, p := range reg.Projects {
		ws := p.Workspace
		// Sandbox transcripts: shared ~/.claude/projects, only while the container is up.
		if session.DockerContainerRunning(session.ContainerNameForWorkspace(ws)) {
			d.tailProjectDir(cs, st, sandboxProfile, ws, filepath.Join(dir, claudeProjectSlug(ws)))
		}
		// Host-terminal transcripts: corral-owned tree, only while the -claude tmux
		// session is live. Same <slug> subdir naming (cwd is the workspace either way).
		if hostDir := hostClaudeProjectsDir(); hostDir != "" && hostSessionLive(claudeShellSession(ws)) {
			d.tailProjectDir(cs, st, hostProfile, ws, filepath.Join(hostDir, claudeProjectSlug(ws)))
		}
	}
}

// tailProjectDir ingests every .jsonl transcript in one <slug> session directory
// under the given origin profile.
func (d *dashboardServer) tailProjectDir(cs *convstore.ConvStore, st *sandboxTailState, profile convOriginProfile, workspace, sessDir string) {
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		d.tailTranscriptFile(cs, st, profile, workspace, filepath.Join(sessDir, e.Name()))
	}
}

// tailTranscriptFile reads new lines from one session transcript and appends them
// to the conversations DB under the given origin profile, tracking the byte offset
// so each line is consumed once.
func (d *dashboardServer) tailTranscriptFile(cs *convstore.ConvStore, st *sandboxTailState, profile convOriginProfile, workspace, path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	off := st.offsets[path]
	if fi.Size() <= off {
		return // no growth
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(off, 0); err != nil {
		return
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var consumed int64
	for sc.Scan() {
		line := sc.Bytes()
		consumed += int64(len(line)) + 1 // + newline
		d.ingestTranscriptLine(cs, st, profile, workspace, line)
	}
	// Advance the offset only by what we actually scanned (a final partial line,
	// mid-write, is left for the next sweep by not counting it — Scanner drops an
	// unterminated last token, so consumed reflects only complete lines).
	st.offsets[path] = off + consumed
}

// claudeSessionRecord is the subset of Claude Code's session JSONL we read.
type claudeSessionRecord struct {
	Type      string `json:"type"`      // user | assistant | system | …
	SessionID string `json:"sessionId"` // conversation thread id
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"` // string OR array of blocks
	} `json:"message"`
}

type claudeContentBlock struct {
	Type      string          `json:"type"` // text | tool_use | tool_result
	Text      string          `json:"text"`
	Name      string          `json:"name"`    // tool_use
	Input     json.RawMessage `json:"input"`   // tool_use
	Content   json.RawMessage `json:"content"` // tool_result (string or array)
	ToolUseID string          `json:"tool_use_id"`
}

// ingestTranscriptLine parses one Claude Code session record and appends its
// message(s) to the conversation for its session id (creating the conversation
// on first sight, stamped per the origin profile). Non-conversational record
// types (mode, snapshots, …) are skipped. Best-effort.
func (d *dashboardServer) ingestTranscriptLine(cs *convstore.ConvStore, st *sandboxTailState, profile convOriginProfile, workspace string, line []byte) {
	var rec claudeSessionRecord
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	if rec.SessionID == "" || (rec.Type != "user" && rec.Type != "assistant") {
		return
	}

	// Key the conv map by origin+session so the same session id under two origins
	// (it won't happen now that dirs are separate, but keep it unambiguous) maps to
	// distinct conversations.
	convKey := profile.keyPrefix + ":" + rec.SessionID
	convID := st.convIDs[convKey]
	if convID == 0 {
		id, err := cs.StartConversation(convstore.ConvMeta{
			ConvKey:              convKey,
			ClaudeSessionID:      rec.SessionID,
			OriginKind:           profile.kind,
			OriginID:             rec.SessionID,
			ProjectID:            ProjectID(workspace),
			ProjectLabel:         filepath.Base(workspace),
			ParentConversationID: profile.parent(workspace),
		})
		if err != nil {
			return
		}
		convID = id
		st.convIDs[convKey] = id
		// Resume across restarts: if this session already has messages from a prior
		// run, skip re-ingesting by seeding the offset is handled at the file level;
		// here we simply continue appending (dup lines are avoided by the byte
		// offset, which starts at 0 only for genuinely new files this process sees).
	}

	role := rec.Message.Role
	if role == "" {
		role = rec.Type
	}

	// Content is either a plain string (a simple user turn) or an array of blocks.
	var blocks []claudeContentBlock
	if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil {
		var s string
		if json.Unmarshal(rec.Message.Content, &s) == nil && strings.TrimSpace(s) != "" {
			_ = cs.AppendMessage(convID, convstore.Message{Role: role, Type: "text", Text: s})
		}
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				_ = cs.AppendMessage(convID, convstore.Message{Role: role, Type: "text", Text: b.Text})
			}
		case "tool_use":
			_ = cs.AppendMessage(convID, convstore.Message{
				Role: role, Type: "tool_use", ToolName: b.Name, ToolInput: string(b.Input),
			})
		case "tool_result":
			_ = cs.AppendMessage(convID, convstore.Message{
				Role: role, Type: "tool_result", ToolResult: toolResultText(b.Content),
			})
		}
	}
}

// (tool_result content is flattened via toolResultText, shared with chat.go.)

// sandboxParentConv returns the conversation that spawned this project (for the
// causal chain), if the project was created from a captured conversation. Looked
// up from the project's recorded parent (set at create time, PR7 linkage). 0 when
// unknown.
func (d *dashboardServer) sandboxParentConv(workspace string) int64 {
	// The project config may carry the spawning conversation id (written by the
	// create path when X-Corral-Parent-Conversation was present). Best-effort read.
	return readProjectParentConv(workspace)
}

// readProjectParentConv reads a persisted parent-conversation id from the
// project's .corral dir, if present. Returns 0 when absent/unreadable.
func readProjectParentConv(workspace string) int64 {
	path := filepath.Join(config.ProjectDirFor(workspace), "parent-conversation")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n
}
