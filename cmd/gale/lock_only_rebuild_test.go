package main

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

const (
	lockRebuildSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	lockRebuildPkg = "just"
	lockRebuildVer = "1.0.0"
	lockRebuildAlt = "9.9.9"
)

type lockRebuildFix struct {
	home, galeDir, storeRoot, configPath, lockPath string
}

func newLockRebuildGlobal(t *testing.T) *lockRebuildFix {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	t.Chdir(t.TempDir())
	galeDir := filepath.Join(home, ".gale")
	fx := &lockRebuildFix{
		home:       home,
		galeDir:    galeDir,
		storeRoot:  filepath.Join(galeDir, "pkg"),
		configPath: filepath.Join(galeDir, "gale.toml"),
		lockPath:   filepath.Join(galeDir, "gale.lock"),
	}
	if err := os.MkdirAll(fx.storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return fx
}

func newLockRebuildProject(t *testing.T) *lockRebuildFix {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	proj := t.TempDir()
	t.Chdir(proj)
	fx := &lockRebuildFix{
		home:       home,
		galeDir:    filepath.Join(proj, ".gale"),
		storeRoot:  filepath.Join(home, ".gale", "pkg"),
		configPath: filepath.Join(proj, "gale.toml"),
		lockPath:   filepath.Join(proj, "gale.lock"),
	}
	if err := os.MkdirAll(fx.storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fx.galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return fx
}

func (fx *lockRebuildFix) writeManifest(t *testing.T, pin, extra string) {
	t.Helper()
	body := "[packages]\n" + lockRebuildPkg + " = \"" + pin + "\"\n" + extra
	if err := os.MkdirAll(filepath.Dir(fx.configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (fx *lockRebuildFix) plantResolveDir(t *testing.T, version string) {
	t.Helper()
	bin := filepath.Join(fx.storeRoot, lockRebuildPkg, version, "bin", lockRebuildPkg)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("resolve-"+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (fx *lockRebuildFix) fetchDir(t *testing.T) string {
	t.Helper()
	dest, err := store.NewStore(fx.storeRoot).FetchPath(
		lockRebuildPkg, lockRebuildVer, lockRebuildSHA,
	)
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func (fx *lockRebuildFix) plantFetch(t *testing.T) {
	t.Helper()
	dest := fx.fetchDir(t)
	bin := filepath.Join(dest, "bin", lockRebuildPkg)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("fetch-"+lockRebuildVer+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (fx *lockRebuildFix) writeV2(t *testing.T, lf *lockfile.V2) {
	t.Helper()
	if err := lockfile.WriteV2(fx.lockPath, lf); err != nil {
		t.Fatal(err)
	}
}

func (fx *lockRebuildFix) completeV2(host map[string]lockfile.Target) *lockfile.V2 {
	lf := &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{lockRebuildPkg + "@" + lockRebuildVer}},
			Host:    host,
		},
		Packages: map[string]lockfile.V2Package{
			lockRebuildPkg + "@" + lockRebuildVer: {
				Artifacts: map[string]lockfile.V2Artifact{
					currentPlatform(): {
						URL:    "https://github.com/kelp/just/releases/download/1.0.0/just",
						Format: "binary",
						SHA256: lockRebuildSHA,
						Method: provenance.MethodFetch,
					},
				},
			},
		},
	}
	return lf
}

func (fx *lockRebuildFix) versions(t *testing.T) map[string]string {
	t.Helper()
	got, err := generation.CurrentVersions(fx.galeDir, fx.storeRoot)
	if err != nil {
		t.Fatalf("current versions: %v", err)
	}
	return got
}

func (fx *lockRebuildFix) linked(t *testing.T) string {
	t.Helper()
	dirs, err := generation.CurrentStoreDirs(fx.galeDir, fx.storeRoot)
	if err != nil {
		t.Fatalf("current store dirs: %v", err)
	}
	return dirs[lockRebuildPkg]
}

func TestRebuildGenerationUsesLockNotManifest(t *testing.T) {
	t.Run("global", func(t *testing.T) {
		assertRebuildUsesLock(t, newLockRebuildGlobal(t))
	})
	t.Run("project", func(t *testing.T) {
		assertRebuildUsesLock(t, newLockRebuildProject(t))
	})
}

func assertRebuildUsesLock(t *testing.T, fx *lockRebuildFix) {
	t.Helper()
	fx.writeManifest(t, lockRebuildAlt, "")
	fx.plantResolveDir(t, lockRebuildAlt)
	fx.plantFetch(t)
	fx.writeV2(t, fx.completeV2(nil))

	if err := rebuildGeneration(fx.galeDir, fx.storeRoot, fx.configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}
	got := fx.versions(t)
	want := map[string]string{lockRebuildPkg: lockRebuildVer}
	if !maps.Equal(got, want) {
		t.Fatalf("generation versions = %v, want lock %v", got, want)
	}
}

func TestRebuildGenerationLinksFetchNotResolveDir(t *testing.T) {
	fx := newLockRebuildGlobal(t)
	fx.writeManifest(t, lockRebuildVer, "")
	fx.plantResolveDir(t, lockRebuildVer)
	fx.plantFetch(t)
	fx.writeV2(t, fx.completeV2(nil))

	if err := rebuildGeneration(fx.galeDir, fx.storeRoot, fx.configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}
	got := fx.linked(t)
	want := fx.fetchDir(t)
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("linked %q, want fetch %q", got, want)
	}
}

func TestRebuildGenerationRefusesV1Lock(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		fx := newLockRebuildProject(t)
		fx.writeManifest(t, lockRebuildVer, "")
		fx.plantResolveDir(t, lockRebuildVer)
		if err := rebuildGeneration(fx.galeDir, fx.storeRoot, fx.configPath, nil); err != nil {
			t.Fatalf("unlocked seed: %v", err)
		}
		before := fx.versions(t)
		writeScopeLock(t, fx.lockPath, lockRebuildPkg+"@"+lockRebuildVer+"-1", lockRebuildSHA)

		err := rebuildGeneration(fx.galeDir, fx.storeRoot, fx.configPath, nil)
		if !errors.Is(err, errSwitchV1) {
			t.Fatalf("err = %v, want errSwitchV1", err)
		}
		if !strings.Contains(err.Error(), "gale fetch-adopt") {
			t.Errorf("v1 refuse must name fetch-adopt, got %v", err)
		}
		if after := fx.versions(t); !maps.Equal(after, before) {
			t.Errorf("generation moved: before %v after %v", before, after)
		}
	})
}

func TestRebuildGenerationRefusesIncompleteV2(t *testing.T) {
	otherPlat := "darwin/amd64"
	if currentPlatform() == otherPlat {
		otherPlat = "linux/amd64"
	}
	cases := []struct {
		name string
		art  map[string]lockfile.V2Artifact
	}{
		{
			name: "missing-platform",
			art: map[string]lockfile.V2Artifact{
				otherPlat: {
					URL:    "https://github.com/kelp/just/releases/download/1.0.0/just",
					Format: "binary",
					SHA256: lockRebuildSHA,
					Method: provenance.MethodFetch,
				},
			},
		},
		{
			name: "empty-sha",
			art: map[string]lockfile.V2Artifact{
				currentPlatform(): {
					URL:    "https://github.com/kelp/just/releases/download/1.0.0/just",
					Format: "binary",
					Method: provenance.MethodFetch,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newLockRebuildGlobal(t)
			fx.writeManifest(t, lockRebuildVer, "")
			fx.plantResolveDir(t, lockRebuildVer)
			fx.writeV2(t, &lockfile.V2{
				Version: lockfile.SchemaV2,
				Targets: lockfile.Targets{
					Default: &lockfile.Target{Roots: []string{lockRebuildPkg + "@" + lockRebuildVer}},
				},
				Packages: map[string]lockfile.V2Package{
					lockRebuildPkg + "@" + lockRebuildVer: {Artifacts: tc.art},
				},
			})

			err := rebuildGeneration(fx.galeDir, fx.storeRoot, fx.configPath, nil)
			if !errors.Is(err, lockfile.ErrMissingArtifact) {
				t.Fatalf("err = %v, want ErrMissingArtifact", err)
			}
			if strings.Contains(err.Error(), "gale fetch-adopt") {
				t.Errorf("incomplete v2 must not name fetch-adopt, got %v", err)
			}
			if got := fx.versions(t); len(got) != 0 {
				t.Errorf("generation moved on incomplete v2: %v", got)
			}
		})
	}
}

func TestRebuildGenerationIgnoresLeftoverHostPackages(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	fx := newLockRebuildGlobal(t)
	fx.writeManifest(t, lockRebuildVer,
		"\n[hosts.testhost.packages]\n"+lockRebuildPkg+" = \""+lockRebuildAlt+"\"\n")
	fx.plantResolveDir(t, lockRebuildAlt)
	fx.plantFetch(t)
	fx.writeV2(t, fx.completeV2(nil))

	if err := rebuildGeneration(fx.galeDir, fx.storeRoot, fx.configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}
	got := fx.versions(t)
	want := map[string]string{lockRebuildPkg: lockRebuildVer}
	if !maps.Equal(got, want) {
		t.Fatalf("generation versions = %v, want lock %v (leftover host packages must not select)",
			got, want)
	}
}

func TestRebuildGenerationRefusesHostTarget(t *testing.T) {
	fx := newLockRebuildGlobal(t)
	fx.writeManifest(t, lockRebuildVer, "")
	fx.plantFetch(t)
	fx.writeV2(t, fx.completeV2(map[string]lockfile.Target{
		"testhost": {Roots: []string{lockRebuildPkg + "@" + lockRebuildVer}},
	}))

	err := rebuildGeneration(fx.galeDir, fx.storeRoot, fx.configPath, nil)
	if !errors.Is(err, errSwitchHosts) {
		t.Fatalf("err = %v, want errSwitchHosts", err)
	}
}

func TestRebuildGenerationCanonicalDoesNotResolveRecipesUnderV2(t *testing.T) {
	fx := newLockRebuildGlobal(t)
	fx.writeManifest(t, lockRebuildVer, "")
	fx.plantFetch(t)
	fx.writeV2(t, fx.completeV2(nil))

	called := false
	err := rebuildGeneration(fx.galeDir, fx.storeRoot, fx.configPath,
		func(name, version string) (*recipe.Recipe, error) {
			called = true
			return &recipe.Recipe{Package: recipe.Package{
				Name: name, Version: version, Revision: 9,
			}}, nil
		})
	if err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}
	if called {
		t.Fatal("recipe resolver must not run when a v2 lock is present")
	}
	got := fx.versions(t)
	want := map[string]string{lockRebuildPkg: lockRebuildVer}
	if !maps.Equal(got, want) {
		t.Fatalf("generation versions = %v, want %v", got, want)
	}
}

func TestRebuildInputsRefusesStalePlan(t *testing.T) {
	fx := newLockRebuildGlobal(t)
	fx.writeManifest(t, lockRebuildVer, "")
	fx.plantFetch(t)
	fx.writeV2(t, fx.completeV2(nil))

	err := rebuildGenerationWith(genRebuild{
		galeDir:    fx.galeDir,
		storeRoot:  fx.storeRoot,
		configPath: fx.configPath,
		pkgs:       map[string]string{lockRebuildPkg: lockRebuildAlt},
	})
	if err == nil {
		t.Fatal("stale r.pkgs must refuse")
	}
	if errors.Is(err, errSwitchV1) || errors.Is(err, errSwitchHosts) {
		t.Errorf("stale plan must not look like a lock-kind refuse, got %v", err)
	}
}

func TestRebuildUnderLockDoesNotSkipResolveDirWhenLockHasFetch(t *testing.T) {
	fx := newLockRebuildGlobal(t)
	fx.writeManifest(t, lockRebuildVer, "")
	fx.plantResolveDir(t, lockRebuildVer)
	fx.plantFetch(t)
	if err := generation.Build(
		map[string]string{lockRebuildPkg: lockRebuildVer},
		fx.galeDir, fx.storeRoot,
	); err != nil {
		t.Fatalf("seed ResolveDir generation: %v", err)
	}
	if filepath.Clean(fx.linked(t)) != filepath.Clean(
		filepath.Join(fx.storeRoot, lockRebuildPkg, lockRebuildVer),
	) {
		t.Fatalf("setup: want ResolveDir link, got %q", fx.linked(t))
	}
	fx.writeV2(t, fx.completeV2(nil))

	if err := rebuildUnderLock(genRebuild{
		galeDir:    fx.galeDir,
		storeRoot:  fx.storeRoot,
		configPath: fx.configPath,
	}, recoveryRebuild{skipUnchanged: true}); err != nil {
		t.Fatalf("rebuildUnderLock: %v", err)
	}
	got := fx.linked(t)
	want := fx.fetchDir(t)
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("linked %q, want fetch %q (skipUnchanged must not treat ResolveDir as done)",
			got, want)
	}
}

func TestRebuildUnderLockRefusesHostTargetEvenWhenDefaultMatches(t *testing.T) {
	fx := newLockRebuildGlobal(t)
	fx.writeManifest(t, lockRebuildVer, "")
	fx.plantResolveDir(t, lockRebuildVer)
	fx.plantFetch(t)
	if err := generation.Build(
		map[string]string{lockRebuildPkg: lockRebuildVer},
		fx.galeDir, fx.storeRoot,
	); err != nil {
		t.Fatalf("seed matching default: %v", err)
	}
	fx.writeV2(t, fx.completeV2(map[string]lockfile.Target{
		"testhost": {Roots: []string{lockRebuildPkg + "@" + lockRebuildVer}},
	}))

	err := rebuildUnderLock(genRebuild{
		galeDir:    fx.galeDir,
		storeRoot:  fx.storeRoot,
		configPath: fx.configPath,
	}, recoveryRebuild{skipUnchanged: true})
	if !errors.Is(err, errSwitchHosts) {
		t.Fatalf("err = %v, want errSwitchHosts (must not skip-succeed)", err)
	}
}
