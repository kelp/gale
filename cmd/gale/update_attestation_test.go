package main

import (
	"context"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
)

// §7d: the index cannot switch attestation off. An update that
// drops the attestation a locked package carried is a refusal;
// --allow-attestation-drop is the explicit escape, and it warns.
func attestDropIndexDoc(name, version string) string {
	return `[package]
name = "` + name + `"
description = "test package"
license = "MIT"
homepage = "https://github.com/kelp/` + name + `"
repo = "kelp/` + name + `"
latest = "` + version + `"

[versions."` + version + `".artifacts."darwin/arm64"]
url = "https://github.com/kelp/` + name + `/releases/download/` + version + `/` + name + `.tar.gz"
format = "tar.gz"
sha256 = "` + lockFetchSHA + `"
tree_digest = "` + fetchTreeDigest(name) + `"
hash_source = "upstream-sha256sums"
strip = 1

[[versions."` + version + `".artifacts."darwin/arm64".files]]
src = "` + name + `"
dest = "bin/` + name + `"
mode = 0o755
`
}

func attestDropFixture(t *testing.T) *lockFetchFix {
	t.Helper()
	fx := newLockFetchFix(t)
	if err := runLockFetch(context.Background(), fx.c,
		fx.req("just@1.56.0")); err != nil {
		t.Fatal(err)
	}
	// A newer 9.9.9 block whose darwin/arm64 artifact carries NO
	// attestation, while the locked 1.56.0 was attested (the
	// fixture index sets attestation = true on darwin/arm64).
	fx.h.files["/"+lockFetchPinB+"/index/j/just.toml"] =
		attestDropIndexDoc("just", "9.9.9")
	return fx
}

func resetAllowAttestationDrop(t *testing.T, v bool) {
	t.Helper()
	old := allowAttestationDrop
	allowAttestationDrop = v
	t.Cleanup(func() { allowAttestationDrop = old })
}

func TestUpdateRefusesAttestationDrop(t *testing.T) {
	clearAdoptCI(t)
	fx := attestDropFixture(t)
	resetAllowAttestationDrop(t, false)

	err := runUpdateFetch(context.Background(), fx.c, []string{"just"},
		index.Source{BaseURL: fx.src.BaseURL, Commit: lockFetchPinB},
		newOutput(),
	)
	if err == nil || !strings.Contains(err.Error(), "attestation") {
		t.Fatalf("err = %v, want attestation-drop refusal", err)
	}
	got, readErr := lockfile.ReadV2(fx.lockPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if _, ok := got.Packages["just@1.56.0"]; !ok {
		t.Errorf("refused update still rewrote the lock: %v", got.Packages)
	}
}

func TestUpdateAllowsAttestationDropWithFlagAndWarns(t *testing.T) {
	clearAdoptCI(t)
	fx := attestDropFixture(t)
	resetAllowAttestationDrop(t, true)
	installToStore = stageTestFetch
	t.Cleanup(func() { installToStore = nil })

	var buf strings.Builder
	out := newOutputForWriter(&buf)
	err := runUpdateFetch(context.Background(), fx.c, []string{"just"},
		index.Source{BaseURL: fx.src.BaseURL, Commit: lockFetchPinB},
		out,
	)
	if err != nil {
		t.Fatalf("allowed update: %v", err)
	}
	if !strings.Contains(buf.String(), "attestation") {
		t.Errorf("output %q did not warn about the dropped attestation",
			buf.String())
	}
	got, readErr := lockfile.ReadV2(fx.lockPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	pkg, ok := got.Packages["just@9.9.9"]
	if !ok {
		t.Fatalf("lock not updated to 9.9.9: %v", got.Packages)
	}
	if art, ok := pkg.Artifacts["darwin/arm64"]; ok && art.Attestation != nil {
		t.Error("escaped update kept an attestation row")
	}
}

// The refusal must be reachable from the command surface.
func TestUpdateHasAllowAttestationDropFlag(t *testing.T) {
	if updateCmd.Flags().Lookup("allow-attestation-drop") == nil {
		t.Fatal("update --allow-attestation-drop is missing")
	}
}
