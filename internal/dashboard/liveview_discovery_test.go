package dashboard

import (
	"os"
	"reflect"
	"testing"
)

// TestLivePortsDiscoveryAgainstContainer runs discovery against a live container
// (opt-in). Start a server on some port inside CORRAL_LIVE_TEST_CONTAINER first;
// it should appear, and the corral-internal ports (3128/3129/53) should not.
func TestLivePortsDiscoveryAgainstContainer(t *testing.T) {
	container := os.Getenv("CORRAL_LIVE_TEST_CONTAINER")
	if container == "" {
		t.Skip("set CORRAL_LIVE_TEST_CONTAINER to run")
	}
	ports := discoverListeningPorts(container)
	t.Logf("discovered ports: %v", ports)
	for _, p := range ports {
		if liveInternalPorts[p] {
			t.Errorf("internal port %d should have been filtered out", p)
		}
	}
}

// TestLiveInternalPortsFiltered is a pure check that the filter set is applied —
// it doesn't touch docker.
func TestLiveInternalPortsFiltered(t *testing.T) {
	// Simulate what discoverListeningPorts does with a fixed ss-like input by
	// re-implementing the filter inline over a known set.
	in := []int{53, 3000, 3128, 3129, 5173}
	var got []int
	for _, p := range in {
		if liveInternalPorts[p] {
			continue
		}
		got = append(got, p)
	}
	want := []int{3000, 5173}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter = %v, want %v", got, want)
	}
}
