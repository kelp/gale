package index

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kelp/gale/internal/gitutil"
)

const (
	pinA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pinB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestDefaultBaseURLHasNoMain(t *testing.T) {
	if strings.Contains(DefaultBaseURL, "/main") {
		t.Errorf("DefaultBaseURL = %q, must not include /main", DefaultBaseURL)
	}
}

type indexHTTP struct {
	mu          sync.Mutex
	hits        int
	paths       []string
	commit      string
	files       map[string]string
	etag        string
	status      int
	notModified int
	ifNone      []string
}

func (h *indexHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.hits++
	h.paths = append(h.paths, r.URL.Path)
	h.ifNone = append(h.ifNone, r.Header.Get("If-None-Match"))
	status := h.status
	body, ok := h.files[r.URL.Path]
	etag := h.etag
	h.mu.Unlock()

	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			h.mu.Lock()
			h.notModified++
			h.mu.Unlock()
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = w.Write([]byte(body))
}

func (h *indexHTTP) nHits() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits
}

func startIndex(t *testing.T, h *indexHTTP) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func remoteSrc(base, commit string) Source {
	return Source{BaseURL: base, Commit: commit}
}

func TestOpenGetJust(t *testing.T) {
	h := &indexHTTP{
		commit: pinA,
		files: map[string]string{
			"/" + pinA + "/index/j/just.toml": goldenTOML(),
		},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if sess.Commit != pinA {
		t.Errorf("Commit = %q, want pin A", sess.Commit)
	}
	f, err := sess.Get(t.Context(), "just")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.Package.Name != "just" || f.Package.Latest != "1.56.0" {
		t.Errorf("file = %+v", f.Package)
	}
}

func TestOpenTipPinsCommit(t *testing.T) {
	h := &indexHTTP{
		commit: pinA,
		files: map[string]string{
			"/" + pinA + "/index/j/just.toml": goldenTOML(),
			"/" + pinB + "/index/j/just.toml": "should-not-be-read",
		},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), Source{
		BaseURL: srv.URL,
		Tip:     func(context.Context) (string, error) { return pinA, nil },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.mu.Lock()
	h.commit = pinB
	h.mu.Unlock()
	if _, err := sess.Get(t.Context(), "just"); err != nil {
		t.Fatalf("Get after tip move: %v", err)
	}
	for _, p := range h.paths {
		if strings.Contains(p, pinB) {
			t.Errorf("request used moved tip: %s", p)
		}
	}
}

func TestGetKeepsPinnedCommit(t *testing.T) {
	h := &indexHTTP{
		commit: pinA,
		files: map[string]string{
			"/" + pinA + "/index/j/just.toml": goldenTOML(),
		},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Get(t.Context(), "just"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.commit = pinB
	h.mu.Unlock()
	if _, err := sess.Get(t.Context(), "just"); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	for _, p := range h.paths {
		if strings.Contains(p, pinB) {
			t.Errorf("second Get used new main: %s", p)
		}
	}
}

func TestGetETagThen304(t *testing.T) {
	h := &indexHTTP{
		commit: pinA,
		etag:   `"just-1"`,
		files: map[string]string{
			"/" + pinA + "/index/j/just.toml": goldenTOML(),
		},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Get(t.Context(), "just"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Get(t.Context(), "just"); err != nil {
		t.Fatalf("304 Get: %v", err)
	}
	if h.nHits() != 2 {
		t.Errorf("hits = %d, want 2", h.nHits())
	}
	if h.notModified != 1 {
		t.Errorf("304s = %d, want 1; If-None-Match=%q etags-sent=%q",
			h.notModified, h.ifNone, h.etag)
	}
}

func TestGetDoesNotServeStaleOn500(t *testing.T) {
	h := &indexHTTP{
		commit: pinA,
		etag:   `"just-1"`,
		files: map[string]string{
			"/" + pinA + "/index/j/just.toml": goldenTOML(),
		},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Get(t.Context(), "just"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.status = http.StatusInternalServerError
	h.mu.Unlock()
	if _, err := sess.Get(t.Context(), "just"); err == nil {
		t.Fatal("500 after cache must error")
	}
}

func TestGet404Then200(t *testing.T) {
	h := &indexHTTP{commit: pinA, files: map[string]string{}}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = sess.Get(t.Context(), "just")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: %v, want ErrNotFound", err)
	}
	h.mu.Lock()
	h.files["/"+pinA+"/index/j/just.toml"] = goldenTOML()
	h.mu.Unlock()
	if _, err := sess.Get(t.Context(), "just"); err != nil {
		t.Fatalf("Get after publish: %v", err)
	}
}

func TestGetCanceled(t *testing.T) {
	h := &indexHTTP{
		commit: pinA,
		files:  map[string]string{"/" + pinA + "/index/j/just.toml": goldenTOML()},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := sess.Get(ctx, "just"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Get: %v", err)
	}
}

func TestGetIgnoresRegistryCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cache := filepath.Join(home, ".gale", "cache", "registry")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &indexHTTP{commit: pinA, files: map[string]string{}}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Get(t.Context(), "just"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get: %v, want ErrNotFound (not a planted cache hit)", err)
	}
}

func TestInvalidNameAndCommitSkipHTTP(t *testing.T) {
	h := &indexHTTP{commit: pinA, files: map[string]string{}}
	srv := startIndex(t, h)
	for _, commit := range []string{"main", "deadbeef", "../x"} {
		_, err := Open(t.Context(), remoteSrc(srv.URL, commit))
		if err == nil {
			t.Errorf("Open(%q) succeeded", commit)
		}
	}
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"JQ", "../x", "jq/x", ""} {
		if _, err := sess.Get(t.Context(), name); err == nil {
			t.Errorf("Get(%q) succeeded", name)
		}
	}
	if h.nHits() != 0 {
		t.Errorf("hits = %d, want 0", h.nHits())
	}
}

func TestGetRejectsNameMismatch(t *testing.T) {
	body := strings.Replace(goldenTOML(), `name = "just"`, `name = "other"`, 1)
	h := &indexHTTP{
		commit: pinA,
		files:  map[string]string{"/" + pinA + "/index/j/just.toml": body},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Get(t.Context(), "just"); err == nil {
		t.Fatal("mismatched package.name must error")
	}
}

func TestGetRejectsLintFailure(t *testing.T) {
	body := strings.Replace(goldenTOML(), `latest = "1.56.0"`, `latest = "9.9.9"`, 1)
	h := &indexHTTP{
		commit: pinA,
		files:  map[string]string{"/" + pinA + "/index/j/just.toml": body},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Get(t.Context(), "just"); err == nil {
		t.Fatal("dangling latest must error")
	}
}

func TestResolveLatestAndVersion(t *testing.T) {
	h := &indexHTTP{
		commit: pinA,
		files:  map[string]string{"/" + pinA + "/index/j/just.toml": goldenTOML()},
	}
	srv := startIndex(t, h)
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	ver, art, err := sess.Resolve(t.Context(), "just", "")
	if err != nil {
		t.Fatalf("Resolve latest: %v", err)
	}
	if ver != "1.56.0" {
		t.Errorf("latest = %q, want 1.56.0", ver)
	}
	if _, ok := art.Artifacts["darwin/arm64"]; !ok {
		t.Fatal("latest missing darwin/arm64")
	}
	ver, art, err = sess.Resolve(t.Context(), "just", "1.56.0")
	if err != nil {
		t.Fatalf("Resolve explicit: %v", err)
	}
	if ver != "1.56.0" || len(art.Artifacts) != 1 {
		t.Errorf("explicit = %s %+v", ver, art.Artifacts)
	}
	if _, _, err := sess.Resolve(t.Context(), "just", "0.0.1"); err == nil {
		t.Fatal("missing version must error")
	}
}

func TestResolveDownServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	sess, err := Open(t.Context(), remoteSrc(srv.URL, pinA))
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if _, _, err := sess.Resolve(t.Context(), "just", ""); err == nil {
		t.Fatal("down server must error")
	}
}

func TestLocalDirPinsGitShow(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "Test")
	git("config", "commit.gpgsign", "false")
	path := filepath.Join(dir, "index", "j", "just.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(goldenTOML()), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "index/j/just.toml")
	git("commit", "-m", "index")
	want, err := gitutil.Head(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := Open(t.Context(), Source{Dir: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if sess.Commit != want {
		t.Errorf("Commit = %q, want %q", sess.Commit, want)
	}
	if _, err := sess.Get(t.Context(), "just"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := os.WriteFile(path, []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := sess.Get(t.Context(), "just")
	if err != nil {
		t.Fatalf("Get after dirty: %v", err)
	}
	if f.Package.Latest != "1.56.0" {
		t.Errorf("dirty tree leaked: %+v", f.Package)
	}
}
