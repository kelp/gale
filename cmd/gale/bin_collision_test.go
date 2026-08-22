package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

func collidingBinHome(t *testing.T) (galeDir, storeRoot, configPath string) {
	t.Helper()
	galeDir, storeRoot = setupGCHome(t)
	configPath = filepath.Join(galeDir, "gale.toml")
	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")
	return galeDir, storeRoot, configPath
}

func assertBinCollision(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want bin collision, got nil")
	}
	var collErr *generation.BinCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error type = %T (%v), want *generation.BinCollisionError",
			err, err)
	}
	msg := err.Error()
	for _, want := range []string{"foo", "alpha", "beta", "remove"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "[bin]") {
		t.Errorf("error still advertises [bin]: %q", msg)
	}
}

func TestBinTableDoesNotSettleCollision(t *testing.T) {
	galeDir, storeRoot, configPath := collidingBinHome(t)
	writeGlobalConfig(t, galeDir, "[packages]\nalpha = \"1.0\"\n")
	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("seed rebuild: %v", err)
	}
	if cur, err := generation.Current(galeDir); err != nil || cur != 1 {
		t.Fatalf("setup: current = %d (err=%v), want 1", cur, err)
	}

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")
	err := rebuildGeneration(galeDir, storeRoot, configPath, nil)
	assertBinCollision(t, err)
	if cur, err := generation.Current(galeDir); err != nil || cur != 1 {
		t.Fatalf("current = %d (err=%v), want 1 — leftover [bin] must not swap",
			cur, err)
	}
}

func TestHostBinDoesNotSettleCollision(t *testing.T) {
	galeDir, storeRoot, configPath := collidingBinHome(t)
	t.Setenv("GALE_HOST", "testhost")
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"alpha\"\n\n"+
			"[hosts.testhost.bin]\nfoo = \"beta\"\n")
	assertBinCollision(t, rebuildGeneration(galeDir, storeRoot, configPath, nil))
}

func TestLockedRebuildLeftoverBinDoesNotSettle(t *testing.T) {
	galeDir, storeRoot, configPath := collidingBinHome(t)
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")
	assertBinCollision(t, rebuildGenerationWith(genRebuild{
		galeDir:    galeDir,
		storeRoot:  storeRoot,
		configPath: configPath,
		pkgs:       map[string]string{"alpha": "1.0", "beta": "1.0"},
	}))
}

func TestFinalizeFetchLeftoverBinDoesNotSettle(t *testing.T) {
	galeDir, storeRoot, configPath := collidingBinHome(t)
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"beta\"\n")
	sha := strings.Repeat("ab", 32)
	tree := mappedBinDigest(t, "foo", "ok")
	stage := func(
		_ context.Context, st *store.Store, name, version string, _ index.Artifact,
	) (string, error) {
		dest, err := st.FetchPath(name, version, sha)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Join(dest, "bin"), 0o755); err != nil {
			return "", err
		}
		return "", os.WriteFile(filepath.Join(dest, "bin", "foo"), []byte("ok"), 0o755)
	}
	fetchArtifacts := func(name string) map[string]lockfile.V2Artifact {
		return map[string]lockfile.V2Artifact{
			currentPlatform(): {
				URL:        "https://example.invalid/" + name,
				Format:     "binary",
				SHA256:     sha,
				TreeDigest: tree,
				Method:     provenance.MethodFetch,
				Files: []lockfile.V2File{{
					Src: "foo", Dest: "bin/foo", Mode: 0o755,
				}},
			},
		}
	}
	lf := &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{
				Roots: []string{"alpha@1.0", "beta@1.0"},
			},
		},
		Packages: map[string]lockfile.V2Package{
			"alpha@1.0": {Artifacts: fetchArtifacts("alpha")},
			"beta@1.0":  {Artifacts: fetchArtifacts("beta")},
		},
	}
	c := &cmdContext{
		GalePath:  configPath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
	}
	err := finalizeFetch(context.Background(), c, fetchPublish{
		Lock: lf,
		Arts: []fetchArt{
			{Name: "alpha", Version: "1.0",
				Art: index.Artifact{SHA256: sha, TreeDigest: tree}},
			{Name: "beta", Version: "1.0",
				Art: index.Artifact{SHA256: sha, TreeDigest: tree}},
		},
		ToStore: stage,
	})
	assertBinCollision(t, err)
}

func TestUndeclaredLeftoverBinStillLoads(t *testing.T) {
	galeDir, _, configPath := collidingBinHome(t)
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"gamma\"\n")
	if _, err := loadEffectiveConfig(configPath); err != nil {
		t.Fatalf("leftover undeclared [bin] must still load: %v", err)
	}
}
