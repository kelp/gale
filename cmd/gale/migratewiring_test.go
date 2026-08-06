package main

import (
	"archive/tar"
	"bytes"
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

	"github.com/klauspost/compress/zstd"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/recipe"
)

// Tests in this file drive migrate's PRODUCTION callers — migrateOne
// and runMigrate — rather than the safety mechanisms those callers
// wire up.
//
// Each mechanism already has a unit test, and every one of them passed
// with its call site deleted: a unit test proves the function decides
// correctly, never that anything asks it. The class of defect that
// leaves behind is the one that costs bytes: a refactor moves the
// ReplaceGuard assignment, or drops migratePreflight in favour of the
// per-commit check that "already covers it", and the store is
// converged by a command that has stopped consulting the machine.
//
// So the assertions here are about observable side effects, not only
// about which error came back. Several of these mechanisms are
// backstopped by another one that returns the SAME error later, after
// the work they exist to prevent has happened.

// migrateMachine is one machine as migrate sees it: an isolated HOME,
// the gale dir that is both galeHome and the global scope, the store
// under it, and a cmdContext whose installer really installs.
//
// galeHome is the gale dir ITSELF, never a ".gale" nested inside it.
// runMigrate derives it as filepath.Dir(StoreRoot) and projects.Scopes
// calls exactly that directory the global scope, so a fixture that
// nests one builds a scope nothing ever looks at, and every cross-scope
// veto then passes by seeing nothing.
type migrateMachine struct {
	t         *testing.T
	galeHome  string
	storeRoot string
	ctx       *cmdContext
	// recipes is what the resolver answers from, so a fixture declares
	// a package by name and both the scan and the installer see the
	// same recipe.
	recipes map[string]*recipe.Recipe
}

func newMigrateMachine(t *testing.T) *migrateMachine {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeHome := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeHome, "pkg")
	m := &migrateMachine{
		t: t, galeHome: galeHome, storeRoot: storeRoot,
		recipes: map[string]*recipe.Recipe{},
	}
	m.ctx = buildFakeCtx(t, filepath.Join(galeHome, "gale.toml"),
		galeHome, storeRoot, m.resolve)
	return m
}

func (m *migrateMachine) resolve(name string) (*recipe.Recipe, error) {
	r, ok := m.recipes[name]
	if !ok {
		return nil, fmt.Errorf("no recipe declared for %s", name)
	}
	return r, nil
}

// binaryPkg declares a package whose prebuilt binary really downloads
// and installs, with the given runtime dependencies.
//
// A real archive rather than a stub result: migrate commits only what
// the installer produced, and every check between the fetch and the
// commit reads the staged directory, so a fixture that skips the
// install skips the code under test.
func (m *migrateMachine) binaryPkg(name string, deps ...string) *recipe.Recipe {
	m.t.Helper()
	r := &recipe.Recipe{
		Package:      recipe.Package{Name: name, Version: "1.0"},
		Dependencies: recipe.Dependencies{Runtime: deps},
		Binary: servedBinary(m.t, archiveEntry{
			name: "bin/" + name, body: "#!/bin/sh\n", mode: 0o755,
		}),
	}
	m.recipes[name] = r
	return r
}

// fallbackPkg declares a package whose binary fetch 404s and whose
// source build succeeds, which is the shape an unlocked install
// silently demotes: the artifact that lands is one nobody was cleared
// against.
func (m *migrateMachine) fallbackPkg(name string) *recipe.Recipe {
	t := m.t
	t.Helper()
	tarball, sum := sourceTarball(t, name)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/source.tar.gz" {
				http.ServeFile(w, r, tarball)
				return
			}
			http.NotFound(w, r)
		},
	))
	t.Cleanup(srv.Close)
	r := &recipe.Recipe{
		Package: recipe.Package{Name: name, Version: "1.0"},
		Source:  recipe.Source{URL: srv.URL + "/source.tar.gz", SHA256: sum},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL: srv.URL + "/missing.tar.zst", SHA256: shaX,
				Trust: recipe.TrustSHA256Only,
			},
		},
		Build: recipe.Build{Steps: []string{
			"mkdir -p $PREFIX/bin",
			"echo '#!/bin/sh' > $PREFIX/bin/" + name,
			"chmod +x $PREFIX/bin/" + name,
		}},
	}
	m.recipes[name] = r
	return r
}

// untouchableBinaryPkg declares a package whose prebuilt binary is
// served by an endpoint that fails the test if it is ever contacted,
// carrying the hash a seeded provenance record already names.
//
// The hash matters as much as the endpoint: a fixture whose recipe
// disagreed with the record on disk would take the resume branch away
// for a reason of its own making.
func (m *migrateMachine) untouchableBinaryPkg(name string) *recipe.Recipe {
	t := m.t
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			t.Errorf("%s was refetched; the canonical artifact already "+
				"attests itself, so there is nothing to install", name)
			w.WriteHeader(http.StatusNotFound)
		},
	))
	t.Cleanup(srv.Close)
	r := &recipe.Recipe{
		Package: recipe.Package{Name: name, Version: "1.0"},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL: srv.URL + "/artifact.tar.zst", SHA256: testSHA,
				Trust: recipe.TrustSHA256Only,
			},
		},
	}
	m.recipes[name] = r
	return r
}

// servedBinary serves a prebuilt archive from a local server and
// returns the platform binary map a recipe declares for it.
//
// The declared hash is the archive's own, so the installer's stream
// verification passes and the commit carries MethodBinary — the only
// method migrate may commit, and the one every check downstream
// compares against.
func servedBinary(t *testing.T, entries ...archiveEntry) map[string]recipe.Binary {
	t.Helper()
	archive, sum := tarZstArchive(t, entries...)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, archive)
		},
	))
	t.Cleanup(srv.Close)
	return map[string]recipe.Binary{
		runtime.GOOS + "-" + runtime.GOARCH: {
			URL: srv.URL + "/artifact.tar.zst", SHA256: sum,
			Trust: recipe.TrustSHA256Only,
		},
	}
}

// archiveEntry is one file inside a prebuilt-archive fixture. The mode
// is explicit because it decides what the extracted artifact is: a
// library the farm picks up, or a binary a generation links.
type archiveEntry struct {
	name string
	body string
	mode int64
}

// tarZstArchive writes a tar.zst holding entries and returns its path
// and hex SHA256, which is what the recipe declares and what the
// installer verifies the download against.
func tarZstArchive(t *testing.T, entries ...archiveEntry) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.tar.zst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: e.name,
			Mode: e.mode, Size: int64(len(e.body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []io.Closer{tw, zw, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path, hashOf(t, path)
}

// legacyMarker puts a recognisable file in a store directory, so a
// test can tell "left exactly as it was" from "replaced". A refusal
// that preserves the bytes is the whole promise these mechanisms make.
func legacyMarker(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "legacy-marker")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertMarkerKept fails when the marked directory was replaced, and
// says which promise that breaks.
func assertMarkerKept(t *testing.T, marker, why string) {
	t.Helper()
	kept, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("%s: %v", why, err)
	}
	if string(kept) != "old" {
		t.Errorf("%s: the marker holds %q, want %q", why, kept, "old")
	}
}

// recordedOutput captures what a command prints, for the assertions
// that are about the ORDER of the work rather than its outcome.
func recordedOutput() (*output.Output, *bytes.Buffer) {
	var buf bytes.Buffer
	return output.New(&buf, false), &buf
}

// The commit of a canonical candidate is put to the cross-scope veto.
//
// migrateOne installs into a directory the whole machine shares, so
// design §13 has it hand the installer a ReplaceGuard that
// re-establishes every scope's agreement at the moment the staged
// artifact would supersede the old one. Nothing else stands there for
// this shape: the preflight ran before the fetch, and a project
// registered or re-locked since then is invisible to it.
//
// Driven through migrateOne rather than through checkMigrateCommit,
// because the unit test on that function passes with the assignment
// deleted. Without the wiring the installer has no guard at all, so
// migrateOne returns success and the error assertion is what fires
// first; the marker records what that success cost, which is the part
// worth reading in the failure output.
func TestMigrateOneRefusesACommitAnotherScopeDisputes(t *testing.T) {
	m := newMigrateMachine(t)
	proj := t.TempDir()
	if err := projects.Register(m.galeHome, proj); err != nil {
		t.Fatal(err)
	}
	// The project requires other bytes at the identity being replaced.
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"disputed@1.0-1", shaY)

	dir := seedStore(t, m.storeRoot, "disputed", "1.0-1")
	marker := legacyMarker(t, dir)
	r := m.binaryPkg("disputed")

	_, err := migrateOne(m.ctx, m.galeHome, migrateTarget{
		name: "disputed", version: "1.0-1", dir: dir, recipe: r,
	}, discardOutput())
	if !errors.Is(err, errScopeDisagrees) {
		t.Fatalf("err = %v, want errScopeDisagrees", err)
	}
	assertMarkerKept(t, marker,
		"the commit replaced bytes a registered scope names differently")
}

// A relocation refuses to commit a source build in place of the
// binary migrate was cleared against.
//
// The RELOCATING shape is where this has to be said, and BinaryOnly is
// the only thing that says it. A pre-revision candidate's canonical
// directory is absent, so the installer's guard is skipped entirely
// (guardReplace returns early on an absent destination) and the
// unlocked fallback from a failed binary fetch to a source build
// commits an artifact no scope was asked about. What follows catches
// it too late: canonicalAttests reports the method afterwards, with
// the source-built directory already in the store.
//
// So the error is not the discriminator here — both paths refuse. The
// canonical directory is: with the wiring it never exists.
func TestMigrateOneRefusesToDemoteARelocationToSource(t *testing.T) {
	m := newMigrateMachine(t)
	bare := seedStore(t, m.storeRoot, "demote", "1.0")
	marker := legacyMarker(t, bare)
	r := m.fallbackPkg("demote")
	canonical := filepath.Join(m.storeRoot, "demote", "1.0-1")

	_, err := migrateOne(m.ctx, m.galeHome, migrateTarget{
		name: "demote", version: "1.0-1", dir: bare, bare: true, recipe: r,
	}, discardOutput())
	if err == nil {
		t.Fatal("a relocation demoted to a source build was accepted")
	}
	if _, statErr := os.Lstat(canonical); !os.IsNotExist(statErr) {
		t.Errorf("a source build landed in %s (%v); the machine was "+
			"cleared against the declared binary alone", canonical, statErr)
	}
	assertMarkerKept(t, marker,
		"a refused relocation destroyed the pre-revision directory")
}

// Every candidate is cleared with every scope BEFORE the first
// replacement.
//
// Design §13's second qualifying property, and the reason the pass is
// one machine-wide unit: a run that replaced half the store and then
// met a disagreement would leave the machine in a state neither the
// old nor the new description covers.
//
// The per-commit guard is not a substitute, and this fixture is shaped
// to show why. It returns the same errScopeDisagrees, so the error
// alone cannot tell the two apart — but it fires per candidate, so the
// undisputed one has already been replaced by the time the disputed
// one is examined. The surviving marker is the property; the error is
// only the entry condition.
func TestRunMigrateClearsEveryCandidateBeforeReplacingAny(t *testing.T) {
	m := newMigrateMachine(t)
	proj := t.TempDir()
	if err := projects.Register(m.galeHome, proj); err != nil {
		t.Fatal(err)
	}
	// The project disputes one candidate and says nothing about the
	// other. The scan sorts by name, so the undisputed one is reached
	// first and is what a per-commit-only check would destroy.
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"zdisputed@1.0-1", shaY)

	marker := legacyMarker(t, seedStore(t, m.storeRoot, "aagreed", "1.0-1"))
	seedStore(t, m.storeRoot, "zdisputed", "1.0-1")
	m.binaryPkg("aagreed")
	m.binaryPkg("zdisputed")

	err := runMigrate(m.ctx, discardOutput())
	if !errors.Is(err, errScopeDisagrees) {
		t.Fatalf("err = %v, want errScopeDisagrees", err)
	}
	assertMarkerKept(t, marker,
		"an undisputed candidate was replaced before the pass had "+
			"cleared every candidate with every scope")
}

// A candidate another candidate depends on is replaced first.
//
// Design §5 and §7 make the order load-bearing: provenance is
// all-or-nothing, so refetching a dependent while its dependency is
// still unprovenanced commits an artifact with no record at all.
// Alphabetical order decides whether the machine converges, and "app"
// sorts before the "zdep" it links.
//
// Driven through runMigrate, because orderCandidates' own tests pass
// with the call replaced by the scan's output. The printed sequence is
// the observable: it is written before each replacement, so it reports
// the order the pass actually took rather than an order recomputed
// afterwards.
func TestRunMigrateReplacesADependencyBeforeItsDependent(t *testing.T) {
	m := newMigrateMachine(t)
	seedStore(t, m.storeRoot, "app", "1.0-1")
	seedStore(t, m.storeRoot, "zdep", "1.0-1")
	m.binaryPkg("zdep")
	m.binaryPkg("app", "zdep")

	out, buf := recordedOutput()
	if err := runMigrate(m.ctx, out); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}

	dep := strings.Index(buf.String(), "zdep@1.0-1")
	dependent := strings.Index(buf.String(), "app@1.0-1")
	if dep < 0 || dependent < 0 {
		t.Fatalf("the pass did not migrate both candidates:\n%s", buf)
	}
	if dep > dependent {
		t.Errorf("app was migrated before the zdep it links:\n%s", buf)
	}
}

// A scope running a pre-revision generation is moved off the bare
// directory before that directory is deleted.
//
// This is the half of a relocation no per-scope command may perform,
// and the reason design §13 hands pre-revision directories to
// machine-wide migrate: another project's generation LINKS the bare
// path, and its owner ran nothing.
//
// The fixture builds the generation while only the bare directory
// exists, which is the whole point. A generation built after the
// canonical directory is in place already names the canonical path, so
// reinstalling repairs the link on its own and the regeneration this
// test exists for is never exercised.
//
// Without it the pass does not merely leave a stale link: the scope
// still reaches the bare directory, so removeRelocatedDir refuses, and
// the relocation cannot finish at all.
func TestFinishRelocationsMovesAScopeOffTheBareDir(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "pkg")
	proj := t.TempDir()
	if err := projects.Register(home, proj); err != nil {
		t.Fatal(err)
	}
	galeDir := filepath.Join(proj, ".gale")

	bare := seedStore(t, storeRoot, "legacy", "1.0")
	writeDepsMeta(t, storeRoot, "legacy", "1.0")
	if err := generation.Build(
		map[string]string{"legacy": "1.0"}, galeDir, storeRoot,
	); err != nil {
		t.Fatal(err)
	}
	if got := linkTarget(t, galeDir, "legacy"); !strings.Contains(
		got, filepath.Join("legacy", "1.0"),
	) {
		t.Fatalf("the fixture does not link the pre-revision dir: %s", got)
	}

	// The canonical artifact the pass has already installed and
	// verified, sitting beside it.
	seedProvenanced(t, storeRoot, "legacy", "1.0-1")
	writeDepsMeta(t, storeRoot, "legacy", "1.0-1")
	r, rerr := migrateResolver("legacy")("legacy", "1.0-1")
	if rerr != nil {
		t.Fatal(rerr)
	}

	if err := finishRelocations(
		&cmdContext{StoreRoot: storeRoot}, home,
		[]migrateTarget{{
			name: "legacy", version: "1.0-1", dir: bare, bare: true, recipe: r,
		}},
		discardOutput(),
	); err != nil {
		t.Fatalf("finishRelocations: %v", err)
	}

	if got := linkTarget(t, galeDir, "legacy"); !strings.Contains(
		got, filepath.Join("legacy", "1.0-1"),
	) {
		t.Errorf("the scope still links %s, want the canonical dir", got)
	}
	if _, err := os.Lstat(bare); !os.IsNotExist(err) {
		t.Errorf("the pre-revision dir survived the pass (%v)", err)
	}
}

// An interrupted relocation is resumed rather than reinstalled.
//
// The state is the one an earlier pass leaves behind when it installs
// and verifies the canonical artifact and stops before the bare
// directory goes away: an attested canonical directory beside an
// unprovenanced bare one. Reinstalling over it meets its own record
// and fails stillUnprovenanced, so without the resume branch one
// interrupted relocation is unrecoverable by the command that created
// it — the definition of a dead end.
//
// The recipe's endpoint fails the test if it is contacted at all,
// because "did not refetch" is the property; an assertion on the
// result alone would pass for a run that downloaded and reinstalled
// the same bytes.
func TestMigrateOneResumesAnInterruptedRelocation(t *testing.T) {
	m := newMigrateMachine(t)
	bare := seedStore(t, m.storeRoot, "resumed", "1.0")
	seedProvenanced(t, m.storeRoot, "resumed", "1.0-1")
	r := m.untouchableBinaryPkg("resumed")

	moved, err := migrateOne(m.ctx, m.galeHome, migrateTarget{
		name: "resumed", version: "1.0-1", dir: bare, bare: true, recipe: r,
	}, discardOutput())
	if err != nil {
		t.Fatalf("an interrupted relocation could not be resumed: %v", err)
	}
	if !moved {
		t.Error("a resumed relocation must still report the move, or the " +
			"pre-revision directory is never removed")
	}
	// Still there: the removal belongs to the end of the pass, and a
	// resume that destroyed it here would skip every check that runs
	// against the machine first.
	if _, statErr := os.Lstat(bare); statErr != nil {
		t.Errorf("the resume destroyed the pre-revision dir: %v", statErr)
	}
}

// The pre-revision directory is removed after the WHOLE pass, not
// during it.
//
// Ordering and finishing each have their own tests; their composition
// has none, and it is the composition design §13 argues for. A bare
// directory is reached by whatever was built against it, those
// dependents are candidates too, and one processed later in the pass
// drops its reference when it is refetched. Finishing inside the loop
// would decide a directory's fate while a candidate that speaks about
// it has not had its turn.
//
// So the fixture is a bare DEPENDENCY plus a later dependent whose old
// dependency metadata still names it, and the assertion is the
// sequence: the removal is printed after the dependent's replacement,
// never before it.
//
// Stated exactly, because it is easy to claim more: finishing inside
// the loop does not fail this fixture outright. Store resolution
// floats a bare dependency reference onto the canonical sibling as
// soon as that sibling exists, so the mid-loop removal check finds
// nothing reaching the old directory and proceeds. What is lost is the
// ordering itself — the destructive step stops being the last thing
// the pass does — and that is what this test refuses to let go.
func TestRunMigrateRemovesABareDirOnlyAfterThePassIsDone(t *testing.T) {
	m := newMigrateMachine(t)
	bare := seedStore(t, m.storeRoot, "adep", "1.0")
	writeDepsMeta(t, m.storeRoot, "adep", "1.0")
	seedStore(t, m.storeRoot, "zapp", "1.0-1")
	writeDepsMeta(t, m.storeRoot, "zapp", "1.0-1",
		depsmeta.ResolvedDep{Name: "adep", Version: "1.0", Revision: 1})
	if err := generation.Build(map[string]string{"zapp": "1.0-1"},
		m.galeHome, m.storeRoot); err != nil {
		t.Fatal(err)
	}
	m.binaryPkg("adep")
	m.binaryPkg("zapp")

	out, buf := recordedOutput()
	if err := runMigrate(m.ctx, out); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, buf)
	}
	if _, err := os.Lstat(bare); !os.IsNotExist(err) {
		t.Errorf("the pre-revision dir survived the pass (%v)\n%s", err, buf)
	}
	replaced := strings.Index(buf.String(), "Migrating unprovenanced zapp")
	removed := strings.Index(buf.String(), "removed "+bare)
	if replaced < 0 || removed < 0 {
		t.Fatalf("the pass did not both replace and remove:\n%s", buf)
	}
	if removed < replaced {
		t.Errorf("the bare dir was removed before the dependent was "+
			"replaced:\n%s", buf)
	}
}
