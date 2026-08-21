package dashboard

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// CmdAPI implements `corral api` — a thin, generic client for the dashboard's
// HTTP control plane. It's the one command the host Claude skill and a human on
// the terminal use to drive the whole documented API (see GET /api/openapi.json);
// there are no per-endpoint wrappers to maintain.
//
//	corral api GET  /api/flows
//	corral api GET  /api/logs?category=ai
//	corral api POST /api/flows/3:run -d '{"vars":{"repo":"acme/widget"}}'
//	corral api POST /p/<id>/start
//	corral api GET  /api/openapi.json         # discover the surface
//
// Auth + base URL are auto-discovered from ~/.corral/dashboard.json (the running
// dashboard's loopback port + token), so it just works when a dashboard is up.
// Override with --url/--token or CORRAL_DASH_URL / CORRAL_DASH_TOKEN for scripting
// or a non-default instance.
//
// Output is the response body verbatim (JSON) on stdout. A non-2xx response
// prints the body to stderr and exits non-zero, so it composes in scripts and
// the skill can detect failure (e.g. a 403 when API writes are disabled).
func CmdAPI(args []string) error {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: corral api <METHOD> <path> [flags]

  <METHOD>   GET | POST | PUT | DELETE | PATCH
  <path>     request path, e.g. /api/flows or /p/<id>/start (leading / optional)

flags:
  -d, --data <json>   request body (string); use @file to read from a file
  --url <base>        dashboard base URL (default: from ~/.corral/dashboard.json
                      or $CORRAL_DASH_URL)
  --token <tok>       dashboard token (default: from dashboard.json or
                      $CORRAL_DASH_TOKEN)
  -i, --include       print the status line + response headers too

examples:
  corral api GET  /api/openapi.json
  corral api GET  /api/flows
  corral api POST /api/flows/3:run -d '{"vars":{"repo":"acme/widget"}}'
  corral api POST /p/abc123/start
`)
	}
	var data, dataLong, urlFlag, tokenFlag string
	var include, includeLong bool
	fs.StringVar(&data, "d", "", "request body (string, or @file)")
	fs.StringVar(&dataLong, "data", "", "request body (string, or @file)")
	fs.StringVar(&urlFlag, "url", "", "dashboard base URL")
	fs.StringVar(&tokenFlag, "token", "", "dashboard token")
	fs.BoolVar(&include, "i", false, "include response status + headers")
	fs.BoolVar(&includeLong, "include", false, "include response status + headers")

	// The natural way to call this — `corral api POST /path -d '{...}'` — puts the
	// METHOD + path positionals BEFORE the flags. Go's flag package stops parsing
	// at the first non-flag arg, so those trailing flags would be silently dropped
	// (the classic symptom: `-d` ignored → empty body → 400). Pull the positionals
	// out first, then parse the remaining args as flags, so flag order doesn't
	// matter. We take the first two args that aren't a flag / a flag's value as the
	// METHOD and path.
	method, path, flagArgs, perr := splitAPIArgs(args)
	if perr != nil {
		fs.Usage()
		return perr
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if dataLong != "" {
		data = dataLong
	}
	include = include || includeLong

	method = strings.ToUpper(method)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	base, token, err := resolveDashboardTarget(urlFlag, tokenFlag)
	if err != nil {
		return err
	}

	// Body: literal string, @file to read from disk, or @- to read from stdin
	// (both handy for large payloads — @- avoids a temp file, matching curl).
	var body io.Reader
	if data != "" {
		switch {
		case data == "@-":
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read body from stdin: %w", err)
			}
			body = strings.NewReader(string(b))
		case strings.HasPrefix(data, "@"):
			b, err := os.ReadFile(data[1:])
			if err != nil {
				return fmt.Errorf("read body file: %w", err)
			}
			body = strings.NewReader(string(b))
		default:
			body = strings.NewReader(data)
		}
	}

	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	if data != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// Cross-origin conversation linkage: when this CLI is driven by a captured
	// Claude turn, the dashboard stamped the driving conversation id into our env.
	// Forward it so any work this request kicks off (a worker, a created project)
	// records parent_conversation_id = that conversation, letting the UI follow the
	// causal chain back up.
	if pc := os.Getenv("CORRAL_PARENT_CONVERSATION_ID"); pc != "" {
		req.Header.Set("X-Corral-Parent-Conversation", pc)
	}
	// The dashboard accepts the token via the corral_dash_token cookie (the
	// browser flow uses ?token= once then a cookie; a CLI just sends the cookie).
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: token})

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if include {
		fmt.Fprintf(os.Stderr, "%s %d\n", resp.Proto, resp.StatusCode)
		for k, v := range resp.Header {
			fmt.Fprintf(os.Stderr, "%s: %s\n", k, strings.Join(v, ", "))
		}
		fmt.Fprintln(os.Stderr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Error: body to stderr, non-zero exit, so scripts + the skill detect it.
		fmt.Fprint(os.Stderr, string(respBody))
		if len(respBody) > 0 && respBody[len(respBody)-1] != '\n' {
			fmt.Fprintln(os.Stderr)
		}
		return fmt.Errorf("HTTP %d %s %s", resp.StatusCode, method, path)
	}

	os.Stdout.Write(respBody)
	if len(respBody) > 0 && respBody[len(respBody)-1] != '\n' {
		fmt.Fprintln(os.Stdout)
	}
	return nil
}

// dashboardRequest issues one request to the running dashboard (auto-discovering
// its loopback URL + token), returning the status code and response body. Shared
// by the typed CLIs (e.g. `corral pr link`) that hit specific endpoints instead
// of the generic `corral api`. jsonBody is sent as-is with a JSON content type
// when non-empty. It forwards the parent-conversation header like CmdAPI does.
func dashboardRequest(method, path, jsonBody string) (int, []byte, error) {
	base, token, err := resolveDashboardTarget("", "")
	if err != nil {
		return 0, nil, err
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var body io.Reader
	if jsonBody != "" {
		body = strings.NewReader(jsonBody)
	}
	req, err := http.NewRequest(strings.ToUpper(method), strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return 0, nil, err
	}
	if jsonBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if pc := os.Getenv("CORRAL_PARENT_CONVERSATION_ID"); pc != "" {
		req.Header.Set("X-Corral-Parent-Conversation", pc)
	}
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: token})

	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// apiValueFlags are the `corral api` flags that consume the following argument as
// their value (in the space-separated `-d value` form). Used by splitAPIArgs to
// skip a flag's value when hunting for the two positionals. Boolean flags (-i,
// --include) are absent — they never consume a following arg.
var apiValueFlags = map[string]bool{
	"-d": true, "--data": true, "--url": true, "--token": true,
}

// splitAPIArgs pulls the METHOD and path positionals out of a `corral api` arg
// list, returning them plus the remaining (flag) args. This makes flag POSITION
// irrelevant: `POST /path -d '{...}'` and `-d '{...}' POST /path` both work, where
// Go's flag package alone would drop the trailing flags in the first form.
//
// It walks the args, skipping flags (a token starting with "-") and, for a
// value-flag in the `-d value` form, the value that follows it. The first two
// non-flag tokens are the METHOD and path; everything else is returned as flags.
func splitAPIArgs(args []string) (method, path string, flagArgs []string, err error) {
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flagArgs = append(flagArgs, a)
			// `--flag=value` carries its own value; `-d value` takes the next arg.
			if !strings.Contains(a, "=") && apiValueFlags[a] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		positionals = append(positionals, a)
	}
	if len(positionals) < 2 {
		return "", "", nil, fmt.Errorf("need a METHOD and a path")
	}
	// Any positionals beyond the first two are unexpected; keep the behavior strict
	// and predictable rather than silently ignoring them.
	if len(positionals) > 2 {
		return "", "", nil, fmt.Errorf("unexpected extra argument %q (usage: corral api <METHOD> <path> [flags])", positionals[2])
	}
	return positionals[0], positionals[1], flagArgs, nil
}

// resolveDashboardTarget determines the base URL + token, in precedence order:
// explicit flags → env (CORRAL_DASH_URL / CORRAL_DASH_TOKEN) → the running
// dashboard's ~/.corral/dashboard.json. Errors clearly when nothing is available
// so the user knows to start the dashboard.
func resolveDashboardTarget(urlFlag, tokenFlag string) (base, token string, err error) {
	base = urlFlag
	if base == "" {
		base = os.Getenv("CORRAL_DASH_URL")
	}
	token = tokenFlag
	if token == "" {
		token = os.Getenv("CORRAL_DASH_TOKEN")
	}

	// Fill any gap from the persisted dashboard state.
	if base == "" || token == "" {
		state, rerr := ReadDashboardState()
		if rerr != nil {
			return "", "", fmt.Errorf("read dashboard state: %w", rerr)
		}
		if state == nil {
			return "", "", fmt.Errorf("no running dashboard found (start it with `corral dashboard`), and no --url/--token given")
		}
		if base == "" {
			base = fmt.Sprintf("http://127.0.0.1:%d", state.Port)
		}
		if token == "" {
			// Prefer the dedicated API token: mutating calls made with it are
			// subject to the API-writes gate (the browser's own token isn't). Fall
			// back to the session token for a dashboard old enough not to have one.
			if state.APIToken != "" {
				token = state.APIToken
			} else {
				token = state.Token
			}
		}
	}
	if token == "" {
		return "", "", fmt.Errorf("no dashboard token available (set --token or $CORRAL_DASH_TOKEN)")
	}
	return base, token, nil
}
