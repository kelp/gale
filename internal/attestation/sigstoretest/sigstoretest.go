// Package sigstoretest builds synthetic Sigstore trust material and
// signed attestation bundles for offline unit tests. It produces a
// trusted root (as JSON, the same shape as trusted_root.json) plus
// DSSE-wrapped in-toto bundles shaped like GitHub Artifact
// Attestations: a Fulcio-style leaf certificate with a GitHub
// Actions workflow SAN and the OIDC-issuer (1.3.6.1.4.1.57264.1.8)
// and SourceRepositoryURI (1.3.6.1.4.1.57264.1.12) extensions, a
// Rekor v1 dsse tlog entry with an inclusion proof and promise, and
// an RFC 3161 timestamp.
//
// Bundles minted here verify with:
//
//	verify.NewVerifier(trustedRoot,
//		verify.WithTransparencyLog(1),
//		verify.WithObserverTimestamps(1))
//
// (WithIntegratedTimestamps and WithSignedTimestamps also hold.)
// They do NOT satisfy verify.WithSignedCertificateTimestamps: the
// synthetic leaf certificates carry no embedded SCTs, and neither
// sigstore-go nor certificate-transparency-go exports SCT-minting
// helpers — sigstore-go's own test suite only checks SCTs against
// real production bundles. Production code verifying against the
// real Sigstore trust root should still require SCTs; tests must be
// able to drop that single option.
package sigstoretest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"time"

	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	ca "github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore/pkg/signature"
	sigdsse "github.com/sigstore/sigstore/pkg/signature/dsse"
)

// Canonical identity values baked into fixtures. They mirror what a
// real GitHub Artifact Attestation for kelp/gale-recipes carries.
const (
	// Repo is the OWNER/REPO the fixtures attest for.
	Repo = "kelp/gale-recipes"
	// SourceRepositoryURI is the Fulcio SourceRepositoryURI
	// extension value (OID 1.3.6.1.4.1.57264.1.12).
	SourceRepositoryURI = "https://github.com/" + Repo
	// Issuer is the GitHub Actions OIDC issuer, carried in the
	// Fulcio issuer extensions (OIDs .1 and .8).
	Issuer = "https://token.actions.githubusercontent.com"
	// WorkflowSAN is the leaf certificate URI SAN, shaped like a
	// GitHub Actions workflow identity.
	WorkflowSAN = SourceRepositoryURI +
		"/.github/workflows/build.yml@refs/heads/main"
	// PredicateSLSAProvenanceV1 is the in-toto predicate type of
	// GitHub Artifact Attestations.
	PredicateSLSAProvenanceV1 = "https://slsa.dev/provenance/v1"

	payloadType = "application/vnd.in-toto+json"
)

// oidIssuerV1 is the deprecated Fulcio issuer extension
// (1.3.6.1.4.1.57264.1.1, raw string value). Real GitHub
// attestation certificates still carry it alongside the DER-encoded
// v2 extension, so fixtures include both. Declared locally because
// certificate.OIDIssuer is marked deprecated upstream.
var oidIssuerV1 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}

// Fixture holds an ephemeral Sigstore instance (Fulcio CA, Rekor
// key, TSA, CT log key) and mints bundles that chain to it. All
// keys live only in memory; nothing touches the network.
type Fixture struct {
	vs      *ca.VirtualSigstore
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	trusted *root.TrustedRoot
}

// New generates a fresh ephemeral Sigstore instance. It reuses
// sigstore-go's exported VirtualSigstore for the TSA, Rekor log,
// and CT log, and pairs it with a locally held Fulcio intermediate
// key so leaf certificates can carry GitHub Actions extensions.
func New() (*Fixture, error) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		return nil, fmt.Errorf("virtual sigstore: %w", err)
	}

	rootCert, rootKey, err := ca.GenerateRootCa()
	if err != nil {
		return nil, fmt.Errorf("fulcio root CA: %w", err)
	}
	intCert, intKey, err := ca.GenerateFulcioIntermediate(rootCert, rootKey)
	if err != nil {
		return nil, fmt.Errorf("fulcio intermediate CA: %w", err)
	}

	fulcioCA := &root.FulcioCertificateAuthority{
		Root:                rootCert,
		Intermediates:       []*x509.Certificate{intCert},
		ValidityPeriodStart: time.Now().Add(-5 * time.Hour),
		ValidityPeriodEnd:   time.Now().Add(time.Hour),
		URI:                 "https://fulcio.sigstoretest.invalid",
	}

	ctLogs, err := rawLogIDs(vs.CTLogs())
	if err != nil {
		return nil, err
	}
	rekorLogs, err := rawLogIDs(vs.RekorLogs())
	if err != nil {
		return nil, err
	}

	trusted, err := root.NewTrustedRoot(
		root.TrustedRootMediaType01,
		[]root.CertificateAuthority{fulcioCA},
		ctLogs,
		vs.TimestampingAuthorities(),
		rekorLogs,
	)
	if err != nil {
		return nil, fmt.Errorf("trusted root: %w", err)
	}

	return &Fixture{vs: vs, caCert: intCert, caKey: intKey, trusted: trusted}, nil
}

// rawLogIDs rewrites each TransparencyLog.ID from the hex-string
// form VirtualSigstore uses to the raw digest bytes the trusted
// root protobuf expects. Without this, marshaling the trusted root
// to JSON and parsing it back would double-hex-encode the log IDs
// and log lookups during verification would fail.
func rawLogIDs(
	logs map[string]*root.TransparencyLog,
) (map[string]*root.TransparencyLog, error) {
	for id, tl := range logs {
		raw, err := hex.DecodeString(id)
		if err != nil {
			return nil, fmt.Errorf("decode log id %q: %w", id, err)
		}
		tl.ID = raw
	}
	return logs, nil
}

// TrustedRoot returns the in-memory trusted root that fixture
// bundles chain to.
func (f *Fixture) TrustedRoot() *root.TrustedRoot {
	return f.trusted
}

// TrustedRootJSON serializes the trusted root in the standard
// trusted_root.json format, parseable by root.NewTrustedRootFromJSON.
func (f *Fixture) TrustedRootJSON() ([]byte, error) {
	data, err := f.trusted.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal trusted root: %w", err)
	}
	return data, nil
}

// Opts selects the identity and statement content baked into a
// minted bundle. Vary single fields off GitHubOpts to produce
// negative fixtures (wrong repo, wrong issuer, wrong predicate).
type Opts struct {
	// SAN is the leaf certificate URI SAN.
	SAN string
	// Issuer is the OIDC issuer extension value.
	Issuer string
	// SourceRepositoryURI is the Fulcio SourceRepositoryURI
	// extension value.
	SourceRepositoryURI string
	// PredicateType is the in-toto statement predicate type.
	PredicateType string
	// Artifact is the attestation subject; its SHA256 becomes the
	// statement's subject digest.
	Artifact []byte
}

// GitHubOpts returns Opts shaped like a real GitHub Artifact
// Attestation for kelp/gale-recipes over artifact.
func GitHubOpts(artifact []byte) Opts {
	return Opts{
		SAN:                 WorkflowSAN,
		Issuer:              Issuer,
		SourceRepositoryURI: SourceRepositoryURI,
		PredicateType:       PredicateSLSAProvenanceV1,
		Artifact:            artifact,
	}
}

// SignedBundle mints a Sigstore bundle (v0.3 JSON) containing a
// DSSE-wrapped in-toto statement, signed by a fresh Fulcio-style
// leaf certificate, logged in the fixture's Rekor instance, and
// timestamped by the fixture's TSA.
func (f *Fixture) SignedBundle(opts Opts) ([]byte, error) {
	stmt, err := statementJSON(opts)
	if err != nil {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}
	leaf, err := f.issueLeafCert(&key.PublicKey, opts)
	if err != nil {
		return nil, err
	}

	env, sig, err := signEnvelope(key, stmt)
	if err != nil {
		return nil, err
	}

	return f.assembleBundle(leaf, stmt, env, sig)
}

// statementJSON builds a minimal in-toto v1 statement whose subject
// digest is the SHA256 of opts.Artifact.
func statementJSON(opts Opts) ([]byte, error) {
	digest := sha256.Sum256(opts.Artifact)
	stmt := map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{{
			"name": "gale-fixture.tar.gz",
			"digest": map[string]string{
				"sha256": hex.EncodeToString(digest[:]),
			},
		}},
		"predicateType": opts.PredicateType,
		"predicate": map[string]any{
			"buildDefinition": map[string]any{
				"buildType": "https://actions.github.io/buildtypes/workflow/v1",
			},
			"runDetails": map[string]any{
				"builder": map[string]any{"id": opts.SAN},
			},
		},
	}
	data, err := json.Marshal(stmt)
	if err != nil {
		return nil, fmt.Errorf("marshal statement: %w", err)
	}
	return data, nil
}

// issueLeafCert signs a Fulcio-style leaf certificate carrying the
// GitHub Actions identity from opts: a URI SAN plus the OIDC-issuer
// and SourceRepositoryURI extensions.
func (f *Fixture) issueLeafCert(
	pub *ecdsa.PublicKey, opts Opts,
) (*x509.Certificate, error) {
	sanURL, err := url.Parse(opts.SAN)
	if err != nil {
		return nil, fmt.Errorf("parse SAN: %w", err)
	}
	exts, err := githubExtensions(opts.Issuer, opts.SourceRepositoryURI)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber:    big.NewInt(1),
		URIs:            []*url.URL{sanURL},
		NotBefore:       time.Now().Add(-time.Minute),
		NotAfter:        time.Now().Add(10 * time.Minute),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		IsCA:            false,
		ExtraExtensions: exts,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, f.caCert, pub, f.caKey)
	if err != nil {
		return nil, fmt.Errorf("sign leaf certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse leaf certificate: %w", err)
	}
	return cert, nil
}

// githubExtensions builds the Fulcio extensions GitHub Actions
// certificates carry: the deprecated raw-string issuer (OID .1) and
// the DER-encoded issuer v2 (.8) and SourceRepositoryURI (.12).
func githubExtensions(issuer, sourceRepo string) ([]pkix.Extension, error) {
	derIssuer, err := asn1.Marshal(issuer)
	if err != nil {
		return nil, fmt.Errorf("encode issuer extension: %w", err)
	}
	derRepo, err := asn1.Marshal(sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("encode source repository extension: %w", err)
	}
	return []pkix.Extension{
		{Id: oidIssuerV1, Value: []byte(issuer)},
		{Id: certificate.OIDIssuerV2, Value: derIssuer},
		{Id: certificate.OIDSourceRepositoryURI, Value: derRepo},
	}, nil
}

// signEnvelope wraps payload in a DSSE envelope signed with key and
// returns the envelope plus the raw signature bytes.
func signEnvelope(
	key *ecdsa.PrivateKey, payload []byte,
) (*dsse.Envelope, []byte, error) {
	signer, err := signature.LoadECDSASignerVerifier(key, crypto.SHA256)
	if err != nil {
		return nil, nil, fmt.Errorf("load signer: %w", err)
	}
	dsseSigner, err := dsse.NewEnvelopeSigner(&sigdsse.SignerAdapter{
		SignatureSigner: signer,
		Pub:             key.Public(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("dsse signer: %w", err)
	}
	env, err := dsseSigner.SignPayload(context.Background(), payloadType, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("sign payload: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signatures[0].Sig)
	if err != nil {
		return nil, nil, fmt.Errorf("decode signature: %w", err)
	}
	return env, sig, nil
}

// assembleBundle combines the leaf certificate, DSSE envelope, TSA
// timestamp, and Rekor tlog entry into a v0.3 Sigstore bundle and
// returns its JSON encoding.
func (f *Fixture) assembleBundle(
	leaf *x509.Certificate, payload []byte, env *dsse.Envelope, sig []byte,
) ([]byte, error) {
	tsr, err := f.vs.TimestampResponse(sig)
	if err != nil {
		return nil, fmt.Errorf("timestamp response: %w", err)
	}

	tle, err := f.tlogEntry(leaf, env, sig)
	if err != nil {
		return nil, err
	}

	mediaType, err := bundle.MediaTypeString("v0.3")
	if err != nil {
		return nil, fmt.Errorf("bundle media type: %w", err)
	}

	pb := &protobundle.Bundle{
		MediaType: mediaType,
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_Certificate{
				Certificate: &protocommon.X509Certificate{RawBytes: leaf.Raw},
			},
			TlogEntries: []*protorekor.TransparencyLogEntry{tle},
			TimestampVerificationData: &protobundle.TimestampVerificationData{
				Rfc3161Timestamps: []*protocommon.RFC3161SignedTimestamp{
					{SignedTimestamp: tsr},
				},
			},
		},
		Content: &protobundle.Bundle_DsseEnvelope{
			DsseEnvelope: &protodsse.Envelope{
				Payload:     payload,
				PayloadType: payloadType,
				Signatures:  []*protodsse.Signature{{Sig: sig}},
			},
		},
	}

	b, err := bundle.NewBundle(pb)
	if err != nil {
		return nil, fmt.Errorf("build bundle: %w", err)
	}
	data, err := b.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal bundle: %w", err)
	}
	return data, nil
}

// tlogEntry logs the envelope in the fixture's Rekor instance and
// returns a fully populated protobuf transparency log entry with an
// inclusion proof, inclusion promise (SET), and kind/version.
func (f *Fixture) tlogEntry(
	leaf *x509.Certificate, env *dsse.Envelope, sig []byte,
) (*protorekor.TransparencyLogEntry, error) {
	integrated := time.Now().Add(5 * time.Minute).Unix()
	entry, err := f.vs.GenerateTlogEntry(leaf, env, sig, integrated, true)
	if err != nil {
		return nil, fmt.Errorf("generate tlog entry: %w", err)
	}

	tle := entry.TransparencyLogEntry()
	// GenerateTlogEntry leaves KindVersion and InclusionPromise
	// unset on the protobuf (they live only on the in-memory
	// Entry), but bundle parsing requires KindVersion and the SET
	// is what makes integrated timestamps verifiable. Fill both.
	tle.KindVersion = &protorekor.KindVersion{
		Kind:    "dsse",
		Version: "0.0.1",
	}
	set, err := f.vs.RekorSignPayload(tlog.RekorPayload{
		Body:           base64.StdEncoding.EncodeToString(tle.CanonicalizedBody),
		IntegratedTime: tle.IntegratedTime,
		LogIndex:       tle.LogIndex,
		LogID:          hex.EncodeToString(tle.LogId.KeyId),
	})
	if err != nil {
		return nil, fmt.Errorf("sign entry timestamp: %w", err)
	}
	tle.InclusionPromise = &protorekor.InclusionPromise{
		SignedEntryTimestamp: set,
	}
	return tle, nil
}
