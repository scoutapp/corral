package release

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.4.0", "v0.3.0", true},
		{"v0.4.0", "v0.4.0", false},
		{"v0.3.9", "v0.4.0", false},
		{"v1.0.0", "v0.9.9", true},
		{"v0.4.1", "v0.4.0", true},
		{"0.4.0", "v0.3.0", true},           // no-v prefix
		{"v0.4.0", "dev", true},             // dev build -> any release is newer
		{"v0.4.0-rc1", "v0.3.0", true},      // prerelease suffix ignored
		{"garbage", "v0.3.0", false},        // unparseable latest -> never nag
	}
	for _, c := range cases {
		if got := IsNewer(c.latest, c.current); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}
