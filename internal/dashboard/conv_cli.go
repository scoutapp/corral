package dashboard

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/scoutapp/corral/internal/convstore"
)

// CmdConversations implements `corral conversations` — inspect captured Claude
// conversations from the conversations DB directly (no running dashboard needed),
// so the host Claude / operator can grep the full history for debugging.
//
//	corral conversations                        recent conversations (one per line)
//	corral conversations --origin sandbox       filter by origin kind
//	corral conversations --project <id>         filter by project
//	corral conversations --grep "flaky test"    full-text search across all messages
//	corral conversations --limit 50 --json      NDJSON
//	corral conversations show <id>              print one conversation's messages
//	corral conversations show <id> --grep foo   search within one conversation
//	corral conversations chain <id>            the causal chain (forest) it belongs to
func CmdConversations(args []string) error {
	// Subcommands: show / chain take an id; otherwise it's a list.
	if len(args) >= 1 {
		switch args[0] {
		case "show":
			return convShow(args[1:])
		case "chain":
			return convChain(args[1:])
		}
	}
	return convList(args)
}

func openConvStore() (*convstore.ConvStore, error) {
	cs, err := convstore.Open()
	if err != nil {
		return nil, fmt.Errorf("open conversations db: %w", err)
	}
	return cs, nil
}

func convList(args []string) error {
	fs := flag.NewFlagSet("conversations", flag.ContinueOnError)
	limit := fs.Int("limit", 50, "max conversations to print")
	origin := fs.String("origin", "", "filter by origin (global-chat, project-chat, pr-review-chat, merge, worker, analysis, sandbox, …)")
	project := fs.String("project", "", "filter by project id")
	grep := fs.String("grep", "", "full-text search across all message text/tool calls")
	asJSON := fs.Bool("json", false, "output NDJSON (one JSON object per line)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cs, err := openConvStore()
	if err != nil {
		return err
	}
	defer cs.Close()

	page, err := cs.List(convstore.ListQuery{
		Limit: *limit, Origin: *origin, Project: *project, Q: *grep,
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	for _, c := range page.Conversations {
		if *asJSON {
			_ = enc.Encode(c)
			continue
		}
		title := c.Title
		if title == "" {
			title = c.FirstPrompt
		}
		fmt.Printf("#%d  %-14s  %-24s  %d msg  %s  %s\n",
			c.ID, c.OriginKind, truncate(title, 24), c.MessageCount, c.Status, c.CreatedAt)
	}
	return nil
}

func convShow(args []string) error {
	fs := flag.NewFlagSet("conversations show", flag.ContinueOnError)
	grep := fs.String("grep", "", "search within this conversation")
	asJSON := fs.Bool("json", false, "output NDJSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("usage: corral conversations show <id> [--grep <q>]")
	}
	id, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid conversation id %q", rest[0])
	}
	cs, err := openConvStore()
	if err != nil {
		return err
	}
	defer cs.Close()

	conv, err := cs.Get(id)
	if err != nil {
		return fmt.Errorf("conversation #%d not found", id)
	}
	if !*asJSON {
		fmt.Printf("Conversation #%d — %s (%s), %d messages\n\n", conv.ID, conv.OriginKind, conv.Status, conv.MessageCount)
	}
	msgs, err := cs.Messages(id, *grep)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	for _, m := range msgs {
		if *asJSON {
			_ = enc.Encode(m)
			continue
		}
		switch m.Type {
		case "tool_use":
			fmt.Printf("[%s] %s → %s %s\n", m.Role, m.Type, m.ToolName, truncate(m.ToolInput, 120))
		case "tool_result":
			fmt.Printf("[%s] %s: %s\n", m.Role, m.Type, truncate(m.ToolResult, 200))
		default:
			fmt.Printf("[%s] %s\n", m.Role, m.Text)
		}
	}
	return nil
}

func convChain(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: corral conversations chain <id>")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid conversation id %q", args[0])
	}
	cs, err := openConvStore()
	if err != nil {
		return err
	}
	defer cs.Close()

	chain, err := cs.Chain(id)
	if err != nil {
		return err
	}
	for _, c := range chain {
		marker := "  "
		if c.ID == id {
			marker = "→ " // the conversation you asked about
		}
		fmt.Printf("%s#%d  %-14s  %-24s  %d msg\n", marker, c.ID, c.OriginKind, truncate(c.Title, 24), c.MessageCount)
	}
	return nil
}
