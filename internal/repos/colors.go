package repos

import (
	"crypto/sha1"
	"fmt"
)

// defaultPalette is the fixed sequence of label colors: the 1st repo gets
// palette[0], the 2nd palette[1], etc. Chosen to be distinct on the dark
// dashboard. Repos beyond the palette get a deterministic color derived from
// their id (colorFromID) so it's stable and spread out.
var defaultPalette = []string{
	"#e8833a", // orange
	"#a05cff", // purple
	"#37b7c3", // teal
	"#e05c8a", // pink
	"#5ce68a", // green
	"#e6c34a", // gold
	"#5c8aff", // blue
	"#e6605c", // red
	"#8ad35c", // lime
	"#c37ad3", // orchid
	"#4ac3a0", // seafoam
	"#d38a5c", // clay
	"#7a9de6", // periwinkle
	"#d3c15c", // mustard
	"#5cd3c1", // aqua
}

// defaultColorForIndex returns the palette color for a repo at add-order index
// i (0-based); index >= len(palette) falls back to a hashed color.
func defaultColorForIndex(i int, id string) string {
	if i >= 0 && i < len(defaultPalette) {
		return defaultPalette[i]
	}
	return colorFromID(id)
}

// colorFromID derives a stable, reasonably-saturated hex color from a repo id
// (used for repos past the fixed palette). Hue from the id hash; fixed S/L for
// legibility on dark.
func colorFromID(id string) string {
	h := sha1.Sum([]byte(id))
	hue := (int(h[0])<<8 | int(h[1])) % 360
	r, g, b := hslToRGB(float64(hue), 0.55, 0.62)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func hslToRGB(h, s, l float64) (int, int, int) {
	c := (1 - abs(2*l-1)) * s
	x := c * (1 - abs(mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return int((r + m) * 255), int((g + m) * 255), int((b + m) * 255)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
func mod(a, b float64) float64 {
	for a >= b {
		a -= b
	}
	return a
}

// SetColor sets a repo's label color (hex) and persists it.
func SetColor(id, color string) error {
	reg, err := readRegistry()
	if err != nil {
		return err
	}
	for i := range reg.Repos {
		if reg.Repos[i].ID == id {
			reg.Repos[i].Color = color
			return writeRegistry(reg)
		}
	}
	return fmt.Errorf("repo not found: %s", id)
}

// backfillColors assigns a default palette color to any repo missing one and
// persists if anything changed. Called from List so every listed repo has a
// concrete color (assign-on-first-sight). Uses registry (add) order for the
// palette index so a repo's default is stable regardless of pin-sorting.
func backfillColors(reg *registry) bool {
	changed := false
	for i := range reg.Repos {
		if reg.Repos[i].Color == "" {
			reg.Repos[i].Color = defaultColorForIndex(i, reg.Repos[i].ID)
			changed = true
		}
	}
	return changed
}
