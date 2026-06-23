// allowlist-proxy: HTTP CONNECT proxy that enforces a domain allowlist.
//
// The allowlist is loaded from an AES-256-GCM encrypted file at startup and
// can be hot-reloaded by sending SIGHUP. The encryption key is derived from
// the ALLOWLIST_KEY environment variable using SHA-256.
//
// Usage:
//
//	allowlist-proxy \
//	  --listen 127.0.0.1:3128 \
//	  --allowlist /path/to/allowed-domains.txt.enc \
//	  --upstream http://host.docker.internal:8080
package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
// Encryption helpers
// ----------------------------------------------------------------------------

// deriveKey derives a 32-byte AES-256 key from the given passphrase using
// SHA-256(passphrase || ":allowlist-proxy-v1"). Simple and stdlib-only.
func deriveKey(passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("ALLOWLIST_KEY environment variable is not set")
	}
	h := sha256.Sum256([]byte(passphrase + ":allowlist-proxy-v1"))
	return h[:], nil
}

// encryptFile encrypts plaintext using AES-256-GCM and writes the result to
// dst. Format: 12-byte random nonce || GCM ciphertext+tag.
func encryptFile(key []byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decryptFile decrypts AES-256-GCM ciphertext produced by encryptFile.
func decryptFile(key []byte, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, data := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, data, nil)
}

// ----------------------------------------------------------------------------
// Allowlist
// ----------------------------------------------------------------------------

type Allowlist struct {
	mu      sync.RWMutex
	domains map[string]struct{}
}

// load reads the encrypted allowlist file, decrypts it with key, and replaces
// the in-memory domain set atomically.
func (al *Allowlist) load(path string, key []byte) error {
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read allowlist: %w", err)
	}
	plaintext, err := decryptFile(key, ciphertext)
	if err != nil {
		return fmt.Errorf("decrypt allowlist: %w", err)
	}

	domains := make(map[string]struct{})
	domainList := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(plaintext)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		domains[lower] = struct{}{}
		domainList = append(domainList, lower)
	}

	al.mu.Lock()
	al.domains = domains
	al.mu.Unlock()

	log.Printf("allowlist: loaded %d domains from %s", len(domains), path)
	log.Printf("allowed domains:")
	for _, d := range domainList {
		log.Printf("  - %s", d)
	}
	return nil
}

// Allowed returns true if host (without port) is in the allowlist or is a
// subdomain of a listed domain.
func (al *Allowlist) Allowed(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(h)

	al.mu.RLock()
	defer al.mu.RUnlock()

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
	allowlist       *Allowlist
	upstream        *url.URL // nil = direct
	passthroughLog  string   // if set, allow unknown domains and append them here
	transparentPort string   // listen port of the transparent listener (for REDIRECT vs TPROXY detection)
}

// appendDomain appends a domain to the passthrough log file if not already present.
func (p *ProxyHandler) appendDomain(domain string) {
	h, _, err := net.SplitHostPort(domain)
	if err != nil {
		h = domain
	}
	h = strings.ToLower(h)

	// Read current contents to check for duplicates, then reopen for append.
	existing, _ := os.ReadFile(p.passthroughLog)
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == h {
			return // already present
		}
	}

	f, err := os.OpenFile(p.passthroughLog, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("passthrough-log: failed to open %s: %v", p.passthroughLog, err)
		return
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s\n", h); err != nil {
		log.Printf("passthrough-log: failed to write %s: %v", p.passthroughLog, err)
	} else {
		log.Printf("WROTE    %s → %s", h, p.passthroughLog)
	}
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
		if p.passthroughLog != "" {
			p.appendDomain(host)
			log.Printf("ALLOWED* %s (plain HTTP, unknown — logged)", host)
		} else {
			log.Printf("BLOCKED  %s (plain HTTP)", host)
			http.Error(w, fmt.Sprintf("domain not in allowlist: %s", host), http.StatusForbidden)
			return
		}
	} else {
		log.Printf("ALLOWED  %s (plain HTTP)", host)
	}

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
		if p.passthroughLog != "" {
			p.appendDomain(host)
			log.Printf("ALLOWED* %s (unknown — logged)", host)
		} else {
			log.Printf("BLOCKED  %s", host)
			http.Error(w, fmt.Sprintf("domain not in allowlist: %s", host), http.StatusForbidden)
			return
		}
	} else {
		log.Printf("ALLOWED  %s", host)
	}

	targetConn, err := p.dialTarget(host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
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

// dialTarget opens a connection to host ("hostname:port"). If an upstream proxy
// is configured it issues an HTTP CONNECT to it and returns the tunneled conn;
// otherwise it dials host directly. Shared by the explicit CONNECT handler and
// the transparent listener.
func (p *ProxyHandler) dialTarget(host string) (net.Conn, error) {
	if p.upstream == nil {
		conn, err := net.Dial("tcp", host)
		if err != nil {
			return nil, fmt.Errorf("dial failed: %v", err)
		}
		return conn, nil
	}

	// Force IPv4 for the upstream dial. host.docker.internal resolves to BOTH an
	// IPv4 and an IPv6 address in /etc/hosts, but mitmproxy on the host listens on
	// IPv4 only; without this, Go's dual-stack dialer may pick the IPv6 address and
	// the connection fails, which surfaces as the client getting refused.
	conn, err := net.Dial("tcp4", p.upstream.Host)
	if err != nil {
		return nil, fmt.Errorf("upstream dial failed: %v", err)
	}
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
	// Read the CONNECT response byte-by-byte up to the end of headers (\r\n\r\n).
	// We must NOT use a buffered reader here: it would consume bytes belonging to
	// the tunneled stream that follows the response, and those bytes are then lost
	// when we splice the raw conn — which stalls the tunneled TLS handshake.
	status, err := readHTTPStatusLine(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("upstream CONNECT response: %v", err)
	}
	if !strings.Contains(status, " 200 ") {
		conn.Close()
		return nil, fmt.Errorf("upstream CONNECT failed: %s", strings.TrimSpace(status))
	}
	return conn, nil
}

// readHTTPStatusLine reads an HTTP response from conn one byte at a time until the
// end of the header block (\r\n\r\n), returning the status line. Reading byte by
// byte guarantees we do not consume any bytes of the tunneled stream that follows.
func readHTTPStatusLine(conn net.Conn) (string, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := conn.Read(one)
		if n > 0 {
			buf = append(buf, one[0])
			if len(buf) >= 4 && buf[len(buf)-4] == '\r' && buf[len(buf)-3] == '\n' &&
				buf[len(buf)-2] == '\r' && buf[len(buf)-1] == '\n' {
				break
			}
			// Guard against an unbounded/garbage response.
			if len(buf) > 8192 {
				return "", errors.New("response headers too large")
			}
		}
		if err != nil {
			return "", err
		}
	}
	// Status line is the first CRLF-terminated line.
	if i := strings.Index(string(buf), "\r\n"); i >= 0 {
		return string(buf[:i]), nil
	}
	return string(buf), nil
}

// ----------------------------------------------------------------------------
// Main
// ----------------------------------------------------------------------------

func main() {
	listen         := flag.String("listen", "127.0.0.1:3128", "address to listen on")
	transparent    := flag.String("transparent-listen", "", "if set, also run a transparent (intercepting) listener on this address for iptables-REDIRECTed connections")
	upstreamStr    := flag.String("upstream", "", "upstream proxy URL (e.g. http://host.docker.internal:8080); empty = direct")
	allowlistPath  := flag.String("allowlist", "", "path to encrypted allowlist file (allowed-domains.txt.enc)")
	passthroughLog := flag.String("passthrough-log", "", "if set, allow unknown domains and append them to this file instead of blocking")

	// Encrypt subcommand: allowlist-proxy encrypt <plaintext> <output.enc>
	// Checked before flag.Parse so it doesn't conflict with flags.
	if len(os.Args) >= 2 && os.Args[1] == "encrypt" {
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: allowlist-proxy encrypt <plaintext-file> <output.enc>")
			os.Exit(1)
		}
		key, err := deriveKey(os.Getenv("ALLOWLIST_KEY"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		plaintext, err := os.ReadFile(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading input: %v\n", err)
			os.Exit(1)
		}
		ciphertext, err := encryptFile(key, plaintext)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error encrypting: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(os.Args[3], ciphertext, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "error writing output: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Encrypted %s → %s (%d bytes, key fingerprint: %s)\n",
			os.Args[2], os.Args[3], len(ciphertext),
			keyFingerprint(key))
		return
	}

	flag.Parse()

	if *allowlistPath == "" {
		log.Fatalf("--allowlist is required: path to encrypted allowed-domains.txt.enc")
	}

	key, err := deriveKey(os.Getenv("ALLOWLIST_KEY"))
	if err != nil {
		log.Fatalf("encryption key error: %v", err)
	}

	al := &Allowlist{}
	if err := al.load(*allowlistPath, key); err != nil {
		log.Fatalf("failed to load allowlist: %v", err)
	}

	// Hot-reload on SIGHUP.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	go func() {
		for range sigCh {
			log.Printf("SIGHUP received — reloading allowlist from %s", *allowlistPath)
			if err := al.load(*allowlistPath, key); err != nil {
				log.Printf("allowlist reload failed: %v", err)
			}
		}
	}()

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

	if *passthroughLog != "" {
		log.Printf("passthrough mode: unknown domains will be allowed and appended to %s", *passthroughLog)
	}

	handler := &ProxyHandler{allowlist: al, upstream: upstream, passthroughLog: *passthroughLog}

	// Transparent (intercepting) listener for iptables-REDIRECTed connections.
	// Runs alongside the explicit proxy so clients need no proxy env vars.
	if *transparent != "" {
		go func() {
			if err := handler.serveTransparent(*transparent); err != nil {
				log.Fatalf("transparent listener error: %v", err)
			}
		}()
	}

	server := &http.Server{
		Addr:    *listen,
		Handler: handler,
	}

	// Bind IPv4 explicitly (same reason as the transparent listener): clients
	// reach the explicit proxy via 127.0.0.1 / 172.x, all IPv4.
	ln, err := net.Listen("tcp4", *listen)
	if err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}

	log.Printf("allowlist-proxy listening on %s", *listen)
	if err := server.Serve(ln); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// keyFingerprint returns the first 8 hex chars of the key's SHA-256 for display.
func keyFingerprint(key []byte) string {
	h := sha256.Sum256(key)
	return hex.EncodeToString(h[:4])
}
