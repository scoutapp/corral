package config

import "testing"

func TestNormalizeRepo(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		// GitHub short + URL forms collapse to owner/name.
		{"scoutapp/sandclaude", "scoutapp/sandclaude", true},
		{"  scoutapp/sandclaude  ", "scoutapp/sandclaude", true},
		{"https://github.com/foo/bar", "foo/bar", true},
		{"https://github.com/foo/bar.git", "foo/bar", true},
		{"github.com/foo/bar/", "foo/bar", true},
		// Non-github hosts stay full base URLs.
		{"https://git.acme.com/foo/bar", "https://git.acme.com/foo/bar", true},
		{"https://git.acme.com/foo/bar/", "https://git.acme.com/foo/bar", true},
		{"http://gitea.local/me/sandclaude.git", "http://gitea.local/me/sandclaude", true},
		// Rejections.
		{"", "", false},
		{"nope", "", false},
		{"a/b/c", "", false},
		{"/only", "", false},
		{"https://github.com/only", "", false}, // github URL needs owner/name
		{"https://bare-host.com", "", false},   // no path segment
	}
	for _, c := range cases {
		got, ok := NormalizeRepo(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("NormalizeRepo(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestRepoToBaseURL(t *testing.T) {
	cases := map[string]string{
		"scoutapp/sandclaude":            "https://github.com/scoutapp/sandclaude",
		"https://git.acme.com/foo/bar":   "https://git.acme.com/foo/bar",
		"http://gitea.local/me/sandclaude": "http://gitea.local/me/sandclaude",
	}
	for in, want := range cases {
		if got := RepoToBaseURL(in); got != want {
			t.Errorf("RepoToBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpdateRepoOrDefault(t *testing.T) {
	if got := (&GlobalSettings{}).UpdateRepoOrDefault(); got != DefaultUpdateRepo {
		t.Errorf("empty settings = %q, want default %q", got, DefaultUpdateRepo)
	}
	if got := (&GlobalSettings{UpdateRepo: "garbage"}).UpdateRepoOrDefault(); got != DefaultUpdateRepo {
		t.Errorf("garbage repo = %q, want default fallback", got)
	}
	if got := (&GlobalSettings{UpdateRepo: "acme/fork"}).UpdateRepoOrDefault(); got != "acme/fork" {
		t.Errorf("custom repo = %q, want acme/fork", got)
	}
}
