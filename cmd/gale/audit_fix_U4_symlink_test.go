package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/config"
)

// TestGCRetainsOtherHostPinsDottedHost reproduces the macOS CI
// failure of TestGCRetainsOtherHostPins on any platform. On
// macOS runners os.Hostname() returns a dotted name like
// "Mac-1748345678901.local"; interpolated unquoted into a
// [hosts.<name>-other.packages] header, the dot splits the key
// into nested TOML tables. The typed GaleConfig.Hosts map
// silently drops the nested overlay, the host-union retention
// in gc sees no pin, and the store entry is deleted. gc must
// retain packages under ANY table nested below [hosts] — gc is
// destructive, and a pin it cannot see is data it deletes.
func TestGCRetainsOtherHostPinsDottedHost(t *testing.T) {
	galeDir, storeRoot := setupGCHome(t)
	// Mirror a macOS hostname: CurrentHost() reads GALE_HOST
	// first, so this makes the original test's exact config
	// content appear on Linux too.
	t.Setenv("GALE_HOST", "mac-ci.local")
	other := config.CurrentHost() + "-other"
	writeGlobalConfig(t, galeDir,
		"[packages]\n[hosts."+other+".packages]\njq = \"1.7\"\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7-1")

	_ = gcCmd.RunE(gcCmd, nil)

	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("jq/1.7-1 is pinned under a dotted host key "+
			"and must survive gc: %v", err)
	}
}

// TestGCRetainsOtherHostPinsSymlinkedHome pins the macOS path
// layout: on macOS every t.TempDir() lives under /var/folders,
// where /var is a symlink to /private/var, so the raw $HOME
// spelling and the kernel-resolved os.Getwd() spelling of the
// same gale dir diverge. This test recreates that split on
// Linux (HOME through a symlink, cwd resolved by the kernel)
// and asserts the other-host pin still survives gc — retention
// keys and the deletion walk are both name@version identities
// derived from the same store root, so path spelling must not
// matter.
func TestGCRetainsOtherHostPinsSymlinkedHome(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// HOME uses the symlinked spelling; Getwd() will return the
	// resolved one — exactly the macOS /var vs /private/var split.
	t.Setenv("HOME", link)
	t.Setenv("GALE_OFFLINE", "1")
	t.Setenv("GALE_HOST", "testhost")
	galeDir := filepath.Join(link, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	dryRun = false
	t.Cleanup(func() { dryRun = false })

	writeGlobalConfig(t, galeDir,
		"[packages]\n[hosts.testhost-other.packages]\njq = \"1.7\"\n")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.7-1")

	_ = gcCmd.RunE(gcCmd, nil)

	if _, err := os.Stat(jqDir); err != nil {
		t.Errorf("jq/1.7-1 is pinned by another host's overlay "+
			"and must survive gc under a symlinked HOME: %v", err)
	}
}
