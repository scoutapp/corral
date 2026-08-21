package dashboard

import (
	"testing"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/dindcache"
)

// TestResolveRepoCacheRef covers the auto-attach rule: attach the repo baseline
// only when everything lines up, copy by default, shared on request, and nil
// (today's empty-volume behavior) in every opt-out / missing case.
func TestResolveRepoCacheRef(t *testing.T) {
	present := func(string) bool { return true }
	absent := func(string) bool { return false }

	t.Run("attaches SHARED by default (mount, zero copy — the fast path)", func(t *testing.T) {
		ref := resolveRepoCacheRef(repoCacheDecision{
			dind: true, globalOn: true, primaryRepoID: "abc123", cacheExists: present,
		})
		if ref == nil {
			t.Fatal("expected a ref")
		}
		if ref.Name != dindcache.RepoCacheName("abc123") {
			t.Fatalf("ref.Name = %q", ref.Name)
		}
		if ref.Mode != config.DindCacheModeShared {
			t.Fatalf("default mode = %q, want shared", ref.Mode)
		}
	})

	t.Run("copy only when explicitly requested (the fork-and-promote path)", func(t *testing.T) {
		ref := resolveRepoCacheRef(repoCacheDecision{
			dind: true, globalOn: true, primaryRepoID: "abc123",
			requestedMode: config.DindCacheModeCopy, cacheExists: present,
		})
		if ref == nil || ref.Mode != config.DindCacheModeCopy {
			t.Fatalf("explicit copy must be honored, got %+v", ref)
		}
	})

	t.Run("explicit shared also honored", func(t *testing.T) {
		ref := resolveRepoCacheRef(repoCacheDecision{
			dind: true, globalOn: true, primaryRepoID: "abc123",
			requestedMode: config.DindCacheModeShared, cacheExists: present,
		})
		if ref == nil || ref.Mode != config.DindCacheModeShared {
			t.Fatalf("want shared ref, got %+v", ref)
		}
	})

	// Each of these must yield nil (fall back to an empty volume).
	nilCases := []struct {
		name string
		d    repoCacheDecision
	}{
		{"dind off", repoCacheDecision{dind: false, globalOn: true, primaryRepoID: "abc", cacheExists: present}},
		{"opted out", repoCacheDecision{dind: true, noRepoCache: true, globalOn: true, primaryRepoID: "abc", cacheExists: present}},
		{"global off", repoCacheDecision{dind: true, globalOn: false, primaryRepoID: "abc", cacheExists: present}},
		{"no primary repo", repoCacheDecision{dind: true, globalOn: true, primaryRepoID: "", cacheExists: present}},
		{"cache absent", repoCacheDecision{dind: true, globalOn: true, primaryRepoID: "abc", cacheExists: absent}},
		{"nil cacheExists", repoCacheDecision{dind: true, globalOn: true, primaryRepoID: "abc", cacheExists: nil}},
	}
	for _, c := range nilCases {
		t.Run("nil: "+c.name, func(t *testing.T) {
			if ref := resolveRepoCacheRef(c.d); ref != nil {
				t.Fatalf("expected nil ref, got %+v", ref)
			}
		})
	}
}

// TestRepoCacheName covers the repo→cache-name mapping and detection.
func TestRepoCacheName(t *testing.T) {
	name := dindcache.RepoCacheName("3315137c0903")
	if name != "repo-3315137c0903" {
		t.Fatalf("RepoCacheName = %q", name)
	}
	if !dindcache.IsRepoCache(name) {
		t.Fatalf("IsRepoCache(%q) = false", name)
	}
	if dindcache.IsRepoCache("my-hand-named-cache") {
		t.Fatal("a hand-named cache must not read as a repo cache")
	}
	// The derived name must be a valid cache slug.
	if !dindcache.ValidName(name) {
		t.Fatalf("derived repo cache name %q is not a valid slug", name)
	}
}
