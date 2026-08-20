package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// CmdWorker implements `corral worker` — drive conductor worker jobs. The key
// subcommand is `wake`, which a DETACHED worker uses to resume ITSELF: a worker
// that kicked off slow background work ends its turn with a wake request instead
// of stranding itself (a detached headless turn has no other way to continue).
//
//	corral worker list                          list worker/merge jobs
//	corral worker wake <jobId> [--in <secs>] [--prompt "..."]
//
// wake enqueues a continuation turn now (or after --in seconds). On the next
// turn the worker resumes with full prior context (--resume). Use it like:
// "kick off the transfer in the background, then `corral worker wake <id> --in 30`
// and end the turn; you'll be re-invoked to check on it."
func CmdWorker(args []string) error {
	if len(args) == 0 {
		return workerUsage()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return dindGet("/merge-jobs") // reuse the status-aware GET printer
	case "wake":
		return workerWake(rest)
	default:
		return workerUsage()
	}
}

func workerUsage() error {
	fmt.Fprint(os.Stderr, `usage: corral worker <command>

  list                                     list worker/merge jobs
  wake <jobId> [--in <secs>] [--prompt "..."]
                                           resume a detached worker (self-wake)

wake: enqueue a continuation turn for the job now, or after --in seconds (capped
at 30m). The worker resumes with full prior context. A worker waiting on slow
background work should schedule a wake and END its turn — corral re-invokes it.
`)
	return fmt.Errorf("missing or unknown worker command")
}

func workerWake(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: corral worker wake <jobId> [--in <secs>] [--prompt \"...\"]")
	}
	jobID := args[0]
	var inSeconds int64
	var prompt string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--in":
			if i+1 < len(args) {
				inSeconds, _ = strconv.ParseInt(args[i+1], 10, 64)
				i++
			}
		case "--prompt":
			if i+1 < len(args) {
				prompt = args[i+1]
				i++
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{"prompt": prompt, "inSeconds": inSeconds})
	status, body, err := dashboardRequest("POST", "/merge-jobs/"+jobID+"/wake", string(payload))
	if err != nil {
		return err
	}
	return prPrintResult(status, body)
}
