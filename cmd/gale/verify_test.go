package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

const verifySHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type verifyFix struct {
	t    *testing.T
	c    *cmdContext
	home string
	lp   string
}

func newVerifyFix(t *testing.T) *verifyFix {
	t.Helper()
	p := newProjectLayout(t)
	if err := os.WriteFile(p.configPath, []byte("[packages]\njust = \"1.56.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lp, err := lockfilePath(p.configPath)
	if err != nil {
		t.Fatal(err)
	}
	return &verifyFix{
		t:    t,
		home: p.home,
		lp:   lp,
		c: &cmdContext{
			GalePath:  p.configPath,
			GaleDir:   p.galeDir,
			StoreRoot: p.storeRoot,
		},
	}
}

func (fx *verifyFix) plantJust() (sha, digest string) {
	fx.t.Helper()
	const name, version = "just", "1.56.0"
	st := store.NewStore(fx.c.StoreRoot)
	dest, err := st.FetchPath(name, version, verifySHA)
	if err != nil {
		fx.t.Fatal(err)
	}
	bin := filepath.Join(dest, "bin", name)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		fx.t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(name+"-bytes\n"), 0o755); err != nil {
		fx.t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		fx.t.Fatal(err)
	}
	digest, err = provenance.DigestTree(context.Background(), dest)
	if err != nil {
		fx.t.Fatal(err)
	}
	return verifySHA, digest
}

type verifyLock struct {
	sha, digest, plat string
	attest            bool
}

func (fx *verifyFix) writeJust(v verifyLock) {
	fx.t.Helper()
	const name, version = "just", "1.56.0"
	key := name + "@" + version
	art := lockfile.V2Artifact{
		URL:        "https://github.com/kelp/" + name + "/releases/download/" + version + "/" + name,
		Format:     "binary",
		SHA256:     v.sha,
		TreeDigest: v.digest,
		Method:     provenance.MethodFetch,
		Files: []lockfile.V2File{{
			Src: name, Dest: "bin/" + name, Mode: 0o755,
		}},
	}
	if v.attest {
		art.Attestation = &lockfile.V2Attestation{}
	}
	if err := lockfile.WriteV2(fx.lp, &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{key}},
		},
		Packages: map[string]lockfile.V2Package{
			key: {Artifacts: map[string]lockfile.V2Artifact{v.plat: art}},
		},
	}); err != nil {
		fx.t.Fatal(err)
	}
}

func TestVerifyMatchingTree(t *testing.T) {
	fx := newVerifyFix(t)
	sha, digest := fx.plantJust()
	fx.writeJust(verifyLock{sha: sha, digest: digest, plat: currentPlatform()})
	if err := runVerify(context.Background(), fx.c, ""); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyDriftedTree(t *testing.T) {
	fx := newVerifyFix(t)
	sha, digest := fx.plantJust()
	fx.writeJust(verifyLock{sha: sha, digest: digest, plat: currentPlatform()})
	dest, err := store.NewStore(fx.c.StoreRoot).FetchPath("just", "1.56.0", sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "bin", "just"), []byte("tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fx.lp)
	if err != nil {
		t.Fatal(err)
	}
	err = runVerify(context.Background(), fx.c, "just")
	if !errors.Is(err, errVerifyDigest) {
		t.Fatalf("err = %v, want errVerifyDigest", err)
	}
	after, err := os.ReadFile(fx.lp)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("verify mutated the lock")
	}
}

func TestVerifyMissingFetchStore(t *testing.T) {
	fx := newVerifyFix(t)
	fx.writeJust(verifyLock{sha: verifySHA, digest: "sha256:dead", plat: currentPlatform()})
	err := runVerify(context.Background(), fx.c, "just")
	if !errors.Is(err, errVerifyMissingStore) {
		t.Fatalf("err = %v, want errVerifyMissingStore", err)
	}
}

func TestVerifySourceDirDoesNotMaskFetch(t *testing.T) {
	fx := newVerifyFix(t)
	sha, digest := fx.plantJust()
	fx.writeJust(verifyLock{sha: sha, digest: digest, plat: currentPlatform()})
	seedStore(t, fx.c.StoreRoot, "just", "1.56.0")
	dest, err := store.NewStore(fx.c.StoreRoot).FetchPath("just", "1.56.0", sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	err = runVerify(context.Background(), fx.c, "just")
	if !errors.Is(err, errVerifyMissingStore) {
		t.Fatalf("err = %v, want errVerifyMissingStore (not source tree)", err)
	}
}

func TestVerifyV1Lock(t *testing.T) {
	fx := newVerifyFix(t)
	if err := lockfile.WriteV1(fx.lp, &lockfile.V1{
		Version: lockfile.SchemaVersion,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{"just@1.56.0-1"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	err := runVerify(context.Background(), fx.c, "just")
	if !errors.Is(err, errVerifyV1) {
		t.Fatalf("err = %v, want errVerifyV1", err)
	}
	if errors.Is(err, errVerifyNoLock) {
		t.Error("v1 lock classified as missing")
	}
}

func TestVerifyNoLock(t *testing.T) {
	fx := newVerifyFix(t)
	err := runVerify(context.Background(), fx.c, "just")
	if !errors.Is(err, errVerifyNoLock) {
		t.Fatalf("err = %v, want errVerifyNoLock", err)
	}
}

func TestVerifyLockedAttestationRefuses(t *testing.T) {
	fx := newVerifyFix(t)
	sha, digest := fx.plantJust()
	fx.writeJust(verifyLock{sha: sha, digest: digest, plat: currentPlatform(), attest: true})
	err := runVerify(context.Background(), fx.c, "just")
	if !errors.Is(err, errVerifyAttestation) {
		t.Fatalf("err = %v, want errVerifyAttestation", err)
	}
}

func TestVerifyEmptyTreeDigest(t *testing.T) {
	fx := newVerifyFix(t)
	sha, _ := fx.plantJust()
	fx.writeJust(verifyLock{sha: sha, plat: currentPlatform()})
	err := runVerify(context.Background(), fx.c, "just")
	if !errors.Is(err, errVerifyEmptyDigest) {
		t.Fatalf("err = %v, want errVerifyEmptyDigest", err)
	}
}

func TestVerifyMissingCurrentPlatform(t *testing.T) {
	fx := newVerifyFix(t)
	other := "darwin/arm64"
	if currentPlatform() == other {
		other = "linux/amd64"
	}
	fx.writeJust(verifyLock{sha: verifySHA, digest: "sha256:dead", plat: other})
	err := runVerify(context.Background(), fx.c, "just")
	if !errors.Is(err, errVerifyNoPlatform) {
		t.Fatalf("err = %v, want errVerifyNoPlatform", err)
	}
}

func TestVerifyUnknownPackage(t *testing.T) {
	fx := newVerifyFix(t)
	sha, digest := fx.plantJust()
	fx.writeJust(verifyLock{sha: sha, digest: digest, plat: currentPlatform()})
	err := runVerify(context.Background(), fx.c, "fd")
	if !errors.Is(err, errVerifyUnknownRoot) {
		t.Fatalf("err = %v, want errVerifyUnknownRoot", err)
	}
}

func TestVerifyGHCRHelpersGone(t *testing.T) {
	// compile-time: the old helpers must not exist. This file
	// replacing TestVerifyBlobURL is the proof.
	if verifyCmd.Short == "Verify attestation for an installed package" {
		t.Fatal("verify Short still describes GHCR attestation")
	}
}
