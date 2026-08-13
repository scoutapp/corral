package repos

import (
	"path/filepath"
	"testing"
)

func TestPinSortsToTop(t *testing.T) {
	t.Setenv("CORRAL_HOME", t.TempDir())
	// Seed a registry directly (avoid network clone).
	reg := &registry{Repos: []Repo{
		{ID: "a", Name: "alpha"},
		{ID: "b", Name: "bravo"},
		{ID: "c", Name: "charlie"},
	}}
	if err := writeRegistry(reg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Ensure it wrote where List reads.
	_ = filepath.Base(registryPath())

	if err := SetPinned("b", true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}
	list, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list[0].ID != "b" || !list[0].Pinned {
		t.Errorf("pinned repo 'b' should sort first, got %+v", list)
	}
	// Unpinned keep registry order after the pinned one.
	if list[1].ID != "a" || list[2].ID != "c" {
		t.Errorf("unpinned order not stable: %v", []string{list[1].ID, list[2].ID})
	}

	// Unpin restores order.
	SetPinned("b", false)
	list, _ = List()
	if list[0].ID != "a" {
		t.Errorf("after unpin expected registry order, got %s first", list[0].ID)
	}
}
