package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// TestInstallLocked_MismatchLeavesStoreClean is acceptance 1 and 8
// together, because they are one scenario: the served bytes do not
// hash to what the lock names.
//
// Both arms run against ONE fixture, and that is the point. Unlocked,
// a failed binary fetch falls back to source by design, and the
// marker proves the fallback is genuinely reachable here. Locked, the
// method is a locked field, so the same failure must abort with the
// store clean: a source build would install bytes the lock never
// named and then fail its own hash check after minutes of work.
// Asserting only the locked arm would pass just as well against a
// fixture whose source build was broken anyway.
func TestInstallLocked_MismatchLeavesStoreClean(t *testing.T) {
	srcTar := createTestSourceTarGz(t)
	tarzst := createTestTarZstd(t, "bin/tool", "real")
	// Well-formed, so the install fails on the hash rather than on
	// extraction, which is the case the lock exists to catch.
	tampered := createTestTarZstd(t, "bin/tool", "tampered")
	srv := serveFiles(t, map[string]string{
		"/source.tar.gz":       srcTar,
		"/testpkg-1.0.tar.zst": tampered,
	})
	witnessDir := t.TempDir()
	newRecipe := func(witness string) *recipe.Recipe {
		return lockedBinaryRecipe(
			srv.URL+"/source.tar.gz", hashFile(t, srcTar),
			srv.URL+"/testpkg-1.0.tar.zst", hashFile(t, tarzst),
			filepath.Join(witnessDir, witness),
		)
	}

	// Control arm: unlocked, the same mismatch falls back to source
	// and installs. Issue #20's behaviour, preserved exactly.
	unlockedRoot := guardStoreRoot(t)
	unlocked := &Installer{
		Store:             store.NewStore(unlockedRoot),
		BinaryFallbackLog: io.Discard,
	}
	if _, err := unlocked.Install(newRecipe("unlocked")); err != nil {
		t.Fatalf("unlocked install must still fall back to source: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(witnessDir, "unlocked")); err != nil {
		t.Fatalf("fixture cannot reach a source build (%v), so the "+
			"locked arm below would prove nothing", err)
	}

	// Locked arm: the same fixture, refused.
	storeRoot := guardStoreRoot(t)
	r := newRecipe("locked")
	inst := &Installer{
		Store:             store.NewStore(storeRoot),
		BinaryFallbackLog: io.Discard,
		Plan:              planOf(t, r, lockgraph.MethodBinary, hashFile(t, tarzst)),
	}
	if _, err := inst.Install(r); err == nil {
		t.Fatal("locked install accepted an artifact the lock does not name")
	}

	canonical := filepath.Join(storeRoot, "testpkg", "1.0-1")
	if _, err := os.Lstat(canonical); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("store dir %s survives a mismatch (lstat err %v): the "+
			"mismatching artifact must never be committed", canonical, err)
	}
	// The store being clean is not enough. A fallback that ran and
	// was then caught by the source-artifact hash check would leave
	// the store just as clean, having burned a full build first —
	// which is the cost §4 rejects the fallback to avoid. The
	// prohibition has to bite before the build ever starts.
	if _, err := os.Lstat(filepath.Join(witnessDir, "locked")); err == nil {
		t.Error("locked binary node attempted a source build: the " +
			"locked method must be binding, not merely checked afterwards")
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
	_, err := inst.Install(r)
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
	if _, err := inst.Install(r); !errors.Is(err, provenance.ErrInvalid) {
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
	res, err := inst.Install(r)
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
	_, err := inst.Install(other)
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

// TestInstallLocked_TransitiveVersionsComeFromThePlan is acceptance 5.
// The registry resolver deliberately answers with a NEWER dependency
// than the lock names. Under a plan the locked version must win, and
// the newer one must never reach the store: dependency resolution is
// exactly where the pre-#182 installer let live recipes override a
// committed lock, because the lock was never threaded into it.
func TestInstallLocked_TransitiveVersionsComeFromThePlan(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	rootTar := createTestTarZstd(t, "bin/root", "root")
	depTar := createTestTarZstd(t, "bin/dep", "dep-1.0")
	newerTar := createTestTarZstd(t, "bin/dep", "dep-9.9")
	srv := serveFiles(t, map[string]string{
		"/root.tar.zst":  rootTar,
		"/dep.tar.zst":   depTar,
		"/newer.tar.zst": newerTar,
	})

	depRecipe := lockedDepRecipe(
		recipe.Package{Name: "dep", Version: "1.0", Revision: 1},
		srv.URL+"/dep.tar.zst", hashFile(t, depTar), nil,
	)
	rootRecipe := lockedDepRecipe(
		recipe.Package{Name: "root", Version: "1.0", Revision: 1},
		srv.URL+"/root.tar.zst", hashFile(t, rootTar), []string{"dep"},
	)

	// What the registry would say today: dep has moved on.
	newer := lockedDepRecipe(
		recipe.Package{Name: "dep", Version: "9.9", Revision: 1},
		srv.URL+"/newer.tar.zst", hashFile(t, newerTar), nil,
	)

	inst := &Installer{
		Store: store.NewStore(storeRoot),
		// Answers only for "dep", and answers with a NEWER version.
		// Anything else reaching the registry under a plan is itself
		// the defect this test is about.
		Resolver: func(name string) (*recipe.Recipe, error) {
			if name != "dep" {
				return nil, fmt.Errorf(
					"registry consulted for %q under a lock", name,
				)
			}
			return newer, nil
		},
		Plan: planWith(t, []planSpec{
			{
				name: "root", version: "1.0-1",
				method: lockgraph.MethodBinary, sha: hashFile(t, rootTar),
				runtimeDeps: []string{"dep@1.0-1"}, recipe: rootRecipe,
			},
			{
				name: "dep", version: "1.0-1",
				method: lockgraph.MethodBinary, sha: hashFile(t, depTar),
				recipe: depRecipe,
			},
		}),
	}

	if _, err := inst.Install(rootRecipe); err != nil {
		t.Fatalf("locked install: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(storeRoot, "dep", "1.0-1")); err != nil {
		t.Errorf("locked dep version absent from the store: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(storeRoot, "dep", "9.9-1")); err == nil {
		t.Error("the resolver's newer dep reached the store: under a " +
			"lock the recipe must never select a version")
	}
}

// TestInstallLocked_SourceSHAMismatchFails is acceptance 10. A source
// build whose finished archive does not hash to the locked artifact
// is refused BEFORE promotion, so the store dir never appears. The
// comparison is on the build archive, never a rehash of the store
// directory: fixupExtracted rewrites Mach-O load commands in staging,
// so post-fixup bytes can never match an artifact SHA.
func TestInstallLocked_SourceSHAMismatchFails(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	srcTar := createTestSourceTarGz(t)
	srv := serveFiles(t, map[string]string{"/source.tar.gz": srcTar})

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source: recipe.Source{
			URL: srv.URL + "/source.tar.gz", SHA256: hashFile(t, srcTar),
		},
		Build: recipe.Build{Steps: []string{
			"mkdir -p $PREFIX/bin",
			"echo built > $PREFIX/bin/tool",
		}},
	}
	// A source build's output hash is not knowable in advance, so the
	// fixture locks a hash it cannot produce. That is the real shape
	// of the failure: a machine that does not reproduce the build.
	inst := &Installer{
		Store: store.NewStore(storeRoot),
		Plan:  planOf(t, r, lockgraph.MethodSource, sha64("not what this builds")),
	}

	_, err := inst.Install(r)
	if !errors.Is(err, provenance.ErrInvalid) {
		t.Fatalf("err = %v, want provenance.ErrInvalid", err)
	}
	canonical := filepath.Join(storeRoot, "testpkg", "1.0-1")
	if _, serr := os.Lstat(canonical); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("store dir %s survives a source SHA mismatch (%v): the "+
			"check must run before promotion, not after", canonical, serr)
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
	if !errors.Is(err, ErrUnlockedSource) {
		t.Errorf("--path under a plan: err = %v, want ErrUnlockedSource", err)
	}

	gitRecipe := lockedDepRecipe(
		recipe.Package{Name: "testpkg", Version: "1.0", Revision: 1},
		"http://unused", sha64("a"), nil,
	)
	gitRecipe.Source.Repo = "https://example.invalid/nope.git"
	_, err = inst.InstallGitWithFinalize(gitRecipe, nil)
	if !errors.Is(err, ErrUnlockedSource) {
		t.Errorf("--git under a plan: err = %v, want ErrUnlockedSource", err)
	}

	// Nothing was built or committed.
	if _, serr := os.Lstat(filepath.Join(storeRoot, "testpkg")); !errors.Is(
		serr, os.ErrNotExist,
	) {
		t.Errorf("refused install left store state behind: %v", serr)
	}
}
