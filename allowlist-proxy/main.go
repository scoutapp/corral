// allowlist-proxy: HTTP CONNECT proxy that enforces a domain allowlist.
//
// All CONNECT requests are checked against an allowlist file. Allowed
// requests are forwarded to an upstream proxy (e.g. mitmproxy for
// credential injection). Blocked requests get a 403 immediately.
//
// Usage:
//
//	allowlist-proxy \
//	  --listen 127.0.0.1:3128 \
//	  --upstream http://host.docker.internal:8080 \
//	  --allowlist /path/to/allowed-domains.txt
//
// The allowlist file contains one domain per line. Lines starting with #
// and blank lines are ignored. Subdomains are automatically allowed:
// listing "example.com" also allows "api.example.com".
//
// Send SIGHUP to reload the allowlist without restarting.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

// ----------------------------------------------------------------------------
// Allowlist
// ----------------------------------------------------------------------------

type Allowlist struct {
	mu      sync.RWMutex
	domains map[string]struct{}
	path    string
}

func NewAllowlist(path string) (*Allowlist, error) {
	al := &Allowlist{path: path}
	if err := al.reload(); err != nil {
		return nil, err
	}
	return al, nil
}

func (al *Allowlist) reload() error {
	f, err := os.Open(al.path)
	if err != nil {
		return fmt.Errorf("open allowlist %s: %w", al.path, err)
	}
	defer f.Close()

	domains := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		domains[strings.ToLower(line)] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read allowlist: %w", err)
	}

	al.mu.Lock()
	al.domains = domains
	al.mu.Unlock()

	log.Printf("allowlist: loaded %d domains from %s", len(domains), al.path)
	return nil
}

// Allowed returns true if host (without port) is in the allowlist or is a
// subdomain of a listed domain.
func (al *Allowlist) Allowed(host string) bool {
	// Strip port if present.
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(h)

	al.mu.RLock()
	defer al.mu.RUnlock()

	// Exact match.
	if _, ok := al.domains[h]; ok {
		return true
	}

	// Check parent domains: "a.b.c" → check "b.c", then "c".
	parts := strings.Split(h, ".")
	for i := 1; i < len(parts); i++ {
		parent := strings.Join(parts[i:], ".")
		if _, ok := al.domains[parent]; ok {
			return true
		}
	}

	return false
}

// ----------------------------------------------------------------------------
// Proxy handler
// ----------------------------------------------------------------------------

type ProxyHandler struct {
	allowlist *Allowlist
	upstream  *url.URL // nil = direct
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}

	host := r.Host // "hostname:port"

	if !p.allowlist.Allowed(host) {
		log.Printf("BLOCKED  %s", host)
		http.Error(w, fmt.Sprintf("domain not in allowlist: %s", host), http.StatusForbidden)
		return
	}

	log.Printf("ALLOWED  %s", host)

	// Dial the target — either via upstream proxy or directly.
	var targetConn net.Conn
	var err error

	if p.upstream != nil {
		// Connect to upstream proxy and send a CONNECT to it.
		targetConn, err = net.Dial("tcp", p.upstream.Host)
		if err != nil {
			http.Error(w, fmt.Sprintf("upstream dial failed: %v", err), http.StatusBadGateway)
			return
		}
		// Send CONNECT to upstream.
		fmt.Fprintf(targetConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
		// Read upstream's response.
		resp, err := http.ReadResponse(bufio.NewReader(targetConn), r)
		if err != nil || resp.StatusCode != http.StatusOK {
			targetConn.Close()
			code := http.StatusBadGateway
			msg := "upstream CONNECT failed"
			if resp != nil {
				code = resp.StatusCode
				msg = resp.Status
			}
			http.Error(w, msg, code)
			return
		}
	} else {
		// Direct connection.
		targetConn, err = net.Dial("tcp", host)
		if err != nil {
			http.Error(w, fmt.Sprintf("dial failed: %v", err), http.StatusBadGateway)
			return
		}
	}
	defer targetConn.Close()

	// Hijack the client connection and tell it the tunnel is open.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, fmt.Sprintf("hijack failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// Signal to client that tunnel is ready.
	clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

	// Bidirectional copy.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(targetConn, clientConn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(clientConn, targetConn)
		done <- struct{}{}
	}()
	<-done
}

// ----------------------------------------------------------------------------
// Main
// ----------------------------------------------------------------------------

func main() {
	listen := flag.String("listen", "127.0.0.1:3128", "address to listen on")
	upstreamStr := flag.String("upstream", "", "upstream proxy URL (e.g. http://host.docker.internal:8080); empty = direct")
	allowlistPath := flag.String("allowlist", "/home/claude/.firewall/allowed-domains.txt", "path to allowed-domains file")
	flag.Parse()

	al, err := NewAllowlist(*allowlistPath)
	if err != nil {
		log.Fatalf("failed to load allowlist: %v", err)
	}

	var upstream *url.URL
	if *upstreamStr != "" {
		upstream, err = url.Parse(*upstreamStr)
		if err != nil {
			log.Fatalf("invalid upstream URL %q: %v", *upstreamStr, err)
		}
		log.Printf("upstream proxy: %s", upstream)
	} else {
		log.Printf("upstream proxy: none (direct connections)")
	}

	// Reload allowlist on SIGHUP.
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGHUP)
		for range ch {
			log.Println("SIGHUP received — reloading allowlist")
			if err := al.reload(); err != nil {
				log.Printf("reload failed: %v", err)
			}
		}
	}()

	handler := &ProxyHandler{allowlist: al, upstream: upstream}
	server := &http.Server{
		Addr:    *listen,
		Handler: handler,
	}

	log.Printf("allowlist-proxy listening on %s", *listen)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
