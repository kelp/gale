package main

// Tests for the command-level lock writers: which gale.lock target
// each command regenerates, and that the target key is the concrete
// hostname rather than the `current` alias.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// testSHA is the 64-hex-digit shape lockgraph requires. A shorter
// placeholder would only pass by weakening the record's own checks.
const testSHA = "4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d"

// seedProvenanced seeds a store dir for name@version and writes the
// provenance record beside it, which is what the v1 lock writer
// resolves the closure from. Without the record the writer has
// nothing to describe, so every install fixture the lock touches
// needs one.
func seedProvenanced(t *testing.T, storeRoot, name, version string) {
	t.Helper()
	seedStore(t, storeRoot, name, version)
	writeProvenance(t, storeRoot, name, version)
}

// writeProvenance writes the record into an existing store dir, for
// fixtures that lay the directory out themselves.
func writeProvenance(t *testing.T, storeRoot, name, version string) {
	t.Helper()
	dir := filepath.Join(storeRoot, name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := provenance.New(storeRoot, lockgraph.Node{
		Name:    name,
		Version: version,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Method:  lockgraph.MethodBinary,
		SHA256:  testSHA,
	})
	if err != nil {
		t.Fatalf("provenance.New(%s): %v", name, err)
	}
	if err := provenance.Write(dir, rec); err != nil {
		t.Fatalf("provenance.Write(%s): %v", name, err)
	}
}

// writeTestRecipe writes a letter-bucketed recipe file for testpkg so
// resolverForRecipe treats it as a recipes-repo recipe and resolves
// deps locally instead of reaching for the registry.
func writeTestRecipe(t *testing.T, root string) string {
	t.Helper()
	const (
		name    = "testpkg"
		version = "1.0.0"
	)
	dir := filepath.Join(root, "gale-recipes", "recipes", name[:1])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".toml")
	body := strings.Join([]string{
		`[package]`,
		`name = "` + name + `"`,
		`version = "` + version + `"`,
		`revision = 1`,
		``,
		`[source]`,
		`url = "https://example.invalid/` + name + `.tar.gz"`,
		`sha256 = "` + strings.Repeat("0", 64) + `"`,
		``,
		`[build]`,
		`steps = ["true"]`,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// installCtx builds a cmdContext pointing at a HOME-rooted gale dir
// with the given gale.toml body.
func installCtx(t *testing.T, tmp, configBody string) *cmdContext {
	t.Helper()
	galeDir := filepath.Join(tmp, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(galeDir, "gale.toml")
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return &cmdContext{
		GalePath:  configPath,
		GaleDir:   galeDir,
		StoreRoot: storeRoot,
		Installer: &installer.Installer{Store: store.NewStore(storeRoot)},
	}
}

// readLock reads the v1 lockfile beside ctx's gale.toml.
func readLock(t *testing.T, ctx *cmdContext) *lockfile.V1 {
	t.Helper()
	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	lf, err := lockfile.ReadV1(lp)
	if err != nil {
		t.Fatalf("reading %s: %v", lp, err)
	}
	return lf
}

// TestInstallHostCurrentWritesConcreteTarget is acceptance test 15.
// `gale install <pkg> --host current` writes the manifest under
// [hosts.<CurrentHost>.packages], so the lock target it regenerates
// must be that same concrete hostname. resolveHostFlag expands the
// alias before the config write, and the lock has to follow the
// section actually written: a target keyed "current" would match no
// machine, so every reader would plan without it.
func TestInstallHostCurrentWritesConcreteTarget(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	recipePath := writeTestRecipe(t, tmp)
	ctx := installCtx(t, tmp, "[packages]\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	writeMatchingRecipeDigest(t, filepath.Join(ctx.StoreRoot, "testpkg", "1.0.0-1"), recipePath)
	host, err := resolveHostFlag("current")
	if err != nil {
		t.Fatalf("resolveHostFlag: %v", err)
	}
	ctx.Host = host

	if err := installFromRecipeFile(
		ctx, recipePath, output.New(os.Stderr, false),
	); err != nil {
		t.Fatalf("installFromRecipeFile: %v", err)
	}

	cfg, err := os.ReadFile(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "[hosts.testbox.packages]") {
		t.Errorf("gale.toml = %q, want a [hosts.testbox.packages] section", cfg)
	}

	lf := readLock(t, ctx)
	if _, aliased := lf.Targets.Host["current"]; aliased {
		t.Error(`lock has a [targets.host."current"] key, want the concrete hostname`)
	}
	target, ok := lf.Targets.Host["testbox"]
	if !ok {
		t.Fatalf(`lock has no [targets.host."testbox"], hosts = %v`, lf.Targets.Host)
	}
	if len(target.Roots) != 1 || target.Roots[0] != "testpkg@1.0.0-1" {
		t.Errorf("host target roots = %v, want [testpkg@1.0.0-1]", target.Roots)
	}
	if lf.Targets.Default != nil {
		t.Errorf("default target = %v, want none: the manifest write went to the host overlay",
			lf.Targets.Default)
	}
	if _, ok := lf.Packages["testpkg@1.0.0-1"]; !ok {
		t.Errorf("lock packages = %v, want a testpkg@1.0.0-1 node", lf.Packages)
	}
}

// TestInstallWritesDefaultTarget: a plain install writes shared
// [packages], so it regenerates [targets.default]. The root is the
// canonical version-revision, never the bare pin gale.toml carries,
// because a bare identity addresses no store directory.
func TestInstallWritesDefaultTarget(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	recipePath := writeTestRecipe(t, tmp)
	ctx := installCtx(t, tmp, "[packages]\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	writeMatchingRecipeDigest(t, filepath.Join(ctx.StoreRoot, "testpkg", "1.0.0-1"), recipePath)

	if err := installFromRecipeFile(
		ctx, recipePath, output.New(os.Stderr, false),
	); err != nil {
		t.Fatalf("installFromRecipeFile: %v", err)
	}

	lf := readLock(t, ctx)
	if lf.Targets.Default == nil {
		t.Fatalf("no [targets.default]; targets = %+v", lf.Targets)
	}
	if len(lf.Targets.Default.Roots) != 1 ||
		lf.Targets.Default.Roots[0] != "testpkg@1.0.0-1" {
		t.Errorf("default roots = %v, want [testpkg@1.0.0-1]",
			lf.Targets.Default.Roots)
	}
	if len(lf.Targets.Host) != 0 {
		t.Errorf("host targets = %v, want none", lf.Targets.Host)
	}
}

// TestInstallPreservesHostLocationInLock: a plain install of a
// package the current host's overlay already lists updates that
// overlay in place, so the lock must regenerate the HOST target and
// leave the default one alone.
//
// Splitting them is the point. Writing the default target here would
// root a package the shared section does not declare, which every
// reader reports as a stale lock, and would leave the overlay the
// manifest actually carries unlocked.
func TestInstallPreservesHostLocationInLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	recipePath := writeTestRecipe(t, tmp)
	ctx := installCtx(t, tmp,
		"[packages]\n\n[hosts.testbox.packages]\n  testpkg = \"0.9.0\"\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	writeMatchingRecipeDigest(t, filepath.Join(ctx.StoreRoot, "testpkg", "1.0.0-1"), recipePath)

	if err := installFromRecipeFile(
		ctx, recipePath, output.New(os.Stderr, false),
	); err != nil {
		t.Fatalf("installFromRecipeFile: %v", err)
	}

	lf := readLock(t, ctx)
	target, ok := lf.Targets.Host["testbox"]
	if !ok {
		t.Fatalf(`no [targets.host."testbox"]; targets = %+v`, lf.Targets)
	}
	if len(target.Roots) != 1 || target.Roots[0] != "testpkg@1.0.0-1" {
		t.Errorf("host roots = %v, want [testpkg@1.0.0-1]", target.Roots)
	}
	if lf.Targets.Default != nil {
		t.Errorf("default target = %+v, want none: the manifest write "+
			"went to the host overlay", lf.Targets.Default)
	}
}

// sourceTarball writes a minimal source archive and returns its path
// and SHA256, so an install can run end to end against a local
// server instead of being short-circuited by a pre-seeded store.
// The archive's root directory carries a version only because tar
// convention wants one; no caller has ever needed a second value, so
// it is fixed here rather than threaded through every fixture.
func sourceTarball(t *testing.T, name string) (string, string) {
	t.Helper()
	const version = "1.0"
	path := filepath.Join(t.TempDir(), "source.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tarball: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	root := name + "-" + version + "/"
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir, Name: root, Mode: 0o755,
	}); err != nil {
		t.Fatal(err)
	}
	const content = "placeholder"
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: root + "README",
		Mode: 0o644, Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []io.Closer{tw, gw, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, fmt.Sprintf("%x", sha256.Sum256(data))
}

// TestSyncWritesNoLockfile pins section 11's first line: sync never
// writes gale.lock. It is a pure consumer of one — the lock says
// what to install — so a sync that also wrote it would ratify
// whatever it happened to install and leave nothing to enforce.
//
// The install is real rather than cached, because the write this
// test denies only ever happened after a successful install.
func TestSyncWritesNoLockfile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	storeRoot := filepath.Join(tmp, "store")
	galeDir := filepath.Join(tmp, ".gale")
	galePath := filepath.Join(tmp, "gale.toml")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(galePath,
		[]byte("[packages]\n  syncpkg = \"1.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tarball, sum := sourceTarball(t, "syncpkg")
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, tarball)
		},
	))
	defer srv.Close()

	resolver := func(name string) (*recipe.Recipe, error) {
		return &recipe.Recipe{
			Package: recipe.Package{Name: name, Version: "1.0"},
			Source:  recipe.Source{URL: srv.URL + "/source.tar.gz", SHA256: sum},
			Build: recipe.Build{Steps: []string{
				"mkdir -p $PREFIX/bin",
				"echo '#!/bin/sh' > $PREFIX/bin/syncpkg",
				"chmod +x $PREFIX/bin/syncpkg",
			}},
		}, nil
	}

	ctx := buildFakeCtx(t, galePath, galeDir, storeRoot, resolver)
	out := runSyncOne(ctx, syncItem{name: "syncpkg", version: "1.0"}, false)
	if out.installErr != nil || out.result == nil {
		t.Fatalf("install did not succeed: err=%v result=%v",
			out.installErr, out.result)
	}

	lp, err := lockfilePath(galePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(lp); !os.IsNotExist(err) {
		data, _ := os.ReadFile(lp)
		t.Errorf("sync created %s:\n%s", lp, data)
	}
}

// TestUpdateRegeneratesTheSectionItRewrote: `update` rewrites the
// pins in the section the package lives in, so it regenerates that
// section's target with the version it verified this run. The prior
// root at the old version must not survive: the lock would then
// describe a package the manifest no longer declares at that
// version.
func TestUpdateRegeneratesTheSectionItRewrote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.7.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recipesDir := filepath.Join(projDir, "recipes", "j")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(recipesDir, "jq.toml"),
		[]byte("[package]\nname = \"jq\"\nversion = \"1.8.0\"\n\n"+
			"[source]\nurl = \"https://example.invalid/jq.tar.gz\"\n"+
			"sha256 = \""+strings.Repeat("0", 64)+"\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	// The new version is already in the store, so the install is a
	// cache hit and the test needs no network.
	seedProvenanced(t, defaultStoreRoot(), "jq", "1.8.0-1")
	writeMatchingRecipeDigest(t,
		filepath.Join(defaultStoreRoot(), "jq", "1.8.0-1"),
		filepath.Join(recipesDir, "jq.toml"))

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	updateRecipes = filepath.Join(projDir, "recipes")
	t.Cleanup(func() { updateRecipes = "" })

	if err := updateCmd.RunE(updateCmd, []string{"jq"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	lf, err := lockfile.ReadV1(filepath.Join(projDir, "gale.lock"))
	if err != nil {
		t.Fatalf("reading lock: %v", err)
	}
	if lf.Targets.Default == nil ||
		len(lf.Targets.Default.Roots) != 1 ||
		lf.Targets.Default.Roots[0] != "jq@1.8.0-1" {
		t.Errorf("default roots = %+v, want [jq@1.8.0-1]", lf.Targets.Default)
	}
}

// TestAddDoesNotTouchTheLock: `gale add` writes gale.toml without
// installing, so it has verified nothing and locks nothing. The
// resulting stale lock is deliberate, and its remedy is the message
// add prints; writing a root for bytes no one fetched would be the
// opposite of what the lock is for.
func TestAddDoesNotTouchTheLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(projDir)
	t.Cleanup(func() { os.Chdir(orig) })

	addProject = true
	t.Cleanup(func() { addProject = false })

	// @version keeps the network resolver out of it.
	if err := addCmd.RunE(addCmd, []string{"jq@1.8.1"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	lp := filepath.Join(projDir, "gale.lock")
	if _, err := os.Lstat(lp); !os.IsNotExist(err) {
		data, _ := os.ReadFile(lp)
		t.Errorf("add created %s:\n%s", lp, data)
	}
}

// TestInstallRefusesALegacyLock: a lockfile with no version key was
// written by a gale that recorded checksums without enforcing them,
// so its entries are not evidence about anything. Replacing it as a
// side effect of an install would silently convert an unenforced
// lock into an enforced one that describes only the package that
// happened to be installed. The write refuses, names the schema, and
// leaves the file byte-identical; `gale lock` is the way across.
func TestInstallRefusesALegacyLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("GALE_HOST", "testbox")

	recipePath := writeTestRecipe(t, tmp)
	ctx := installCtx(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	writeMatchingRecipeDigest(t, filepath.Join(ctx.StoreRoot, "testpkg", "1.0.0-1"), recipePath)

	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyLock(t, lp, "testpkg", "1.0.0", "old")
	before, err := os.ReadFile(lp)
	if err != nil {
		t.Fatal(err)
	}

	err = installFromRecipeFile(ctx, recipePath, output.New(os.Stderr, false))
	if !errors.Is(err, lockfile.ErrLegacySchema) {
		t.Errorf("install error = %v, want ErrLegacySchema", err)
	}
	after, err := os.ReadFile(lp)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("legacy lock was rewritten:\n%s", after)
	}
}

// TestWriteLockRefusesDanglingSymlink closes the writer half of a
// fail-open the readers closed: a gale.lock symlink whose target is
// missing read as absent, so the writer derived a document from
// nothing and atomicfile.Write's rename replaced the symlink itself
// with a regular file. The link is a user artifact — a chezmoi-managed
// link into a shared lock is the obvious case — and replacing it
// silently loses it.
func TestWriteLockRefusesDanglingSymlink(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ctx := installCtx(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	ctx.noteLockRoot("", "testpkg", "1.0.0-1")

	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "shared.lock")
	if err := os.Symlink(target, lp); err != nil {
		t.Fatal(err)
	}

	if err := ctx.WriteLock(); err == nil {
		t.Error("WriteLock wrote through a dangling symlink, want refusal")
	}
	fi, err := os.Lstat(lp)
	if err != nil {
		t.Fatalf("lock path is gone: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("lock path is now a %s, want the symlink preserved", fi.Mode())
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("symlink target was created: %v", err)
	}
}

// failTheWriteOnly arranges for everything up to the atomic write to
// succeed and the write itself to fail: the file lock's sentinel is
// created while the directory is still writable, so acquiring it needs
// no new entry, and the read of an absent lock is an ordinary ENOENT.
// atomicfile.Write then cannot create its temp file.
//
// Failing earlier — a directory at the lock path, say — makes the read
// fail instead, and a test that only asserts "some error" would stop
// exercising the write without saying so.
func failTheWriteOnly(t *testing.T, ctx *cmdContext) {
	t.Helper()
	lp, err := lockfilePath(ctx.GalePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lp+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(lp)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) }) //nolint:gosec
}

// TestWriteLockSurfacesWriteFailure pins that a lock that cannot be
// written is an error the command reports, never a silent skip: the
// install would otherwise activate a generation nothing describes.
func TestWriteLockSurfacesWriteFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ctx := installCtx(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	ctx.noteLockRoot("", "testpkg", "1.0.0-1")
	failTheWriteOnly(t, ctx)

	if err := ctx.WriteLock(); err == nil {
		t.Error("WriteLock returned nil when the lock could not be written")
	}
}

// TestWriteLockWarnsOnlyAfterTheWriteLands: the unlocked warning
// describes what the NEW lockfile leaves out. When the write fails
// the previous lockfile is intact and omits nothing, so saying
// otherwise sends the user to repair a file that was never changed.
func TestWriteLockWarnsOnlyAfterTheWriteLands(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ctx := installCtx(t, tmp,
		"[packages]\n  testpkg = \"1.0.0\"\n  orphan = \"3.0.0\"\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	ctx.noteLockRoot("", "testpkg", "1.0.0-1")
	failTheWriteOnly(t, ctx)

	var writeErr error
	stderr := captureStderr(t, func() { writeErr = ctx.WriteLock() })
	if writeErr == nil {
		t.Fatal("WriteLock returned nil with a directory at the lock path")
	}
	if strings.Contains(stderr, "not locked") {
		t.Errorf("a failed write reported omissions:\n%s", stderr)
	}
}

// TestWriteLockReadsTheManifestUnderTheLock: gale.toml is not covered
// by the lockfile lock, so a concurrent command can commit a manifest
// change while this write is in flight. The write therefore has to
// resolve against the manifest as it stands once the lock is held, not
// against a snapshot taken before.
//
// The interleaving is the losing half of two concurrent writers: A
// prepares its roots, B commits a new manifest and its own lock, then
// A writes. Reading before the lock, A cannot see B at all and commits
// a lock the manifest no longer backs — which is the exact
// disagreement checkRootsDeclared exists to prevent, and which passes
// that check because A is comparing against its own stale copy.
//
// Reading under the lock does not serialize A with B's manifest write;
// nothing can, since config writers take a different lock. It makes A
// see whatever B committed, so A fails instead of writing a stale
// lock. Failing is the intended outcome.
func TestWriteLockReadsTheManifestUnderTheLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ctx := installCtx(t, tmp, "[packages]\n  testpkg = \"1.0.0\"\n")
	seedProvenanced(t, ctx.StoreRoot, "testpkg", "1.0.0-1")
	ctx.noteLockRoot("", "testpkg", "1.0.0-1")

	// The concurrent writer: it repins testpkg while this write holds
	// the lockfile lock, which is a window a snapshot taken earlier
	// cannot see.
	beforeLockRead = func() {
		if err := os.WriteFile(ctx.GalePath,
			[]byte("[packages]\n  testpkg = \"2.0.0\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeLockRead = nil })

	err := ctx.WriteLock()
	if err == nil {
		t.Error("WriteLock committed a root the manifest no longer declares")
	}

	lp, lpErr := lockfilePath(ctx.GalePath)
	if lpErr != nil {
		t.Fatal(lpErr)
	}
	if _, statErr := os.Lstat(lp); !os.IsNotExist(statErr) {
		data, _ := os.ReadFile(lp)
		t.Errorf("a lock was written from a stale manifest:\n%s", data)
	}
}
