package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
)

// Pipeline-layer tests for gh#191's content-tag reversal sites.
// Nothing mints a tag yet, so these fixtures plant a tagged store
// directory by hand and assert the readers behave as they will
// have to once phase 2 lands.
//
// The two sites covered here are the destructive ones:
// otherScopeReferences decides whether `gale remove` deletes
// bytes, and storeRetentionKey decides what `gale gc` keeps.

// contentTagScope is the fixture both tests below build: a home
// with a global scope, a project scope, and a store. Returned as
// paths rather than as a struct of helpers so each test plants
// only the store dirs and generations it needs.
type contentTagScope struct {
	globalDir string
	projDir   string
	projGale  string
	storeRoot string
}

// setupContentTagScope points HOME at a temp dir, writes an empty
// gale.toml into both scopes, and chdirs into the project. Both
// configs are empty, so any reference a test observes comes from
// a generation rather than from a config pin.
func setupContentTagScope(t *testing.T) contentTagScope {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	sc := contentTagScope{
		globalDir: filepath.Join(home, ".gale"),
		projDir:   filepath.Join(home, "proj"),
	}
	sc.projGale = filepath.Join(sc.projDir, ".gale")
	sc.storeRoot = filepath.Join(sc.globalDir, "pkg")

	writeU9File(t, filepath.Join(sc.globalDir, "gale.toml"),
		"[packages]\n")
	writeU9File(t, filepath.Join(sc.projDir, "gale.toml"),
		"[packages]\n")

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sc.projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	return sc
}

// plantStoreDir creates <storeRoot>/<name>/<version>/bin/<name>
// and returns the version directory.
func plantStoreDir(t *testing.T, storeRoot, name, version string) string {
	t.Helper()
	dir := filepath.Join(storeRoot, name, version)
	writeU9File(t, filepath.Join(dir, "bin", name), "#!/bin/sh\n")
	return dir
}

// linkGeneration builds gen/<n> under galeDir with one bin
// symlink into storeDir, and points current at it.
func linkGeneration(t *testing.T, galeDir, storeDir, name string, n string) {
	t.Helper()
	binDir := filepath.Join(galeDir, "gen", n, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(storeDir, "bin", name),
		filepath.Join(binDir, name),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", n), filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}
}

// TestOtherScopeReferencesSeesTaggedSiblingInOtherGeneration is
// the cross-scope data-loss guard for gh#191. The global
// generation links a content-tagged sibling that no gale.toml
// names — configs never carry a tag — so the config-derived
// reference set alone misses it and `gale remove` in the project
// scope would delete a directory the global PATH resolves
// through. That is the ad4e685 / 289d13b class.
//
// The read goes through RetainedVersionsStrict, never
// generation.List: this is a decision to destroy, and the lenient
// reader answers "links nothing" for a generation it could not
// read (gh#210).
func TestOtherScopeReferencesSeesTaggedSiblingInOtherGeneration(
	t *testing.T,
) {
	sc := setupContentTagScope(t)
	tagged := plantStoreDir(t, sc.storeRoot, "hello",
		"1.0.0+h.abc123def456-1")
	linkGeneration(t, sc.globalDir, tagged, "hello", "1")

	st := store.NewStore(sc.storeRoot)
	ctx := &cmdContext{GaleDir: sc.projGale, StoreRoot: sc.storeRoot}
	out := output.New(io.Discard, false)

	if !otherScopeReferences(ctx, st, "hello", tagged, out) {
		t.Errorf("otherScopeReferences(hello, %q) = false, want true "+
			"— the global generation links it, so removing it "+
			"dangles the global PATH", filepath.Base(tagged))
	}
}

// TestOtherScopeReferencesReleasesUnlinkedSibling is the negative
// control: the guard must stay additive. A store dir no config
// names and no other scope's retained generation links is still
// deletable, or `gale remove` would never reclaim anything.
func TestOtherScopeReferencesReleasesUnlinkedSibling(t *testing.T) {
	sc := setupContentTagScope(t)
	linked := plantStoreDir(t, sc.storeRoot, "hello",
		"1.0.0+h.abc123def456-1")
	orphan := plantStoreDir(t, sc.storeRoot, "hello",
		"1.0.0+h.bbbbbbbbbbbb-1")
	linkGeneration(t, sc.globalDir, linked, "hello", "1")

	st := store.NewStore(sc.storeRoot)
	ctx := &cmdContext{GaleDir: sc.projGale, StoreRoot: sc.storeRoot}
	out := output.New(io.Discard, false)

	if otherScopeReferences(ctx, st, "hello", orphan, out) {
		t.Errorf("otherScopeReferences(hello, %q) = true, want false "+
			"— nothing links this sibling", filepath.Base(orphan))
	}
}

// TestOtherScopeReferencesIgnoresOwnGeneration pins the scope
// split. otherScopeReferences runs BEFORE the initiating scope's
// generation is rebuilt, so that scope's own current generation
// still links the package being removed. Counting it would make
// `gale remove` refuse every removal.
func TestOtherScopeReferencesIgnoresOwnGeneration(t *testing.T) {
	sc := setupContentTagScope(t)
	dir := plantStoreDir(t, sc.storeRoot, "hello", "1.0.0-1")
	linkGeneration(t, sc.projGale, dir, "hello", "1")

	st := store.NewStore(sc.storeRoot)
	ctx := &cmdContext{GaleDir: sc.projGale, StoreRoot: sc.storeRoot}
	out := output.New(io.Discard, false)

	if otherScopeReferences(ctx, st, "hello", dir, out) {
		t.Error("otherScopeReferences counted the initiating " +
			"scope's own generation; `gale remove` would never " +
			"delete anything")
	}
}

// TestStoreRetentionKeyCanonicalizesContentTag pins the gc side.
// A retention key derived from a store dir must name the identity
// configs and lockfiles carry, which never includes a tag (§5 of
// the proposal: a lock naming a tagged version names a path a
// teammate cannot produce).
func TestStoreRetentionKeyCanonicalizesContentTag(t *testing.T) {
	sc := setupContentTagScope(t)
	plantStoreDir(t, sc.storeRoot, "hello", "1.0.0+h.abc123def456-1")
	plantStoreDir(t, sc.storeRoot, "plain", "2.0.0-3")
	st := store.NewStore(sc.storeRoot)

	cases := []struct {
		name, version, want string
	}{
		{"hello", "1.0.0+h.abc123def456-1", "hello@1.0.0-1"},
		{"plain", "2.0.0-3", "plain@2.0.0-3"},
		// A bare pin resolves through the store and can never
		// reach a tagged sibling, so it keeps naming what it
		// names today.
		{"plain", "2.0.0", "plain@2.0.0-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"@"+tc.version, func(t *testing.T) {
			got := storeRetentionKey(st, tc.name, tc.version, nil)
			if got != tc.want {
				t.Errorf("storeRetentionKey(%s, %s) = %q, want %q",
					tc.name, tc.version, got, tc.want)
			}
		})
	}
}
