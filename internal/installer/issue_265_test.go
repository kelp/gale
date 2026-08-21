package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/recipemeta"
	"github.com/kelp/gale/internal/store"
)

// gh#265: a working-tree recipe whose content changed must not
// return MethodCached for an occupied store directory built from
// different recipe bytes. Registry recipes keep today's IsInstalled
// cache. A digest miss must rebuild via the staged path, not extract
// into the live canonical dir.

func TestInstallWorkingTreeRecipeEditRebuilds(t *testing.T) {
	oneTar := createTestTarZstd(t, "bin/tool", "one")
	twoTar := createTestTarZstd(t, "bin/tool", "two")
	oneHash := hashFile(t, oneTar)
	twoHash := hashFile(t, twoTar)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/one.tar.zst":
				http.ServeFile(w, r, oneTar)
			case "/two.tar.zst":
				http.ServeFile(w, r, twoTar)
			default:
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	first := workingTreeBinaryRecipe(t, wtBinary{
		url: srv.URL + "/one.tar.zst", hash: oneHash, body: []byte("recipe-one"),
	})
	if _, err := inst.Install(context.Background(), first); err != nil {
		t.Fatalf("first install: %v", err)
	}
	assertInstalledMarker(t, storeRoot, "tool", "1.0-1", "one")

	second := workingTreeBinaryRecipe(t, wtBinary{
		url: srv.URL + "/two.tar.zst", hash: twoHash, body: []byte("recipe-two"),
	})
	result, err := inst.Install(context.Background(), second)
	if err != nil {
		t.Fatalf("second install after recipe edit: %v", err)
	}
	if result.Method == MethodCached {
		t.Fatal("method = cached after editing a working-tree recipe")
	}
	assertInstalledMarker(t, storeRoot, "tool", "1.0-1", "two")
}

func TestInstallWorkingTreeUnchangedCaches(t *testing.T) {
	tarzst := createTestTarZstd(t, "bin/tool", "one")
	hash := hashFile(t, tarzst)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits++
			http.ServeFile(w, r, tarzst)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}
	body := []byte("same-recipe")
	r := workingTreeBinaryRecipe(t, wtBinary{
		url: srv.URL + "/tool.tar.zst", hash: hash, body: body,
	})
	if _, err := inst.Install(context.Background(), r); err != nil {
		t.Fatalf("first install: %v", err)
	}
	afterFirst := hits

	again := workingTreeBinaryRecipe(t, wtBinary{
		url: srv.URL + "/tool.tar.zst", hash: hash, body: body,
	})
	result, err := inst.Install(context.Background(), again)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if result.Method != MethodCached {
		t.Errorf("method = %q, want cached for an unchanged working-tree recipe",
			result.Method)
	}
	if hits != afterFirst {
		t.Errorf("unchanged recipe fetched %d more times, want a cache hit",
			hits-afterFirst)
	}
}

func TestInstallRegistryIgnoresRecipeDigest(t *testing.T) {
	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)
	if _, err := s.Create("tool", "1.0-1"); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(storeRoot, "tool", "1.0-1", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "tool"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	inst := &Installer{Store: s}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Source:  recipe.Source{URL: "http://should-not-be-called", SHA256: "bad"},
		Digest:  "different-from-anything-on-disk",
	}
	result, err := inst.Install(context.Background(), r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method != MethodCached {
		t.Errorf("method = %q, want cached: registry recipes do not key the cache on digest",
			result.Method)
	}
}

func TestInstallWorkingTreeMissingSidecarRebuilds(t *testing.T) {
	tarzst := createTestTarZstd(t, "bin/tool", "new")
	hash := hashFile(t, tarzst)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, tarzst)
		},
	))
	defer srv.Close()

	storeRoot := t.TempDir()
	s := store.NewStore(storeRoot)
	if _, err := s.Create("tool", "1.0-1"); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(storeRoot, "tool", "1.0-1", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "tool"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	inst := &Installer{Store: s}
	r := workingTreeBinaryRecipe(t, wtBinary{
		url: srv.URL + "/tool.tar.zst", hash: hash, body: []byte("recipe"),
	})
	result, err := inst.Install(context.Background(), r)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Method == MethodCached {
		t.Fatal("method = cached for a working-tree recipe with no sidecar; " +
			"missing metadata must rebuild")
	}
	assertInstalledMarker(t, storeRoot, "tool", "1.0-1", "new")
}

func TestIsStaleWorkingTreeDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := depsmeta.Write(dir, depsmeta.Metadata{}); err != nil {
		t.Fatal(err)
	}
	if err := recipemeta.Write(dir, recipemeta.Metadata{Digest: "old"}); err != nil {
		t.Fatal(err)
	}
	r := &recipe.Recipe{
		Package:         recipe.Package{Name: "mypkg", Version: "1.0.0"},
		FromWorkingTree: true,
		Digest:          "new",
	}
	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolverFor(nil)})
	if err != nil {
		t.Fatalf("IsStale: %v", err)
	}
	if !stale {
		t.Error("IsStale = false, want true when a working-tree recipe digest changed")
	}
}

func TestIsStaleWorkingTreeMatchingDigestNotStale(t *testing.T) {
	dir := t.TempDir()
	if err := depsmeta.Write(dir, depsmeta.Metadata{}); err != nil {
		t.Fatal(err)
	}
	if err := recipemeta.Write(dir, recipemeta.Metadata{Digest: "same"}); err != nil {
		t.Fatal(err)
	}
	r := &recipe.Recipe{
		Package:         recipe.Package{Name: "mypkg", Version: "1.0.0"},
		FromWorkingTree: true,
		Digest:          "same",
	}
	stale, err := IsStale(context.Background(), StaleQuery{StoreDir: dir, Recipe: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Resolver: resolverFor(nil)})
	if err != nil {
		t.Fatalf("IsStale: %v", err)
	}
	if stale {
		t.Error("IsStale = true, want false when the working-tree digest matches")
	}
}

type wtBinary struct {
	url, hash string
	body      []byte
}

func workingTreeBinaryRecipe(t *testing.T, spec wtBinary) *recipe.Recipe {
	t.Helper()
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL:    spec.url,
				SHA256: spec.hash,
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}
	r.MarkWorkingTree(spec.body)
	return r
}

func assertInstalledMarker(t *testing.T, storeRoot, name, version, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(storeRoot, name, version, "bin", name))
	if err != nil {
		t.Fatalf("read store binary: %v", err)
	}
	if string(got) != want {
		t.Errorf("store binary = %q, want %q", got, want)
	}
}

func TestWorkingTreeRecipeStale(t *testing.T) {
	dir := t.TempDir()
	r := &recipe.Recipe{FromWorkingTree: true, Digest: "abc"}
	if !workingTreeRecipeStale(dir, r) {
		t.Error("missing sidecar must be stale for a working-tree recipe")
	}
	if err := recipemeta.Write(dir, recipemeta.Metadata{Digest: "abc"}); err != nil {
		t.Fatal(err)
	}
	if workingTreeRecipeStale(dir, r) {
		t.Error("matching digest must not be stale")
	}
	r.Digest = "def"
	if !workingTreeRecipeStale(dir, r) {
		t.Error("mismatched digest must be stale")
	}
	registry := &recipe.Recipe{Digest: "def"}
	if workingTreeRecipeStale(dir, registry) {
		t.Error("registry recipes must not be stale on digest")
	}
	if workingTreeRecipeStale(dir, nil) {
		t.Error("nil recipe must not be stale")
	}
}
