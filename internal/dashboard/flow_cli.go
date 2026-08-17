package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// CmdFlow implements `corral flow` — list and run flows from the terminal,
// talking to the running dashboard over its loopback API (the same target the
// `corral api` command discovers). This is the CLI door onto the flow engine; the
// host chat uses the same /api/flows surface.
//
//	corral flow list                 names + step counts + schedule
//	corral flow run <name>           run a flow by name, print the result
//	corral flow run <name> --json    machine-readable result
//
// Auth + base URL come from ~/.corral/dashboard.json (or --url/--token, or
// CORRAL_DASH_URL/TOKEN), exactly like `corral api`.
func CmdFlow(args []string) error {
	if len(args) == 0 {
		return flowUsage()
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return cmdFlowList(rest)
	case "run":
		return cmdFlowRun(rest)
	case "help", "-h", "--help":
		return flowUsage()
	default:
		return fmt.Errorf("unknown flow subcommand %q (try: list, run)", sub)
	}
}

func flowUsage() error {
	fmt.Fprint(os.Stderr, `usage: corral flow <command>

  list                 list flows (name, steps, schedule)
  run <name> [--json]  run a flow by name

Talks to the running dashboard (start it with `+"`corral dashboard`"+`).
`)
	return nil
}

// flowSummary is the subset of a flow we need for list/run.
type flowSummary struct {
	ID    int64 `json:"id"`
	Name  string `json:"name"`
	Steps []struct {
		StepKey string `json:"stepKey"`
	} `json:"steps"`
}

func cmdFlowList(args []string) error {
	asJSON := hasFlag(args, "--json")
	body, err := dashboardGet("/api/flows")
	if err != nil {
		return err
	}
	if asJSON {
		os.Stdout.Write(body)
		return nil
	}
	var out struct {
		Flows []flowSummary `json:"flows"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("parse flows: %w", err)
	}
	if len(out.Flows) == 0 {
		fmt.Println("No flows yet. Create one in the dashboard's Automations → Flows.")
		return nil
	}
	for _, f := range out.Flows {
		fmt.Printf("%-30s %d step%s\n", f.Name, len(f.Steps), plural(len(f.Steps)))
	}
	return nil
}

func cmdFlowRun(args []string) error {
	asJSON := hasFlag(args, "--json")
	var name string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			name = a
			break
		}
	}
	if name == "" {
		return fmt.Errorf("usage: corral flow run <name>")
	}

	id, err := resolveFlowID(name)
	if err != nil {
		return err
	}

	respBody, err := dashboardPost(fmt.Sprintf("/api/flows/%d:run", id), []byte("{}"))
	if err != nil {
		return err
	}
	if asJSON {
		os.Stdout.Write(respBody)
		if len(respBody) > 0 && respBody[len(respBody)-1] != '\n' {
			fmt.Println()
		}
		return nil
	}
	var res struct {
		Status string `json:"status"`
		Steps  []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Output string `json:"output"`
			Err    string `json:"error"`
		} `json:"steps"`
		RunID int64 `json:"runId"`
	}
	if err := json.Unmarshal(respBody, &res); err != nil {
		return fmt.Errorf("parse result: %w", err)
	}
	fmt.Printf("flow %q — %s (run #%d)\n", name, res.Status, res.RunID)
	for i, s := range res.Steps {
		mark := "ok"
		if s.Status != "ok" {
			mark = s.Status
		}
		fmt.Printf("  %d. %-20s %s\n", i+1, s.Name, mark)
		if s.Err != "" {
			fmt.Printf("     error: %s\n", s.Err)
		}
	}
	if res.Status != "ok" {
		return fmt.Errorf("flow finished with status %q", res.Status)
	}
	return nil
}

// resolveFlowID looks up a flow's id by name (case-insensitive exact match). A
// flow can be run by name from the CLI/chat without the user tracking numeric ids.
func resolveFlowID(name string) (int64, error) {
	body, err := dashboardGet("/api/flows")
	if err != nil {
		return 0, err
	}
	var out struct {
		Flows []flowSummary `json:"flows"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("parse flows: %w", err)
	}
	var matches []flowSummary
	for _, f := range out.Flows {
		if strings.EqualFold(f.Name, name) {
			matches = append(matches, f)
		}
	}
	switch len(matches) {
	case 0:
		return 0, fmt.Errorf("no flow named %q (try `corral flow list`)", name)
	case 1:
		return matches[0].ID, nil
	default:
		return 0, fmt.Errorf("%d flows named %q — names must be unique to run by name", len(matches), name)
	}
}

// --- shared dashboard request helpers (auth via resolveDashboardTarget) -----

func dashboardGet(path string) ([]byte, error) { return dashboardDo(http.MethodGet, path, nil) }
func dashboardPost(path string, body []byte) ([]byte, error) {
	return dashboardDo(http.MethodPost, path, body)
}

func dashboardDo(method, path string, body []byte) ([]byte, error) {
	base, token, err := resolveDashboardTarget("", "")
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: token})
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d %s %s: %s", resp.StatusCode, method, path, strings.TrimSpace(string(rb)))
	}
	return rb, nil
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
