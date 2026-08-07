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
	"sync/atomic"
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
	return al.setFromText(string(plaintext), path, "allowlist")
}

// loadPlain reads a plaintext (unencrypted) domain list. Used for the monitor-list,
// whose contents are just hostnames — not secret — so they're stored plainly in
// config.json and materialized to a bind-mounted file, unlike the allowlist.
func (al *Allowlist) loadPlain(path string) error {
	text, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read monitor-list: %w", err)
	}
	return al.setFromText(string(text), path, "monitor-list")
}

// setFromText parses a newline-separated domain list (ignoring blanks and #
// comments) and atomically replaces the in-memory set.
func (al *Allowlist) setFromText(text, path, label string) error {
	domains := make(map[string]struct{})
	domainList := []string{}
	scanner := bufio.NewScanner(strings.NewReader(text))
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

	log.Printf("%s: loaded %d domains from %s", label, len(domains), path)
	for _, d := range domainList {
		log.Printf("  - %s", d)
	}
	return nil
}

// Allowed returns true if host (without port) is in the allowlist or is a
// subdomain of a listed domain.
// Len reports how many domains the list holds (0 = empty). Concurrency-safe.
func (al *Allowlist) Len() int {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return len(al.domains)
}

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

	// Selective mitm. When monitorActive is true, monitorlist restricts which
	// allowed hosts are routed through the mitmweb upstream (full TLS interception
	// + credential injection); hosts that are allowed but NOT listed are dialed
	// directly — still allowlist-checked and logged, but never decrypted. When
	// monitorActive is false the default is "monitor everything," preserving prior
	// behavior. monitorlist is always non-nil so SIGHUP can reload into it in place
	// (its own RWMutex makes that concurrency-safe with in-flight requests);
	// monitorActive is the toggle the request path reads.
	//
	// mitmPorts is the set of destination ports eligible for mitm at all. mitmweb
	// only speaks HTTP/TLS, so any CONNECT to a port outside this set (ssh:22,
	// SOCKS, databases, git-over-ssh) is dialed directly regardless of monitorlist
	// — sending it upstream would just break a protocol mitmweb can't parse.
	monitorlist   *Allowlist
	monitorActive atomic.Bool
	mitmPorts     map[string]struct{}

	// credentialHosts are hosts that have an injected credential (from the creds
	// file). They MUST always be mitm'd — the container sends a dummy token and the
	// proxy swaps in the real one, which only happens when the host is intercepted.
	// Direct-dialing a credentialed host would leak the dummy value and break auth.
	// So shouldMitm force-mitms any host in this set regardless of the monitorlist.
	// Reloaded in place on SIGHUP like the other lists. Empty when no creds.
	credentialHosts *Allowlist
}

// shouldMitm decides whether a CONNECT to host ("hostname:port") should be
// routed through the mitmweb upstream. It assumes the host has already passed the
// allowlist check. Direct-dial (false) still logs the flow; it just isn't
// decrypted. Returns the reason for the log line.
func (p *ProxyHandler) shouldMitm(host string) (bool, string) {
	if p.upstream == nil {
		return false, "no-upstream"
	}

	_, port, err := net.SplitHostPort(host)
	if err != nil {
		port = "443" // CONNECT should always carry a port, but assume TLS if not
	}
	if _, ok := p.mitmPorts[port]; !ok {
		return false, "port:" + port
	}

	hostname, _, herr := net.SplitHostPort(host)
	if herr != nil {
		hostname = host
	}

	// Credentialed hosts are ALWAYS mitm'd — a credential is injected for them, so
	// they must be intercepted regardless of the monitor-list. This is independent
	// of (and overrides) the user's discretionary monitor selection.
	if p.credentialHosts != nil && p.credentialHosts.Allowed(hostname) {
		return true, "credential"
	}

	if p.monitorActive.Load() {
		if !p.monitorlist.Allowed(hostname) {
			return false, "not-monitored"
		}
	}
	return true, ""
}

// parsePorts turns "80,443" into a set. Blank/whitespace entries are skipped.
func parsePorts(s string) map[string]struct{} {
	ports := make(map[string]struct{})
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			ports[p] = struct{}{}
		}
	}
	return ports
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

	// Decide mitm vs direct. Either way the host was logged above; this only
	// controls whether the tunnel is routed through mitmweb for decryption.
	mitm, reason := p.shouldMitm(host)
	if mitm {
		log.Printf("MONITORED %s", host)
	} else {
		log.Printf("DIRECT   %s (%s)", host, reason)
	}

	targetConn, err := p.dialTarget(host, mitm)
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

// dialTarget opens a connection to host ("hostname:port"). When mitm is true and
// an upstream proxy is configured it issues an HTTP CONNECT to the upstream and
// returns the tunneled conn (for TLS interception); otherwise it dials host
// directly. Shared by the explicit CONNECT handler and the transparent listener.
func (p *ProxyHandler) dialTarget(host string, mitm bool) (net.Conn, error) {
	if p.upstream == nil || !mitm {
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
	// Stamp log lines in UTC explicitly. The dashboard parses these timestamps
	// (proxy.log DIRECT lines) as UTC to interleave them with mitmweb flows; being
	// explicit here keeps that correct regardless of the container's TZ.
	log.SetFlags(log.LstdFlags | log.LUTC)

	listen         := flag.String("listen", "127.0.0.1:3128", "address to listen on")
	transparent    := flag.String("transparent-listen", "", "if set, also run a transparent (intercepting) listener on this address for iptables-REDIRECTed connections")
	upstreamStr    := flag.String("upstream", "", "upstream proxy URL (e.g. http://host.docker.internal:8080); empty = direct")
	allowlistPath  := flag.String("allowlist", "", "path to encrypted allowlist file (allowed-domains.txt.enc)")
	passthroughLog := flag.String("passthrough-log", "", "if set, allow unknown domains and append them to this file instead of blocking")
	monitorPath    := flag.String("monitorlist", "", "path to encrypted monitor-list; if set, only these hosts are routed through the mitm upstream (others allowed+logged but direct-dialed). Empty = monitor all.")
	credHostsPath  := flag.String("credential-hosts", "", "path to a plaintext list of hosts that have an injected credential; these are ALWAYS mitm'd regardless of the monitor-list (credential injection requires interception)")
	mitmPortsStr   := flag.String("mitm-ports", "80,443", "comma-separated destination ports eligible for mitm; CONNECT to any other port is direct-dialed (ssh, socks, etc.)")

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

	mitmPorts := parsePorts(*mitmPortsStr)
	log.Printf("mitm-eligible ports: %s (other ports direct-dialed)", *mitmPortsStr)

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

	handler := &ProxyHandler{
		allowlist:       al,
		upstream:        upstream,
		passthroughLog:  *passthroughLog,
		monitorlist:     &Allowlist{},
		credentialHosts: &Allowlist{},
		mitmPorts:       mitmPorts,
	}

	// Transparent (intercepting) listener for iptables-REDIRECTed connections.
	// Runs alongside the explicit proxy so clients need no proxy env vars.
	if *transparent != "" {
		go func() {
			if err := handler.serveTransparent(*transparent); err != nil {
				log.Fatalf("transparent listener error: %v", err)
			}
		}()
	}

	// loadMonitor (re)loads the monitor-list into handler.monitorlist in place and
	// toggles monitorActive. Absent file (or unset flag) = monitor all allowed
	// hosts — the default. Called at startup and on every SIGHUP so selective-mitm
	// routing refreshes alongside the allowlist.
	loadMonitor := func() {
		if *monitorPath == "" {
			handler.monitorActive.Store(false)
			return
		}
		if _, err := os.Stat(*monitorPath); err != nil {
			handler.monitorActive.Store(false)
			log.Printf("monitor-list absent (%s) — monitoring all allowed hosts", *monitorPath)
			return
		}
		if err := handler.monitorlist.loadPlain(*monitorPath); err != nil {
			log.Printf("monitor-list load failed: %v — monitoring all allowed hosts", err)
			handler.monitorActive.Store(false)
			return
		}
		// An EMPTY monitor file means "monitor all" (same as absent) — NOT "monitor
		// nothing". A present-but-empty file is common: the mount target is written
		// unconditionally so Docker never turns it into a directory, and a project
		// with no custom monitor list writes an empty one. Without this, an empty
		// file would leave monitorActive=true with zero hosts, silently direct-
		// dialing everything.
		if handler.monitorlist.Len() == 0 {
			handler.monitorActive.Store(false)
			log.Printf("monitor-list empty (%s) — monitoring all allowed hosts", *monitorPath)
			return
		}
		handler.monitorActive.Store(true)
		log.Printf("monitor-list active: only listed hosts routed through mitm upstream")
	}
	loadMonitor()

	// loadCredentialHosts (re)loads the always-mitm credential-host list in place.
	// These hosts have an injected credential, so they must always be intercepted
	// regardless of the monitor-list. Absent/empty file = no forced hosts. Called
	// at startup and on SIGHUP so a credential change takes effect on reload.
	loadCredentialHosts := func() {
		if *credHostsPath == "" {
			return
		}
		if _, err := os.Stat(*credHostsPath); err != nil {
			// Absent = no credentialed hosts. Reset to empty so a removed cred stops
			// forcing mitm on the next reload.
			handler.credentialHosts.setFromText("", *credHostsPath, "credential-hosts")
			return
		}
		if err := handler.credentialHosts.loadPlain(*credHostsPath); err != nil {
			log.Printf("credential-hosts load failed: %v", err)
			return
		}
		log.Printf("credential-hosts active: these hosts are always mitm'd (credential injection)")
	}
	loadCredentialHosts()

	// Hot-reload on SIGHUP — refreshes both the allowlist and the monitor-list, so
	// a single firewall-reload/proxy-apply updates selective-mitm routing too.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	go func() {
		for range sigCh {
			log.Printf("SIGHUP received — reloading allowlist from %s", *allowlistPath)
			if err := al.load(*allowlistPath, key); err != nil {
				log.Printf("allowlist reload failed: %v", err)
			}
			loadMonitor()
			loadCredentialHosts()
		}
	}()

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
