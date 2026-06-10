package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kelp/gale/internal/filelock"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
)

// TestInstallFromRecipeFile_HoldsLockAcrossFinalize is the
// regression test for gh#69: `gale install --recipe` called
// inst.Install (which releases the per-package lock on return)
// and then ctx.FinalizeRecipeInstall as a separate step. In the
// window between the two, a concurrent `gale gc` saw the
// package referenced by no gale.toml, grabbed the now-free
// per-version lock, and reaped the just-installed store dir.
// The 0004 race fix migrated the registry, --path, and --git
// paths to *WithFinalize variants; --recipe was left behind.
//
// Setup: the store is pre-seeded so Install returns
// MethodCached instantly, and the test holds the generation
// lock so finalize parks deterministically inside its
// generation rebuild. While finalize is parked, a concurrent
// store.Remove of the same package must block on the
// per-package lock.
//
// RED (pre-fix): the lock was released when Install returned,
// so Remove completes while finalize is still running. GREEN:
// the lock spans install + finalize and Remove stays blocked
// until installFromRecipeFile returns.
func TestInstallFromRecipeFile_HoldsLockAcrossFinalize(t *testing.T) {
	galeDir := t.TempDir()
	storeRoot := filepath.Join(galeDir, "pkg")
	s := store.NewStore(storeRoot)

	// Pre-seed the store so the install is MethodCached (no
	// network, no build).
	dir, err := s.Create("u3lockpkg", "1.0-1")
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(binDir, "u3lockpkg"), []byte("fake"), 0o755,
	); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	recipePath := filepath.Join(t.TempDir(), "u3lockpkg.toml")
	recipeTOML := `[package]
name = "u3lockpkg"
version = "1.0"

[source]
url = "https://example.invalid/u3lockpkg-1.0.tar.gz"
sha256 = "0000000000000000000000000000000000000000000000000000000000000000"
`
	if err := os.WriteFile(recipePath, []byte(recipeTOML), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}

	ctx := &cmdContext{
		GalePath:  filepath.Join(galeDir, "gale.toml"),
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
	}
	out := output.New(io.Discard, false)

	// Hold the generation lock so the gen rebuild inside
	// FinalizeRecipeInstall parks until we release it. This
	// gives a deterministic window in which finalize is
	// in-flight.
	genUnlock, err := filelock.Acquire(
		filepath.Join(galeDir, "generation.lock"),
	)
	if err != nil {
		t.Fatalf("acquire generation lock: %v", err)
	}
	genReleased := false
	defer func() {
		if !genReleased {
			genUnlock()
		}
	}()

	installDone := make(chan error, 1)
	go func() {
		installDone <- installFromRecipeFile(ctx, recipePath, out)
	}()

	// Wait until finalize has started: writeConfigAndLock
	// writes gale.toml before the gen rebuild parks on the
	// generation lock. Once gale.toml exists, Install has
	// definitely returned and finalize is running.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ctx.GalePath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finalize never wrote gale.toml")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Race a concurrent store.Remove (what `gale gc` does for
	// an unreferenced package). It must block on the
	// per-package lock while finalize is still in flight.
	removeDone := make(chan error, 1)
	go func() {
		removeDone <- s.Remove("u3lockpkg", "1.0-1")
	}()

	removeCompleted := false
	select {
	case <-removeDone:
		removeCompleted = true
		t.Error("store.Remove completed while finalize was " +
			"still running — installFromRecipeFile does not " +
			"hold the per-package lock across finalize (gh#69)")
	case <-time.After(300 * time.Millisecond):
		// Good: Remove is blocked behind the per-package lock.
	}

	// Unpark finalize and drain both goroutines.
	genReleased = true
	genUnlock()

	select {
	case err := <-installDone:
		// On the RED path the store dir was reaped mid-finalize
		// and install legitimately errors; only assert success
		// when the lock discipline held.
		if !t.Failed() && err != nil {
			t.Fatalf("installFromRecipeFile: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("installFromRecipeFile never returned")
	}
	if !removeCompleted {
		select {
		case <-removeDone:
		case <-time.After(15 * time.Second):
			t.Fatal("store.Remove never returned")
		}
	}
}
