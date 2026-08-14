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
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		dur := time.Since(start).Milliseconds()

		event := "http.request"
		if path == "/status" {
			event = "http.poll" // high-frequency; UI collapses these
		}
		level := applog.LevelInfo
		if sw.status >= 500 {
			level = applog.LevelError
		} else if sw.status >= 400 {
			level = applog.LevelWarn
		}
		d.applog().Log(applog.Entry{
			Level:      level,
			Category:   applog.CatHTTP,
			Event:      event,
			Message:    applog.Fmt("%s %s → %d", r.Method, path, sw.status),
			DurationMs: dur,
			Meta:       map[string]any{"method": r.Method, "path": path, "status": sw.status},
		})
	})
}

// skipLogPath drops static assets + healthz (no signal, high volume).
func skipLogPath(p string) bool {
	return p == "/healthz" || p == "/favicon.ico" ||
		strings.HasPrefix(p, "/static/")
}

// statusWriter captures the response status for logging. It forwards Hijack (for
// WebSocket upgrades) and Flush when the underlying writer supports them.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
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
