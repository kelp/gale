package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/lockfile"
)

// §7d: the index cannot switch attestation off. An update that
// drops the attestation a locked package carried is a refusal;
// --allow-attestation-drop is the explicit escape, and it warns.
func attestGaleDoc(version string, withAttestation bool) string {
	return lockIndexTOML("gale", version, withAttestation)
}

func attestDropFixture(t *testing.T) *lockFetchFix {
	t.Helper()
	fx := newLockFetchFix(t)
	// Locked at 1.56.0 with an attestation; the newer 9.9.9 block
	// drops it. "gale" is the one package with an identity policy,
	// so the initial lock records a full identity.
	fx.h.files["/"+lockFetchPinA+"/index/g/gale.toml"] = attestGaleDoc("1.56.0", true)
	fx.h.files["/"+lockFetchPinB+"/index/g/gale.toml"] = attestGaleDoc("9.9.9", false)
	if err := os.WriteFile(fx.c.GalePath,
		[]byte("[packages]\ngale = \"1.56.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runLockFetch(context.Background(), fx.c,
		fx.req("gale@1.56.0")); err != nil {
		t.Fatal(err)
	}
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

	err := runUpdateFetch(
		context.Background(), fx.c, []string{"gale"},
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
	if _, ok := got.Packages["gale@1.56.0"]; !ok {
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
	err := runUpdateFetch(
		context.Background(), fx.c, []string{"gale"},
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
	pkg, ok := got.Packages["gale@9.9.9"]
	if !ok {
		t.Fatalf("lock not updated to 9.9.9: %v", got.Packages)
	}
	if art, ok := pkg.Artifacts[currentPlatform()]; ok && art.Attestation != nil {
		t.Error("escaped update kept an attestation row")
	}
}

// The refusal must be reachable from the command surface.
func TestUpdateHasAllowAttestationDropFlag(t *testing.T) {
	if updateCmd.Flags().Lookup("allow-attestation-drop") == nil {
		t.Fatal("update --allow-attestation-drop is missing")
	}
}
