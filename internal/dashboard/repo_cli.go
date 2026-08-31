package dashboard

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/scoutapp/corral/internal/automations"
	"github.com/scoutapp/corral/internal/store"
)

// CmdRepo implements `corral repo <subcommand>` — repo-scoped operations that
// write straight to corral.db (no running dashboard needed). Today it's the
// agent-context read/write used by the AGENTS.md generator worker, which needs an
// unattended write path (the HTTP API gates writes behind a separate token).
//
//	corral repo set-agent-context <id> --stdin   read the CLAUDE.md body from stdin
//	corral repo set-agent-context <id> ""        clear the context
//	corral repo get-agent-context <id>           print the current context
func CmdRepo(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: corral repo <set-agent-context|get-agent-context> <repo-id> [...]")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "set-agent-context":
		return cmdRepoSetAgentContext(rest)
	case "get-agent-context":
		return cmdRepoGetAgentContext(rest)
	default:
		return fmt.Errorf("unknown `repo` subcommand %q (want set-agent-context|get-agent-context)", sub)
	}
}

func cmdRepoSetAgentContext(args []string) error {
	// The repo id is a positional that may appear before the flags
	// (`set-agent-context <id> --stdin`). Go's flag package stops at the first
	// non-flag arg, so pull the first positional out before parsing flags.
	repoID := ""
	var flagArgs []string
	for _, a := range args {
		if repoID == "" && !strings.HasPrefix(a, "-") {
			repoID = a
			continue
		}
		flagArgs = append(flagArgs, a)
	}
	if repoID == "" {
		return fmt.Errorf("usage: corral repo set-agent-context <repo-id> (--stdin | \"<content>\")")
	}

	fs := flag.NewFlagSet("repo set-agent-context", flag.ContinueOnError)
	fromStdin := fs.Bool("stdin", false, "read the context body from stdin")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	rest := fs.Args()

	var content string
	if *fromStdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		content = string(b)
	} else if len(rest) > 0 {
		// A quoted positional body, e.g. `set-agent-context <id> ""` to clear.
		content = strings.Join(rest, " ")
	} else {
		return fmt.Errorf("no content: pass --stdin or a quoted \"<content>\" argument")
	}

	s, err := store.Open()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	if err := automations.New(s).SetRepoAgentContext(repoID, content); err != nil {
		return err
	}
	if strings.TrimSpace(content) == "" {
		fmt.Printf("Cleared agent context for repo %s.\n", repoID)
	} else {
		fmt.Printf("Saved agent context for repo %s (%d bytes).\n", repoID, len(content))
	}
	return nil
}

func cmdRepoGetAgentContext(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: corral repo get-agent-context <repo-id>")
	}
	repoID := args[0]

	s, err := store.Open()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	content, err := automations.New(s).RepoAgentContext(repoID)
	if err != nil {
		return err
	}
	fmt.Print(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		fmt.Println()
	}
	return nil
}
