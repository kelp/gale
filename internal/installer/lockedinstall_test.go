package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/lockplan"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// sha64 expands a short label into a well-formed artifact hash. The
// persisted format is 64 lowercase hex digits, so a fixture cannot
// use a memorable stand-in and still pass validation.
func sha64(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

// planOf builds a one-node plan whose digest is computed by the real
// serializer rather than stamped. A stamped digest would let the
// comparator and the writer drift apart in exactly the way the
// reinstall-loop regressions did.
func planOf(t *testing.T, r *recipe.Recipe, method, sha string) *lockplan.Plan {
	t.Helper()
	key := lockgraph.Key(r.Package.Name, r.Package.Full())
	g := lockgraph.Node{
		Name:    r.Package.Name,
		Version: r.Package.Full(),
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Method:  method,
		SHA256:  sha,
	}
	digests, order, err := lockgraph.Closure(map[string]lockgraph.Node{key: g})
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	return &lockplan.Plan{
		Nodes: map[string]lockplan.Node{key: {
			Name:        r.Package.Name,
			Version:     r.Package.Full(),
			Method:      method,
			SHA256:      sha,
			GraphDigest: digests[key],
			Recipe:      r,
			Graph:       g,
		}},
		Order: order,
		Roots: []string{key},
	}
}

// serveFiles serves each named path from the file on disk it maps
// to. Two routes matter here: a WORKING source tarball and a binary
// blob that does not hash to what the lock names. Without a working
// source route the source-fallback prohibition is untestable — the
// fallback would fail on its own and every assertion would pass for
// the wrong reason.
func serveFiles(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			path, ok := routes[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, path)
		},
	))
	t.Cleanup(srv.Close)
	restore := download.SetHTTPClient(srv.Client())
	t.Cleanup(restore)
	return srv
}

func guardStoreRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// lockedBinaryRecipe is a recipe with a viable source path and a
// binary the lock names.
//
// witness is an absolute path OUTSIDE the store that the first build
// step touches, so a test can tell "never built" from "built and then
// discarded". Inside the store it could not: every path that rejects
// a source build also removes the store dir, so an in-store marker
// reads the same either way. It is not an HTTP hit counter either —
// build.fetchSource consults a source cache under the real $HOME, so
// a second run of the suite fetches nothing.
func lockedBinaryRecipe(srcURL, srcSHA, binURL, binSHA, witness string) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: srcURL, SHA256: srcSHA},
		Build: recipe.Build{Steps: []string{
			"echo built > " + witness,
			"mkdir -p $PREFIX/bin",
			"echo built > $PREFIX/bin/tool",
		}},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL:    binURL,
				SHA256: binSHA,
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}
}

// TestInstallLocked_CachedDirMustAttestTheLock is acceptance 2 and 3.
// An occupied canonical store dir is either already attesting the
// locked bytes, in which case the install is a cache hit, or it is a
// conflict. Design §4 permits committing only ABSENT canonical dirs,
// so there is no third branch: a locked install performs no stale
// replacement and leaves the directory byte-for-byte.
func TestInstallLocked_CachedDirMustAttestTheLock(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	canonical := filepath.Join(storeRoot, "testpkg", "1.0-1")
	if err := os.MkdirAll(filepath.Join(canonical, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(canonical, "bin", "tool")
	if err := os.WriteFile(kept, []byte("installed"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := lockedBinaryRecipe("http://unused", sha64("s"), "http://unused", sha64("a"), filepath.Join(t.TempDir(), "witness"))
	plan := planOf(t, r, lockgraph.MethodBinary, sha64("a"))
	node := plan.Nodes[lockgraph.Key("testpkg", "1.0-1")]
	inst := &Installer{Store: store.NewStore(storeRoot), Plan: plan}

	// No provenance at all: the lock names bytes nothing on disk
	// attests, which is a conflict, not a lesser state.
	_, err := inst.Install(context.Background(), r)
	if !errors.Is(err, provenance.ErrInvalid) {
		t.Fatalf("unprovenanced dir: err = %v, want provenance.ErrInvalid", err)
	}
	if got, rerr := os.ReadFile(kept); rerr != nil || string(got) != "installed" {
		t.Fatalf("dir mutated on refusal: %q, %v", got, rerr)
	}

	// Provenance that disagrees on the hash: still a conflict, and
	// still no replacement.
	wrong := node.Graph
	wrong.SHA256 = sha64("b")
	rec, err := provenance.New(storeRoot, wrong)
	if err != nil {
		t.Fatal(err)
	}
	if err := provenance.Write(canonical, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(context.Background(), r); !errors.Is(err, provenance.ErrInvalid) {
		t.Fatalf("conflicting provenance: err = %v, want provenance.ErrInvalid", err)
	}
	if got, rerr := os.ReadFile(kept); rerr != nil || string(got) != "installed" {
		t.Fatalf("dir mutated on refusal: %q, %v", got, rerr)
	}

	// Provenance that agrees: a cache hit, reported as such.
	rec, err = provenance.New(storeRoot, node.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := provenance.Write(canonical, rec); err != nil {
		t.Fatal(err)
	}
	res, err := inst.Install(context.Background(), r)
	if err != nil {
		t.Fatalf("agreeing provenance must be a cache hit: %v", err)
	}
	if res.Method != MethodCached {
		t.Errorf("method = %q, want %q", res.Method, MethodCached)
	}
}

// TestInstallLocked_UnplannedPackageRefused pins the contract that
// makes the lock the exclusive selector. A package the plan does not
// name has no locked version, hash or method, so installing it under
// a plan would silently reintroduce live resolution for that node.
func TestInstallLocked_UnplannedPackageRefused(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	r := lockedBinaryRecipe("http://unused", sha64("s"), "http://unused", sha64("a"), filepath.Join(t.TempDir(), "witness"))
	plan := planOf(t, r, lockgraph.MethodBinary, sha64("a"))

	other := &recipe.Recipe{
		Package: recipe.Package{Name: "elsewhere", Version: "2.0"},
		Build:   recipe.Build{Steps: []string{"true"}},
	}
	inst := &Installer{Store: store.NewStore(storeRoot), Plan: plan}
	_, err := inst.Install(context.Background(), other)
	if !errors.Is(err, ErrUnplanned) {
		t.Fatalf("err = %v, want ErrUnplanned", err)
	}
}

// planWith builds a plan over several nodes at once, computing every
// digest with the real serializer so a dependent's digest genuinely
// binds its dependency.
func planWith(t *testing.T, specs []planSpec) *lockplan.Plan {
	t.Helper()
	graph := make(map[string]lockgraph.Node, len(specs))
	for _, s := range specs {
		edges := make([]lockgraph.Edge, 0, len(s.runtimeDeps))
		for _, d := range s.runtimeDeps {
			name, version, _ := strings.Cut(d, "@")
			edges = append(edges, lockgraph.Edge{
				Kind: lockgraph.KindRuntime, Name: name, Version: version,
			})
		}
		graph[lockgraph.Key(s.name, s.version)] = lockgraph.Node{
			Name: s.name, Version: s.version,
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
			Method: s.method, SHA256: s.sha, Edges: edges,
		}
	}
	digests, order, err := lockgraph.Closure(graph)
	if err != nil {
		t.Fatalf("closure: %v", err)
	}
	nodes := make(map[string]lockplan.Node, len(specs))
	for _, s := range specs {
		key := lockgraph.Key(s.name, s.version)
		nodes[key] = lockplan.Node{
			Name: s.name, Version: s.version,
			Method: s.method, SHA256: s.sha,
			RuntimeDeps: s.runtimeDeps,
			GraphDigest: digests[key],
			Recipe:      s.recipe,
			Graph:       graph[key],
		}
	}
	return &lockplan.Plan{Nodes: nodes, Order: order}
}

type planSpec struct {
	name, version, method, sha string
	runtimeDeps                []string
	recipe                     *recipe.Recipe
}

// lockedDepRecipe is a recipe with only a binary artifact, for fixtures
// where the source path is irrelevant.
func lockedDepRecipe(pkg recipe.Package, url, sha string, deps []string) *recipe.Recipe {
	return &recipe.Recipe{
		Package:      pkg,
		Dependencies: recipe.Dependencies{Runtime: deps},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL: url, SHA256: sha, Trust: recipe.TrustSHA256Only,
			},
		},
	}
}

// TestInstallLocked_UnlockedSourcePathsRefuse closes the hole the
// pair review found in P7a.
//
// --path and --git bypass installLocked entirely, so they see no
// plan, get no checkSourceArtifact, and installLocalLocked calls
// replaceStoreDir under the store-gen lock — which design §4
// prohibits in-plan. Neither can be locked even in principle: a git
// install's identity is a commit hash and a --path install's bytes
// are whatever is in the working tree, so no lock can name either.
//
// The refusal is at the exported entry point, before the clone or
// the build, not inside the locked body: InstallGitWithFinalize
// clones and builds in installGitPrepare BEFORE it takes the
// package lock, so a guard further in would refuse only after
// minutes of work. The installer fails closed on its own rather
// than relying on no command ever wiring a plan into these.
func TestInstallLocked_UnlockedSourcePathsRefuse(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	r := lockedDepRecipe(
		recipe.Package{Name: "testpkg", Version: "1.0", Revision: 1},
		"http://unused", sha64("a"), nil,
	)
	inst := &Installer{
		Store: store.NewStore(storeRoot),
		Plan:  planOf(t, r, lockgraph.MethodBinary, sha64("a")),
	}

	// A source dir that would build if the guard let it through, so
	// a pass here means the guard fired rather than the fixture
	// failing on its own.
	sourceDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(sourceDir, "Makefile"), []byte("all:\n\t@true\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	_, err := inst.InstallLocalWithFinalize(r, sourceDir, nil)
	if !errors.Is(err, ErrSourceGone) {
		t.Errorf("--path under a plan: err = %v, want ErrSourceGone", err)
	}

	gitRecipe := lockedDepRecipe(
		recipe.Package{Name: "testpkg", Version: "1.0", Revision: 1},
		"http://unused", sha64("a"), nil,
	)
	gitRecipe.Source.Repo = "https://example.invalid/nope.git"
	_, err = inst.InstallGitWithFinalize(gitRecipe, nil)
	if !errors.Is(err, ErrSourceGone) {
		t.Errorf("--git under a plan: err = %v, want ErrSourceGone", err)
	}

	// Nothing was built or committed.
	if _, serr := os.Lstat(filepath.Join(storeRoot, "testpkg")); !errors.Is(
		serr, os.ErrNotExist,
	) {
		t.Errorf("refused install left store state behind: %v", serr)
	}
}
