package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// CmdPR implements `corral pr` — inspect and manage a PR's LOCAL Corral links
// (relationships between PRs stored in Corral's DB, shown on the PR page; nothing
// is pushed to GitHub). It drives the dashboard's /api/prs/<id>/links endpoints,
// so a dashboard must be running.
//
//	corral pr links   <prId>                     list a PR's links
//	corral pr suggest <prId>                     suggest PRs to link (by file overlap)
//	corral pr link    <prId> <linkedPrId> [--rel related] [--note "..."]
//	corral pr unlink  <prId> <linkId>            remove a link
//
// PR ids are Corral's internal DB ids (NOT GitHub PR numbers) — the same ids the
// links/suggest output prints. Relationships: tests | tested_by | related |
// depends_on (default: related).
func CmdPR(args []string) error {
	if len(args) == 0 {
		return prUsage()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "links":
		return prLinksList(rest)
	case "suggest":
		return prLinksSuggest(rest)
	case "stack":
		return prStack(rest)
	case "link":
		return prLinkAdd(rest)
	case "unlink":
		return prLinkRemove(rest)
	default:
		return prUsage()
	}
}

func prUsage() error {
	fmt.Fprint(os.Stderr, `usage: corral pr <command>

  links   <prId>                          list a PR's local links
  suggest <prId>                          suggest PRs to link (by changed-file overlap)
  stack   <prId>                          detect stacked PRs (git ancestry, from the mirror)
  link    <prId> <linkedPrId> [flags]     link two PRs
  unlink  <prId> <linkId>                 remove a link

link flags:
  --rel  <relationship>   tests | tested_by | related | depends_on  (default: related)
  --note <text>           free-text note on the link

PR ids are Corral's internal ids (from 'corral pr links'/'suggest' or the PR page),
not GitHub numbers. Links are LOCAL to Corral — nothing is pushed to GitHub.
`)
	return fmt.Errorf("missing or unknown pr command")
}

// prGetJSON does a GET and prints the body; prMutate does a POST/DELETE.
func prPrintResult(status int, body []byte) error {
	if status < 200 || status >= 300 {
		fmt.Fprint(os.Stderr, string(body))
		if len(body) > 0 && body[len(body)-1] != '\n' {
			fmt.Fprintln(os.Stderr)
		}
		return fmt.Errorf("HTTP %d", status)
	}
	os.Stdout.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		fmt.Fprintln(os.Stdout)
	}
	return nil
}

func parsePRID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid pr id %q (expected a Corral internal id)", s)
	}
	return id, nil
}

func prLinksList(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: corral pr links <prId>")
	}
	id, err := parsePRID(args[0])
	if err != nil {
		return err
	}
	status, body, err := dashboardRequest("GET", fmt.Sprintf("/api/prs/%d/links", id), "")
	if err != nil {
		return err
	}
	return prPrintResult(status, body)
}

func prLinksSuggest(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: corral pr suggest <prId>")
	}
	id, err := parsePRID(args[0])
	if err != nil {
		return err
	}
	status, body, err := dashboardRequest("GET", fmt.Sprintf("/api/prs/%d/links/suggest", id), "")
	if err != nil {
		return err
	}
	return prPrintResult(status, body)
}

func prStack(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: corral pr stack <prId>")
	}
	id, err := parsePRID(args[0])
	if err != nil {
		return err
	}
	status, body, err := dashboardRequest("GET", fmt.Sprintf("/api/prs/%d/stack", id), "")
	if err != nil {
		return err
	}
	return prPrintResult(status, body)
}

func prLinkAdd(args []string) error {
	// Positional: <prId> <linkedPrId>; then optional --rel/--note (any order).
	var pos []string
	rel, note := "related", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rel":
			if i+1 < len(args) {
				rel = args[i+1]
				i++
			}
		case "--note":
			if i+1 < len(args) {
				note = args[i+1]
				i++
			}
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: corral pr link <prId> <linkedPrId> [--rel <rel>] [--note <text>]")
	}
	prID, err := parsePRID(pos[0])
	if err != nil {
		return err
	}
	linkedID, err := parsePRID(pos[1])
	if err != nil {
		return fmt.Errorf("invalid linked pr id: %w", err)
	}
	payload, _ := json.Marshal(map[string]any{"linkedPrId": linkedID, "relationship": rel, "note": note})
	status, body, err := dashboardRequest("POST", fmt.Sprintf("/api/prs/%d/links", prID), string(payload))
	if err != nil {
		return err
	}
	return prPrintResult(status, body)
}

func prLinkRemove(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: corral pr unlink <prId> <linkId>")
	}
	prID, err := parsePRID(args[0])
	if err != nil {
		return err
	}
	linkID, err := strconv.ParseInt(strings.TrimSpace(args[1]), 10, 64)
	if err != nil || linkID <= 0 {
		return fmt.Errorf("invalid link id %q", args[1])
	}
	status, body, err := dashboardRequest("DELETE", fmt.Sprintf("/api/prs/%d/links/%d", prID, linkID), "")
	if err != nil {
		return err
	}
	return prPrintResult(status, body)
}
