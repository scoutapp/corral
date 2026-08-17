package dashboard

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/session"
)

// Live View (#6): watch a web app the sandbox is running, embedded in the
// dashboard. The dashboard REVERSE-PROXIES to the app under
//
//	/p/<id>/live/<port>/…
//
// rather than making the user publish a host port + hand-wire a socat bridge.
//
// Reachability is the crux. The dashboard runs on the HOST, but the app runs
// inside the container (often one layer deeper, inside DinD). The DinD bridge
// address 172.18.0.1 only exists inside the OUTER container's network namespace
// — it is NOT routable from the host — so we cannot point a stock reverse-proxy
// at it. Instead we TUNNEL through `docker exec` (the same host→container channel
// the Terminal tab uses): a per-connection `docker exec -i <container> bash`
// opens a raw TCP socket to the target with bash's /dev/tcp builtin and pipes it
// to its stdio. We wrap that stdio pair as a net.Conn and hand it to
// httputil.ReverseProxy via a custom DialContext.
//
// Trust model: this stays one-directional — the dashboard DIALS IN (host →
// container), exactly like `docker exec` for the Terminal tab. The sandbox never
// reaches the dashboard, and this path never touches the egress allowlist-proxy
// (it's not outbound traffic). The route lives under /p/<id>/ so it inherits the
// dashboard's session-cookie auth. Rendering the (untrusted) app in the browser
// is isolated separately — see the iframe sandboxing on the client + the headers
// stripped below.

// liveDialTargets is the ordered list of in-container addresses the tunnel tries
// for a given port: first the DinD bridge (where an inner container published
// with `-p` is reachable), then loopback (an app running directly in the outer
// container). The bash relay walks these until one connects.
func liveDialTargets(port int) []string {
	return []string{
		fmt.Sprintf("172.18.0.1:%d", port),
		fmt.Sprintf("127.0.0.1:%d", port),
	}
}

// execConn adapts a running `docker exec` process to net.Conn: writes go to the
// process stdin (→ the in-container socket), reads come from its stdout (← the
// socket). Close kills the exec so the tunnel tears down.
type execConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cancel context.CancelFunc
}

func (c *execConn) Read(b []byte) (int, error)  { return c.stdout.Read(b) }
func (c *execConn) Write(b []byte) (int, error) { return c.stdin.Write(b) }
func (c *execConn) Close() error {
	c.cancel()
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	// Reap the process so we don't leak a zombie; ignore its exit status (kill).
	_ = c.cmd.Wait()
	return nil
}

// The net.Conn address/deadline methods are stubs — httputil.ReverseProxy's HTTP
// transport doesn't rely on them for a tunneled connection, and there's no real
// socket on the host side to expose. Deadlines are enforced by the exec context
// + the transport's own timeouts.
func (c *execConn) LocalAddr() net.Addr                { return liveAddr{} }
func (c *execConn) RemoteAddr() net.Addr               { return liveAddr{} }
func (c *execConn) SetDeadline(t time.Time) error      { return nil }
func (c *execConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *execConn) SetWriteDeadline(t time.Time) error { return nil }

type liveAddr struct{}

func (liveAddr) Network() string { return "corral-live" }
func (liveAddr) String() string  { return "corral-live" }

// liveStderr logs the relay's stderr (rare — only on a relay-side error).
type liveStderr struct{}

func (liveStderr) Write(p []byte) (int, error) {
	if msg := strings.TrimSpace(string(p)); msg != "" {
		log.Printf("live view tunnel: %s", msg)
	}
	return len(p), nil
}

// liveRelayPy is a tiny Python relay run inside the container: it connects to
// the first reachable target, then pumps our stdin → socket and socket → our
// stdout. The crucial detail a naive `cat` pipeline gets wrong is the HALF-CLOSE:
// on stdin-EOF it shutdown(SHUT_WR)s the socket so the server sees end-of-request
// and sends its response, while we keep reading the socket until it closes. The
// targets are injected as argv so nothing from the request path is interpolated
// into the script. Python3 is always present in the sandbox image.
const liveRelayPy = `import socket,sys,os,threading
ts=[]
for a in sys.argv[1:]:
    h,p=a.rsplit(":",1); ts.append((h,int(p)))
s=None
for h,p in ts:
    try:
        s=socket.create_connection((h,p),timeout=3); break
    except OSError: s=None
if s is None: sys.exit(1)
s.settimeout(None)
infd=sys.stdin.fileno(); outfd=sys.stdout.fileno()
def up():
    while True:
        d=os.read(infd,65536)
        if not d:
            try: s.shutdown(socket.SHUT_WR)
            except OSError: pass
            return
        s.sendall(d)
threading.Thread(target=up,daemon=True).start()
while True:
    d=s.recv(65536)
    if not d: break
    os.write(outfd,d)`

// dialExecTunnel opens a tunnel to the given port inside container, returning a
// net.Conn backed by a `docker exec … python3` relay. The relay tries each
// candidate target in turn (DinD bridge, then loopback); the first that connects
// wins. If none connect the relay exits non-zero and the first Read returns EOF,
// which the transport surfaces as a failed round-trip (→ 502 to the browser).
func dialExecTunnel(ctx context.Context, container string, port int) (net.Conn, error) {
	args := []string{"exec", "-i", container, "python3", "-c", liveRelayPy}
	for _, tgt := range liveDialTargets(port) {
		args = append(args, tgt)
	}
	ectx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ectx, "docker", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	// Surface relay stderr (e.g. "no target") into the dashboard log for
	// debugging a failed tunnel; it never reaches the browser.
	cmd.Stderr = &liveStderr{}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	return &execConn{cmd: cmd, stdin: stdin, stdout: stdout, cancel: cancel}, nil
}

// handleLiveProxy reverse-proxies /p/<id>/live/<port>/<path…> to the app on
// <port> inside the project's container. sub is the path after "live/", e.g.
// "3000/api/thing".
func (d *dashboardServer) handleLiveProxy(w http.ResponseWriter, r *http.Request, id, sub string) {
	workspace, err := lookupWorkspaceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	container := session.ContainerNameForWorkspace(workspace)
	if !session.DockerContainerRunning(container) {
		http.Error(w, "project container is not running", http.StatusServiceUnavailable)
		return
	}

	portStr, rest, _ := strings.Cut(sub, "/")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "bad port", http.StatusBadRequest)
		return
	}

	liveProxyTo(w, r, container, id, port, rest)
}

// liveProxyTo builds and runs the reverse-proxy for one request. Split out from
// handleLiveProxy (which resolves id→workspace→container) so it can be tested
// against a container name directly.
func liveProxyTo(w http.ResponseWriter, r *http.Request, container, id string, port int, rest string) {
	// The path the app sees is everything after /live/<port> — rooted at "/".
	upstreamPath := "/" + rest

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			// Host is cosmetic for the tunnel (we dial via exec, not DNS), but the
			// app may route on it — present it as localhost:<port>.
			req.URL.Host = fmt.Sprintf("localhost:%d", port)
			req.Host = req.URL.Host
			req.URL.Path = upstreamPath
			// Strip the dashboard's own auth cookie so the untrusted app can never
			// see or replay it. It has no business receiving the dashboard session.
			stripCookie(req, dashboardCookieName)
			// X-Forwarded-Prefix lets a well-behaved app build correct absolute
			// URLs under the /p/<id>/live/<port> mount, if it honors the header.
			req.Header.Set("X-Forwarded-Prefix", fmt.Sprintf("/p/%s/live/%d", id, port))
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialExecTunnel(ctx, container, port)
			},
			// The tunnel is not reusable across requests the way a real socket is,
			// and app responses can be slow to start; keep this simple and robust.
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		ModifyResponse: hardenLiveResponse,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, e error) {
			http.Error(w, fmt.Sprintf("live view: could not reach the app on port %d (%v)", port, e), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// hardenLiveResponse rewrites the framed app's response headers so embedding the
// UNTRUSTED sandbox app in the dashboard can't become a sandbox→dashboard path.
// This is the server half of the isolation; the client half is the iframe's
// sandbox= attribute (which runs the app in an opaque origin with no
// same-origin access to the dashboard).
//
//   - Replace whatever framing policy the app sent with our own: only the
//     dashboard itself may frame this content (frame-ancestors 'self'). We
//     REMOVE the app's X-Frame-Options entirely — a DENY/SAMEORIGIN there would
//     otherwise make the browser refuse to render our iframe at all — and pin
//     the CSP frame-ancestors ourselves so it can be framed by us and no one else.
//   - Never let the framed app set cookies in the dashboard's origin. Its
//     Set-Cookie headers are dropped so it can't plant a cookie the browser
//     would then send on dashboard requests.
func hardenLiveResponse(resp *http.Response) error {
	h := resp.Header
	// The app's own anti-framing headers would block our legitimate embed; drop
	// them and assert our own frame-ancestors policy.
	h.Del("X-Frame-Options")
	h.Del("Content-Security-Policy")
	h.Del("Content-Security-Policy-Report-Only")
	h.Set("Content-Security-Policy", "frame-ancestors 'self'")
	// The framed app must not set cookies scoped to the dashboard origin.
	h.Del("Set-Cookie")
	return nil
}

// stripCookie removes a single named cookie from the request's Cookie header,
// leaving any others intact.
func stripCookie(r *http.Request, name string) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name == name {
			continue
		}
		r.AddCookie(c)
	}
}
