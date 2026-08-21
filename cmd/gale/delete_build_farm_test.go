package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
)

func farmLibBesideStore(storeRoot string) string {
	return filepath.Join(filepath.Dir(storeRoot), "lib")
}

func homeFarmLib(home string) string {
	return filepath.Join(home, ".gale", "lib")
}

func TestInstallFetchDoesNotCreateFarmLib(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	_ = os.RemoveAll(farmLibBesideStore(fx.c.StoreRoot))
	_ = os.RemoveAll(homeFarmLib(fx.home))
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installToStore = stageTestFetch
	t.Cleanup(func() { installToStore = nil })

	if err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := lockfile.ReadV2(fx.lockPath()); err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	if _, err := os.Stat(farmLibBesideStore(fx.c.StoreRoot)); !os.IsNotExist(err) {
		t.Errorf("install created farm lib %s, err=%v", farmLibBesideStore(fx.c.StoreRoot), err)
	}
	if _, err := os.Stat(homeFarmLib(fx.home)); !os.IsNotExist(err) {
		t.Errorf("install created ~/.gale/lib, err=%v", err)
	}
}

func TestInstallFetchIgnoresPreexistingFarmLib(t *testing.T) {
	clearAdoptCI(t)
	fx := newLockFetchFix(t)
	lib := farmLibBesideStore(fx.c.StoreRoot)
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(lib, "leftover-marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.c.GalePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installToStore = stageTestFetch
	t.Cleanup(func() { installToStore = nil })

	if err := runInstallFetch(context.Background(), fx.c, "just", "1.56.0", fx.src); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("pre-existing farm lib was rebuilt or removed: %v", err)
	}
	if string(got) != "keep" {
		t.Errorf("marker = %q, want keep", got)
	}
}

func TestRollbackCommandDoesNotCreateFarmLib(t *testing.T) {
	_, storeRoot, _ := rollbackTempProject(t)
	lib := farmLibBesideStore(storeRoot)
	_ = os.RemoveAll(lib)

	if err := genRollbackCmd.RunE(genRollbackCmd, nil); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Stat(lib); !os.IsNotExist(err) {
		t.Errorf("rollback created %s, err=%v", lib, err)
	}
}

func TestMigrateRefuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	storeRoot := filepath.Join(home, ".gale", "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := &cmdContext{
		GaleDir:   filepath.Join(home, ".gale"),
		StoreRoot: storeRoot,
	}
	err := runMigrate(ctx, discardOutput())
	if err == nil {
		t.Fatal("gale migrate must refuse")
	}
	msg := err.Error()
	if !strings.Contains(msg, "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
	if !strings.Contains(msg, "fetch-adopt") {
		t.Errorf("refusal must name fetch-adopt: %v", err)
	}
}

func TestProductionHasNoGHCR(t *testing.T) {
	root := findGoModRoot(t)
	if _, err := os.Stat(filepath.Join(root, "internal", "ghcr")); err == nil {
		t.Error("internal/ghcr still exists")
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "tmp":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		imp := "github.com/kelp/gale/internal/" + "ghcr"
		if strings.Contains(string(data), `"`+imp+`"`) {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s still imports internal/ghcr", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProductionHasNoBuildOrFarmImport(t *testing.T) {
	root := findGoModRoot(t)
	for _, pkg := range []string{"internal/build", "internal/farm"} {
		if _, err := os.Stat(filepath.Join(root, pkg)); err == nil {
			t.Errorf("%s still exists", pkg)
		}
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "tmp":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		buildImp := "github.com/kelp/gale/internal/" + "build"
		farmImp := "github.com/kelp/gale/internal/" + "farm"
		if strings.Contains(text, `"`+buildImp+`"`) ||
			strings.Contains(text, `"`+farmImp+`"`) {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s still imports internal/build or internal/farm", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func findGoModRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
