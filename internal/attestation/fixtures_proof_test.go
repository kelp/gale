package attestation

// Proving test for the sigstoretest fixture package: generates a
// synthetic trusted root and signed bundles in memory, then runs a
// real sigstore-go verifier against them, offline.
//
// Verifier options the fixtures satisfy:
//
//	verify.WithTransparencyLog(1)
//	verify.WithObserverTimestamps(1)
//
// verify.WithSignedCertificateTimestamps is production-only — the
// synthetic leaf certificates carry no embedded SCTs (see the
// sigstoretest package comment).

import (
	"crypto/sha256"
	"testing"

	"github.com/kelp/gale/internal/attestation/sigstoretest"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// newFixtureVerifier builds a verifier from the fixture's
// trusted_root.json bytes, exercising the same
// root.NewTrustedRootFromJSON path production uses.
func newFixtureVerifier(t *testing.T, fx *sigstoretest.Fixture) *verify.Verifier {
	t.Helper()
	trJSON, err := fx.TrustedRootJSON()
	if err != nil {
		t.Fatalf("trusted root JSON: %v", err)
	}
	tr, err := root.NewTrustedRootFromJSON(trJSON)
	if err != nil {
		t.Fatalf("parse trusted root: %v", err)
	}
	v, err := verify.NewVerifier(
		tr,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	return v
}

// galeRecipesIdentity matches the SAN regex, OIDC issuer, and
// SourceRepositoryURI extension of kelp/gale-recipes, mirroring
// the policy the production verifier will enforce.
func galeRecipesIdentity(t *testing.T) verify.CertificateIdentity {
	t.Helper()
	san, err := verify.NewSANMatcher("", `^https://github\.com/kelp/gale-recipes/`)
	if err != nil {
		t.Fatalf("san matcher: %v", err)
	}
	issuer, err := verify.NewIssuerMatcher(sigstoretest.Issuer, "")
	if err != nil {
		t.Fatalf("issuer matcher: %v", err)
	}
	certID, err := verify.NewCertificateIdentity(san, issuer, certificate.Extensions{
		SourceRepositoryURI: sigstoretest.SourceRepositoryURI,
	})
	if err != nil {
		t.Fatalf("certificate identity: %v", err)
	}
	return certID
}

func TestSigstoreFixtures(t *testing.T) {
	fx, err := sigstoretest.New()
	if err != nil {
		t.Fatalf("new fixture: %v", err)
	}
	v := newFixtureVerifier(t, fx)

	artifact := []byte("gale sigstore fixture artifact")
	digest := sha256.Sum256(artifact)
	policy := verify.NewPolicy(
		verify.WithArtifactDigest("sha256", digest[:]),
		verify.WithCertificateIdentity(galeRecipesIdentity(t)),
	)

	tests := []struct {
		name          string
		mutate        func(*sigstoretest.Opts)
		wantErr       bool
		wantPredicate string
	}{
		{
			name:          "valid bundle verifies",
			mutate:        func(*sigstoretest.Opts) {},
			wantPredicate: sigstoretest.PredicateSLSAProvenanceV1,
		},
		{
			name: "wrong source repository fails",
			mutate: func(o *sigstoretest.Opts) {
				o.SAN = "https://github.com/evil/other/.github/workflows/build.yml@refs/heads/main"
				o.SourceRepositoryURI = "https://github.com/evil/other"
			},
			wantErr: true,
		},
		{
			name: "wrong OIDC issuer fails",
			mutate: func(o *sigstoretest.Opts) {
				o.Issuer = "https://token.evil.example.com"
			},
			wantErr: true,
		},
		{
			// Signature verification succeeds; production must catch
			// the predicate type in its post-verify statement check.
			name: "wrong predicate type verifies with wrong predicate",
			mutate: func(o *sigstoretest.Opts) {
				o.PredicateType = "https://example.com/not-provenance/v1"
			},
			wantPredicate: "https://example.com/not-provenance/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := sigstoretest.GitHubOpts(artifact)
			tt.mutate(&opts)
			bundleJSON, err := fx.SignedBundle(opts)
			if err != nil {
				t.Fatalf("signed bundle: %v", err)
			}

			var b bundle.Bundle
			if err := b.UnmarshalJSON(bundleJSON); err != nil {
				t.Fatalf("unmarshal bundle: %v", err)
			}

			res, err := v.Verify(&b, policy)
			if tt.wantErr {
				if err == nil {
					t.Fatal("bundle verified, want failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("bundle failed verification: %v", err)
			}
			if res.Statement == nil {
				t.Fatal("verification result missing statement")
			}
			if got := res.Statement.PredicateType; got != tt.wantPredicate {
				t.Fatalf("predicate type = %q, want %q", got, tt.wantPredicate)
			}
		})
	}
}
