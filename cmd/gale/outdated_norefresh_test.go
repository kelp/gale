package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kelp/gale/internal/registry"
)

// validOutdatedRecipeTOML is a minimal recipe served by the
// httptest server.  The version differs from the "installed"
// version so any successful resolve flags the package as
// outdated — useful to confirm both the network short-circuit
// AND a cache-hit happy path in the same test.
const validOutdatedRecipeTOML = `
[package]
name = "outpkg"
version = "1.2.0"
revision = 1
license = "MIT"

[source]
url = "https://example.invalid/outpkg-1.2.0.tar.gz"
sha256 = "0000000000000000000000000000000000000000000000000000000000000000"
`

// TestOutdatedNoRefreshSkipsRecipeFetch pins RO-K-1: under
// `--no-refresh` (or any equivalent "use cached recipes only"
// signal), `outdated` must NOT issue per-package HTTP requests
// when the cache is warm.  Prior behaviour only skipped the
// tap-refresh step; the per-recipe HTTP fetch still happened.
//
// The fix routes `--no-refresh` through to the registry's
// `Offline` flag so cachedGet serves the cached body and never
// touches the network.
func TestOutdatedNoRefreshSkipsRecipeFetch(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.Header().Set("ETag", `"abc"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(validOutdatedRecipeTOML))
		},
	))
	defer srv.Close()

	// Warm the cache against the live server.
	cacheDir := t.TempDir()
	reg := &registry.Registry{BaseURL: srv.URL, CacheDir: cacheDir}
	if _, err := reg.FetchRecipe("outpkg"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	hitsAfterWarm := atomic.LoadInt32(&hits)
	if hitsAfterWarm == 0 {
		t.Fatal("sanity: warming should have hit the server")
	}

	// Now apply the --no-refresh contract.  After
	// applyOutdatedNoRefresh sets reg.Offline, subsequent
	// resolves must not touch the network.
	applyOutdatedNoRefresh(reg, true)

	if !reg.Offline {
		t.Fatal("applyOutdatedNoRefresh did not set Offline=true")
	}

	if _, err := reg.FetchRecipe("outpkg"); err != nil {
		t.Fatalf("offline resolve with warm cache: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != hitsAfterWarm {
		t.Errorf("--no-refresh hit the server: before=%d after=%d",
			hitsAfterWarm, got)
	}
}

// TestOutdatedNoRefreshWithoutCacheSurfacesError verifies the
// `no cache, no network` corner. With `--no-refresh` and no
// cached entry, the resolver must error rather than silently
// falling through to the network.
func TestOutdatedNoRefreshWithoutCacheSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("--no-refresh must not hit the server: %s",
				r.URL.Path)
		},
	))
	defer srv.Close()

	reg := &registry.Registry{
		BaseURL:  srv.URL,
		CacheDir: t.TempDir(),
	}
	applyOutdatedNoRefresh(reg, true)

	_, err := reg.FetchRecipe("outpkg")
	if err == nil {
		t.Fatal("expected error from --no-refresh with cold cache")
	}
	if !strings.Contains(err.Error(), "offline") &&
		!strings.Contains(err.Error(), "GALE_OFFLINE") {
		t.Errorf("expected offline-style error, got: %v", err)
	}
}

// TestApplyOutdatedNoRefreshIsNoOpWhenFalse confirms the
// helper leaves Offline untouched when the flag is unset, so
// the default network behaviour stays intact.
func TestApplyOutdatedNoRefreshIsNoOpWhenFalse(t *testing.T) {
	reg := &registry.Registry{}
	applyOutdatedNoRefresh(reg, false)
	if reg.Offline {
		t.Error("Offline should remain false when noRefresh=false")
	}
}

// TestApplyOutdatedNoRefreshHandlesNil ensures the helper
// tolerates the `--recipes` mode where the resolver chain has
// no registry attached.  Outdated still calls the helper
// unconditionally; a nil registry must not panic.
func TestApplyOutdatedNoRefreshHandlesNil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("applyOutdatedNoRefresh(nil, true) panicked: %v", r)
		}
	}()
	applyOutdatedNoRefresh(nil, true)
}
