package main

// Tests for runSyncOne — the per-package body of sync.
//
// All runSyncOne tests FAIL against the stub (runSyncOne returns
// syncOutcome{} unconditionally).
//
// TestSortedSyncItemsReturnsAlphabeticalOrder FAILS against the
// sortedSyncItems stub (returns nil).
//
// There is no lockfile-write behaviour here any more: sync never
// writes gale.lock (design §11). TestSyncWritesNoLockfile in
// lockwriter_test.go pins that.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// seedStore creates a canonical store dir for name/version
// and writes a bin/<name> placeholder so IsInstalled returns true.
func seedStore(t *testing.T, storeRoot, name, version string) string {
	t.Helper()
	s := store.NewStore(storeRoot)
	dir, err := s.Create(name, version)
	if err != nil {
		t.Fatalf("seedStore Create: %v", err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("seedStore MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, name),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seedStore WriteFile: %v", err)
	}
	return dir
}

// minimalRecipe returns a recipe with no deps and no binary,
// so Install would try a source build from a URL.
func minimalRecipe(name, version string) *recipe.Recipe {
	return &recipe.Recipe{
		Package: recipe.Package{
			Name:    name,
			Version: version,
		},
	}
}

// writeDepsMetadata writes an empty .gale-deps.toml into storeDir,
// marking the install as non-stale (depsmeta.Has returns true,
// IsStale returns false when there are no declared deps).
func writeDepsMetadataFile(t *testing.T, storeDir string) {
	t.Helper()
	if err := depsmeta.Write(storeDir, depsmeta.Metadata{}); err != nil {
		t.Fatalf("writeDepsMetadata: %v", err)
	}
}

// buildFakeCtx constructs a minimal cmdContext for runSyncOne tests.
// The lockfile at lp must already exist (use lockfile.Write to seed it).
func buildFakeCtx(
	t *testing.T,
	galePath, galeDir, storeRoot string,
	resolver installer.RecipeResolver,
) *cmdContext {
	t.Helper()
	inst := &installer.Installer{
		Store:    store.NewStore(storeRoot),
		Resolver: resolver,
		Verifier: nil, // skip attestation
	}
	ctx := &cmdContext{
		GalePath:  galePath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		Resolver:  resolver,
		Installer: inst,
		Registry:  nil,
	}
	return ctx
}

// TestRunSyncOneAlreadyInstalledNonStaleReturnsUpToDate verifies that
// when a package is already in the store with valid deps metadata
// (IsStale false), runSyncOne returns upToDate=true and attempts
// no install.
//
// Note: the version equality check relies on resolveVersionedRecipe's
// bare-version equality being checked before Full() equality. If that
// order changes this test will fail loudly — that is intentional
// (a refactor-detector).
func TestRunSyncOneAlreadyInstalledNonStaleReturnsUpToDate(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	// Seed store with the package present and deps metadata
	// present (no declared deps → IsStale returns false).
	storeDir := seedStore(t, storeRoot, "mypkg", "2.0.0-1")
	writeDepsMetadataFile(t, storeDir)

	// Resolver returns a recipe whose version matches what's
	// in the store so IsStale has no dep changes to report.
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return minimalRecipe(name, "2.0.0"), nil
	}

	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	w := syncItem{name: "mypkg", version: "2.0.0"}

	out := runSyncOne(context.Background(), ctx, w, false)

	if !out.upToDate {
		t.Errorf("upToDate = false, want true for installed non-stale package")
	}
	if out.stale {
		t.Errorf("stale = true, want false")
	}
	if out.result != nil {
		t.Errorf("result = %v, want nil (no install should occur)", out.result)
	}
	if out.installErr != nil {
		t.Errorf("installErr = %v, want nil", out.installErr)
	}
	if out.resolveErr != nil {
		t.Errorf("resolveErr = %v, want nil", out.resolveErr)
	}
}

// A canceled parent must not be treated as "not stale". The
// offline fallback would mark the package up to date and a
// mid-check --if-needed timeout would then stamp complete.
func TestRunSyncOneCancelIsNotUpToDate(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")
	storeDir := seedStore(t, storeRoot, "mypkg", "2.0.0-1")
	writeDepsMetadataFile(t, storeDir)
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return minimalRecipe(name, "2.0.0"), nil
	}
	cc := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	w := syncItem{name: "mypkg", version: "2.0.0"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := runSyncOne(ctx, cc, w, false)

	if out.upToDate {
		t.Error("canceled stale check must not mark the package up to date")
	}
	if !errors.Is(out.installErr, context.Canceled) {
		t.Fatalf("installErr = %v, want context.Canceled", out.installErr)
	}
}

func TestCollectSyncOutcomesCancelFailsRemaining(t *testing.T) {
	tmp := t.TempDir()
	cc := buildFakeCtx(t, filepath.Join(tmp, "gale.toml"),
		filepath.Join(tmp, ".gale"), filepath.Join(tmp, "store"),
		func(_ context.Context, name string) (*recipe.Recipe, error) {
			return minimalRecipe(name, "1.0"), nil
		})
	items := []syncItem{
		{name: "a", version: "1.0"},
		{name: "b", version: "1.0"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := collectSyncOutcomes(ctx, cc, items)
	if len(got) != 2 {
		t.Fatalf("outcomes = %d, want 2 (remaining must be recorded)", len(got))
	}
	for _, o := range got {
		if !errors.Is(o.installErr, context.Canceled) {
			t.Errorf("%s: installErr = %v, want context.Canceled", o.name, o.installErr)
		}
		if o.upToDate {
			t.Errorf("%s: canceled collect must not mark up to date", o.name)
		}
	}
}

// writeDepsWithFile writes a .gale-deps.toml into storeDir recording
// the given resolved deps, so IsStale compares them against the
// current recipe's resolved deps.
func writeDepsWithFile(t *testing.T, storeDir string, deps ...depsmeta.ResolvedDep) {
	t.Helper()
	if err := depsmeta.Write(storeDir,
		depsmeta.Metadata{Deps: deps}); err != nil {
		t.Fatalf("writeDepsWithFile: %v", err)
	}
}

// TestRunSyncOneOrphanHigherRevisionDoesNotTriggerRebuild pins the
// fix for the infinite rebuild loop: an orphan store dir whose
// revision exceeds the recipe's (left by a withdrawn recipe revision)
// must NOT drive a rebuild. Staleness has to be evaluated against the
// recipe's canonical version-revision — the dir a reinstall writes —
// not the bare pin's highest on-disk revision.
//
// Setup: the recipe is revision 1 and its dep "foo" currently resolves
// to 2.0.0-1. The canonical dir 1.0.0-1 records foo 2.0.0-1 (current →
// not stale); an orphan 1.0.0-2 records foo 1.0.0-1 (stale). A bare
// "1.0.0" pin resolves on disk to the orphan 1.0.0-2. Before the fix,
// runSyncOne checked the orphan, reported stale, and reinstalled the
// recipe revision (1.0.0-1) — which never touched the orphan, so every
// sync rebuilt forever.
func TestRunSyncOneOrphanHigherRevisionDoesNotTriggerRebuild(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	// Canonical (recipe) revision: records the current dep → not stale.
	canonDir := seedStore(t, storeRoot, "mypkg", "1.0.0-1")
	writeDepsWithFile(t, canonDir, depsmeta.ResolvedDep{
		Name: "foo", Version: "2.0.0", Revision: 1,
	})
	// Orphan higher revision: records an old dep → stale. A bare pin
	// resolves to this dir on disk.
	orphanDir := seedStore(t, storeRoot, "mypkg", "1.0.0-2")
	writeDepsWithFile(t, orphanDir, depsmeta.ResolvedDep{
		Name: "foo", Version: "1.0.0", Revision: 1,
	})

	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		switch name {
		case "mypkg":
			r := minimalRecipe(name, "1.0.0")
			r.Package.Revision = 1
			r.Dependencies.Build = []string{"foo"}
			return r, nil
		case "foo":
			r := minimalRecipe(name, "2.0.0")
			r.Package.Revision = 1
			return r, nil
		default:
			return nil, errors.New("unknown package")
		}
	}

	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	w := syncItem{name: "mypkg", version: "1.0.0"}

	out := runSyncOne(context.Background(), ctx, w, false)

	if out.stale {
		t.Error("stale = true, want false: the recipe's canonical " +
			"revision (1.0.0-1) records the current dep; only the " +
			"orphan 1.0.0-2 is stale and must be ignored")
	}
	if !out.upToDate {
		t.Error("upToDate = false, want true: no rebuild should occur")
	}
	if out.result != nil {
		t.Errorf("result = %v, want nil: no install should be attempted", out.result)
	}
	if out.installErr != nil {
		t.Errorf("installErr = %v, want nil", out.installErr)
	}
}

// TestRunSyncOneMissingFromStoreTriggersInstall verifies that when
// a package is absent from the store, runSyncOne triggers the
// Install path and returns a non-nil result with no installErr.
//
// This test uses a closed httptest server to simulate a successful
// binary-fetch failure falling through to source — but because we
// use a recipe with no binary and no source URL, Install will fail.
// We therefore rely on a fake Installer that records calls.
// The simplest reliable approach: use a real store seeded to appear
// empty, a resolver that returns a no-URL recipe, and verify that
// result==nil AND installErr!=nil (because a recipe with no URL
// will fail at build). This verifies Install was attempted, not
// skipped.
//
// TODO: distinguishing Install from Reinstall (behaviour 3) requires
// an injectable Installer call-counting hook, deferred until somebody
// actually breaks it.
func TestRunSyncOneMissingFromStoreTriggersInstallAttempt(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	// Store is empty — package is not installed.
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Use a closed server to force install to fail fast —
	// verifies Install was attempted, not silently skipped.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()

	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return &recipe.Recipe{
			Package: recipe.Package{
				Name:    name,
				Version: "1.0.0",
			},
			Source: recipe.Source{
				URL:    addr + "/source.tar.gz",
				SHA256: "deadbeef",
			},
			Build: recipe.Build{
				Steps: []string{"echo build"},
			},
		}, nil
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	w := syncItem{name: "newpkg", version: "1.0.0"}

	out := runSyncOne(context.Background(), ctx, w, false)

	// The install was attempted (result may be nil because it
	// failed, but installErr must be set OR result is non-nil).
	// Either path proves Install was not skipped.
	if out.result == nil && out.installErr == nil {
		t.Error("both result and installErr are nil: " +
			"Install was not attempted for a missing package")
	}
	if out.upToDate {
		t.Error("upToDate = true, want false: package was not in store")
	}
	if out.stale {
		t.Error("stale = true, want false: package was not even installed")
	}
}

// TestRunSyncOneInstalledButStaleTriggerReinstall verifies that when
// a package is in the store but missing .gale-deps.toml (marking it
// as a pre-revision install), runSyncOne sets stale=true and
// attempts Reinstall.
//
// Gap: we cannot directly distinguish Install vs Reinstall without
// instrumenting the Installer. We verify the observable effect:
// stale==true and result!=nil (or installErr!=nil, proving an
// attempt occurred). Distinguishing Install from Reinstall would
// require an injectable Installer hook — noted for future work.
func TestRunSyncOneInstalledButStaleTriggersReinstall(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	// Seed store with the package but WITHOUT .gale-deps.toml —
	// this is what triggers the "stale" path in runSyncOne.
	seedStore(t, storeRoot, "stalep", "3.0.0-1")
	// Do NOT call writeDepsMetadataFile — absence of the file
	// is what marks the install as pre-revision (stale).

	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Use a closed server so Reinstall fails fast — proves it
	// was called, not silently skipped.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()

	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return &recipe.Recipe{
			Package: recipe.Package{
				Name:    name,
				Version: "3.0.0",
			},
			Source: recipe.Source{
				URL:    addr + "/source.tar.gz",
				SHA256: "deadbeef",
			},
			Build: recipe.Build{
				Steps: []string{"echo build"},
			},
		}, nil
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	w := syncItem{name: "stalep", version: "3.0.0"}

	out := runSyncOne(context.Background(), ctx, w, false)

	if !out.stale {
		t.Error("stale = false, want true: package missing .gale-deps.toml")
	}
	// Either result or installErr must be non-nil to confirm
	// Reinstall was attempted, not skipped.
	if out.result == nil && out.installErr == nil {
		t.Error("both result and installErr are nil: " +
			"Reinstall was not attempted for stale package")
	}
}

// TestRunSyncOneResolverFailurePopulatesResolveErr verifies that
// when the resolver returns an error, runSyncOne records it in
// resolveErr, does not attempt install, and returns result==nil.
func TestRunSyncOneResolverFailurePopulatesResolveErr(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	resolveErr := errors.New("resolver fail: no such package")
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return nil, resolveErr
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	w := syncItem{name: "ghostpkg", version: "1.0.0"}

	out := runSyncOne(context.Background(), ctx, w, false)

	if out.resolveErr == nil {
		t.Error("resolveErr = nil, want non-nil resolver error")
	}
	if out.installErr != nil {
		t.Errorf("installErr = %v, want nil: install should not be attempted after resolve failure",
			out.installErr)
	}
	if out.result != nil {
		t.Errorf("result = %v, want nil: no install should occur after resolve failure",
			out.result)
	}
}

// TestRunSyncOneInstallFailurePopulatesInstallErr verifies that
// when Install returns an error, runSyncOne records it in
// installErr and returns result==nil.
func TestRunSyncOneInstallFailurePopulatesInstallErr(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Use a closed server — install will fail immediately because
	// the source URL is unreachable.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()

	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return &recipe.Recipe{
			Package: recipe.Package{
				Name:    name,
				Version: "1.0.0",
			},
			Source: recipe.Source{
				URL:    addr + "/fail.tar.gz",
				SHA256: "deadbeef",
			},
			Build: recipe.Build{
				Steps: []string{"echo build"},
			},
		}, nil
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	w := syncItem{name: "failpkg", version: "1.0.0"}

	out := runSyncOne(context.Background(), ctx, w, false)

	if out.installErr == nil {
		t.Error("installErr = nil, want non-nil: install used a closed server")
	}
	if out.result != nil {
		t.Errorf("result = %v, want nil when install fails", out.result)
	}
}

// TestRunSyncOneDryRunUpToDate verifies that with dryRun=true and
// a package already installed (non-stale), runSyncOne returns
// upToDate=true and does not attempt an install.
func TestRunSyncOneDryRunUpToDate(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	// Seed store with the package present and non-stale.
	storeDir := seedStore(t, storeRoot, "drypkg", "4.0.0-1")
	writeDepsMetadataFile(t, storeDir)

	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		return minimalRecipe(name, "4.0.0"), nil
	}

	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	w := syncItem{name: "drypkg", version: "4.0.0"}

	storePathBefore, _ := store.NewStore(storeRoot).StorePath("drypkg", "4.0.0")
	entriesBefore, _ := os.ReadDir(storePathBefore)

	out := runSyncOne(context.Background(), ctx, w, true /* dryRun */)

	if !out.upToDate {
		t.Error("upToDate = false, want true: package is installed and non-stale")
	}
	if out.result != nil {
		t.Errorf("result = %v, want nil: dry-run should not install", out.result)
	}

	// Verify no side effects in the store dir.
	entriesAfter, _ := os.ReadDir(storePathBefore)
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("store dir changed during dry-run: before %d entries, after %d",
			len(entriesBefore), len(entriesAfter))
	}
}

// TestSortedSyncItemsReturnsAlphabeticalOrder verifies that
// sortedSyncItems converts a packages map to a []syncItem slice
// in stable alphabetical order by name, with versions travelling
// with their names.
//
// This pins the sorted-emission contract for runSync: per-package
// output lines are emitted in name order.
func TestSortedSyncItemsReturnsAlphabeticalOrder(t *testing.T) {
	pkgs := map[string]string{"zeta": "1", "alpha": "2", "mu": "3"}
	items, err := sortedSyncItems(
		pkgs, nil,
	)
	if err != nil {
		t.Fatalf("sortedSyncItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	got := []string{items[0].name, items[1].name, items[2].name}
	want := []string{"alpha", "mu", "zeta"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("items[%d].name = %q, want %q", i, got[i], want[i])
		}
	}
	// Versions must travel with their names.
	for _, item := range items {
		if pkgs[item.name] != item.version {
			t.Errorf("items[%s].version = %q, want %q",
				item.name, item.version, pkgs[item.name])
		}
	}
}

// TestSyncContinuesAfterPackageFailure pins the serial
// dispatch: a failure on the first sorted package does not
// hide later packages. runSync collects every outcome and
// reportSyncOutcomes still lists them.
func TestSyncContinuesAfterPackageFailure(t *testing.T) {
	tmp := t.TempDir()
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")

	for _, name := range []string{"beta", "gamma"} {
		dir := seedStore(t, storeRoot, name, "1.0.0-1")
		writeDepsMetadataFile(t, dir)
	}

	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var visited []string
	resolver := func(_ context.Context, name string) (*recipe.Recipe, error) {
		visited = append(visited, name)
		if name == "alpha" {
			return nil, errors.New("alpha missing")
		}
		return minimalRecipe(name, "1.0.0"), nil
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	pkgs := map[string]string{
		"gamma": "1.0.0",
		"alpha": "1.0.0",
		"beta":  "1.0.0",
	}
	items, err := sortedSyncItems(pkgs, nil)
	if err != nil {
		t.Fatalf("sortedSyncItems: %v", err)
	}

	var outcomes []syncOutcome
	for _, w := range items {
		outcomes = append(outcomes, runSyncOne(context.Background(), ctx, w, false))
	}

	gotNames := make([]string, len(outcomes))
	for i, o := range outcomes {
		gotNames[i] = o.name
	}
	if !slices.Equal(gotNames, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("outcome names = %v, want [alpha beta gamma]", gotNames)
	}
	if outcomes[0].resolveErr == nil {
		t.Fatal("alpha resolveErr = nil, want failure")
	}
	if !outcomes[1].upToDate || !outcomes[2].upToDate {
		t.Fatalf("later packages: beta upToDate=%v gamma upToDate=%v",
			outcomes[1].upToDate, outcomes[2].upToDate)
	}

	var buf bytes.Buffer
	_, failures := reportSyncOutcomes(newOutputForWriter(&buf), outcomes, false)
	if len(failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(failures))
	}
	text := buf.String()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(text, name) {
			t.Errorf("report missing %s:\n%s", name, text)
		}
	}
	if !slices.Equal(visited, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("resolver visits = %v, want [alpha beta gamma]", visited)
	}
}
