package dindcache

import "testing"

func TestValidName(t *testing.T) {
	ok := []string{"pg-16", "seeded_db", "abc123", "a", repeat("x", 64)}
	for _, n := range ok {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false, want true", n)
		}
	}
	bad := []string{"", "has spaces", "slash/name", "dot.name", "up$per", repeat("x", 65)}
	for _, n := range bad {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true, want false", n)
		}
	}
}

func TestVolumeNameRoundTrip(t *testing.T) {
	if got := VolumeName("pg-16"); got != "corral-dind-cache-pg-16" {
		t.Errorf("VolumeName = %q", got)
	}
	if got := nameFromVolume("corral-dind-cache-pg-16"); got != "pg-16" {
		t.Errorf("nameFromVolume = %q", got)
	}
	// A per-workspace volume must not read back as a cache.
	if got := nameFromVolume("corral-dind-abc123"); got != "" {
		t.Errorf("nameFromVolume(project vol) = %q, want empty", got)
	}
}

func TestParseHumanSize(t *testing.T) {
	cases := map[string]int64{
		"0B":      0,
		"512B":    512,
		"1.5kB":   1500,
		"234.5MB": 234_500_000,
		"2GB":     2_000_000_000,
		"":        0,
		"garbage": 0,
	}
	for in, want := range cases {
		if got := parseHumanSize(in); got != want {
			t.Errorf("parseHumanSize(%q) = %d, want %d", in, got, want)
		}
	}
}

// repeat returns s repeated n times (tiny helper for length-boundary names).
func repeat(s string, n int) string {
	out := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
