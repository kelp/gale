package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
)

// gh#219: a man page or a root-level file two packages both ship is
// resolved by sort order and reported nowhere. doctor reports it. The
// rebuild keeps accepting it — see
// TestRebuildGenerationAcceptsManPageCollision below, which is the
// half of this change that must never regress.

// addStoreFile writes one file at a gen-relative path inside an
// existing store dir, making its parents.
func addStoreFile(t *testing.T, pkgDir, rel string) {
	t.Helper()
	path := filepath.Join(pkgDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// issue219Home lays out a global scope whose two declared packages
// both ship man/man1/foo.1, and returns (galeDir, storeRoot).
func issue219Home(t *testing.T) (galeDir, storeRoot string) {
	t.Helper()
	galeDir, storeRoot = setupGCHome(t)
	for _, name := range []string{"alpha", "beta"} {
		addStoreFile(t,
			mkStorePkg(t, storeRoot, name, "1.0"), "man/man1/foo.1")
	}
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n")
	return galeDir, storeRoot
}

// TestDoctorReportsShadowedManPageWithoutFailing pins the report and
// its advisory verdict together. Two packages shipping one man page is
// an ordinary setup — a library and its CLI, a compat shim — so the
// check names the shadowed path and still passes. Failing it would
// make `gale doctor` red on installations that have always been
// correct, which is how a check gets ignored.
func TestDoctorReportsShadowedManPageWithoutFailing(t *testing.T) {
	galeDir, storeRoot := issue219Home(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if !checkShadowedFiles(doctorCtx(galeDir, storeRoot, cwd, &buf)) {
		t.Errorf("a shadowed man page must not fail the check, got: %q",
			buf.String())
	}
	if msg := buf.String(); !strings.Contains(msg, "man/man1/foo.1") {
		t.Errorf("the report must name the shadowed path, got: %q", msg)
	}
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the report must name provider %q, got: %q",
				want, buf.String())
		}
	}
}

// TestRebuildGenerationAcceptsManPageCollision guards against the
// patch this change invites: reusing bin/'s rule for man/. A duplicate
// man page must rebuild, and current must advance. bin/ refuses
// because it decides what runs; man/ shows the wrong docs.
func TestRebuildGenerationAcceptsManPageCollision(t *testing.T) {
	galeDir, storeRoot := issue219Home(t)
	configPath := filepath.Join(galeDir, "gale.toml")

	if err := rebuildGeneration(
		galeDir, storeRoot, configPath, nil,
	); err != nil {
		t.Fatalf("rebuildGeneration with a duplicate man page: %v — only "+
			"bin/ collisions refuse a rebuild", err)
	}
	cur, err := generation.Current(galeDir)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if cur != 1 {
		t.Errorf("current generation = %d, want 1", cur)
	}
}

// TestRebuildGenerationHonorsHostScopedBinOverride carries gh#190's
// escape hatch across the host boundary (gh#219). [packages] and
// [pinned] both take a [hosts.<selector>.*] overlay; [bin] shipped
// without one, so the only way out of a collision was a single
// manifest-wide winner. Two machines that each need a different
// provider on PATH had no answer at all.
//
// The assertion is the pipeline one on purpose: the merge belongs to
// loadEffectiveConfig, and rebuildInputs reads what that returns, so
// driving the rebuild proves the overlay reaches the generation
// rather than merely the struct.
func TestRebuildGenerationHonorsHostScopedBinOverride(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	t.Setenv("GALE_HOST", "testhost")
	configPath := filepath.Join(galeDir, "gale.toml")

	alphaDir := mkStorePkg(t, storeRoot, "alpha", "1.0")
	betaDir := mkStorePkg(t, storeRoot, "beta", "1.0")
	addStoreBin(t, alphaDir, "foo")
	addStoreBin(t, betaDir, "foo")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[bin]\nfoo = \"alpha\"\n\n"+
			"[hosts.testhost.bin]\nfoo = \"beta\"\n")

	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration with a host-scoped [bin]: %v", err)
	}

	if got := linkTarget(t, galeDir, "foo"); !strings.Contains(
		got, filepath.Join("beta", "1.0"),
	) {
		t.Errorf("bin/foo -> %s, want beta's copy — [hosts.testhost.bin] "+
			"outranks the shared [bin] winner on testhost", got)
	}
}

// TestHostScopedBinWinnerNeedsNoSharedDeclaration keeps the overlay
// usable on its own. A winner declared only under
// [hosts.<selector>.packages], named only by that selector's [bin],
// is the whole point of the feature — the manifest has to load, and
// the override has to survive the merge into cfg.Bin.
func TestHostScopedBinWinnerNeedsNoSharedDeclaration(t *testing.T) {
	galeDir, _ := setupGCHome(t)
	t.Setenv("GALE_HOST", "testhost")
	configPath := filepath.Join(galeDir, "gale.toml")

	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\n\n"+
			"[hosts.testhost.packages]\nbeta = \"1.0\"\n\n"+
			"[hosts.testhost.bin]\nfoo = \"beta\"\n")

	cfg, err := loadEffectiveConfig(configPath)
	if err != nil {
		t.Fatalf("loadEffectiveConfig: %v", err)
	}
	if got := cfg.Bin["foo"]; got != "beta" {
		t.Errorf("cfg.Bin[foo] = %q, want beta — the overlay's winner "+
			"must reach the rebuild's [bin] inputs", got)
	}
}

// TestPinPreservesHostScopedBinOverrides is TestPinPreservesBinOverrides
// for the overlay. PinPackage rewrites gale.toml through a struct
// round-trip, so any table HostConfig does not carry is dropped on the
// next pin. HostConfig.Bin is a field now, and this test is what keeps
// it one.
func TestPinPreservesHostScopedBinOverrides(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	t.Setenv("GALE_HOST", "testhost")
	configPath := filepath.Join(galeDir, "gale.toml")

	mkStorePkg(t, storeRoot, "alpha", "1.0")
	mkStorePkg(t, storeRoot, "beta", "1.0")
	writeGlobalConfig(t, galeDir,
		"[packages]\nalpha = \"1.0\"\nbeta = \"1.0\"\n\n"+
			"[hosts.testhost.bin]\nfoo = \"beta\"\n")

	if err := config.PinPackage(configPath, "", "beta"); err != nil {
		t.Fatalf("PinPackage: %v", err)
	}

	cfg, err := loadEffectiveConfig(configPath)
	if err != nil {
		t.Fatalf("config no longer loads: %v", err)
	}
	if got := cfg.Bin["foo"]; got != "beta" {
		t.Errorf("cfg.Bin[foo] = %q after pin, want beta", got)
	}
}
