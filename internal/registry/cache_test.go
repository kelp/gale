package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// countingHandler wraps a handler and tracks request-level detail
// (path hit count, last If-None-Match header seen per path).
type countingHandler struct {
	mu        sync.Mutex
	hits      map[string]int
	lastINM   map[string]string
	responder http.HandlerFunc
}

func newCountingHandler(responder http.HandlerFunc) *countingHandler {
	return &countingHandler{
		hits:      map[string]int{},
		lastINM:   map[string]string{},
		responder: responder,
	}
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.hits[r.URL.Path]++
	c.lastINM[r.URL.Path] = r.Header.Get("If-None-Match")
	c.mu.Unlock()
	c.responder(w, r)
}

func (c *countingHandler) hitCount(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

func (c *countingHandler) lastIfNoneMatch(path string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastINM[path]
}

// cachedTestRegistry returns a Registry that caches into a
// temp dir scoped to the test.
func cachedTestRegistry(t *testing.T, baseURL string) *Registry {
	t.Helper()
	return &Registry{BaseURL: baseURL, CacheDir: t.TempDir()}
}

// etagHandler serves `body` with an ETag computed from the body
// contents. Honors If-None-Match with a 304. The body stored in
// the handler is mutable via setBody.
type etagHandler struct {
	mu   sync.Mutex
	body []byte
	etag string
}

func newETagHandler(body string) *etagHandler {
	h := &etagHandler{}
	h.setBody(body)
	return h
}

func (h *etagHandler) setBody(body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.body = []byte(body)
	sum := sha256.Sum256(h.body)
	h.etag = `"` + hex.EncodeToString(sum[:]) + `"`
}

func (h *etagHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	body := append([]byte(nil), h.body...)
	etag := h.etag
	h.mu.Unlock()

	w.Header().Set("ETag", etag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write(body)
}

// --- M7 Behavior 1: first fetch populates the cache ---

func TestFetchRecipePopulatesCacheOnFirstFetch(t *testing.T) {
	eh := newETagHandler(validTOML)
	srv := httptest.NewServer(eh)
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// Cache layout: <CacheDir>/registry/<hash>/{body,etag}.
	// Walk the tree and confirm both files landed.
	var sawBody, sawEtag bool
	root := filepath.Join(reg.CacheDir, "registry")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		switch filepath.Base(path) {
		case "body":
			sawBody = true
		case "etag":
			sawEtag = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cache: %v", err)
	}
	if !sawBody {
		t.Error("expected cached body file under registry/<hash>/body")
	}
	if !sawEtag {
		t.Error("expected cached etag file under registry/<hash>/etag")
	}
}

// --- M7 Behavior 2: second fetch sends If-None-Match and
// returns cached body on 304 ---

func TestFetchRecipeHonorsIfNoneMatchOn304(t *testing.T) {
	eh := newETagHandler(validTOML)
	ch := newCountingHandler(eh.ServeHTTP)
	srv := httptest.NewServer(ch)
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)
	// First fetch populates the cache.
	rec1, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	// Second fetch must send If-None-Match and still return the
	// same recipe content.
	rec2, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	if ch.hitCount("/recipes/t/testpkg.toml") != 2 {
		t.Errorf("expected 2 hits, got %d",
			ch.hitCount("/recipes/t/testpkg.toml"))
	}
	if got := ch.lastIfNoneMatch("/recipes/t/testpkg.toml"); got == "" {
		t.Errorf("expected If-None-Match on second fetch, got %q",
			got)
	}
	if rec1.Package.Name != rec2.Package.Name {
		t.Errorf("recipe name mismatch: %q vs %q",
			rec1.Package.Name, rec2.Package.Name)
	}
	if rec2.Package.Name != "testpkg" {
		t.Errorf("got %q, want testpkg", rec2.Package.Name)
	}
}

// --- M7 Behavior 3: mutated server content → 200, new cache ---

func TestFetchRecipeRefreshesCacheOnChangedContent(t *testing.T) {
	eh := newETagHandler(validTOML)
	srv := httptest.NewServer(eh)
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)
	rec1, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if rec1.Package.Version != "1.0.0" {
		t.Fatalf("sanity: got %q", rec1.Package.Version)
	}

	updated := strings.Replace(validTOML,
		`version = "1.0.0"`, `version = "2.0.0"`, 1)
	eh.setBody(updated)

	rec2, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if rec2.Package.Version != "2.0.0" {
		t.Errorf("got version %q, want %q",
			rec2.Package.Version, "2.0.0")
	}
}

// --- M7 Behavior 4: server returns no ETag → still works
// (body returned, nothing cached, or cached without revalidation).
// This guards against a crash if a CDN drops the header. ---

func TestFetchRecipeWithoutETagStillWorks(t *testing.T) {
	var recipeHits int32
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// No ETag header set on either path.
			switch r.URL.Path {
			case "/recipes/t/testpkg.toml":
				atomic.AddInt32(&recipeHits, 1)
				fmt.Fprint(w, validTOML)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	// Two calls, no cacheable validator → both go to the wire.
	if atomic.LoadInt32(&recipeHits) != 2 {
		t.Errorf("expected 2 recipe hits without ETag, got %d",
			atomic.LoadInt32(&recipeHits))
	}
}

// --- M7 Behavior 5: cached body stale but ETag still matches
// ought to be impossible; test that the 304 path delivers the
// cached body bytes verbatim. Guards against accidentally
// returning something other than the cached body. ---

func TestFetchRecipe304ReturnsExactCachedBody(t *testing.T) {
	eh := newETagHandler(validTOML)
	srv := httptest.NewServer(eh)
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)
	rec, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if rec.Package.Name != "testpkg" {
		t.Fatalf("sanity: %q", rec.Package.Name)
	}

	// Corrupt the cached body to prove the second fetch reads
	// from the cache file, not memory. After a 304 the recipe
	// should parse from the corrupt bytes and fail.
	root := filepath.Join(reg.CacheDir, "registry")
	var bodyPath string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Base(path) == "body" {
			bodyPath = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if bodyPath == "" {
		t.Fatal("no cached body found")
	}
	if err := os.WriteFile(bodyPath, []byte("[[[ not toml"), 0o644); err != nil {
		t.Fatalf("corrupt cache: %v", err)
	}

	_, err = reg.FetchRecipe("testpkg")
	if err == nil {
		t.Fatal("expected parse error from corrupted cache, got nil — 304 path must return cached bytes verbatim")
	}
}

// --- M7 Behavior 6: cacheKey is stable across runs ---

func TestCacheKeyStableForSameURL(t *testing.T) {
	a := cacheKey("https://raw.githubusercontent.com/kelp/gale-recipes/main/recipes/j/jq.toml")
	b := cacheKey("https://raw.githubusercontent.com/kelp/gale-recipes/main/recipes/j/jq.toml")
	if a == "" {
		t.Fatal("empty cache key")
	}
	if a != b {
		t.Errorf("unstable: %q vs %q", a, b)
	}
}

func TestCacheKeyDiffersForDifferentURLs(t *testing.T) {
	a := cacheKey("https://example.com/foo")
	b := cacheKey("https://example.com/bar")
	if a == b {
		t.Errorf("collision: both produced %q", a)
	}
}

// --- M7 Behavior 7: binaries fetch uses cache too ---

func TestFetchBinariesUsesCache(t *testing.T) {
	eh := newETagHandler(binariesToml)
	ch := newCountingHandler(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/recipes/j/jq.toml":
			fmt.Fprint(w, recipeNoBinaries)
		case "/recipes/j/jq.binaries.toml":
			eh.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(ch)
	defer srv.Close()

	reg := cachedTestRegistry(t, srv.URL)
	if _, err := reg.FetchRecipe("jq"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := reg.FetchRecipe("jq"); err != nil {
		t.Fatalf("second: %v", err)
	}
	// Both fetches happen (with If-None-Match the second time),
	// but the cache must exist so we can check for the file.
	if ch.lastIfNoneMatch("/recipes/j/jq.binaries.toml") == "" {
		t.Errorf("expected If-None-Match on .binaries.toml refetch")
	}
}

// --- M7 Behavior 8: empty CacheDir disables the cache ---

func TestEmptyCacheDirSkipsCache(t *testing.T) {
	eh := newETagHandler(validTOML)
	ch := newCountingHandler(eh.ServeHTTP)
	srv := httptest.NewServer(ch)
	defer srv.Close()

	// No CacheDir set — nothing should be written, every call
	// should go to the network.
	reg := &Registry{BaseURL: srv.URL}
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := reg.FetchRecipe("testpkg"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := ch.lastIfNoneMatch("/recipes/t/testpkg.toml"); got != "" {
		t.Errorf("expected no If-None-Match when cache disabled, got %q",
			got)
	}
}
