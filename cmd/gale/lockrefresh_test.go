package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/lockgraph"
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
		func(_ context.Context, pkg string) (*recipe.Recipe, error) {
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
		func(_ context.Context, pkg string) (*recipe.Recipe, error) {
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
				func(_ context.Context, pkg string) (*recipe.Recipe, error) {
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
	if _, err := ctx.Installer.Reinstall(context.Background(), r); err != nil {
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

// A refresh converges the closure bottom-up within one run: a
// declared root that is also another declared root's dependency is
// replaced BEFORE the root above it.
//
// Design §5 and §7 between them force this. Provenance is
// all-or-nothing, so an artifact whose dependency carries no record
// commits with no record of its own, and §13's candidate check then
// refuses the replacement. Alphabetical order therefore decides
// whether a scope can refresh at all: "app" sorts before the "zdep"
// it links, so the run rebuilds app against an unprovenanced zdep,
// destroys nothing, and fails — while the same two packages named
// the other way round succeed.
//
// Ordering by dependency is also what makes §5's reverse-dependent
// rule reachable: a dependent processed after its dependency sees
// the new bytes rather than a digest that went stale behind it.
func TestRefreshLocksDependenciesBeforeDependents(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := twoLevelCtx(t, tmp, "app", "zdep")
	for _, name := range []string{"app", "zdep"} {
		seedStore(t, ctx.StoreRoot, name, "1.0-1")
	}

	if err := runLockRefresh(ctx, "", nil, discardOutput()); err != nil {
		t.Fatalf("refresh over a two-level closure: %v", err)
	}
	for _, name := range []string{"app", "zdep"} {
		if _, err := os.Lstat(filepath.Join(
			ctx.StoreRoot, name, "1.0-1", provenance.File,
		)); err != nil {
			t.Errorf("%s was not refreshed: %v", name, err)
		}
	}
}

// twoLevelCtx declares two buildable roots where top depends on
// bottom at runtime, which is the shape every ordering and
// reverse-dependent fixture needs.
func twoLevelCtx(t *testing.T, tmp, top, bottom string) *cmdContext {
	t.Helper()
	tarball, sum := sourceTarball(t, top)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, tarball)
		},
	))
	t.Cleanup(srv.Close)

	return lockCtxResolver(t, tmp,
		fmt.Sprintf("[packages]\n  %s = \"1.0\"\n  %s = \"1.0\"\n", top, bottom),
		func(_ context.Context, pkg string) (*recipe.Recipe, error) {
			r := &recipe.Recipe{
				Package: recipe.Package{Name: pkg, Version: "1.0"},
				Source: recipe.Source{
					URL: srv.URL + "/source.tar.gz", SHA256: sum,
				},
				Build: recipe.Build{Steps: []string{
					"mkdir -p $PREFIX/bin",
					"echo '#!/bin/sh' > $PREFIX/bin/" + pkg,
					"chmod +x $PREFIX/bin/" + pkg,
				}},
			}
			if pkg == top {
				r.Dependencies = recipe.Dependencies{Runtime: []string{bottom}}
			}
			return r, nil
		})
}

// A refresh stops BEFORE destroying anything when a package the
// scope actively loads records the artifact being replaced.
//
// Design §5: replacing an artifact changes the graph_digest of every
// node above it, so refresh must regenerate the reverse-dependent
// closure; §11 and §13 bound that regeneration to a directory that
// is absent, unreferenced, or unprovenanced, and a referenced one
// that records the target stops the operation with the conflict
// error. Acceptance 13.
//
// Stopping is not enough on its own. A run that discovers the
// conflict on its way out has already destroyed the candidate to
// learn something it could have read first, and the stale record it
// stopped for is no better off.
//
// The fixture writes the dependent's record by hand because §7's
// all-or-nothing rule means no consistent history produces this
// state: a parent above an unprovenanced dependency holds no record
// at all. That is precisely why the guard is cheap to state and
// worth having — the store can still be put into this shape by a
// partial restore or a deleted record, and the operation that meets
// it must refuse rather than widen the damage.
//
// The record the fixture writes PARSES and would not verify: its
// graph digest is a well-formed value the closure would never
// recompute. That is what the guard tests, deliberately, and it is
// the safe direction — a record gale cannot vouch for still refuses.
func TestRefreshStopsBeforeReplacingUnderAProvenancedDependent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := twoLevelCtx(t, tmp, "app", "zdep")
	// The candidate: occupied, unprovenanced, and about to be replaced.
	marker := filepath.Join(
		seedStore(t, ctx.StoreRoot, "zdep", "1.0-1"), "legacy-marker",
	)
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The dependent above it: a record naming that exact identity,
	// active in this scope.
	seedStore(t, ctx.StoreRoot, "app", "1.0-1")
	writeDependentProvenance(t, ctx.StoreRoot, "app", "1.0-1", "zdep@1.0-1")
	if err := ctx.RebuildGeneration(); err != nil {
		t.Fatal(err)
	}

	err := runLockRefresh(ctx, "", []string{"zdep"}, discardOutput())
	if !errors.Is(err, errDependentRecord) {
		t.Fatalf("err = %v, want errDependentRecord", err)
	}
	if !strings.Contains(err.Error(), "app@1.0-1") {
		t.Errorf("the refusal must name the dependent: %q", err)
	}
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("the candidate was replaced before the conflict "+
			"was found: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want %q", kept, "old")
	}
}

// writeDependentProvenance writes a structurally readable record for
// a package that records dep as a runtime edge, beside the dependency
// metadata a real install always writes. The digest is well formed
// and is not the value the closure would recompute, which is the
// whole point: the record is the one the replacement would
// invalidate, and the guard does not need it to verify.
func writeDependentProvenance(
	t *testing.T, storeRoot, name, version, dep string,
) {
	t.Helper()
	dir := filepath.Join(storeRoot, name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	depName, depVer, ok := strings.Cut(dep, "@")
	if !ok {
		t.Fatalf("dep %q is not name@version", dep)
	}
	// Metadata spells the version bare with the revision beside it;
	// the strict reader refuses a version that carries the suffix,
	// because "1.0-1" with revision 1 would read two ways.
	bare, rev, ok := strings.Cut(depVer, "-")
	if !ok {
		t.Fatalf("dep version %q carries no revision", depVer)
	}
	n, err := strconv.Atoi(rev)
	if err != nil {
		t.Fatal(err)
	}
	if err := depsmeta.Write(dir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: depName, Version: bare, Revision: n},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := provenance.Write(dir, provenance.Record{
		Name:        name,
		Version:     version,
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		SHA256:      testSHA,
		Method:      lockgraph.MethodSource,
		RuntimeDeps: []string{dep},
		GraphDigest: "sha256:" + strings.Repeat("5c", 32),
	}); err != nil {
		t.Fatalf("provenance.Write(%s): %v", name, err)
	}
}

// A recipe cycle cannot be ordered, and orderRoots says so by
// leaving the order it was given.
//
// Depth-first traversal does not do that on its own: it treats a
// node it is still visiting exactly like a finished one, so a -> b
// -> a emits b before a. Ordering is load-bearing here, so a silent
// reordering on the one input where no order is correct is worse
// than not reordering at all.
func TestOrderRootsKeepsTheGivenOrderOnACycle(t *testing.T) {
	dep := func(name, on string) *recipe.Recipe {
		return &recipe.Recipe{
			Package:      recipe.Package{Name: name, Version: "1.0"},
			Dependencies: recipe.Dependencies{Runtime: []string{on}},
		}
	}
	in := []*recipe.Recipe{dep("a", "b"), dep("b", "a")}
	got := orderRoots(in)
	if len(got) != 2 || got[0].Package.Name != "a" ||
		got[1].Package.Name != "b" {
		names := make([]string, len(got))
		for i, r := range got {
			names[i] = r.Package.Name
		}
		t.Errorf("orderRoots = %v, want the given order [a b]", names)
	}
}

// The dependent scan is re-established inside the commit lock, not
// carried from the answer formed before the fetch.
//
// The initiating scope is exempt from checkReplaceable's cross-scope
// veto, so nothing else watches it: a generation rebuild or a
// concurrent install in this same scope can make a dependent active,
// or provenance one that was already active, while the candidate is
// being rebuilt. Acting on the earlier answer replaces bytes a
// record now names.
//
// The build step writes the dependent's record, which is what a
// concurrent run in this scope would do, and does it
// deterministically.
func TestRefreshRechecksDependentsInsideTheCommitLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	tarball, sum := sourceTarball(t, "adep")
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, tarball)
		},
	))
	defer srv.Close()

	// zapp is active and unprovenanced, so the early scan clears it,
	// and it sorts after the candidate so the run reaches the
	// replacement first.
	scratch := filepath.Join(tmp, "scratch")
	zappDir := filepath.Join(tmp, ".gale", "pkg", "zapp", "1.0-1")
	ctx := lockCtxResolver(t, tmp,
		"[packages]\n  adep = \"1.0\"\n  zapp = \"1.0\"\n",
		func(_ context.Context, pkg string) (*recipe.Recipe, error) {
			r := &recipe.Recipe{
				Package: recipe.Package{Name: pkg, Version: "1.0"},
				Source: recipe.Source{
					URL: srv.URL + "/source.tar.gz", SHA256: sum,
				},
				Build: recipe.Build{Steps: []string{
					"mkdir -p $PREFIX/bin",
					"echo '#!/bin/sh' > $PREFIX/bin/" + pkg,
				}},
			}
			if pkg == "adep" {
				// Written while adep is being rebuilt, which is what a
				// concurrent run in this scope would do.
				r.Build.Steps = append(r.Build.Steps, fmt.Sprintf("cp %s %s",
					filepath.Join(scratch, "zapp", "1.0-1", provenance.File),
					filepath.Join(zappDir, provenance.File)))
			}
			return r, nil
		})
	writeDependentProvenance(t, scratch, "zapp", "1.0-1", "adep@1.0-1")

	marker := filepath.Join(
		seedStore(t, ctx.StoreRoot, "adep", "1.0-1"), "legacy-marker",
	)
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedStore(t, ctx.StoreRoot, "zapp", "1.0-1")
	// The metadata a real install always writes, so the closure walk
	// is complete; only the provenance record arrives mid-build.
	if err := depsmeta.Write(zappDir, depsmeta.Metadata{
		Deps: []depsmeta.ResolvedDep{
			{Name: "adep", Version: "1.0", Revision: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ctx.RebuildGeneration(); err != nil {
		t.Fatal(err)
	}

	err := runLockRefresh(ctx, "", []string{"adep"}, discardOutput())
	if !errors.Is(err, errDependentRecord) {
		t.Fatalf("err = %v, want errDependentRecord", err)
	}
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("the candidate was replaced under a record that "+
			"appeared mid-build: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want %q", kept, "old")
	}
}

// A closure the scan could not read in full cannot authorize a
// replacement.
//
// Unreadable dependency metadata blocks the descent, so a dependent
// below the unreadable directory is invisible: the scan reports no
// conflict because it saw nothing, which is absence of evidence
// standing in for evidence of absence on a decision that destroys
// bytes. The remedy is the post-migrate sequence, exactly as it is
// for another scope in the same state.
//
// The candidate's OWN metadata is excluded from that judgement,
// pinned by TestRefreshIgnoresMalformedMetadataInTheOldDir: it is
// the artifact being replaced, so letting it refuse its own
// replacement is the deadlock cycle 22 already removed once.
func TestRefreshRefusesWhenTheScopeClosureIsUnreadable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := twoLevelCtx(t, tmp, "app", "zdep")
	marker := filepath.Join(
		seedStore(t, ctx.StoreRoot, "zdep", "1.0-1"), "legacy-marker",
	)
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second active package whose metadata does not decode, so the
	// walk cannot see what it depends on.
	appDir := seedStore(t, ctx.StoreRoot, "app", "1.0-1")
	if err := os.WriteFile(
		filepath.Join(appDir, depsmeta.File), []byte("not toml\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := ctx.RebuildGeneration(); err != nil {
		t.Fatal(err)
	}

	err := runLockRefresh(ctx, "", []string{"zdep"}, discardOutput())
	if !errors.Is(err, errScopeClosureUnreadable) {
		t.Fatalf("err = %v, want errScopeClosureUnreadable", err)
	}
	if !strings.Contains(err.Error(), "gale migrate") {
		t.Errorf("the refusal must name the remedy: %q", err)
	}
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("bytes destroyed on a closure gale could not read: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want %q", kept, "old")
	}
}
