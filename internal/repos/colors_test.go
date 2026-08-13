package repos

import (
	"regexp"
	"testing"
)

var hexRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func TestDefaultColorByOrder(t *testing.T) {
	// First repos get fixed palette colors, in order.
	if defaultColorForIndex(0, "x") != defaultPalette[0] {
		t.Errorf("index 0 should be palette[0] (%s)", defaultPalette[0])
	}
	if defaultColorForIndex(1, "x") != defaultPalette[1] {
		t.Errorf("index 1 should be palette[1] (%s)", defaultPalette[1])
	}
	// Past the palette → hashed, stable, valid hex.
	c1 := defaultColorForIndex(99, "repo-abc")
	c2 := defaultColorForIndex(50, "repo-abc") // same id → same color regardless of index
	if c1 != c2 {
		t.Errorf("color past palette should depend on id only, got %s vs %s", c1, c2)
	}
	if !hexRe.MatchString(c1) {
		t.Errorf("hashed color not valid hex: %s", c1)
	}
	// Different ids → (very likely) different colors.
	if defaultColorForIndex(99, "aaa") == defaultColorForIndex(99, "zzz") {
		t.Error("expected different ids to hash to different colors")
	}
}

func TestBackfillAndSetColor(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	writeRegistry(&registry{Repos: []Repo{{ID: "a"}, {ID: "b"}, {ID: "c"}}})
	list, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range list {
		if r.Color == "" {
			t.Errorf("repo %s got no default color", r.ID)
		}
	}
	// Explicit set overrides + persists.
	if err := SetColor("b", "#123abc"); err != nil {
		t.Fatalf("SetColor: %v", err)
	}
	list, _ = List()
	for _, r := range list {
		if r.ID == "b" && r.Color != "#123abc" {
			t.Errorf("SetColor not persisted, got %s", r.Color)
		}
	}
}
