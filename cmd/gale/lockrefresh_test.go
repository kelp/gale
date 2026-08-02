package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/projects"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
)

// --refresh exists to move an identity from one set of bytes to
// another, so it must exist as a flag before anything else here means
// something.
func TestLockRefreshFlagExists(t *testing.T) {
	if lockCmd.Flags().Lookup("refresh") == nil {
		t.Fatal("gale lock: --refresh flag not found")
	}
}

// The design spells the remedy `gale lock --refresh [pkg]`, and
// `unprovenanced` prints it with a package name in it. A command
// that refuses its own documented remedy sends the user nowhere.
//
// The other half matters as much: without --refresh a package name
// means nothing, and accepting it silently would run a different
// operation than the one typed. Both halves go through
// checkLockArgs, because cobra's Args hook cannot see the flag.
func TestLockAcceptsAPackageOnlyWithRefresh(t *testing.T) {
	if err := checkLockArgs([]string{"jq"}, true); err != nil {
		t.Errorf("lock rejected the argument its own remedy prints: %v", err)
	}
	err := checkLockArgs([]string{"jq"}, false)
	if !errors.Is(err, errLockArgsNeedRefresh) {
		t.Errorf("err = %v, want errLockArgsNeedRefresh", err)
	}
	if err := checkLockArgs(nil, false); err != nil {
		t.Errorf("plain `gale lock` refused: %v", err)
	}
}

// A named package that the target does not declare is refused
// before anything is replaced, and the refusal lists what IS
// declared: `--refresh jq` in a project that spells it `jq-git` is
// a typo, not an instruction to refresh everything.
func TestRefreshRefusesAnUndeclaredPackage(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := lockCtx(t, tmp, "[packages]\n  hello = \"1.0.0\"\n",
		map[string]string{"hello": "1.0.0"})
	dir := seedStore(t, ctx.StoreRoot, "hello", "1.0.0-1")

	err := runLockRefresh(ctx, "", []string{"nosuch"}, discardOutput())
	if !errors.Is(err, errUndeclaredRefresh) {
		t.Fatalf("err = %v, want errUndeclaredRefresh", err)
	}
	if !strings.Contains(err.Error(), "hello") {
		t.Errorf("the refusal must list what is declared: %q", err)
	}
	// Nothing was touched: the check runs before any replacement.
	if _, statErr := os.Lstat(dir); statErr != nil {
		t.Errorf("an undeclared name disturbed the store: %v", statErr)
	}
}

// Design §13 and §11: a directory with VALID provenance that
// disagrees with the recipe is always a conflict, never a
// replacement, and --refresh is not offered as its remedy. Replacing
// it would destroy the evidence that two things claim one identity,
// which is the substitution the lock exists to detect.
//
// This is the plan's named first test for the phase (acceptance 13
// and 26).
func TestRefreshRefusesAValidlyProvenancedDisagreement(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := lockCtx(t, tmp, "[packages]\n  hello = \"1.0.0\"\n",
		map[string]string{"hello": "1.0.0"})
	// Valid provenance, but for a package whose recipe declares no
	// binary for this platform at all, so what the record names and
	// what the recipe backs cannot agree.
	seedProvenanced(t, ctx.StoreRoot, "hello", "1.0.0-1")
	dir := filepath.Join(ctx.StoreRoot, "hello", "1.0.0-1")

	err := runLockRefresh(ctx, "", nil, discardOutput())
	if err == nil {
		t.Fatal("refresh accepted a validly provenanced disagreement")
	}
	if !errors.Is(err, errRecipeDisagrees) {
		t.Fatalf("err = %v, want errRecipeDisagrees", err)
	}
	// The remedy must NOT be --refresh: the user just ran it, and
	// offering it again is a loop with no exit.
	if strings.Contains(err.Error(), "--refresh") {
		t.Errorf("refresh offered itself as the remedy for a conflict "+
			"it cannot resolve: %q", err)
	}

	// The directory survives byte-for-byte.
	if _, statErr := os.Lstat(
		filepath.Join(dir, provenance.File),
	); statErr != nil {
		t.Errorf("refresh disturbed a validly provenanced dir: %v", statErr)
	}
}

// buildableCtx is a lock context whose one declared package really
// builds, which every refresh fixture needs: the replacement runs
// the install to completion and decides on the artifact it produced,
// so a recipe that cannot build never reaches the decision.
func buildableCtx(t *testing.T, tmp, name string) *cmdContext {
	t.Helper()
	tarball, sum := sourceTarball(t, name)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, tarball)
		},
	))
	t.Cleanup(srv.Close)

	return lockCtxResolver(t, tmp,
		"[packages]\n  "+name+" = \"1.0\"\n",
		func(pkg string) (*recipe.Recipe, error) {
			return &recipe.Recipe{
				Package: recipe.Package{Name: pkg, Version: "1.0"},
				Source: recipe.Source{
					URL: srv.URL + "/source.tar.gz", SHA256: sum,
				},
				Build: recipe.Build{Steps: []string{
					"mkdir -p $PREFIX/bin",
					"echo '#!/bin/sh' > $PREFIX/bin/" + pkg,
					"chmod +x $PREFIX/bin/" + pkg,
				}},
			}, nil
		})
}

// appendDeclaration adds one more pinned package to a gale.toml
// whose [packages] section is already open.
func appendDeclaration(t *testing.T, galePath, name, version string) {
	t.Helper()
	f, err := os.OpenFile(galePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "  %s = %q\n", name, version); err != nil {
		t.Fatal(err)
	}
}

// The case --refresh is FOR: an occupied directory with no provenance
// at all, which plain `gale lock` refuses because adopting it would
// assert provenance for bytes nothing verified. §13 permits replacing
// it, since the store never holds two sets of bytes for one identity
// and unverifiable legacy bytes are not a second set, they are an
// unknown one.
func TestRefreshReplacesAnUnprovenancedDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := buildableCtx(t, tmp, "legacypkg")

	// A pre-upgrade directory: occupied, and attesting nothing.
	dir := seedStore(t, ctx.StoreRoot, "legacypkg", "1.0-1")
	marker := filepath.Join(dir, "legacy-marker")
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runLockRefresh(ctx, "", nil, discardOutput()); err != nil {
		t.Fatalf("refresh over an unprovenanced dir: %v", err)
	}

	// Replaced, not stamped: the marker is gone and provenance exists.
	if _, statErr := os.Lstat(marker); !os.IsNotExist(statErr) {
		t.Error("the legacy bytes survived, so provenance was stamped " +
			"beside a directory that was never replaced")
	}
	if _, statErr := os.Lstat(
		filepath.Join(dir, provenance.File),
	); statErr != nil {
		t.Errorf("no provenance after replacement: %v", statErr)
	}
}

// Naming a package narrows what may be REPLACED, and nothing else.
// The unnamed root is still locked, and its unprovenanced directory
// is left for lockRoot to refuse: `--refresh jq` is not consent to
// destroy everything else that happens to predate provenance.
func TestRefreshReplacesOnlyTheNamedPackage(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := buildableCtx(t, tmp, "named")
	// A second declared root, unprovenanced exactly like the first.
	appendDeclaration(t, ctx.GalePath, "unnamed", "1.0")
	namedDir := seedStore(t, ctx.StoreRoot, "named", "1.0-1")
	spared := filepath.Join(
		seedStore(t, ctx.StoreRoot, "unnamed", "1.0-1"), "legacy-marker",
	)
	if err := os.WriteFile(spared, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The unnamed root is unprovenanced, so lockRoot refuses it and
	// the run stops. What matters is which bytes changed on the way.
	err := runLockRefresh(ctx, "", []string{"named"}, discardOutput())
	if !errors.Is(err, errUnprovenancedStoreDir) {
		t.Fatalf("err = %v, want the unnamed root to be refused", err)
	}
	kept, rerr := os.ReadFile(spared)
	if rerr != nil {
		t.Fatalf("the unnamed package was replaced: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("unnamed holds %q, want %q", kept, "old")
	}
	// The named one was replaced, so the narrowing is a filter and
	// not a switch that turned refreshing off altogether.
	if _, statErr := os.Lstat(
		filepath.Join(namedDir, provenance.File),
	); statErr != nil {
		t.Errorf("the named package was not refreshed: %v", statErr)
	}
}

// Upgrade day's other shape: the package predates revisions, so it
// lives in a BARE directory while the canonical one does not exist.
//
// --refresh does not touch it, and says so. Relocating the identity
// would mean deleting a path other scopes' generations link, with no
// per-scope command able to repair their symlinks; that work belongs
// to machine-wide migrate, which enumerates every scope first. The
// remedy therefore names migrate ALONE, since offering a flag that
// must refuse is a loop with no exit.
func TestRefreshLeavesAPreRevisionDirToMigrate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := buildableCtx(t, tmp, "prerev")
	bare := seedStore(t, ctx.StoreRoot, "prerev", "1.0")
	marker := filepath.Join(bare, "legacy-marker")
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runLockRefresh(ctx, "", nil, discardOutput())
	if !errors.Is(err, errUnprovenancedStoreDir) {
		t.Fatalf("err = %v, want errUnprovenancedStoreDir", err)
	}
	if !strings.Contains(err.Error(), "gale migrate") {
		t.Errorf("the refusal must name migrate: %q", err)
	}
	if strings.Contains(err.Error(), "--refresh") {
		t.Errorf("the refusal offers a flag that must refuse: %q", err)
	}
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("the pre-revision dir was destroyed: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want %q", kept, "old")
	}
}

// A rebuild that cannot attest its own closure commits with NO
// record (design §7's all-or-nothing rule). Replacing with it would
// trade one unattested directory for another: bytes destroyed,
// nothing repaired, and the run reporting success.
//
// The build step ships a dependency-metadata file the strict reader
// refuses, which is one of the real ways a closure becomes
// unattestable, and produces the no-record commit deterministically.
func TestRefreshRefusesAnUnprovenancedCandidate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	tarball, sum := sourceTarball(t, "bareback")
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, tarball)
		},
	))
	defer srv.Close()

	ctx := lockCtxResolver(t, tmp, "[packages]\n  bareback = \"1.0\"\n",
		func(pkg string) (*recipe.Recipe, error) {
			return &recipe.Recipe{
				Package: recipe.Package{Name: pkg, Version: "1.0"},
				Source: recipe.Source{
					URL: srv.URL + "/source.tar.gz", SHA256: sum,
				},
				Build: recipe.Build{Steps: []string{
					"mkdir -p $PREFIX/bin",
					"echo '#!/bin/sh' > $PREFIX/bin/" + pkg,
					// revision as a string is a decode failure, so the
					// strict reader refuses the file and the artifact
					// commits with no record at all.
					"printf '[[deps]]\\nname = \"x\"\\nversion = \"1\"\\n" +
						"revision = \"42\"\\n' > $PREFIX/" + depsmeta.File,
				}},
			}, nil
		})

	dir := seedStore(t, ctx.StoreRoot, "bareback", "1.0-1")
	marker := filepath.Join(dir, "legacy-marker")
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runLockRefresh(ctx, "", nil, discardOutput())
	if !errors.Is(err, errCandidateUnprovenanced) {
		t.Fatalf("err = %v, want errCandidateUnprovenanced", err)
	}
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("bytes destroyed for an unprovenanced candidate: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want %q", kept, "old")
	}
}

// Between deciding a directory is replaceable and taking the locks
// that replace it, the directory can stop being replaceable. The
// decision is therefore re-established inside the locks, because
// acting on the stale one destroys exactly the record §13 says must
// survive.
//
// What appears there decides which failure it is, and the two must
// not collapse. A VALID record means another gale got there first,
// which the user retries. A record that does not validate is an
// integrity failure exactly as it is in lockRoot, and calling it a
// lost race would send the user to retry into the same corrupt
// state forever.
//
// The build step writes into the store dir, which is what a
// concurrent run would do, and does it deterministically.
func TestRefreshReclassifiesADirChangedMidBuild(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record string
		want   error
		// raced is whether this case is the benign one, stated
		// separately so the negative assertion does not compare
		// sentinels with ==.
		raced bool
	}{
		{"valid record", "", errRaceLostToProvenance, true},
		{"malformed record", "raced\n", provenance.ErrInvalid, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("HOME", tmp)

			tarball, sum := sourceTarball(t, "racepkg")
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					http.ServeFile(w, r, tarball)
				},
			))
			defer srv.Close()

			victim := filepath.Join(tmp, ".gale", "pkg", "racepkg", "1.0-1")
			plant := plantStep(t, tmp, victim, tc.record)
			ctx := lockCtxResolver(t, tmp,
				"[packages]\n  racepkg = \"1.0\"\n",
				func(pkg string) (*recipe.Recipe, error) {
					return &recipe.Recipe{
						Package: recipe.Package{Name: pkg, Version: "1.0"},
						Source: recipe.Source{
							URL: srv.URL + "/source.tar.gz", SHA256: sum,
						},
						Build: recipe.Build{Steps: []string{
							"mkdir -p $PREFIX/bin",
							"echo '#!/bin/sh' > $PREFIX/bin/" + pkg,
							plant,
						}},
					}, nil
				})

			seedStore(t, ctx.StoreRoot, "racepkg", "1.0-1")

			err := runLockRefresh(ctx, "", nil, discardOutput())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !tc.raced && errors.Is(err, errRaceLostToProvenance) {
				t.Error("an integrity failure was reported as a lost race, " +
					"so the remedy is a retry into the same corrupt state")
			}
			// Whatever appeared is still there: the point of refusing
			// is that it was not destroyed.
			if _, statErr := os.Lstat(
				filepath.Join(victim, provenance.File),
			); statErr != nil {
				t.Errorf("the file written mid-build was destroyed: %v", statErr)
			}
		})
	}
}

// plantStep returns a build step that puts a provenance file into
// dir, standing in for a concurrent run. An empty body means a
// record that really validates, minted here so the fixture does not
// hand-roll one that only looks valid.
func plantStep(t *testing.T, tmp, dir, body string) string {
	t.Helper()
	if body != "" {
		return fmt.Sprintf("printf %q > %s",
			body, filepath.Join(dir, provenance.File))
	}
	scratch := filepath.Join(tmp, "scratch")
	writeProvenance(t, scratch, "racepkg", "1.0-1")
	return fmt.Sprintf("cp %s %s",
		filepath.Join(scratch, "racepkg", "1.0-1", provenance.File),
		filepath.Join(dir, provenance.File))
}

// A refresh whose rebuilt artifact stops providing a library must
// still succeed when the only scope claiming that library is the
// one doing the refreshing.
//
// This is the deadlock the farm guard is most likely to cause. The
// initiating scope's proposed closure is built before the rename,
// when the canonical directory still holds the OLD artifact, so a
// claim resolved from directories lists exactly the sonames the
// replacement drops, and the scope vetoes its own repair. Design §4
// calls that the verb veto and forbids it.
//
// Wired through the real guard, not a stub: a stub cannot fail this
// way, which is why the earlier refusal test could not catch it.
func TestRefreshDroppingALibraryIsNotSelfVetoed(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := buildableCtx(t, tmp, "droplib")
	wireFarmGuards(ctx.Installer, ctx.GaleDir, ctx.StoreRoot)

	// The installed artifact exports a library; the rebuilt one will
	// not, because buildableCtx's recipe writes only a bin.
	dir := seedStore(t, ctx.StoreRoot, "droplib", "1.0-1")
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	soname := "libdrop.4.dylib"
	if runtime.GOOS == "linux" {
		soname = "libdrop.so.4"
	}
	if err := os.WriteFile(
		filepath.Join(libDir, soname), []byte("old"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	farmDir := farm.DirFromStoreDir(dir)
	if err := farm.Populate(dir, farmDir); err != nil {
		t.Fatal(err)
	}
	// Active, so the scope claims it.
	if err := ctx.RebuildGeneration(); err != nil {
		t.Fatal(err)
	}

	if err := runLockRefresh(ctx, "", nil, discardOutput()); err != nil {
		t.Fatalf("the scope vetoed its own refresh: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(farmDir, soname)); !os.IsNotExist(err) {
		t.Errorf("the dropped library still has a farm entry: %v", err)
	}
}

// Malformed dependency metadata in the artifact being REPLACED must
// not refuse the refresh that replaces it.
//
// An unprovenanced legacy directory is exactly where malformed
// metadata lives, so a guard that reads it to decide whether the
// operation may proceed hands the decision to the bytes nobody
// trusts. Both farm guards run before the commit, so both must read
// the staged artifact instead.
//
// Command-wired through the real guards: a unit test on the claim
// builder passes even when the FIRST guard still traverses the old
// directory and fails there.
func TestRefreshIgnoresMalformedMetadataInTheOldDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := buildableCtx(t, tmp, "badmeta")
	wireFarmGuards(ctx.Installer, ctx.GaleDir, ctx.StoreRoot)

	dir := seedStore(t, ctx.StoreRoot, "badmeta", "1.0-1")
	if err := os.WriteFile(
		filepath.Join(dir, depsmeta.File), []byte("not toml at all\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := ctx.RebuildGeneration(); err != nil {
		t.Fatal(err)
	}

	if err := runLockRefresh(ctx, "", nil, discardOutput()); err != nil {
		t.Fatalf("the superseded artifact's metadata refused its own "+
			"replacement: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(dir, provenance.File),
	); err != nil {
		t.Errorf("no provenance after refresh: %v", err)
	}
}

// An archive shipping a malformed .gale-deps.toml installs today:
// the installer commits it with no provenance record rather than
// failing (design §7's all-or-nothing rule). The farm guard runs on
// that same commit, so it must not be stricter than the policy it
// guards, or a package that installs today stops installing.
//
// Command-wired, because the installer's own tests do not attach
// the production guard and so cannot see this.
func TestInstallWithMalformedStagedDepsPassesTheFarmGuard(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	tarball, sum := sourceTarball(t, "badmetainstall")
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, tarball)
		},
	))
	defer srv.Close()

	ctx := lockCtxResolver(t, tmp, "[packages]\n", nil)
	wireFarmGuards(ctx.Installer, ctx.GaleDir, ctx.StoreRoot)

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "badmetainstall", Version: "1.0"},
		Source: recipe.Source{
			URL: srv.URL + "/source.tar.gz", SHA256: sum,
		},
		Build: recipe.Build{Steps: []string{
			"mkdir -p $PREFIX/bin",
			"echo '#!/bin/sh' > $PREFIX/bin/badmetainstall",
			// revision as a string: does not decode.
			"printf '[[deps]]\\nname = \"x\"\\nversion = \"1\"\\n" +
				"revision = \"42\"\\n' > $PREFIX/" + depsmeta.File,
		}},
	}
	if _, err := ctx.Installer.Reinstall(r); err != nil {
		t.Fatalf("the farm guard refused an install the provenance "+
			"policy allows: %v", err)
	}
	dir := filepath.Join(ctx.StoreRoot, "badmetainstall", "1.0-1")
	if _, err := os.Lstat(filepath.Join(dir, "bin")); err != nil {
		t.Errorf("the package did not commit: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(dir, provenance.File),
	); !os.IsNotExist(err) {
		t.Errorf("an unattestable closure was given a record: %v", err)
	}
}

// The two wired guards read the proposed closure with different
// strictness, and the difference is which of them DELETES.
//
// Dropping a farm entry is a deletion, so its claim must be
// complete: a dependency the staged artifact names and the store no
// longer has leaves the closure unsatisfied, and approving a
// deletion against a partial picture is the fail-open the guard
// exists to prevent. Population is deliberately lenient, because
// retargeting a soname a vanished package cannot provide is
// harmless and refusing it would freeze every scope with a
// collected dependency.
//
// Tested on the wired closures directly. A full refresh cannot
// reach the difference: a staged artifact whose dependency is
// missing gets no provenance record, and §13's candidate check
// refuses before either farm guard runs. The sync path's staged
// reinstall has no such check, which is where the strictness earns
// its place.
func TestWiredFarmGuardsDifferOnAnIncompleteClosure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := buildableCtx(t, tmp, "ghostdep")
	wireFarmGuards(ctx.Installer, ctx.GaleDir, ctx.StoreRoot)
	dir := seedStore(t, ctx.StoreRoot, "ghostdep", "1.0-1")
	if err := ctx.RebuildGeneration(); err != nil {
		t.Fatal(err)
	}

	// A staged artifact naming a dependency that is not in the store.
	staging := filepath.Join(t.TempDir(), ".build-ghost")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(staging, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "ghost", Version: "9.9", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	placements := []farm.Placement{{ScanDir: staging, FinalDir: dir}}

	if err := ctx.Installer.FarmRemoveGuard(
		placements, []string{"libghost.4.dylib"},
	); !errors.Is(err, farm.ErrClaimConflict) {
		t.Errorf("removal guard err = %v, want a refusal: it cannot "+
			"approve a deletion against a closure it could not read", err)
	}
	if err := ctx.Installer.FarmGuard(placements); err != nil {
		t.Errorf("population guard refused a collected dependency: %v", err)
	}
}

// The cross-scope veto applies to --refresh, not only to migrate.
// Another scope requiring different bytes at the same path turns the
// replacement into a conflict a human resolves.
func TestRefreshHonoursTheCrossScopeVeto(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := buildableCtx(t, tmp, "vetoed")
	dir := seedStore(t, ctx.StoreRoot, "vetoed", "1.0-1")
	marker := filepath.Join(dir, "legacy-marker")
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A second project requiring a different hash for the identity.
	proj := t.TempDir()
	if err := projects.Register(filepath.Join(tmp, ".gale"), proj); err != nil {
		t.Fatal(err)
	}
	writeScopeLock(t, filepath.Join(proj, "gale.lock"),
		"vetoed@1.0-1", strings.Repeat("cd", 32))

	err := runLockRefresh(ctx, "", nil, discardOutput())
	if !errors.Is(err, errScopeDisagrees) {
		t.Fatalf("err = %v, want errScopeDisagrees", err)
	}
	if !strings.Contains(err.Error(), proj) {
		t.Errorf("the refusal must name the disagreeing scope: %q", err)
	}
	// A refusal replaces nothing. The old bytes are the only copy of
	// what the other scope is arguing about, so destroying them to
	// find out whether the replacement was allowed would settle the
	// argument by erasing one side of it.
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("the vetoed directory is gone: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want %q", kept, "old")
	}
}
