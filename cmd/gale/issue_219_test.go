package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
)

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
