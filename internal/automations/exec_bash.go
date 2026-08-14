package automations

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
)

// The bash action is the escape hatch: a raw script the user attaches to an
// event, for anything the typed catalog doesn't cover (a Slack CLI, a curl to an
// internal service, a custom notifier). It runs through the same runner as every
// other action, so it's recorded in run history like the rest.
//
// IMPORTANT — this runs on the HOST, NOT in the sandbox. The corral dashboard is
// a host process (see `corral dashboard` — the host-wide dashboard), so a bash
// action executes `bash -c <script>` directly on the operator's machine with the
// dashboard's environment (os.Environ()): the operator's PATH and any CLIs
// already authenticated in that session (gh, aws, kubectl, …) with their real
// credentials. There is NO sandbox, no allowlist proxy, no container FS boundary
// around it — same trust basis as the host chat panel. The UI surfaces a
// prominent "runs on the host" warning; treat scripts as trusted host code.
//
// Context: every run-context var is exported as CORRAL_<UPPER_SNAKE> (e.g.
// pr_number → CORRAL_PR_NUMBER), plus CORRAL_EVENT and CORRAL_REPO_ID, so a
// script reads them as ordinary env vars.

// BashSpec configures a bash action. Script is the shell body; it is NOT
// {{var}}-substituted (vars arrive as env, which is safer than string-splicing
// user input into a command line). WorkDir optionally sets the cwd.
type BashSpec struct {
	Script  string `json:"script"`
	WorkDir string `json:"workDir,omitempty"`
}

// BashExecutor runs a bash action's script with the context exported as env.
type BashExecutor struct{}

func (BashExecutor) Execute(ctx context.Context, a Action, rc RunContext) StepResult {
	var spec BashSpec
	if err := json.Unmarshal([]byte(a.Spec), &spec); err != nil {
		return StepResult{Status: StatusError, Err: "bad bash spec: " + err.Error()}
	}
	if strings.TrimSpace(spec.Script) == "" {
		return StepResult{Status: StatusError, Err: "script is required"}
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", spec.Script)
	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	cmd.Env = append(os.Environ(), contextEnv(rc)...)

	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		// A non-zero exit (or spawn failure): report as error with the output as
		// the reason, but still return the captured output so the log is useful.
		msg := err.Error()
		if output != "" {
			msg = output
		}
		return StepResult{Status: StatusError, Output: output, Err: msg}
	}
	return StepResult{Status: StatusOK, Output: output}
}

// contextEnv converts run-context vars to CORRAL_<UPPER_SNAKE> env entries,
// plus CORRAL_EVENT and CORRAL_REPO_ID. Keys are upper-cased; any char that
// isn't a letter/digit becomes '_', matching shell env-var conventions.
func contextEnv(rc RunContext) []string {
	env := []string{
		"CORRAL_EVENT=" + rc.Event,
		"CORRAL_REPO_ID=" + rc.RepoID,
	}
	for k, v := range rc.Vars {
		env = append(env, "CORRAL_"+envKey(k)+"="+v)
	}
	return env
}

func envKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32) // to upper
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
