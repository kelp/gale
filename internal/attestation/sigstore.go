package attestation

// Native in-process Sigstore verification via sigstore-go.
// Verifies GitHub Artifact Attestation bundles (JSONL) against a
// subject digest and a GitHub repository identity, without shelling
// out to the gh CLI. Repository matching is case-insensitive (gh
// parity): the SAN regex carries (?i) and the SourceRepositoryURI
// extension is compared with EqualFold after verification.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// githubOIDCIssuer is the OIDC issuer for GitHub Actions workflow
// identities.
const githubOIDCIssuer = "https://token.actions.githubusercontent.com"

// provenancePredicateType is the in-toto predicate type required of
// every accepted attestation statement.
const provenancePredicateType = "https://slsa.dev/provenance/v1"

// testNoSCTEnv, when set alongside the trusted-root override
// (TrustedRootEnv), drops the Signed Certificate Timestamp
// requirement from the production verifier options. It exists
// because synthetic test fixtures cannot mint SCTs. It adds no
// attack surface: whoever controls the trusted-root env already
// controls the root of trust entirely. With the production root
// (no override) the flag is inert.
const testNoSCTEnv = "GALE_SIGSTORE_TEST_NO_SCT"

// SigstoreVerifier verifies GitHub Artifact Attestation bundles
// in-process with sigstore-go.
type SigstoreVerifier struct {
	roots *trustRootSource
	// opts overrides the sigstore-go verifier options; nil means
	// the production set (SCT + tlog + observer timestamps).
	// Test seam: fixtures cannot mint SCTs.
	opts []verify.VerifierOption

	once sync.Once
	v    *verify.Verifier
	err  error
}

// NewVerifier returns a production-configured verifier.
func NewVerifier() *SigstoreVerifier {
	return &SigstoreVerifier{roots: newTrustRootSource()}
}

// VerifyOCI verifies bundle JSONL against a GHCR manifest digest
// ("sha256:<hex>" or bare hex) for repo ("owner/name").
func (s *SigstoreVerifier) VerifyOCI(manifestDigest, repo string, bundles []byte) error {
	digest := strings.TrimPrefix(manifestDigest, "sha256:")
	return s.verifyBundleJSONL(digest, repo, bundles)
}

// VerifyFile hashes filePath, fetches its attestation bundles from
// the GitHub API (FetchBundle in attestation.go), and verifies.
func (s *SigstoreVerifier) VerifyFile(filePath, repo string) error {
	if err := requireFileSubject(filePath); err != nil {
		return err
	}
	digest, err := hashFile(filePath)
	if err != nil {
		return fmt.Errorf("hash attestation subject: %w", err)
	}
	bundles, err := FetchBundle(digest, repo)
	if err != nil {
		return fmt.Errorf("fetch attestation bundle: %w", err)
	}
	return s.verifyBundleJSONL(digest, repo, bundles)
}

// verifyBundleJSONL verifies newline-delimited bundle JSON against
// a hex sha256 subject digest; at least one bundle must verify.
func (s *SigstoreVerifier) verifyBundleJSONL(subjectDigest, repo string, bundles []byte) error {
	rawDigest, err := hex.DecodeString(subjectDigest)
	if err != nil {
		return fmt.Errorf("decode subject digest %q: %w", subjectDigest, err)
	}
	if len(rawDigest) != sha256.Size {
		return fmt.Errorf("subject digest is %d bytes, want %d (sha256)",
			len(rawDigest), sha256.Size)
	}

	v, err := s.verifier()
	if err != nil {
		return err
	}

	policy, err := repoPolicy(repo, rawDigest)
	if err != nil {
		return err
	}

	repoURI := "https://github.com/" + repo
	var failures []error
	for _, line := range bytes.Split(bundles, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if err := verifyBundleLine(v, policy, repoURI, line); err != nil {
			failures = append(failures, err)
			continue
		}
		return nil
	}

	if len(failures) == 0 {
		return errors.New("no attestation bundles to verify")
	}
	return fmt.Errorf("no attestation bundle verified: %w", errors.Join(failures...))
}

// verifier builds and memoizes the sigstore-go verifier from the
// resolved trusted root.
func (s *SigstoreVerifier) verifier() (*verify.Verifier, error) {
	s.once.Do(func() {
		if s.roots == nil {
			s.err = errors.New("sigstore verifier has no trust root source")
			return
		}
		tr, err := s.roots.load()
		if err != nil {
			s.err = fmt.Errorf("load sigstore trusted root: %w", err)
			return
		}
		opts := s.opts
		if opts == nil {
			opts = s.productionOptions()
		}
		v, err := verify.NewVerifier(tr, opts...)
		if err != nil {
			s.err = fmt.Errorf("build sigstore verifier: %w", err)
			return
		}
		s.v = v
	})
	return s.v, s.err
}

// productionOptions returns the verifier options used when no
// override is configured: an embedded SCT, a transparency log
// entry, and an observer timestamp.
//
// Test containment seam: when BOTH the trusted-root override
// (TrustedRootEnv) and testNoSCTEnv are set, the SCT requirement
// is dropped in favor of the tlog integrated timestamp. See
// testNoSCTEnv for the rationale; the env-root condition keeps
// the flag inert under the production root.
func (s *SigstoreVerifier) productionOptions() []verify.VerifierOption {
	if s.roots.envPath != "" && os.Getenv(testNoSCTEnv) != "" {
		return []verify.VerifierOption{
			verify.WithTransparencyLog(1),
			verify.WithObserverTimestamps(1),
		}
	}
	return []verify.VerifierOption{
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	}
}

// verifyBundleLine verifies one bundle against the policy, then
// requires (case-insensitively) that the certificate's
// SourceRepositoryURI extension names repoURI, and that the
// statement is SLSA provenance v1.
func verifyBundleLine(
	v *verify.Verifier, policy verify.PolicyBuilder,
	repoURI string, line []byte,
) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(line); err != nil {
		return fmt.Errorf("parse attestation bundle: %w", err)
	}

	res, err := v.Verify(&b, policy)
	if err != nil {
		return fmt.Errorf("verify attestation bundle: %w", err)
	}

	if res.Signature == nil || res.Signature.Certificate == nil {
		return errors.New("verification result missing certificate summary")
	}
	got := res.Signature.Certificate.SourceRepositoryURI
	if !strings.EqualFold(got, repoURI) {
		return fmt.Errorf("certificate source repository %q does not match %q",
			got, repoURI)
	}

	if res.Statement == nil {
		return errors.New("verification result missing in-toto statement")
	}
	if got := res.Statement.PredicateType; got != provenancePredicateType {
		return fmt.Errorf("unexpected predicate type %q, want %q",
			got, provenancePredicateType)
	}
	return nil
}

// repoPolicy builds the verification policy binding the artifact
// digest to a GitHub Actions identity from repo ("owner/name"): a
// case-insensitive SAN regex under the repo plus the GitHub OIDC
// issuer. The SourceRepositoryURI extension is checked after
// verification (EqualFold in verifyBundleLine) because the policy
// extension matcher is exact-match and GitHub repo names carry
// arbitrary canonical case.
func repoPolicy(repo string, rawDigest []byte) (verify.PolicyBuilder, error) {
	san, err := verify.NewSANMatcher(
		"", `(?i)^https://github\.com/`+regexp.QuoteMeta(repo)+`/`,
	)
	if err != nil {
		return verify.PolicyBuilder{}, fmt.Errorf("build SAN matcher: %w", err)
	}
	issuer, err := verify.NewIssuerMatcher(githubOIDCIssuer, "")
	if err != nil {
		return verify.PolicyBuilder{}, fmt.Errorf("build issuer matcher: %w", err)
	}
	certID, err := verify.NewCertificateIdentity(san, issuer, certificate.Extensions{})
	if err != nil {
		return verify.PolicyBuilder{}, fmt.Errorf("build certificate identity: %w", err)
	}

	return verify.NewPolicy(
		verify.WithArtifactDigest("sha256", rawDigest),
		verify.WithCertificateIdentity(certID),
	), nil
}
