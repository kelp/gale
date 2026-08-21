package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
)

func TestLockRefreshFlagGone(t *testing.T) {
	if lockCmd.Flags().Lookup("refresh") != nil {
		t.Fatal("gale lock: --refresh must be deleted")
	}
}

func TestLockRefusesPackageArgs(t *testing.T) {
	err := checkLockArgs([]string{"jq"})
	if !errors.Is(err, errLockTakesNoPackages) {
		t.Errorf("err = %v, want errLockTakesNoPackages", err)
	}
	if !strings.Contains(err.Error(), "fetch-adopt") {
		t.Errorf("refusal must name fetch-adopt: %v", err)
	}
	if err := checkLockArgs(nil); err != nil {
		t.Errorf("plain `gale lock` refused: %v", err)
	}
}

func TestLockDoesNotReplaceUnprovenanced(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := lockCtx(t, tmp, "[packages]\n  hello = \"1.0.0\"\n",
		map[string]string{"hello": "1.0.0"})
	dir := seedStore(t, ctx.StoreRoot, "hello", "1.0.0-1")
	marker := filepath.Join(dir, "legacy-marker")
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runLock(ctx, "", discardOutput())
	if !errors.Is(err, errUnprovenancedStoreDir) {
		t.Fatalf("err = %v, want errUnprovenancedStoreDir", err)
	}
	if strings.Contains(err.Error(), "--refresh") {
		t.Errorf("dead --refresh named as a remedy: %v", err)
	}
	if !strings.Contains(err.Error(), "fetch-adopt") {
		t.Errorf("unprovenanced remedy must name fetch-adopt: %v", err)
	}
	kept, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("store dir was replaced: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("marker holds %q, want old", kept)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, provenance.File)); !os.IsNotExist(statErr) {
		t.Errorf("lock stamped provenance onto unverified bytes: %v", statErr)
	}
}

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

func TestWiredFarmGuardsDifferOnAnIncompleteClosure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ctx := buildableCtx(t, tmp, "ghostdep")
	wireFarmGuards(ctx.Installer, ctx.GaleDir, ctx.StoreRoot)
	dir := seedStore(t, ctx.StoreRoot, "ghostdep", "1.0-1")
	if err := ctx.RebuildGeneration(); err != nil {
		t.Fatal(err)
	}

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
