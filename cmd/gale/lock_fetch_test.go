package main

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

const (
	lockFetchPinA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	lockFetchPinB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	lockFetchSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	lockFetchTree = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type lockFetchHTTP struct {
	mu    sync.Mutex
	paths []string
	files map[string]string
}

func (h *lockFetchHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.paths = append(h.paths, r.URL.Path)
	body, ok := h.files[r.URL.Path]
	h.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(body))
}

func (h *lockFetchHTTP) saw(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.paths {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

type lockFetchFix struct {
	t    *testing.T
	home string
	root string
	c    *cmdContext
	toml []byte
	prev int
	h    *lockFetchHTTP
	src  index.Source
}

func newLockFetchFix(t *testing.T) *lockFetchFix {
	t.Helper()
	p := newProjectLayout(t)
	toml := []byte("[packages]\njust = \"1.56.0\"\nfd = \"10.2.0\"\n")
	if err := os.WriteFile(p.configPath, toml, 0o644); err != nil {
		t.Fatal(err)
	}
	seedStore(t, p.storeRoot, "seed", "1")
	if err := generation.Build(
		map[string]string{"seed": "1"}, p.galeDir, p.storeRoot,
	); err != nil {
		t.Fatalf("seed generation: %v", err)
	}

	h := &lockFetchHTTP{files: map[string]string{
		"/" + lockFetchPinA + "/index/j/just.toml": lockIndexTOML("just", "1.56.0"),
		"/" + lockFetchPinA + "/index/f/fd.toml":   lockIndexTOML("fd", "10.2.0"),
		"/" + lockFetchPinB + "/index/j/just.toml": lockIndexTOML("just", "9.9.9"),
		"/" + lockFetchPinB + "/index/f/fd.toml":   lockIndexTOML("fd", "9.9.9"),
	}}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &lockFetchFix{
		t:    t,
		home: p.home,
		root: p.root,
		c: &cmdContext{
			GalePath:  p.configPath,
			GaleDir:   p.galeDir,
			StoreRoot: p.storeRoot,
		},
		toml: toml,
		prev: currentGen(t, p.galeDir),
		h:    h,
		src:  index.Source{BaseURL: srv.URL, Commit: lockFetchPinA},
	}
}

func (fx *lockFetchFix) req(roots ...string) lockFetch {
	return lockFetch{Source: fx.src, Roots: roots}
}

func (fx *lockFetchFix) lockPath() string {
	fx.t.Helper()
	lp, err := lockfilePath(fx.c.GalePath)
	if err != nil {
		fx.t.Fatal(err)
	}
	return lp
}

func lockIndexTOML(name, version string) string {
	return `[package]
name = "` + name + `"
description = "test package"
license = "MIT"
homepage = "https://github.com/kelp/` + name + `"
repo = "kelp/` + name + `"
latest = "` + version + `"

[versions."` + version + `".artifacts."darwin/arm64"]
url = "https://github.com/kelp/` + name + `/releases/download/` + version + `/` + name + `.tar.gz"
format = "tar.gz"
sha256 = "` + lockFetchSHA + `"
tree_digest = "` + lockFetchTree + `"
hash_source = "upstream-sha256sums"
strip = 1
attestation = true

[[versions."` + version + `".artifacts."darwin/arm64".files]]
src = "` + name + `"
dest = "bin/` + name + `"
mode = 0o755

[versions."` + version + `".artifacts."linux/amd64"]
url = "https://github.com/kelp/` + name + `/releases/download/` + version + `/` + name + `-linux.tar.gz"
format = "tar.gz"
sha256 = "` + lockFetchSHA + `"
tree_digest = "` + lockFetchTree + `"
hash_source = "upstream-sha256sums"
strip = 1

[[versions."` + version + `".artifacts."linux/amd64".files]]
src = "` + name + `"
dest = "bin/` + name + `"
mode = 0o755
`
}

func TestRunLockFetchPinsOneIndexCommit(t *testing.T) {
	fx := newLockFetchFix(t)
	if err := runLockFetch(context.Background(), fx.c, fx.req(
		"just@1.56.0", "fd@10.2.0",
	)); err != nil {
		t.Fatalf("runLockFetch: %v", err)
	}

	got, err := lockfile.ReadV2(fx.lockPath())
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	if v, err := lockfile.Load(fx.lockPath()); err != nil || v.Kind != lockfile.KindV2 {
		t.Errorf("Load = (%v, %v), want KindV2", v, err)
	}
	if len(got.Packages) != 2 {
		t.Fatalf("packages = %d, want 2", len(got.Packages))
	}
	for key, pkg := range got.Packages {
		if len(pkg.Artifacts) == 0 {
			t.Errorf("%s: no artifacts", key)
		}
		for plat, art := range pkg.Artifacts {
			if art.IndexCommit != lockFetchPinA {
				t.Errorf("%s %s index_commit = %q, want pin A", key, plat, art.IndexCommit)
			}
			if art.Method != provenance.MethodFetch {
				t.Errorf("%s %s method = %q, want fetch", key, plat, art.Method)
			}
			if art.URL == "" {
				t.Errorf("%s %s missing url", key, plat)
			}
		}
	}
	just := got.Packages["just@1.56.0"].Artifacts["darwin/arm64"]
	if just.Attestation == nil {
		t.Error("just darwin attestation: want presence")
	}
	fd := got.Packages["fd@10.2.0"].Artifacts["linux/amd64"]
	if fd.Attestation != nil {
		t.Error("fd linux attestation: want absent")
	}
	if fx.h.saw("github.com") || fx.h.saw(".tar.gz") {
		t.Error("artifact URL was requested")
	}
	if destExists(t, fx.c.StoreRoot, "just", "1.56.0", lockFetchSHA) {
		t.Error("fetch dest exists after lock-only write")
	}
	if currentGen(t, fx.c.GaleDir) != fx.prev {
		t.Errorf("current = %d, want %d", currentGen(t, fx.c.GaleDir), fx.prev)
	}
	gotTOML, err := os.ReadFile(fx.c.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotTOML) != string(fx.toml) {
		t.Errorf("gale.toml changed:\n%s", gotTOML)
	}
	if registryContains(t, fx.home, fx.root) {
		t.Error("lock-only write registered the project")
	}
}

func TestRunLockFetchIgnoresMovingTip(t *testing.T) {
	fx := newLockFetchFix(t)
	fx.src.Commit = ""
	fx.src.Tip = func(context.Context) (string, error) { return lockFetchPinA, nil }

	if err := runLockFetch(context.Background(), fx.c, lockFetch{
		Source: fx.src,
		Roots:  []string{"just@1.56.0", "fd@10.2.0"},
	}); err != nil {
		t.Fatalf("runLockFetch: %v", err)
	}
	if fx.h.saw(lockFetchPinB) {
		t.Error("resolve used a commit after Open")
	}
	got, err := lockfile.ReadV2(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	for key, pkg := range got.Packages {
		for plat, art := range pkg.Artifacts {
			if art.IndexCommit != lockFetchPinA {
				t.Errorf("%s %s index_commit = %q", key, plat, art.IndexCommit)
			}
		}
	}
}

func TestRunLockFetchSecondResolveLeavesLock(t *testing.T) {
	fx := newLockFetchFix(t)
	prior := []byte("prior-lock\n")
	if err := os.WriteFile(fx.lockPath(), prior, 0o644); err != nil {
		t.Fatal(err)
	}
	err := runLockFetch(context.Background(), fx.c, fx.req(
		"just@1.56.0", "missing@1.0.0",
	))
	if err == nil {
		t.Fatal("expected resolve error")
	}
	got, readErr := os.ReadFile(fx.lockPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(prior) {
		t.Errorf("lock changed after a failed resolve:\n%s", got)
	}
}

func TestRunLockFetchCanceledLeavesLock(t *testing.T) {
	fx := newLockFetchFix(t)
	prior := []byte("prior-lock\n")
	if err := os.WriteFile(fx.lockPath(), prior, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runLockFetch(ctx, fx.c, fx.req("just@1.56.0")); err == nil {
		t.Fatal("canceled context succeeded")
	}
	got, err := os.ReadFile(fx.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(prior) {
		t.Errorf("lock changed after cancel:\n%s", got)
	}
}

func TestRunLockFetchConflictingRoots(t *testing.T) {
	fx := newLockFetchFix(t)
	err := runLockFetch(context.Background(), fx.c, fx.req(
		"just@1.56.0", "just@1.57.0",
	))
	if !errors.Is(err, lockfile.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if _, err := os.Lstat(fx.lockPath()); !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
		data, _ := os.ReadFile(fx.lockPath())
		t.Errorf("conflicting roots wrote a lock:\n%s", data)
	}
	if fx.h.saw("/index/") {
		t.Error("index was fetched for a lock that cannot be written")
	}
}

func TestRunLockFetchMutationLockExclusive(t *testing.T) {
	fx := newLockFetchFix(t)
	held := make(chan struct{})
	release := make(chan struct{})
	holderErr := make(chan error, 1)
	go func() {
		holderErr <- filelock.With(mutateLockPath(fx.c.GaleDir), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-holderErr:
		t.Fatalf("holder returned early: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("did not acquire mutate.lock")
	}

	done := make(chan error, 1)
	go func() {
		done <- runLockFetch(context.Background(), fx.c, fx.req("just@1.56.0"))
	}()
	select {
	case <-time.After(50 * time.Millisecond):
	case err := <-done:
		t.Fatalf("runLockFetch finished while mutate.lock was held: %v", err)
	}

	close(release)
	if err := <-holderErr; err != nil {
		t.Fatalf("holder: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runLockFetch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runLockFetch did not finish after release")
	}
}

func destExists(t *testing.T, storeRoot, name, version, sha string) bool {
	t.Helper()
	ok, err := store.NewStore(storeRoot).FetchExists(name, version, sha)
	if err != nil {
		t.Fatalf("FetchExists: %v", err)
	}
	return ok
}
