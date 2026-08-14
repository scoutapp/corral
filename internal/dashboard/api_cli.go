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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if dataLong != "" {
		data = dataLong
	}
	include = include || includeLong

	rest := fs.Args()
	if len(rest) < 2 {
		fs.Usage()
		return fmt.Errorf("need a METHOD and a path")
	}
	method := strings.ToUpper(rest[0])
	path := rest[1]
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	base, token, err := resolveDashboardTarget(urlFlag, tokenFlag)
	if err != nil {
		return err
	}

	// Body: literal string, or @file to read from disk (handy for large payloads).
	var body io.Reader
	if data != "" {
		if strings.HasPrefix(data, "@") {
			b, err := os.ReadFile(data[1:])
			if err != nil {
				return fmt.Errorf("read body file: %w", err)
			}
			body = strings.NewReader(string(b))
		} else {
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
			token = state.Token
		}
	}
	if token == "" {
		return "", "", fmt.Errorf("no dashboard token available (set --token or $CORRAL_DASH_TOKEN)")
	}
	return base, token, nil
}
