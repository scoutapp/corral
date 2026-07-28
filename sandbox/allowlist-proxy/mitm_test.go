package main

import (
	"net/url"
	"testing"
)

// mkAllow builds an Allowlist containing the given domains, for tests.
func mkAllow(domains ...string) *Allowlist {
	m := make(map[string]struct{})
	for _, d := range domains {
		m[d] = struct{}{}
	}
	return &Allowlist{domains: m}
}

func TestShouldMitm(t *testing.T) {
	upstream, _ := url.Parse("http://host.docker.internal:8080")

	cases := []struct {
		name          string
		host          string
		upstream      bool // whether an upstream is configured
		monitorActive bool
		monitor       *Allowlist
		mitmPorts     []string
		wantMitm      bool
	}{
		{"default monitors all on 443", "api.anthropic.com:443", true, false, nil, []string{"80", "443"}, true},
		{"default monitors all on 80", "example.com:80", true, false, nil, []string{"80", "443"}, true},
		{"ssh port bypasses mitm", "github.com:22", true, false, nil, []string{"80", "443"}, false},
		{"socks port bypasses mitm", "host:1080", true, false, nil, []string{"80", "443"}, false},
		{"no upstream never mitms", "api.anthropic.com:443", false, false, nil, []string{"80", "443"}, false},
		{"monitor-list: listed host mitm'd", "api.anthropic.com:443", true, true, mkAllow("api.anthropic.com"), []string{"80", "443"}, true},
		{"monitor-list: unlisted host direct", "github.com:443", true, true, mkAllow("api.anthropic.com"), []string{"80", "443"}, false},
		{"monitor-list: subdomain of listed mitm'd", "sub.api.anthropic.com:443", true, true, mkAllow("anthropic.com"), []string{"80", "443"}, true},
		{"monitor-list but ssh still bypasses", "api.anthropic.com:22", true, true, mkAllow("api.anthropic.com"), []string{"80", "443"}, false},
		{"custom port list allows 8443", "api.foo.com:8443", true, false, nil, []string{"443", "8443"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &ProxyHandler{mitmPorts: parsePorts(join(tc.mitmPorts))}
			if tc.upstream {
				p.upstream = upstream
			}
			if tc.monitor != nil {
				p.monitorlist = tc.monitor
			} else {
				p.monitorlist = &Allowlist{}
			}
			p.monitorActive.Store(tc.monitorActive)

			got, reason := p.shouldMitm(tc.host)
			if got != tc.wantMitm {
				t.Errorf("shouldMitm(%q) = %v (%s), want %v", tc.host, got, reason, tc.wantMitm)
			}
		})
	}
}

func join(ps []string) string {
	out := ""
	for i, p := range ps {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}
