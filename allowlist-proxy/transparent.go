// Transparent (intercepting) proxy listener.
//
// iptables REDIRECT rewrites a connection's destination to this listener while
// the kernel remembers the original destination in conntrack. We recover it via
// getsockopt(SO_ORIGINAL_DST), peek the TLS ClientHello for the SNI hostname
// (falling back to the HTTP Host header, then the original-dst IP), enforce the
// allowlist, and splice the bytes to the target via the same upstream chain the
// explicit CONNECT handler uses. The peeked bytes are replayed so mitmproxy
// still sees an intact stream and terminates TLS normally.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"syscall"
	"unsafe"
)

// SO_ORIGINAL_DST is defined in <linux/netfilter_ipv4.h>; not exported by the
// syscall package.
const soOriginalDst = 80

// originalDst recovers the pre-REDIRECT destination of a TCP connection.
func originalDst(conn *net.TCPConn) (string, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return "", fmt.Errorf("syscall conn: %w", err)
	}

	var addr syscall.RawSockaddrInet4
	var getErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		size := uint32(syscall.SizeofSockaddrInet4)
		// getsockopt(fd, SOL_IP, SO_ORIGINAL_DST, &addr, &size)
		_, _, e := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			uintptr(syscall.SOL_IP),
			uintptr(soOriginalDst),
			uintptr(unsafe.Pointer(&addr)),
			uintptr(unsafe.Pointer(&size)),
			0,
		)
		if e != 0 {
			getErr = e
		}
	})
	if ctrlErr != nil {
		return "", ctrlErr
	}
	if getErr != nil {
		return "", fmt.Errorf("getsockopt SO_ORIGINAL_DST: %w", getErr)
	}

	ip := net.IPv4(addr.Addr[0], addr.Addr[1], addr.Addr[2], addr.Addr[3])
	// addr.Port holds the port in network (big-endian) byte order. Read the two
	// bytes in memory order and interpret as big-endian to get the host value.
	pb := (*[2]byte)(unsafe.Pointer(&addr.Port))
	port := binary.BigEndian.Uint16(pb[:])
	return net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port)), nil
}

// serveTransparent runs the transparent-listener accept loop.
//
// The socket is bound IPv4-only with IP_TRANSPARENT set. IP_TRANSPARENT lets the
// socket accept connections whose original destination is a non-local address
// (required for TPROXY) and makes the accepted conn's LocalAddr() report that
// original destination directly. It is also harmless for plain REDIRECT, where we
// fall back to SO_ORIGINAL_DST.
func (p *ProxyHandler) serveTransparent(listen string) error {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			// IP_TRANSPARENT (=19) is required only for TPROXY-style capture and
			// needs CAP_NET_ADMIN. It is harmless for plain iptables REDIRECT, where
			// we recover the destination via SO_ORIGINAL_DST instead. Best-effort:
			// if the capability is absent, log and continue (REDIRECT still works).
			return c.Control(func(fd uintptr) {
				if err := syscall.SetsockoptInt(int(fd), syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
					log.Printf("transparent: IP_TRANSPARENT not set (%v) — REDIRECT capture still works, TPROXY will not", err)
				}
			})
		},
	}
	ln, err := lc.Listen(context.Background(), "tcp4", listen)
	if err != nil {
		return err
	}
	// Remember our own listen port so the handler can tell a REDIRECTed connection
	// (LocalAddr port == our port → use SO_ORIGINAL_DST) from a TPROXY one
	// (LocalAddr is the real destination, any port).
	if _, portStr, perr := net.SplitHostPort(ln.Addr().String()); perr == nil {
		p.transparentPort = portStr
	}
	log.Printf("allowlist-proxy transparent listener on %s (IP_TRANSPARENT)", listen)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("transparent accept error: %v", err)
			continue
		}
		go p.handleTransparent(c)
	}
}

func (p *ProxyHandler) handleTransparent(client net.Conn) {
	defer client.Close()

	tcp, ok := client.(*net.TCPConn)
	if !ok {
		log.Printf("transparent: non-TCP conn %T", client)
		return
	}

	// With TPROXY + IP_TRANSPARENT, the accepted conn's LocalAddr() IS the original
	// destination (the kernel did not rewrite it). With plain REDIRECT, the kernel
	// rewrote LocalAddr to our own listener (IP:transparentPort), so we must recover
	// the original via SO_ORIGINAL_DST. Distinguish by the LocalAddr port: if it
	// equals our listen port, it was REDIRECTed.
	var origDst string
	la, _ := client.LocalAddr().(*net.TCPAddr)
	if la != nil && fmt.Sprintf("%d", la.Port) != p.transparentPort {
		origDst = la.String() // TPROXY: real destination
	} else {
		d, err := originalDst(tcp) // REDIRECT: recover pre-NAT destination
		if err != nil {
			log.Printf("transparent: original-dst failed: %v", err)
			return
		}
		origDst = d
	}
	origIP, origPort, _ := net.SplitHostPort(origDst)

	// Peek the leading bytes to extract a hostname without consuming them.
	br := bufio.NewReader(client)
	host := sniffHost(br, origPort)

	// Decide the destination address to enforce/dial. Prefer the sniffed
	// hostname (so mitmproxy and the allowlist see a real domain); fall back to
	// the original-dst IP.
	dialHost := origDst
	checkHost := origIP
	if host != "" {
		checkHost = host
		dialHost = net.JoinHostPort(host, origPort)
	}

	if !p.allowlist.Allowed(checkHost) {
		if p.passthroughLog != "" {
			p.appendDomain(checkHost)
			log.Printf("ALLOWED* %s (transparent, unknown — logged)", checkHost)
		} else {
			log.Printf("BLOCKED  %s (transparent)", checkHost)
			return
		}
	} else {
		log.Printf("ALLOWED  %s (transparent)", checkHost)
	}

	target, err := p.dialTarget(dialHost)
	if err != nil {
		log.Printf("transparent: dial %s failed: %v", dialHost, err)
		return
	}
	defer target.Close()

	// Splice both directions and wait for BOTH to finish. br holds the bytes
	// already peeked by sniffHost (Peek does not consume), so copying from br
	// forwards the ClientHello/request and everything after it.
	//
	// Critical: wait for both directions, not just one. When a direction ends we
	// half-close the peer's write side (CloseWrite) so the peer sees EOF and can
	// finish, but the other direction keeps flowing. Waiting on only one copy and
	// then closing both (the naive pattern) tears down the still-active reverse
	// direction mid-handshake — which stalls the tunneled TLS.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(target, br)
		if c, ok := target.(*net.TCPConn); ok {
			c.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, target)
		if c, ok := client.(*net.TCPConn); ok {
			c.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

// peekLen bounds a peek request to the reader's buffer size.
func peekLen(br *bufio.Reader, want int) int {
	if want < br.Size() {
		return want
	}
	return br.Size()
}

// sniffHost peeks the buffered reader and returns a hostname from either the TLS
// ClientHello SNI (port 443 / TLS handshake) or the HTTP Host header. Returns ""
// if neither is found. Never consumes bytes from br.
func sniffHost(br *bufio.Reader, port string) string {
	// A TLS handshake record starts with 0x16 (handshake content type).
	first, err := br.Peek(1)
	if err != nil {
		return ""
	}
	if first[0] == 0x16 {
		// Peek enough for a typical ClientHello; SNI lives early in the record.
		buf, _ := br.Peek(peekLen(br, 4096))
		if sni := parseSNI(buf); sni != "" {
			return sni
		}
		return ""
	}

	// Plain HTTP: read the Host header from the request line region.
	buf, _ := br.Peek(min(br.Size(), 4096))
	return parseHTTPHost(buf)
}

// parseSNI extracts the SNI server name from a TLS ClientHello record.
// Returns "" on any parse failure (best-effort, bounds-checked).
func parseSNI(b []byte) string {
	// TLS record header: type(1) version(2) length(2)
	if len(b) < 5 || b[0] != 0x16 {
		return ""
	}
	rec := b[5:]
	// Handshake header: type(1) length(3)
	if len(rec) < 4 || rec[0] != 0x01 { // 0x01 = ClientHello
		return ""
	}
	hs := rec[4:]
	// client_version(2) random(32)
	if len(hs) < 34 {
		return ""
	}
	pos := 34
	// session_id
	if pos >= len(hs) {
		return ""
	}
	sidLen := int(hs[pos])
	pos += 1 + sidLen
	// cipher_suites
	if pos+2 > len(hs) {
		return ""
	}
	csLen := int(binary.BigEndian.Uint16(hs[pos:]))
	pos += 2 + csLen
	// compression_methods
	if pos+1 > len(hs) {
		return ""
	}
	cmLen := int(hs[pos])
	pos += 1 + cmLen
	// extensions
	if pos+2 > len(hs) {
		return ""
	}
	extTotal := int(binary.BigEndian.Uint16(hs[pos:]))
	pos += 2
	end := pos + extTotal
	if end > len(hs) {
		end = len(hs)
	}
	for pos+4 <= end {
		extType := binary.BigEndian.Uint16(hs[pos:])
		extLen := int(binary.BigEndian.Uint16(hs[pos+2:]))
		pos += 4
		if pos+extLen > end {
			return ""
		}
		if extType == 0x0000 { // server_name
			ext := hs[pos : pos+extLen]
			// server_name_list: list_len(2) then entries name_type(1) len(2) name
			if len(ext) < 5 {
				return ""
			}
			nameType := ext[2]
			if nameType != 0x00 { // host_name
				return ""
			}
			nameLen := int(binary.BigEndian.Uint16(ext[3:]))
			if 5+nameLen > len(ext) {
				return ""
			}
			return strings.ToLower(string(ext[5 : 5+nameLen]))
		}
		pos += extLen
	}
	return ""
}

// parseHTTPHost extracts the Host header value from a buffered HTTP request.
func parseHTTPHost(b []byte) string {
	text := string(b)
	// Only treat as HTTP if it looks like a request line.
	if !strings.Contains(text, "\r\n") {
		return ""
	}
	for _, line := range strings.Split(text, "\r\n") {
		if line == "" {
			break
		}
		if h := strings.TrimSpace(strings.TrimPrefix(line, "Host:")); len(h) < len(line) {
			// Strip any :port — the original-dst port is authoritative.
			if host, _, err := net.SplitHostPort(h); err == nil {
				return strings.ToLower(host)
			}
			return strings.ToLower(h)
		}
	}
	return ""
}
