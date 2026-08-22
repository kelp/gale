package main

// §7d end-to-end for `gale verify`: a locked attestation re-fetches
// the locked URL and verifies the bundle against the locked
// identity. Fully offline via sigstoretest plus local servers.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/attestation/sigstoretest"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/store"
)

const galeArchiveBody = "gale-archive-bytes\n"

type attestVerifyFix struct {
	*verifyFix
	srv *httptest.Server
}

// newAttestVerifyFix pins gale@1.0.0 with a full locked identity,
// plants a matching fetch tree, and points the locked URL at a
// local server serving served. The GitHub attestation API serves
// a synthetic bundle for repo signed over archive.
func newAttestVerifyFix(
	t *testing.T, repo string, served []byte,
) (*attestVerifyFix, string) {
	t.Helper()
	sigfx, err := sigstoretest.New()
	if err != nil {
		t.Fatalf("sigstoretest.New: %v", err)
	}
	bundle, err := sigfx.SignedBundle(sigstoretest.Opts{
		SAN: "https://github.com/" + repo +
			"/.github/workflows/release.yml@refs/heads/main",
		Issuer:              sigstoretest.Issuer,
		SourceRepositoryURI: "https://github.com/" + repo,
		PredicateType:       sigstoretest.PredicateSLSAProvenanceV1,
		Artifact:            []byte(galeArchiveBody),
	})
	if err != nil {
		t.Fatalf("SignedBundle: %v", err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"attestations":[{"bundle":` + string(bundle) + `}]}`,
		))
	}))
	t.Cleanup(api.Close)
	origEndpoint := attestation.AttestationsEndpoint
	attestation.AttestationsEndpoint = api.URL + "/repos/%s/attestations/%s"
	t.Cleanup(func() { attestation.AttestationsEndpoint = origEndpoint })

	rootJSON, err := sigfx.TrustedRootJSON()
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(t.TempDir(), "trusted_root.json")
	if err := os.WriteFile(rootPath, rootJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(attestation.TrustedRootEnv, rootPath)
	t.Setenv("GALE_SIGSTORE_TEST_NO_SCT", "1")

	fx := &attestVerifyFix{verifyFix: newVerifyFix(t)}
	if err := os.WriteFile(fx.c.GalePath,
		[]byte("[packages]\ngale = \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte(galeArchiveBody))
	sha := hex.EncodeToString(sum[:])

	// Plant the fetch tree matching the mapped layout of served.
	st := store.NewStore(fx.c.StoreRoot)
	dest, err := st.FetchPath("gale", "1.0.0", sha)
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dest, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "gale"),
		[]byte(galeArchiveBody), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := provenance.DigestTree(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}

	fx.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(served)
	}))
	t.Cleanup(fx.srv.Close)
	t.Cleanup(download.SetHTTPClient(fx.srv.Client()))

	art := lockfile.V2Artifact{
		URL:        fx.srv.URL + "/gale",
		Format:     "binary",
		SHA256:     sha,
		TreeDigest: digest,
		Method:     provenance.MethodFetch,
		Files:      []lockfile.V2File{{Src: "gale", Dest: "bin/gale", Mode: 0o755}},
		Attestation: &lockfile.V2Attestation{
			Issuer: attestation.GitHubIssuer,
			SAN:    "https://github.com/" + repo,
			Repo:   repo,
		},
	}
	key := "gale@1.0.0"
	if err := lockfile.WriteV2(fx.lp, &lockfile.V2{
		Version: lockfile.SchemaV2,
		Targets: lockfile.Targets{
			Default: &lockfile.Target{Roots: []string{key}},
		},
		Packages: map[string]lockfile.V2Package{
			key: {Artifacts: map[string]lockfile.V2Artifact{
				currentPlatform(): art,
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return fx, sha
}

func TestVerifyAttestedPackage(t *testing.T) {
	fx, _ := newAttestVerifyFix(t, "kelp/gale", []byte(galeArchiveBody))
	if err := runVerify(context.Background(), fx.c, ""); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// The re-fetched bytes must match the lock's SHA256 before the
// bundle check; substituted upstream content refuses even when
// a valid bundle exists for the original digest.
func TestVerifyRefusesSubstitutedRefetch(t *testing.T) {
	fx, _ := newAttestVerifyFix(t, "kelp/gale", []byte("evil\n"))
	err := runVerify(context.Background(), fx.c, "")
	if !errors.Is(err, errVerifyAttestation) {
		t.Fatalf("err = %v, want errVerifyAttestation", err)
	}
	if !strings.Contains(err.Error(), "re-fetched") {
		t.Errorf("err = %v, want it to name the re-fetch", err)
	}
}

// A locked identity other than the policy's cannot pass, even
// against a bundle genuinely signed by that other repo.
func TestVerifyRefusesForeignLockedIdentity(t *testing.T) {
	fx, _ := newAttestVerifyFix(t, "evil/actor", []byte(galeArchiveBody))
	err := runVerify(context.Background(), fx.c, "")
	if !errors.Is(err, errVerifyIdentity) {
		t.Fatalf("err = %v, want errVerifyIdentity", err)
	}
}
