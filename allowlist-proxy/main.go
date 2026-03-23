// allowlist-proxy: HTTP CONNECT proxy that enforces a domain allowlist.
//
// The allowlist is compiled into the binary via go:embed. It cannot be
// changed at runtime — rebuild the binary to update allowed domains.
//
// Usage:
//
//	allowlist-proxy \
//	  --listen 127.0.0.1:3128 \
//	  --upstream http://host.docker.internal:8080
package main

import (
	"bufio"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

//go:embed allowed-domains.txt
var embeddedDomains string

// ----------------------------------------------------------------------------
// Allowlist
// ----------------------------------------------------------------------------

type Allowlist struct {
	domains map[string]struct{}
}

func NewAllowlist() *Allowlist {
	domains := make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(embeddedDomains))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		domains[strings.ToLower(line)] = struct{}{}
	}
	log.Printf("allowlist: loaded %d domains (compiled-in)", len(domains))
	return &Allowlist{domains: domains}
}

// Allowed returns true if host (without port) is in the allowlist or is a
// subdomain of a listed domain.
func (al *Allowlist) Allowed(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(h)

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

// handlePlainHTTP handles non-CONNECT proxy requests (plain HTTP).
// Loopback addresses (localhost, 127.x.x.x, ::1) bypass the allowlist.
func (p *ProxyHandler) handlePlainHTTP(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "plain HTTP proxy requires absolute URL", http.StatusBadRequest)
		return
	}

	host := r.URL.Host
	hostname := r.URL.Hostname()

	isLoopback := hostname == "localhost" || hostname == "::1" ||
		strings.HasPrefix(hostname, "127.")

	if !isLoopback && !p.allowlist.Allowed(host) {
		log.Printf("BLOCKED  %s (plain HTTP)", host)
		http.Error(w, fmt.Sprintf("domain not in allowlist: %s", host), http.StatusForbidden)
		return
	}

	log.Printf("ALLOWED  %s (plain HTTP)", host)

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Del("Proxy-Authenticate")
	outReq.Header.Del("Proxy-Authorization")

	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		p.handlePlainHTTP(w, r)
		return
	}

	host := r.Host // "hostname:port"

	if !p.allowlist.Allowed(host) {
		log.Printf("BLOCKED  %s", host)
		http.Error(w, fmt.Sprintf("domain not in allowlist: %s", host), http.StatusForbidden)
		return
	}

	log.Printf("ALLOWED  %s", host)

	var targetConn net.Conn
	var err error

	if p.upstream != nil {
		targetConn, err = net.Dial("tcp", p.upstream.Host)
		if err != nil {
			http.Error(w, fmt.Sprintf("upstream dial failed: %v", err), http.StatusBadGateway)
			return
		}
		fmt.Fprintf(targetConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
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
		targetConn, err = net.Dial("tcp", host)
		if err != nil {
			http.Error(w, fmt.Sprintf("dial failed: %v", err), http.StatusBadGateway)
			return
		}
	}
	defer targetConn.Close()

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

	clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

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
	flag.Parse()

	al := NewAllowlist()

	var upstream *url.URL
	if *upstreamStr != "" {
		var err error
		upstream, err = url.Parse(*upstreamStr)
		if err != nil {
			log.Fatalf("invalid upstream URL %q: %v", *upstreamStr, err)
		}
		log.Printf("upstream proxy: %s", upstream)
	} else {
		log.Printf("upstream proxy: none (direct connections)")
	}

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
