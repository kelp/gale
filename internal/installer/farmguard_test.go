package installer

import (
	"bytes"
	"context"
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
func refuse([]farm.Placement) error {
	return fmt.Errorf("test refusal: %w", farm.ErrClaimConflict)
}

// TestReinstallFarmGuardRefusalKeepsFarmAndStore: a staged
// reinstall runs the farm guard before it touches anything, so a
// refusal leaves both the shared farm and the canonical store dir
// exactly as they were (acceptance test 28: refused before any farm
// mutation).
//
// The store half is not incidental. Replacing the canonical dir
// first would change bytes an existing generation already reaches,
// and it would also let the operation erase its own evidence: an
// external claimant resolving that same canonical path would
// enumerate the NEW contents, so a conflict over the old ones could
// no longer be seen.
func TestReinstallFarmGuardRefusalKeepsFarmAndStore(t *testing.T) {
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

	_, err := inst.Reinstall(context.Background(), r)
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("refused reinstall must surface the guard error, "+
			"got: %v", err)
	}
	if _, lerr := os.Readlink(
		filepath.Join(farmDir, guardSoname()),
	); lerr != nil {
		t.Errorf("farm mutated despite refusal: %v", lerr)
	}
	kept, rerr := os.ReadFile(filepath.Join(libDir, guardSoname()))
	if rerr != nil {
		t.Fatalf("canonical store dir gone despite refusal: %v", rerr)
	}
	if string(kept) != "old" {
		t.Errorf("canonical dir holds %q, want the pre-install "+
			"bytes %q: a refusal must replace nothing",
			kept, "old")
	}
}

// DeferFarm commits the store and leaves the shared farm alone,
// guard included.
//
// `gale migrate` relocating a pre-revision install needs exactly
// this. The scopes loading those bytes claim the soname at the BARE
// directory, so the canonical copy this install commits proposes the
// same soname at a different path and the per-commit guard must
// refuse it — the guard would veto the one operation that ends the
// disagreement (design §4, §13). The same reasoning already defers
// the farm under a locked plan, where one claim per commit cannot see
// a conflict two roots only reach in the plan's final closure.
//
// What the deferral does NOT do is skip a check: the farm is
// unmutated, so nothing has been decided about it yet, and the caller
// owes it one guarded machine-wide rebuild before anything relies on
// the result.
func TestDeferFarmCommitsWithoutTouchingTheFarm(t *testing.T) {
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
		Store: store.NewStore(storeRoot),
		// A guard standing in for the scope that claims this soname
		// from the pre-revision directory: it refuses everything.
		FarmGuard: refuse,
		DeferFarm: true,
	}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "deferpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			runtime.GOOS + "-" + runtime.GOARCH: {
				URL:    srv.URL + "/deferpkg-1.0.tar.zst",
				SHA256: hashFile(t, tarzst),
				Trust:  recipe.TrustSHA256Only,
			},
		},
	}

	// Reinstall, because that is the path migrate takes: it forces a
	// refetch of a directory the store already counts as installed.
	if _, err := inst.Reinstall(context.Background(), r); err != nil {
		t.Fatalf("a deferred install must not consult the guard: %v", err)
	}
	canonical := filepath.Join(storeRoot, "deferpkg", "1.0-1")
	if _, err := os.Stat(
		filepath.Join(canonical, "lib", guardSoname()),
	); err != nil {
		t.Fatalf("the artifact was not committed: %v", err)
	}
	farmDir := farm.DirFromStoreDir(canonical)
	if entries, _ := os.ReadDir(farmDir); len(entries) != 0 {
		t.Errorf("the farm holds %v, want nothing: population is the "+
			"caller's to run once the whole pass is committed", entries)
	}
}

// TestReplaceRemovesStaleFarmLinks: a staged replacement must drop
// farm entries the new artifact does not provide.
//
// GuardPopulate cannot see them — it only checks sonames the
// proposed closure touches — and Populate only creates or repoints
// the ones the new dir exports. So a soname the OLD artifact
// exported and the new one does not keeps its link, now pointing
// into the replaced directory at a file that no longer exists.
//
// gale sync survives this only by accident: it rebuilds the
// generation after any install, and that rebuild wipes and
// repopulates the whole farm. A command that replaces without
// rebuilding would leave the dangling entry permanently.
func TestReplaceRemovesStaleFarmLinks(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	canonical := filepath.Join(storeRoot, "stalepkg", "1.0-1")
	libDir := filepath.Join(canonical, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The old artifact exports a soname the rebuilt one will not:
	// fallbackRecipe's build writes a bin and no lib at all.
	if err := os.WriteFile(
		filepath.Join(libDir, guardSoname()), []byte("old"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	farmDir := farm.DirFromStoreDir(canonical)
	if err := farm.Populate(canonical, farmDir); err != nil {
		t.Fatal(err)
	}

	r, closeSrv := fallbackRecipe(t, "stalepkg")
	defer closeSrv()

	inst := &Installer{
		Store:             store.NewStore(storeRoot),
		BinaryFallbackLog: &bytes.Buffer{},
	}
	if _, err := inst.Reinstall(context.Background(), r); err != nil {
		t.Fatalf("Reinstall: %v", err)
	}

	link := filepath.Join(farmDir, guardSoname())
	target, err := os.Readlink(link)
	if err == nil {
		t.Errorf("the farm still links %s -> %s, which the replacement "+
			"no longer provides", link, target)
	} else if !os.IsNotExist(err) {
		t.Errorf("reading the farm entry: %v", err)
	}
}

// A soname the replacement drops is a soname some other scope may
// still require, so dropping it is guarded exactly as removal is:
// deletion violates a claim as surely as retargeting does.
func TestReplaceRefusesToDropAClaimedSoname(t *testing.T) {
	storeRoot := guardStoreRoot(t)
	canonical := filepath.Join(storeRoot, "claimedpkg", "1.0-1")
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

	r, closeSrv := fallbackRecipe(t, "claimedpkg")
	defer closeSrv()

	var sawStale []string
	inst := &Installer{
		Store:             store.NewStore(storeRoot),
		BinaryFallbackLog: &bytes.Buffer{},
		FarmRemoveGuard: func(_ []farm.Placement, stale []string) error {
			sawStale = stale
			return fmt.Errorf("claimed: %w", farm.ErrClaimConflict)
		},
	}

	if _, err := inst.Reinstall(context.Background(), r); !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("err = %v, want the removal guard's refusal", err)
	}
	if len(sawStale) != 1 || sawStale[0] != guardSoname() {
		t.Errorf("guard saw stale = %v, want [%s]", sawStale, guardSoname())
	}
	// Refused before anything moved: the old bytes and the old farm
	// entry are both still there.
	if _, err := os.Readlink(filepath.Join(farmDir, guardSoname())); err != nil {
		t.Errorf("farm mutated despite refusal: %v", err)
	}
	kept, err := os.ReadFile(filepath.Join(libDir, guardSoname()))
	if err != nil {
		t.Fatalf("store dir replaced despite refusal: %v", err)
	}
	if string(kept) != "old" {
		t.Errorf("lib holds %q, want %q", kept, "old")
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

	_, err = inst.Install(context.Background(), r)
	if !errors.Is(err, farm.ErrClaimConflict) {
		t.Fatalf("guard refusal must abort the install (no source "+
			"fallback), got: %v", err)
	}
	farmDir := filepath.Join(filepath.Dir(storeRoot), "lib")
	if entries, _ := os.ReadDir(farmDir); len(entries) != 0 {
		t.Errorf("farm mutated despite refusal: %v", entries)
	}
}

// TestInstallFarmGuardReceivesCanonicalDir: the guard is judged at
// the canonical store dir (the path farm links will carry) even
// though it scans a staging dir, and an agreeing guard does not
// disturb the install.
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
		FarmGuard: func(ps []farm.Placement) error {
			for _, p := range ps {
				got = append(got, p.FinalDir)
			}
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

	if _, err := inst.Install(context.Background(), r); err != nil {
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
