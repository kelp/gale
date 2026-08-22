package fetch

// §7d: a declared attestation is verified before extraction.
// These tests run the production verifier fully offline: the
// bundle comes from sigstoretest's synthetic Sigstore, the
// trusted root from the env override, and the GitHub
// attestations API from a local server.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/attestation/sigstoretest"
	"github.com/kelp/gale/internal/index"
)

const galeRepo = "kelp/gale"

func setOfflineSigstore(
	t *testing.T, fx *sigstoretest.Fixture, repo string, artifact []byte,
) {
	t.Helper()
	rootJSON, err := fx.TrustedRootJSON()
	if err != nil {
		t.Fatalf("TrustedRootJSON: %v", err)
	}
	rootPath := filepath.Join(t.TempDir(), "trusted_root.json")
	if err := os.WriteFile(rootPath, rootJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(attestation.TrustedRootEnv, rootPath)
	t.Setenv("GALE_SIGSTORE_TEST_NO_SCT", "1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bundle, err := fx.SignedBundle(sigstoretest.Opts{ //nolint:contextcheck
			SAN: "https://github.com/" + repo +
				"/.github/workflows/release.yml@refs/heads/main",
			Issuer:              sigstoretest.Issuer,
			SourceRepositoryURI: "https://github.com/" + repo,
			PredicateType:       sigstoretest.PredicateSLSAProvenanceV1,
			Artifact:            artifact,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"attestations":[{"bundle":` + string(bundle) + `}]}`))
	}))
	t.Cleanup(srv.Close)
	orig := attestation.AttestationsEndpoint
	attestation.AttestationsEndpoint = srv.URL + "/repos/%s/attestations/%s"
	t.Cleanup(func() { attestation.AttestationsEndpoint = orig })
}

func galeAttestFixture(t *testing.T, repo string) (*fixture, index.Artifact) {
	t.Helper()
	sigfx, err := sigstoretest.New()
	if err != nil {
		t.Fatalf("sigstoretest.New: %v", err)
	}

	fx := newRawFixture(t, "gale", []byte(toolBody))
	setOfflineSigstore(t, sigfx, repo, fx.file)

	art := index.Artifact{
		URL:    fx.srv.URL + fx.urlPath,
		Format: "binary",
		SHA256: hexSHA(fx.file),
		TreeDigest: mappedDigest(t, map[string]fileSpec{
			"bin/gale": {toolBody, 0o755},
		}),
		Files: []index.FileEntry{{
			Src: "gale", Dest: "bin/gale", Mode: 0o755,
		}},
		Attestation: &[]bool{true}[0],
	}
	return fx, art
}

func TestToStoreVerifiesDeclaredAttestation(t *testing.T) {
	fx, art := galeAttestFixture(t, galeRepo)

	dest, err := fx.fetcher.ToStore(context.Background(),
		fx.store, "gale", "1.0.0", art)
	if err != nil {
		t.Fatalf("ToStore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "gale")); err != nil {
		t.Errorf("attested install did not land: %v", err)
	}
}

// A bundle signed for a different repository must refuse the
// landing even though its subject digest matches the artifact.
func TestToStoreRefusesForeignRepoAttestation(t *testing.T) {
	fx, art := galeAttestFixture(t, "evil/actor")

	_, err := fx.fetcher.ToStore(context.Background(),
		fx.store, "gale", "1.0.0", art)
	if err == nil {
		t.Fatal("foreign-repo attestation was accepted")
	}
	if !strings.Contains(err.Error(), "did not verify") &&
		!strings.Contains(err.Error(), "no attestation bundle verified") {
		t.Errorf("err = %v, want verification failure", err)
	}
	assertDestAbsent(t, fx, art.SHA256)
	assertNoStaging(t, fx.store.Root)
}

// The fetch path re-checks resolve-time policy: an unpoliced
// declaration cannot land even when handed straight to ToStore.
func TestToStoreRefusesUnpolicedAttestation(t *testing.T) {
	fx := newFixture(t)
	art := fx.mappedTar()
	art.Attestation = &[]bool{true}[0]

	_, err := fx.fetcher.ToStore(context.Background(),
		fx.store, "just", "1.56.0", art)
	if err == nil || !strings.Contains(err.Error(), "identity policy") {
		t.Fatalf("err = %v, want identity-policy refusal", err)
	}
	assertDestAbsent(t, fx, art.SHA256)
	assertNoStaging(t, fx.store.Root)
}
