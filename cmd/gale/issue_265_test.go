package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
)

// gh#265: an edited working-tree recipe that does not bump version
// or revision used to short-circuit to MethodCached at
// installLocked's IsInstalled check, so gale installed the previous
// recipe's bytes. These tests pin the pipeline: --recipe and
// --recipes both rebuild when the TOML changes, and cache-hit when
// it does not.

func TestInstallFromRecipeFileRebuildsWhenRecipeEdited(t *testing.T) {
	fx := newLocalRecipeFixture(t)

	if err := installFromRecipeFile(fx.ctx, fx.recipePath, fx.out); err != nil {
		t.Fatalf("first install: %v", err)
	}
	assertStoreMarker(t, fx.storeBin(), "one")

	fx.writeRecipe(fx.twoURL, fx.twoHash)
	if err := installFromRecipeFile(fx.ctx, fx.recipePath, fx.out); err != nil {
		t.Fatalf("second install after recipe edit: %v", err)
	}
	assertStoreMarker(t, fx.storeBin(), "two")
}

func TestInstallFromRecipeFileCachesUnchangedRecipe(t *testing.T) {
	fx := newLocalRecipeFixture(t)

	if err := installFromRecipeFile(fx.ctx, fx.recipePath, fx.out); err != nil {
		t.Fatalf("first install: %v", err)
	}
	hits := fx.hits.Load()
	if err := installFromRecipeFile(fx.ctx, fx.recipePath, fx.out); err != nil {
		t.Fatalf("second install of unchanged recipe: %v", err)
	}
	assertStoreMarker(t, fx.storeBin(), "one")
	if got := fx.hits.Load(); got != hits {
		t.Errorf("unchanged recipe fetched %d more times, want a cache hit",
			got-hits)
	}
}

func TestInstallFromRecipesDirRebuildsWhenRecipeEdited(t *testing.T) {
	fx := newLocalRecipeFixture(t)
	resolver := localRecipeResolver(fx.recipesDir)
	fx.ctx.Installer.Resolver = resolver
	fx.ctx.Resolver = resolver

	r, err := resolver(context.Background(), "testpkg")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := fx.ctx.Installer.InstallWithFinalize(context.Background(), r, false,
		func(_ *installer.InstallResult) error {
			return fx.ctx.FinalizeRecipeInstall(r)
		}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	assertStoreMarker(t, fx.storeBin(), "one")

	fx.writeRecipe(fx.twoURL, fx.twoHash)
	r2, err := resolver(context.Background(), "testpkg")
	if err != nil {
		t.Fatalf("resolve after edit: %v", err)
	}
	result, err := fx.ctx.Installer.InstallWithFinalize(context.Background(), r2, false,
		func(_ *installer.InstallResult) error {
			return fx.ctx.FinalizeRecipeInstall(r2)
		})
	if err != nil {
		t.Fatalf("second install after recipe edit: %v", err)
	}
	if result.Method == installer.MethodCached {
		t.Fatal("method = cached after editing the working-tree recipe; " +
			"the previous artifact must not be reused")
	}
	assertStoreMarker(t, fx.storeBin(), "two")
}

func TestRunSyncOneRebuildsWhenLocalRecipeEdited(t *testing.T) {
	fx := newLocalRecipeFixture(t)
	resolver := localRecipeResolver(fx.recipesDir)
	fx.ctx.Installer.Resolver = resolver
	fx.ctx.Resolver = resolver

	r, err := resolver(context.Background(), "testpkg")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := fx.ctx.Installer.Install(context.Background(), r); err != nil {
		t.Fatalf("first install: %v", err)
	}
	assertStoreMarker(t, fx.storeBin(), "one")

	fx.writeRecipe(fx.twoURL, fx.twoHash)
	out := runSyncOne(context.Background(), fx.ctx, syncItem{name: "testpkg", version: "1.0.0"}, false)
	if out.installErr != nil {
		t.Fatalf("sync after recipe edit: %v", out.installErr)
	}
	if out.resolveErr != nil {
		t.Fatalf("sync resolve after recipe edit: %v", out.resolveErr)
	}
	if out.upToDate {
		t.Fatal("sync reported up to date after editing the working-tree " +
			"recipe; it must reinstall")
	}
	assertStoreMarker(t, fx.storeBin(), "two")
}

type localRecipeFixture struct {
	t          *testing.T
	ctx        *cmdContext
	out        *output.Output
	recipePath string
	recipesDir string
	storeRoot  string
	oneURL     string
	oneHash    string
	twoURL     string
	twoHash    string
	hits       *atomic.Int32
}

func newLocalRecipeFixture(t *testing.T) *localRecipeFixture {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	onePath, oneHash := tarZstArchive(t, archiveEntry{
		name: "bin/testpkg", body: "one\n", mode: 0o755,
	})
	twoPath, twoHash := tarZstArchive(t, archiveEntry{
		name: "bin/testpkg", body: "two\n", mode: 0o755,
	})
	hits := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			switch r.URL.Path {
			case "/one.tar.zst":
				http.ServeFile(w, r, onePath)
			case "/two.tar.zst":
				http.ServeFile(w, r, twoPath)
			default:
				http.NotFound(w, r)
			}
		},
	))
	t.Cleanup(srv.Close)

	recipesDir := filepath.Join(tmp, "gale-recipes", "recipes")
	bucket := filepath.Join(recipesDir, "t")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	recipePath := filepath.Join(bucket, "testpkg.toml")

	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fx := &localRecipeFixture{
		t:          t,
		recipePath: recipePath,
		recipesDir: recipesDir,
		storeRoot:  storeRoot,
		oneURL:     srv.URL + "/one.tar.zst",
		oneHash:    oneHash,
		twoURL:     srv.URL + "/two.tar.zst",
		twoHash:    twoHash,
		hits:       hits,
		out:        output.New(os.Stderr, false),
		ctx: &cmdContext{
			GalePath:  configPath,
			GaleDir:   galeDir,
			StoreRoot: storeRoot,
			Installer: &installer.Installer{
				Store: store.NewStore(storeRoot),
			},
		},
	}
	fx.writeRecipe(fx.oneURL, fx.oneHash)
	return fx
}

func (fx *localRecipeFixture) writeRecipe(url, hash string) {
	fx.t.Helper()
	platform := runtime.GOOS + "-" + runtime.GOARCH
	body := strings.Join([]string{
		`[package]`,
		`name = "testpkg"`,
		`version = "1.0.0"`,
		`revision = 1`,
		``,
		`[source]`,
		`url = "https://example.invalid/testpkg-1.0.0.tar.gz"`,
		`sha256 = "` + strings.Repeat("0", 64) + `"`,
		``,
		fmt.Sprintf(`[binary.%s]`, platform),
		`url = "` + url + `"`,
		`sha256 = "` + hash + `"`,
		`trust = "sha256-only"`,
		``,
		`[build]`,
		`steps = ["true"]`,
	}, "\n")
	if err := os.WriteFile(fx.recipePath, []byte(body), 0o644); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *localRecipeFixture) storeBin() string {
	return filepath.Join(fx.storeRoot, "testpkg", "1.0.0-1", "bin", "testpkg")
}

func assertStoreMarker(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store binary: %v", err)
	}
	if string(got) != want+"\n" {
		t.Errorf("store binary = %q, want %q", got, want+"\n")
	}
}
