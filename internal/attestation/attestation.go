package attestation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kelp/gale/internal/ghcr"
	"github.com/kelp/gale/internal/httpclient"
)

// DefaultRepo is the GitHub repository where recipe
// binaries are built and attested.
const DefaultRepo = "kelp/gale-recipes"

// lookPath is the function used to find gh on PATH.
// Overridden in tests.
var lookPath = exec.LookPath

// warnWriter receives the one-time "attestation disabled"
// warning emitted when gh is missing or too old. Defaults
// to stderr; overridden in tests.
var warnWriter io.Writer = os.Stderr

// attestationsEndpoint is the GitHub Attestations API URL
// format string. It is package-level so tests can point it
// at a local HTTP server.
var attestationsEndpoint = "https://api.github.com/repos/%s/attestations/%s"

// Verifier checks Sigstore attestations.
type Verifier interface {
	// Available reports whether attestation verification
	// can run. The first time it returns false in a process
	// it also emits a warning to stderr explaining why —
	// silently skipping attestation would hide a real
	// degradation of the supply-chain guarantee.
	Available() bool
	// UnavailableReason returns a human-readable
	// explanation of why Available returned false. Empty
	// when Available is true.
	UnavailableReason() string
	// VerifyFile verifies a local archive file by fetching
	// its Sigstore bundle from the public GitHub Attestations
	// API and passing it to gh via --bundle.
	VerifyFile(filePath, repo string) error
	// VerifyOCIReferrer verifies an OCI image against a Sigstore
	// bundle that gale already fetched from the registry's OCI
	// referrers. It writes the bundle to a temp file and runs gh
	// offline via --bundle, which needs no GitHub token.
	VerifyOCIReferrer(ociURI, repo string, bundle []byte) error
}

// GHVerifier implements Verifier using the gh CLI.
type GHVerifier struct {
	probeOnce sync.Once
	available bool
	reason    string
	warnOnce  sync.Once
}

// NewVerifier returns a Verifier backed by the gh CLI.
func NewVerifier() Verifier {
	return &GHVerifier{}
}

// Available reports whether a usable gh CLI is locatable
// and supports the "attestation" subcommand. Emits a
// one-time stderr warning on the first false result so
// the user always sees that attestation verification was
// skipped — never silently.
func (v *GHVerifier) Available() bool {
	v.probeOnce.Do(v.probe)
	if !v.available {
		v.warnOnce.Do(func() {
			fmt.Fprintf(warnWriter,
				"warning: attestation verification disabled: %s\n",
				v.reason)
		})
	}
	return v.available
}

// UnavailableReason returns why Available is false, or
// "" when attestation is available.
func (v *GHVerifier) UnavailableReason() string {
	v.probeOnce.Do(v.probe)
	return v.reason
}

// probe locates gh and confirms it supports the
// "attestation" subcommand (added in gh 2.49.0). Runs at
// most once per verifier.
func (v *GHVerifier) probe() {
	ghPath, err := findGh()
	if err != nil {
		v.reason = "gh CLI not found; install with " +
			"`gale install gh` or see https://cli.github.com"
		return
	}
	// `gh attestation --help` exits 0 on a current gh and
	// non-zero with "unknown command" on gh < 2.49.0. We
	// don't care about the output text — only the exit
	// status — which keeps this resilient to future help
	// wording changes.
	cmd := exec.Command(ghPath, "attestation", "--help")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		v.reason = fmt.Sprintf(
			"gh at %s lacks 'attestation' subcommand "+
				"(need gh >= 2.49.0); install a current gh "+
				"with `gale install gh`", ghPath,
		)
		return
	}
	v.available = true
}

// VerifyFile verifies a local archive file by downloading
// its Sigstore bundle from the public GitHub Attestations
// API and passing it to gh via --bundle.
func (v *GHVerifier) VerifyFile(filePath, repo string) error {
	if err := requireFileSubject(filePath); err != nil {
		return err
	}

	digest, err := hashFile(filePath)
	if err != nil {
		return fmt.Errorf("hash attestation subject: %w", err)
	}

	bundle, err := FetchBundle(digest, repo)
	if err != nil {
		return fmt.Errorf("fetch attestation bundle: %w", err)
	}

	bundleFile, err := writeBundleTemp(bundle)
	if err != nil {
		return err
	}
	defer os.Remove(bundleFile)

	return runVerify(filePath, repo, bundleFile)
}

// VerifyOCIReferrer verifies an OCI image against a bundle gale
// already fetched from the registry's OCI referrers. It writes the
// bundle to a temp .jsonl and runs gh attestation verify <ociURI>
// --bundle <temp>, which runs offline and needs no GitHub token.
func (v *GHVerifier) VerifyOCIReferrer(ociURI, repo string, bundle []byte) error {
	bundleFile, err := writeBundleTemp(bundle)
	if err != nil {
		return err
	}
	defer os.Remove(bundleFile)

	return runVerify(ociURI, repo, bundleFile)
}

// PrebuiltParams routes attestation verification for a prebuilt
// binary through one shared decision path so the installer and
// `gale verify` never duplicate the referrer-then-file fallback.
type PrebuiltParams struct {
	// Repo is the GitHub repository whose attestations sign the
	// artifact (e.g. "kelp/gale-recipes").
	Repo string
	// OCIURI is the oci:// reference for the manifest, pinned by
	// digest. Required for the referrer path.
	OCIURI string
	// ManifestDigest is the image manifest digest. Empty disables
	// the referrer path and goes straight to the file fallback.
	ManifestDigest string
	// FetchBundle fetches the Sigstore bundle from the OCI
	// referrers. It returns ghcr.ErrNoReferrer when none exists.
	FetchBundle func() ([]byte, error)
	// Archive yields the local archive to verify on the file
	// fallback, with an optional cleanup func.
	Archive func() (path string, cleanup func(), err error)
}

// VerifyPrebuilt verifies a prebuilt binary, preferring the
// tokenless OCI-referrer path and falling back to the GitHub
// Attestations API file path only when no referrer exists. It
// fails closed: once a referrer bundle is found, a verification
// error propagates without a file fallback, and a non-ErrNoReferrer
// fetch error propagates too.
func VerifyPrebuilt(v Verifier, p PrebuiltParams) error {
	if p.ManifestDigest != "" && p.OCIURI != "" && p.FetchBundle != nil {
		bundle, err := p.FetchBundle()
		switch {
		case err == nil:
			return v.VerifyOCIReferrer(p.OCIURI, p.Repo, bundle)
		case errors.Is(err, ghcr.ErrNoReferrer):
			// Fall through to the file path.
		default:
			return fmt.Errorf("fetch referrer bundle: %w", err)
		}
	}

	path, cleanup, err := p.Archive()
	if err != nil {
		return fmt.Errorf("resolve attestation archive: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	return v.VerifyFile(path, p.Repo)
}

// FetchBundle downloads the Sigstore attestation bundle(s)
// for the given artifact digest from the public GitHub
// Attestations API. The digest must be a hex-encoded SHA256
// string (no "sha256:" prefix). It returns the bundle(s) as
// JSONL, suitable for passing to gh attestation verify --bundle.
//
// If GALE_GITHUB_TOKEN or GITHUB_TOKEN is set, it is used as
// a Bearer token to avoid unauthenticated rate limits.
func FetchBundle(digest, repo string) ([]byte, error) {
	subject := "sha256:" + digest
	u := fmt.Sprintf(attestationsEndpoint, repo, subject)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	if tok := attestationToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := httpclient.Default().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch attestation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch attestation: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read attestation response: %w", err)
	}

	var env struct {
		Attestations []struct {
			Bundle    json.RawMessage `json:"bundle"`
			BundleURL string          `json:"bundle_url"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("parse attestation response: %w", err)
	}

	if len(env.Attestations) == 0 {
		return nil, fmt.Errorf("no attestations found for %s", subject)
	}

	var buf bytes.Buffer
	for _, a := range env.Attestations {
		bundle, berr := bundleBytes(a.Bundle, a.BundleURL)
		if berr != nil {
			return nil, berr
		}
		if len(bundle) == 0 {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.Write(bundle)
	}

	if buf.Len() == 0 {
		return nil, fmt.Errorf("no attestation bundles found for %s", subject)
	}

	return buf.Bytes(), nil
}

// OCIURI constructs an OCI image reference for attestation
// verification. If digest is non-empty, it pins the manifest
// by digest ("oci://ghcr.io/<repoPath>@<digest>"). Otherwise
// it falls back to the tag form ("oci://ghcr.io/<repoPath>:<bareVersion>-<platform>").
func OCIURI(repoPath, version, platform, digest string) string {
	if digest != "" {
		return fmt.Sprintf("oci://ghcr.io/%s@%s", repoPath, digest)
	}
	return fmt.Sprintf(
		"oci://ghcr.io/%s:%s",
		repoPath, BareVersion(version)+"-"+platform,
	)
}

// BareVersion strips a Debian-style numeric revision suffix from v.
// A trailing "-<N>" where N is a positive integer is removed; any
// other suffix (e.g. "-rc1", "-dev.2") is left in place.
func BareVersion(v string) string {
	dash := strings.LastIndexByte(v, '-')
	if dash < 0 {
		return v
	}
	suffix := v[dash+1:]
	if suffix == "" {
		return v
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || n <= 0 {
		return v
	}
	return v[:dash]
}

// bundleBytes returns the raw bundle bytes, fetching from
// bundleURL if the inline bundle is absent.
func bundleBytes(bundle json.RawMessage, bundleURL string) ([]byte, error) {
	if len(bundle) > 0 {
		return bundle, nil
	}
	if bundleURL == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bundleURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build bundle request: %w", err)
	}

	resp, err := httpclient.Default().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch bundle: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch bundle: HTTP %d", resp.StatusCode)
	}

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	return out, nil
}

// attestationToken returns an optional GitHub token for the
// Attestations API, preferring GALE_GITHUB_TOKEN.
func attestationToken() string {
	if tok := os.Getenv("GALE_GITHUB_TOKEN"); tok != "" {
		return tok
	}
	return os.Getenv("GITHUB_TOKEN")
}

// requireFileSubject rejects an attestation subject that is
// missing or a directory. It is the single guard for the
// file-verification path (VerifyFile); the OCI path uses an
// oci:// subject and never reaches it.
func requireFileSubject(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat attestation subject: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf(
			"attestation subject is a directory, expected a file: %s",
			path,
		)
	}
	return nil
}

// hashFile returns the hex-encoded SHA256 hash of path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for hashing: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeBundleTemp writes bundle to a temporary file and returns
// its path. The caller is responsible for deleting the file.
func writeBundleTemp(bundle []byte) (string, error) {
	f, err := os.CreateTemp("", "gale-attestation-*.jsonl")
	if err != nil {
		return "", fmt.Errorf("create attestation bundle file: %w", err)
	}
	if _, err := f.Write(bundle); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("write attestation bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("close attestation bundle file: %w", err)
	}
	return f.Name(), nil
}

// findGh locates the gh CLI, preferring gale's bundled
// ~/.gale/current/bin/gh over the system PATH. Why: an
// older gh earlier on PATH (system packages still ship
// 2.46.x in many distros) lacks the "attestation"
// subcommand added in gh 2.49.0, which would otherwise
// downgrade binary installs to source builds. Gale's
// own gh recipe is kept current.
func findGh() (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		bundled := filepath.Join(
			home, ".gale", "current", "bin", "gh",
		)
		if info, err := os.Stat(bundled); err == nil && !info.IsDir() {
			return bundled, nil
		}
	}
	return lookPath("gh")
}

// runVerify runs gh attestation verify against subject, passing
// --bundle <bundlePath> when bundlePath is non-empty.
func runVerify(subject, repo, bundlePath string) error {
	ghPath, err := findGh()
	if err != nil {
		return fmt.Errorf("gh CLI not found")
	}

	args := []string{"attestation", "verify", subject, "--repo", repo}
	if bundlePath != "" {
		args = append(args, "--bundle", bundlePath)
	}

	cmd := exec.Command(ghPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, `unknown command "attestation"`) {
			return fmt.Errorf(
				"gh at %s lacks 'attestation' (need gh >= 2.49.0); "+
					"install a current gh with: gale install gh",
				ghPath,
			)
		}
		return fmt.Errorf("attestation verification failed: %s", msg)
	}
	return nil
}
