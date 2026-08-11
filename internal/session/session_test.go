package session

import (
	"strings"
	"testing"
)

func TestContainerNameForWorkspace(t *testing.T) {
	// Two DIFFERENT workspaces sharing a basename must get DIFFERENT container
	// names — the whole point of the hash suffix (ephemeral clones of one repo).
	a := ContainerNameForWorkspace("/home/u/work/myapp")
	b := ContainerNameForWorkspace("/home/u/projects/myapp")
	if a == b {
		t.Fatalf("same-basename workspaces collided: both %q", a)
	}
	// Deterministic: same path → same name (all callers must agree).
	if a != ContainerNameForWorkspace("/home/u/work/myapp") {
		t.Error("ContainerNameForWorkspace is not deterministic")
	}
	// Readable prefix retained.
	if !strings.HasPrefix(a, "corral_myapp_") {
		t.Errorf("expected readable corral_myapp_ prefix, got %q", a)
	}
	// Docker-safe: allowed set only.
	for _, name := range []string{
		ContainerNameForWorkspace("/tmp/my app (2)"),
		ContainerNameForWorkspace("/tmp/weird:name.v2"),
	} {
		for _, r := range name {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-'
			if !ok {
				t.Errorf("container name %q has docker-invalid char %q", name, string(r))
			}
		}
	}
}

func TestTmuxSessionNameForContainer(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain name unchanged", "corral_myproject", "corral_myproject"},
		{"underscores preserved", "corral_my_project", "corral_my_project"},
		{"dot dir name sanitized", "corral_my.app", "corral_my_app"},
		{"leading-dot workspace sanitized", "corral_.workspace", "corral__workspace"},
		{"colon sanitized", "corral_a:b", "corral_a_b"},
		{"multiple dots", "corral_a.b.c", "corral_a_b_c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TmuxSessionNameForContainer(c.in); got != c.want {
				t.Errorf("TmuxSessionNameForContainer(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The sanitized name must never contain a tmux target separator, or downstream
// has-session/capture/kill would mis-resolve.
func TestTmuxSessionNameHasNoSeparators(t *testing.T) {
	for _, in := range []string{"corral_my.app", "corral_a:b", "corral_x.y:z"} {
		got := TmuxSessionNameForContainer(in)
		for _, sep := range []rune{'.', ':'} {
			for _, r := range got {
				if r == sep {
					t.Errorf("TmuxSessionNameForContainer(%q) = %q still contains %q", in, got, string(sep))
				}
			}
		}
	}
}
