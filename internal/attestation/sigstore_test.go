package attestation

// Tests for SigstoreVerifier: in-process bundle verification against
// synthetic sigstoretest trust material, fully offline. Verifiers
// are built with the reduced option set (tlog + observer timestamps)
// because fixtures cannot mint SCTs, and trust-root resolution flows
// through trustRootSource's env-path branch on purpose — the two
// units are meant to integrate.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/attestation/sigstoretest"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// stubSentinel is the stub-phase error text. Negative tests reject
// it so an error-returning stub cannot satisfy a want-error
// assertion — they stay red until real verification logic exists.
const stubSentinel = "not implemented"

// testArtifact returns the attestation subject bytes and their hex
// sha256 digest.
func testArtifact() ([]byte, string) {
	artifact := []byte("gale sigstore verifier test artifact")
	digest := sha256.Sum256(artifact)
	return artifact, hex.EncodeToString(digest[:])
}

// newTestSigstoreVerifier builds a fixture and a SigstoreVerifier
// whose trust root resolves through the env-path branch of
// trustRootSource, with the reduced (SCT-free) option set.
func newTestSigstoreVerifier(t *testing.T) (*sigstoretest.Fixture, *SigstoreVerifier) {
	t.Helper()
	fx, err := sigstoretest.New()
	if err != nil {
		t.Fatalf("new fixture: %v", err)
	}
	trJSON, err := fx.TrustedRootJSON()
	if err != nil {
		t.Fatalf("trusted root JSON: %v", err)
	}
	sv := &SigstoreVerifier{
		roots: &trustRootSource{envPath: writeTempFile(t, trJSON)},
		opts: []verify.VerifierOption{
			verify.WithTransparencyLog(1),
			verify.WithObserverTimestamps(1),
		},
	}
	return fx, sv
}

// mintBundle signs a bundle over artifact with the canonical GitHub
// identity, optionally mutated for negative cases.
func mintBundle(
	t *testing.T, fx *sigstoretest.Fixture,
	artifact []byte, mutate func(*sigstoretest.Opts),
) []byte {
	t.Helper()
	opts := sigstoretest.GitHubOpts(artifact)
	if mutate != nil {
		mutate(&opts)
	}
	b, err := fx.SignedBundle(opts)
	if err != nil {
		t.Fatalf("signed bundle: %v", err)
	}
	return b
}

// checkVerifyErr asserts the outcome direction of a verification
// call. Want-error cases also reject the stub sentinel so they fail
// at RED against error-returning stubs.
func checkVerifyErr(t *testing.T, err error, wantErr bool, wantInErr string) {
	t.Helper()
	if !wantErr {
		if err != nil {
			t.Fatalf("verify failed: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("verify succeeded, want error")
	}
	if strings.Contains(err.Error(), stubSentinel) {
		t.Fatalf("error is the stub sentinel, want a real verification error: %v", err)
	}
	if wantInErr != "" && !strings.Contains(err.Error(), wantInErr) {
		t.Fatalf("error %q does not mention %q", err, wantInErr)
	}
}

func TestVerifyBundleJSONLIdentity(t *testing.T) {
	fx, sv := newTestSigstoreVerifier(t)
	artifact, hexDigest := testArtifact()
	otherDigest := sha256.Sum256([]byte("a different artifact entirely"))

	tests := []struct {
		name      string
		mutate    func(*sigstoretest.Opts)
		digest    string
		wantErr   bool
		wantInErr string
	}{
		{
			name:   "valid bundle verifies",
			digest: hexDigest,
		},
		{
			name:    "wrong digest fails",
			digest:  hex.EncodeToString(otherDigest[:]),
			wantErr: true,
		},
		{
			// SAN forged, SourceRepositoryURI extension intact: an
			// implementation checking only the extension would pass.
			name: "wrong SAN fails",
			mutate: func(o *sigstoretest.Opts) {
				o.SAN = "https://github.com/evil/other/.github/workflows/build.yml@refs/heads/main"
			},
			digest:  hexDigest,
			wantErr: true,
		},
		{
			// SourceRepositoryURI extension forged, SAN intact: an
			// implementation checking only the SAN would pass.
			name: "wrong source repository extension fails",
			mutate: func(o *sigstoretest.Opts) {
				o.SourceRepositoryURI = "https://github.com/evil/other"
			},
			digest:  hexDigest,
			wantErr: true,
		},
		{
			// Regression pin: the realistic Fulcio-issuable forgery.
			// An attacker who owns a sibling-named repo gets a real
			// cert whose identity is a prefix-extension of ours.
			// Pins the SAN regex trailing-slash anchor and the
			// EqualFold length check.
			name: "sibling repo prefix attack fails",
			mutate: func(o *sigstoretest.Opts) {
				o.SAN = "https://github.com/kelp/gale-recipes-evil/.github/workflows/build.yml@refs/heads/main"
				o.SourceRepositoryURI = "https://github.com/kelp/gale-recipes-evil"
			},
			digest:  hexDigest,
			wantErr: true,
		},
		{
			// Regression pin: a cert missing the SourceRepositoryURI
			// extension entirely must fail closed, never match.
			name: "empty source repository extension fails",
			mutate: func(o *sigstoretest.Opts) {
				o.SourceRepositoryURI = ""
			},
			digest:  hexDigest,
			wantErr: true,
		},
		{
			name: "wrong OIDC issuer fails",
			mutate: func(o *sigstoretest.Opts) {
				o.Issuer = "https://token.evil.example.com"
			},
			digest:  hexDigest,
			wantErr: true,
		},
		{
			// Crypto verification passes for this bundle; the
			// post-verify statement check must reject it.
			name: "wrong predicate type fails",
			mutate: func(o *sigstoretest.Opts) {
				o.PredicateType = "https://example.com/not-provenance/v1"
			},
			digest:    hexDigest,
			wantErr:   true,
			wantInErr: "predicate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundleJSON := mintBundle(t, fx, artifact, tt.mutate)
			err := sv.verifyBundleJSONL(tt.digest, sigstoretest.Repo, bundleJSON)
			checkVerifyErr(t, err, tt.wantErr, tt.wantInErr)
		})
	}
}

func TestVerifyBundleJSONLInputHandling(t *testing.T) {
	fx, sv := newTestSigstoreVerifier(t)
	artifact, hexDigest := testArtifact()
	valid := mintBundle(t, fx, artifact, nil)

	tests := []struct {
		name    string
		input   []byte
		wantErr bool
	}{
		{
			name:  "garbage line before valid bundle verifies",
			input: append([]byte("not-json\n"), valid...),
		},
		{
			name:    "all garbage lines fail",
			input:   []byte("not-json\nnot-json-either"),
			wantErr: true,
		},
		{
			name:    "empty input fails",
			input:   nil,
			wantErr: true,
		},
		{
			name:    "whitespace-only input fails",
			input:   []byte(" \n\t\n"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sv.verifyBundleJSONL(hexDigest, sigstoretest.Repo, tt.input)
			checkVerifyErr(t, err, tt.wantErr, "")
		})
	}
}

func TestVerifyOCIDigestFormsAndRepoCase(t *testing.T) {
	fx, sv := newTestSigstoreVerifier(t)
	artifact, hexDigest := testArtifact()

	tests := []struct {
		name   string
		digest string
		repo   string
		mutate func(*sigstoretest.Opts)
	}{
		{
			name:   "prefixed digest",
			digest: "sha256:" + hexDigest,
			repo:   sigstoretest.Repo,
		},
		{
			name:   "bare hex digest",
			digest: hexDigest,
			repo:   sigstoretest.Repo,
		},
		{
			name:   "mixed-case repo",
			digest: "sha256:" + hexDigest,
			repo:   "KELP/Gale-Recipes",
		},
		{
			// Certificate side mixed-case, caller lowercase: GitHub
			// repo names can be canonically mixed-case, and the cert
			// SAN and SourceRepositoryURI extension carry that
			// canonical case. Matching must be case-insensitive
			// (EqualFold, gh CLI parity) or such repos NEVER verify.
			// Unlike the forgery cases in TestVerifyBundleJSONLIdentity,
			// SAN and SourceRepositoryURI move TOGETHER here — same
			// repo, re-cased.
			name:   "mixed-case certificate identity",
			digest: "sha256:" + hexDigest,
			repo:   "kelp/gale-recipes",
			mutate: func(o *sigstoretest.Opts) {
				o.SAN = "https://github.com/Kelp/Gale-Recipes/.github/workflows/build.yml@refs/heads/main"
				o.SourceRepositoryURI = "https://github.com/Kelp/Gale-Recipes"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundleJSON := mintBundle(t, fx, artifact, tt.mutate)
			if err := sv.VerifyOCI(tt.digest, tt.repo, bundleJSON); err != nil {
				t.Fatalf("VerifyOCI(%q, %q): %v", tt.digest, tt.repo, err)
			}
		})
	}
}

func TestNewVerifierNonNilRoots(t *testing.T) {
	sv := NewVerifier()
	if sv == nil {
		t.Fatal("NewVerifier returned nil")
	}
	if sv.roots == nil {
		t.Fatal("NewVerifier returned verifier with nil roots")
	}
}

// TestProductionOptionsSCTSelection pins the option-set selection
// directly, in particular the security property that
// GALE_SIGSTORE_TEST_NO_SCT ALONE is inert: without the
// trusted-root override the full production set (SCT + tlog +
// observer timestamps, 3 options) is returned regardless of the
// flag. Only override AND flag together yield the reduced
// 2-option set without the SCT requirement.
func TestProductionOptionsSCTSelection(t *testing.T) {
	tests := []struct {
		name    string
		envPath string
		flag    string
		wantLen int
	}{
		{"no override no flag", "", "", 3},
		{"flag alone is inert", "", "1", 3},
		{"override alone keeps SCT", "/tmp/root.json", "", 3},
		{"override plus flag drops SCT", "/tmp/root.json", "1", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(testNoSCTEnv, tt.flag)
			sv := &SigstoreVerifier{
				roots: &trustRootSource{envPath: tt.envPath},
			}
			if got := len(sv.productionOptions()); got != tt.wantLen {
				t.Fatalf("productionOptions returned %d options, want %d",
					got, tt.wantLen)
			}
		})
	}
}

// TestProductionOptionsNoSCTSeam is the first test of the
// production option-selection path (opts == nil). Fixture bundles
// carry no SCT, so under the production options they must fail on
// the SCT requirement; setting GALE_SIGSTORE_TEST_NO_SCT alongside
// the trusted-root override drops that requirement and the same
// bundle verifies end-to-end.
func TestProductionOptionsNoSCTSeam(t *testing.T) {
	fx, err := sigstoretest.New()
	if err != nil {
		t.Fatalf("new fixture: %v", err)
	}
	trJSON, err := fx.TrustedRootJSON()
	if err != nil {
		t.Fatalf("trusted root JSON: %v", err)
	}
	rootPath := writeTempFile(t, trJSON)
	artifact, hexDigest := testArtifact()
	bundleJSON := mintBundle(t, fx, artifact, nil)

	t.Setenv(TrustedRootEnv, rootPath)

	t.Run("without flag fails on SCT", func(t *testing.T) {
		sv := &SigstoreVerifier{roots: newTrustRootSource()}
		err := sv.verifyBundleJSONL(hexDigest, sigstoretest.Repo, bundleJSON)
		if err == nil {
			t.Fatal("verify succeeded under production options; " +
				"fixture bundle has no SCT, want an error")
		}
	})

	t.Run("with flag verifies end-to-end", func(t *testing.T) {
		t.Setenv(testNoSCTEnv, "1")
		sv := &SigstoreVerifier{roots: newTrustRootSource()}
		if err := sv.verifyBundleJSONL(
			hexDigest, sigstoretest.Repo, bundleJSON,
		); err != nil {
			t.Fatalf("verify with no-SCT seam: %v", err)
		}
	})
}
