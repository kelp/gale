package registry

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestDryRunSuppressesCacheWrites pins finding RO-B/0004:
// when Registry.DryRun is set, a successful 200 must not
// persist body/etag files under CacheDir. The body is still
// returned to the caller, but the on-disk side effect is gone.
func TestDryRunSuppressesCacheWrites(t *testing.T) {
	eh := newETagHandler(validTOML)
	srv := httptest.NewServer(eh)
	defer srv.Close()

	reg := &Registry{
		BaseURL:  srv.URL,
		CacheDir: t.TempDir(),
		DryRun:   true,
	}
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// Walk the cache root — no body/etag files should exist.
	root := filepath.Join(reg.CacheDir, "registry")
	var found []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // walk best-effort; missing dir means no writes
		}
		if info.IsDir() {
			return nil
		}
		found = append(found, path)
		return nil
	})
	if len(found) != 0 {
		t.Errorf("DryRun=true should leave cache empty, found: %v",
			found)
	}
}

// TestOfflineSkipsNetworkAndReturnsCachedBody pins
// finding RO-B/0005 and network-perf/0002 partially:
// when Registry.Offline is set and a cached body exists,
// cachedGet must serve it WITHOUT making any HTTP request.
func TestOfflineSkipsNetworkAndReturnsCachedBody(t *testing.T) {
	eh := newETagHandler(validTOML)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			eh.ServeHTTP(w, r)
		}))
	defer srv.Close()

	cacheDir := t.TempDir()
	// Populate the cache (online).
	reg := &Registry{BaseURL: srv.URL, CacheDir: cacheDir}
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("populate: %v", err)
	}
	hitsBefore := atomic.LoadInt32(&hits)
	if hitsBefore == 0 {
		t.Fatal("sanity: populate should have hit server")
	}

	// Now in offline mode — must NOT make another HTTP request,
	// must return the cached body.
	regOff := &Registry{
		BaseURL:  srv.URL,
		CacheDir: cacheDir,
		Offline:  true,
	}
	rec, err := regOff.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("offline fetch with warm cache: %v", err)
	}
	if rec.Package.Name != "testpkg" {
		t.Errorf("got %q, want testpkg", rec.Package.Name)
	}
	if got := atomic.LoadInt32(&hits); got != hitsBefore {
		t.Errorf("offline mode should not hit server: hits before=%d after=%d",
			hitsBefore, got)
	}
}

// TestOfflineWithoutCacheErrors verifies that Offline mode
// without a cached entry returns a clear error rather than
// hitting the network.
func TestOfflineWithoutCacheErrors(t *testing.T) {
	// Closed server — any HTTP request would fail loudly.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("offline mode must not make HTTP requests: %s",
				r.URL.Path)
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:  srv.URL,
		CacheDir: t.TempDir(),
		Offline:  true,
	}
	_, err := reg.FetchRecipe("testpkg")
	if err == nil {
		t.Fatal("expected error when offline and no cache entry")
	}
	if !strings.Contains(err.Error(), "offline") &&
		!strings.Contains(err.Error(), "Offline") &&
		!strings.Contains(err.Error(), "GALE_OFFLINE") {
		t.Errorf("error message should mention offline mode: %v", err)
	}
}

// TestStaleOnErrorServesCachedBody pins network-perf/0002:
// when the network errors out (connection refused, DNS, etc.)
// and a cached body exists, we serve it.
func TestStaleOnErrorServesCachedBody(t *testing.T) {
	eh := newETagHandler(validTOML)
	srv := httptest.NewServer(eh)

	cacheDir := t.TempDir()
	// Populate cache against working server.
	reg := &Registry{BaseURL: srv.URL, CacheDir: cacheDir}
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("populate: %v", err)
	}

	// Kill the server. Subsequent fetch must use the cache.
	srv.Close()

	rec, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("expected stale-on-error to serve cached body, got: %v",
			err)
	}
	if rec.Package.Name != "testpkg" {
		t.Errorf("got %q, want testpkg", rec.Package.Name)
	}
}

// TestStaleOnErrorDoesNotWriteCache verifies that a
// stale-served body does not corrupt or re-touch the cache
// entry. The on-disk body and etag must remain exactly as
// they were before the error path ran.
func TestStaleOnErrorDoesNotWriteCache(t *testing.T) {
	eh := newETagHandler(validTOML)
	srv := httptest.NewServer(eh)

	cacheDir := t.TempDir()
	reg := &Registry{BaseURL: srv.URL, CacheDir: cacheDir}
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("populate: %v", err)
	}

	// Snapshot etag modtime.
	root := filepath.Join(cacheDir, "registry")
	var etagPath string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Base(path) == "etag" {
			etagPath = path
		}
		return nil
	})
	if etagPath == "" {
		t.Fatal("no etag file written")
	}
	before, err := os.Stat(etagPath)
	if err != nil {
		t.Fatal(err)
	}

	srv.Close()
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("stale-on-error: %v", err)
	}

	after, err := os.Stat(etagPath)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("etag rewritten during stale-on-error path: "+
			"before=%v after=%v", before.ModTime(), after.ModTime())
	}
}

// TestSearchUsesCache pins network-perf/0001: repeated Search
// calls must send If-None-Match and serve from cache on 304.
func TestSearchUsesCache(t *testing.T) {
	const index = "jq\tJSON processor\n" +
		"ripgrep\tSearch tool\n"
	eh := newETagHandler(index)
	ch := newCountingHandler(eh.ServeHTTP)
	srv := httptest.NewServer(ch)
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)
	if _, err := reg.Search("jq"); err != nil {
		t.Fatalf("first search: %v", err)
	}
	if _, err := reg.Search("jq"); err != nil {
		t.Fatalf("second search: %v", err)
	}
	if got := ch.lastIfNoneMatch("/index.tsv"); got == "" {
		t.Errorf("expected If-None-Match on second search, got %q",
			got)
	}
}

// TestSearchStaleOnError verifies search serves from cache
// when the network fails after the index has been cached.
func TestSearchStaleOnError(t *testing.T) {
	const index = "jq\tJSON processor\n"
	eh := newETagHandler(index)
	srv := httptest.NewServer(eh)

	reg := cachedTestRegistry(t, srv.URL)
	if _, err := reg.Search("jq"); err != nil {
		t.Fatalf("populate: %v", err)
	}
	srv.Close()

	results, err := reg.Search("jq")
	if err != nil {
		t.Fatalf("expected stale-on-error to serve cached index, got: %v",
			err)
	}
	if len(results) == 0 || results[0].Name != "jq" {
		t.Errorf("got results %v, want jq first", results)
	}
}

// TestNewRespectsGaleOfflineEnv pins that the package-level
// constructor honours GALE_OFFLINE=1 by setting Offline.
func TestNewRespectsGaleOfflineEnv(t *testing.T) {
	t.Setenv("GALE_OFFLINE", "1")
	reg := New()
	if !reg.Offline {
		t.Error("New() should set Offline=true when GALE_OFFLINE=1")
	}
}

