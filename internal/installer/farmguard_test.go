package installer

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// guardSoname is a versioned dylib basename for the current OS.
func guardSoname() string {
	if runtime.GOOS == "linux" {
		return "libguard.so.4"
	}
	return "libguard.4.dylib"
}

// guardStoreRoot returns a store root whose parent layout matches
// ~/.gale (the root must be named "pkg" or DirFromStoreDir refuses
// to derive a farm dir and the install paths skip farm wiring).
func guardStoreRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// refuse is a FarmGuard that always refuses with the guard's
// sentinel, standing in for a conflicting external claimant.
func refuse([]string) error {
	return fmt.Errorf("test refusal: %w", farm.ErrClaimConflict)
}

// TestReinstallFarmGuardRefusalKeepsFarm: a staged reinstall runs
// the farm guard before farm.Populate, so a refusal leaves the
// shared farm untouched (acceptance test 28: refused before any
// farm mutation).
func TestReinstallFarmGuardRefusalKeepsFarm(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	canonical := filepath.Join(storeRoot, "testpkg", "1.0-1")
	libDir := filepath.Join(canonical, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(libDir, guardSoname()), []byte("old"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	farmDir := farm.DirFromStoreDir(canonical)
	if err := farm.Populate(canonical, farmDir); err != nil {
		t.Fatal(err)
	}

	srcTar := createTestSourceTarGz(t)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, srcTar)
		},
	))
	defer srv.Close()

	inst := &Installer{
		Store:     store.NewStore(storeRoot),
		FarmGuard: refuse,
	}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source: recipe.Source{
			URL: srv.URL + "/source.tar.gz", SHA256: hashFile(t, srcTar),
		},
		Build: recipe.Build{Steps: []string{
			"mkdir -p $PREFIX/lib",
			"echo new > $PREFIX/lib/" + guardSoname(),
		}},
	}

	_, err := inst.Reinstall(r)
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("refused reinstall must surface the guard error, "+
			"got: %v", err)
	}
	if _, lerr := os.Readlink(
		filepath.Join(farmDir, guardSoname()),
	); lerr != nil {
		t.Errorf("farm mutated despite refusal: %v", lerr)
	}
}

// TestInstallBinaryFarmGuardRefusalDoesNotFallBack: a guard
// refusal on the binary path is a cross-project conflict, not a
// defect of the artifact, so it must abort the install rather than
// demote to a source build — the source build would only hit the
// same conflict at the generation rebuild after minutes of wasted
// work. The farm stays untouched.
func TestInstallBinaryFarmGuardRefusalDoesNotFallBack(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	tarzst := createTarZstdWithFiles(t, map[string]string{
		"lib/":                 "",
		"lib/" + guardSoname(): "bytes",
	})
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()
	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	inst := &Installer{
		Store:     store.NewStore(storeRoot),
		FarmGuard: refuse,
	}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL:    srv.URL + "/testpkg-1.0.tar.zst",
				SHA256: hashFile(t, tarzst),
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}

	_, err = inst.Install(r)
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("guard refusal must abort the install (no source "+
			"fallback), got: %v", err)
	}
	farmDir := filepath.Join(filepath.Dir(storeRoot), "lib")
	if entries, _ := os.ReadDir(farmDir); len(entries) != 0 {
		t.Errorf("farm mutated despite refusal: %v", entries)
	}
}

// TestInstallFarmGuardReceivesCanonicalDir: the guard is handed the
// canonical store dir (the path farm links will carry), not the
// staging dir, and an agreeing guard does not disturb the install.
func TestInstallFarmGuardReceivesCanonicalDir(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	tarzst := createTarZstdWithFiles(t, map[string]string{
		"lib/":                 "",
		"lib/" + guardSoname(): "bytes",
	})
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Write(blobData)
		},
	))
	defer srv.Close()
	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	var got []string
	inst := &Installer{
		Store: store.NewStore(storeRoot),
		FarmGuard: func(dirs []string) error {
			got = append(got, dirs...)
			return nil
		},
	}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL:    srv.URL + "/testpkg-1.0.tar.zst",
				SHA256: hashFile(t, tarzst),
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}

	if _, err := inst.Install(r); err != nil {
		t.Fatalf("agreeing guard must not disturb the install: %v", err)
	}
	canonical := filepath.Join(storeRoot, "testpkg", "1.0-1")
	if len(got) != 1 || got[0] != canonical {
		t.Errorf("guard saw %v, want [%s]", got, canonical)
	}
	if _, err := os.Readlink(filepath.Join(
		filepath.Dir(storeRoot), "lib", guardSoname(),
	)); err != nil {
		t.Errorf("farm entry missing after allowed install: %v", err)
	}
}
