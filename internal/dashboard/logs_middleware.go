package dashboard

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/applog"
)

// logRequests wraps a handler to record every HTTP request in the application
// log (category=http): method, path, status, duration. Per the "log everything"
// goal this includes GETs. It skips pure-infrastructure noise — static asset
// fetches and /healthz — which carry no signal and would flood the log; the
// high-frequency /status poll is kept but tagged event=http.poll so the Logs UI
// can collapse it. WebSocket upgrades are logged as a single request (their
// long-lived stream isn't re-logged per frame).
func (d *dashboardServer) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if skipLogPath(path) {
			next.ServeHTTP(w, r)
			return
		}

		// The request is the root of a trace: mint a trace_id + a root span id and
		// carry them in the request context, so any downstream work that already
		// threads ctx (automation hooks, AI/PR-action emits, chat) nests under
		// this request in the trace tree — no manual id threading.
		//
		// The high-frequency /status poll is deliberately left UNTRACED: it fans
		// out nothing worth a tree and would mint a throwaway trace every ~3s.
		event := "http.request"
		traceID, spanID := applog.NewTraceID(), applog.NewTraceID()
		if path == "/status" {
			event = "http.poll" // high-frequency; UI collapses these
			traceID, spanID = "", ""
		}
		if traceID != "" {
			r = r.WithContext(applog.WithSpan(r.Context(), traceID, spanID))
		}

		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		dur := time.Since(start).Milliseconds()

		level := applog.LevelInfo
		if sw.status >= 500 {
			level = applog.LevelError
		} else if sw.status >= 400 {
			level = applog.LevelWarn
		}

		// On an error status, surface WHY: handlers write the reason into the body
		// (http.Error → "bad JSON: EOF", "API writes are disabled", …). Without this
		// the log said only "POST /path → 400" — the status, never the reason. Fold
		// a trimmed snippet of that body into the message + meta so the Logs UI shows
		// the actual cause. Only captured for errors (see statusWriter.captureBody),
		// so success responses aren't buffered.
		msg := applog.Fmt("%s %s → %d", r.Method, path, sw.status)
		meta := map[string]any{"method": r.Method, "path": path, "status": sw.status}
		if reason := errorReason(sw); reason != "" {
			msg = applog.Fmt("%s %s → %d: %s", r.Method, path, sw.status, reason)
			meta["error"] = reason
		}
		// The request row is the root span itself (single row — it already has its
		// own duration; no separate .start/.end needed). Children use span pairs.
		d.applog().Log(applog.Entry{
			Level:      level,
			Category:   applog.CatHTTP,
			Event:      event,
			Message:    msg,
			DurationMs: dur,
			Meta:       meta,
			TraceID:    traceID,
			SpanID:     spanID,
		})
	})
}

// skipLogPath drops static assets + healthz (no signal, high volume).
func skipLogPath(p string) bool {
	return p == "/healthz" || p == "/favicon.ico" ||
		strings.HasPrefix(p, "/static/")
}

// errorReason returns a short, single-line reason for an error response, from the
// captured body. Returns "" for non-error responses or an empty body. The body is
// http.Error's plain text (e.g. "bad JSON: EOF\n") or a small JSON error; we take
// the first line, trimmed, so the log message stays one line.
func errorReason(s *statusWriter) string {
	if s.status < 400 || len(s.body) == 0 {
		return ""
	}
	reason := string(s.body)
	if i := strings.IndexByte(reason, '\n'); i >= 0 {
		reason = reason[:i]
	}
	reason = strings.TrimSpace(reason)
	// Guard against a pathologically long single line.
	if len(reason) > 200 {
		reason = reason[:200] + "…"
	}
	return reason
}

// maxErrorBodyCapture bounds how much of an error response body we buffer for the
// log reason — enough for a message like "bad JSON: EOF" or an allowlist error,
// not a whole page. Success bodies are never captured.
const maxErrorBodyCapture = 512

// statusWriter captures the response status (and, for error statuses, a bounded
// snippet of the body) for logging. It forwards Hijack (for WebSocket upgrades)
// and Flush when the underlying writer supports them.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
	body   []byte // captured only for 4xx/5xx, up to maxErrorBodyCapture bytes
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	// Capture the body only for error responses, and only up to the cap. This is
	// the reason string handlers pass to http.Error; success bodies (which can be
	// large JSON/streams) are never buffered.
	if s.status >= 400 && len(s.body) < maxErrorBodyCapture {
		room := maxErrorBodyCapture - len(s.body)
		if room > len(b) {
			room = len(b)
		}
		s.body = append(s.body, b[:room]...)
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer so WebSocket upgrades (chat,
// terminals, draft flows) still work through this middleware. Without it, the
// gorilla upgrader would fail because the wrapped writer isn't a Hijacker.
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support Hijack")
}
