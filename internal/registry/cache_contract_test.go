package registry

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// --- Negative cache (F-4): 404 responses are cached so repeated
// lookups for packages absent from the registry don't re-hit the
// network on every read-only command invocation.

// notFoundHandler always returns 404. Tracks request hits.
type notFoundHandler struct {
	hits int32
}

func (h *notFoundHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	atomic.AddInt32(&h.hits, 1)
	http.Error(w, "not found", http.StatusNotFound)
}

func (h *notFoundHandler) count() int32 {
	return atomic.LoadInt32(&h.hits)
}

// TestNegativeCachePersists404 verifies that a 404 from origin is
// recorded on disk and that subsequent fetches within the TTL serve
// the cached negative result without hitting the network.
func TestNegativeCachePersists404(t *testing.T) {
	h := &notFoundHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)

	// First call: HTTP 404 wired through as an error.
	_, err := reg.FetchRecipe("ghost")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if h.count() != 1 {
		t.Fatalf("first fetch: hits=%d, want 1", h.count())
	}

	// Verify the on-disk marker exists.
	entryDir := filepath.Join(reg.CacheDir, "registry",
		cacheKey(srv.URL+"/recipes/g/ghost.toml"))
	markerPath := filepath.Join(entryDir, "not_found")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("not_found marker should exist after 404: %v", err)
	}

	// Subsequent calls within TTL must NOT hit the network.
	for i := 0; i < 3; i++ {
		_, err := reg.FetchRecipe("ghost")
		if err == nil {
			t.Fatalf("call %d: expected error, got nil", i)
		}
		if !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("call %d: error should mention HTTP 404: %v", i, err)
		}
	}
	if h.count() != 1 {
		t.Errorf("hits=%d, want 1 (negative cache should suppress repeats)",
			h.count())
	}
}

// TestNegativeCacheExpiresAfterTTL verifies that a stale negative
// cache entry is pruned and the network is consulted again.
func TestNegativeCacheExpiresAfterTTL(t *testing.T) {
	h := &notFoundHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)

	if _, err := reg.FetchRecipe("ghost"); err == nil {
		t.Fatal("expected 404 error")
	}
	if h.count() != 1 {
		t.Fatalf("populate: hits=%d, want 1", h.count())
	}

	// Backdate the marker beyond the TTL.
	entryDir := filepath.Join(reg.CacheDir, "registry",
		cacheKey(srv.URL+"/recipes/g/ghost.toml"))
	markerPath := filepath.Join(entryDir, "not_found")
	stale := time.Now().Add(-2 * negativeCacheTTL).Format(time.RFC3339Nano)
	if err := os.WriteFile(markerPath, []byte(stale), 0o644); err != nil {
		t.Fatalf("backdate marker: %v", err)
	}

	if _, err := reg.FetchRecipe("ghost"); err == nil {
		t.Fatal("expected 404 error after TTL expiry")
	}
	if h.count() != 2 {
		t.Errorf("hits=%d, want 2 (stale negative cache should be re-checked)",
			h.count())
	}

	// The lazy prune should have rewritten the marker with a fresh
	// timestamp.
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("marker should still exist after refresh: %v", err)
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("marker timestamp unreadable: %v", err)
	}
	if time.Since(ts) > time.Minute {
		t.Errorf("marker timestamp is stale: %v", ts)
	}
}

// TestDryRunSuppressesNegativeCacheWrite verifies that
// --dry-run leaves the negative cache empty, mirroring the
// positive-cache contract from RO-B+C.
func TestDryRunSuppressesNegativeCacheWrite(t *testing.T) {
	h := &notFoundHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	reg := &Registry{
		BaseURL:  srv.URL,
		CacheDir: t.TempDir(),
		DryRun:   true,
	}
	if _, err := reg.FetchRecipe("ghost"); err == nil {
		t.Fatal("expected 404 error")
	}

	// The not_found marker must not exist.
	entryDir := filepath.Join(reg.CacheDir, "registry",
		cacheKey(srv.URL+"/recipes/g/ghost.toml"))
	markerPath := filepath.Join(entryDir, "not_found")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Errorf("DryRun=true should not write not_found marker, "+
			"stat=%v", err)
	}
}

// TestOfflineServesFreshNegativeCache verifies that GALE_OFFLINE=1
// honours a fresh negative cache entry and returns the 404-style
// error without making any HTTP request.
func TestOfflineServesFreshNegativeCache(t *testing.T) {
	h := &notFoundHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cacheDir := t.TempDir()

	// Populate negative cache online.
	reg := &Registry{BaseURL: srv.URL, CacheDir: cacheDir}
	if _, err := reg.FetchRecipe("ghost"); err == nil {
		t.Fatal("expected 404 error")
	}
	hitsBefore := h.count()

	// Now offline — must serve cached negative without network.
	regOff := &Registry{
		BaseURL:  srv.URL,
		CacheDir: cacheDir,
		Offline:  true,
	}
	_, err := regOff.FetchRecipe("ghost")
	if err == nil {
		t.Fatal("expected error from cached 404 in offline mode")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("offline negative-cache hit should surface HTTP 404: %v",
			err)
	}
	if h.count() != hitsBefore {
		t.Errorf("offline mode hit network: before=%d after=%d",
			hitsBefore, h.count())
	}
}

// TestOfflineWithStaleNegativeCacheErrors verifies that an expired
// negative cache entry in offline mode falls through to the same
// "no cached entry" error path as a missing positive cache.
func TestOfflineWithStaleNegativeCacheErrors(t *testing.T) {
	h := &notFoundHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cacheDir := t.TempDir()

	reg := &Registry{BaseURL: srv.URL, CacheDir: cacheDir}
	if _, err := reg.FetchRecipe("ghost"); err == nil {
		t.Fatal("expected 404 error")
	}

	// Backdate the marker.
	entryDir := filepath.Join(cacheDir, "registry",
		cacheKey(srv.URL+"/recipes/g/ghost.toml"))
	markerPath := filepath.Join(entryDir, "not_found")
	stale := time.Now().Add(-2 * negativeCacheTTL).Format(time.RFC3339Nano)
	if err := os.WriteFile(markerPath, []byte(stale), 0o644); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	regOff := &Registry{
		BaseURL:  srv.URL,
		CacheDir: cacheDir,
		Offline:  true,
	}
	_, err := regOff.FetchRecipe("ghost")
	if err == nil {
		t.Fatal("expected offline error for stale negative cache")
	}
	if !strings.Contains(err.Error(), "offline") &&
		!strings.Contains(err.Error(), "Offline") &&
		!strings.Contains(err.Error(), "GALE_OFFLINE") {
		t.Errorf("expected offline-style error, got: %v", err)
	}
}

// TestStaleOnErrorServesCachedNegative verifies that when the
// network fails after a cached 404, the cached 404 is served
// rather than surfacing the transport error.
func TestStaleOnErrorServesCachedNegative(t *testing.T) {
	h := &notFoundHandler{}
	srv := httptest.NewServer(h)

	cacheDir := t.TempDir()
	reg := &Registry{BaseURL: srv.URL, CacheDir: cacheDir}
	if _, err := reg.FetchRecipe("ghost"); err == nil {
		t.Fatal("expected 404 error")
	}

	// Kill the server. Next fetch must serve cached 404.
	srv.Close()

	_, err := reg.FetchRecipe("ghost")
	if err == nil {
		t.Fatal("expected error from stale-on-error negative cache")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("stale-on-error negative cache should surface HTTP 404, "+
			"got: %v", err)
	}
}
