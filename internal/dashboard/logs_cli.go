package dashboard

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/scoutapp/corral/internal/applog"
	"github.com/scoutapp/corral/internal/store"
)

// CmdLogs implements `corral logs` — dump/export the application log from the
// shared store to stdout, so it's greppable and portable even though the log
// lives in SQLite (not a file). Reads corral.db directly (no running dashboard
// needed).
//
//	corral logs                          most recent 200 lines
//	corral logs --limit 1000
//	corral logs --category ai            filter by category
//	corral logs --level error
//	corral logs --grep "PR #42"          free-text over message + meta
//	corral logs --json                   NDJSON (one JSON object per line)
//	corral logs > corral.log             redirect to a file for grep/archival
func CmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	limit := fs.Int("limit", 200, "max lines to print")
	category := fs.String("category", "", "filter by category (ai, pr-action, project, automation, script, http, …)")
	level := fs.String("level", "", "filter by level (info|warn|error|debug)")
	project := fs.String("project", "", "filter by project id")
	grep := fs.String("grep", "", "free-text search over message + meta")
	asJSON := fs.Bool("json", false, "output NDJSON (one JSON object per line)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := store.Open()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()
	lg := applog.New(s, true) // include debug when reading

	page, err := lg.Query(applog.Query{
		Limit:    *limit,
		Category: *category,
		Level:    *level,
		Project:  *project,
		Q:        *grep,
	})
	if err != nil {
		return err
	}

	// Query returns newest-first; print oldest-first so a redirected file reads
	// chronologically like a normal log.
	logs := page.Logs
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}

	w := os.Stdout
	enc := json.NewEncoder(w)
	for _, r := range logs {
		if *asJSON {
			_ = enc.Encode(r)
			continue
		}
		// Human line: <ts> <LEVEL> [category] message  (status, dur)
		var extra string
		if r.Status != "" || r.DurationMs > 0 {
			parts := []string{}
			if r.Status != "" {
				parts = append(parts, r.Status)
			}
			if r.DurationMs > 0 {
				parts = append(parts, fmt.Sprintf("%dms", r.DurationMs))
			}
			extra = "  (" + strings.Join(parts, ", ") + ")"
		}
		fmt.Fprintf(w, "%s %-5s [%s] %s%s\n", r.TS, strings.ToUpper(r.Level), r.Category, r.Message, extra)
	}
	return nil
}
