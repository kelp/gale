package attestation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kelp/gale/internal/httpclient"
)

// DefaultRepo is the GitHub repository where recipe
// binaries are built and attested.
const DefaultRepo = "kelp/gale-recipes"

// attestationsEndpoint is the GitHub Attestations API URL
// format string. It is package-level so tests can point it
// at a local HTTP server.
var attestationsEndpoint = "https://api.github.com/repos/%s/attestations/%s"

// Verifier checks Sigstore attestations. The production
// implementation is *SigstoreVerifier (sigstore.go), which
// verifies bundles in-process — no external tools.
type Verifier interface {
	// VerifyFile verifies a local archive file by fetching
	// its Sigstore bundle from the public GitHub
	// Attestations API and verifying it against the file's
	// SHA256 digest.
	VerifyFile(filePath, repo string) error
	// VerifyOCI verifies an already-fetched Sigstore bundle
	// (JSONL) against a digest ("sha256:<hex>" or bare hex).
	// Runs offline and needs no GitHub token.
	VerifyOCI(manifestDigest, repo string, bundles []byte) error
}

// FetchBundle downloads the Sigstore attestation bundle(s)
// for the given artifact digest from the public GitHub
// Attestations API. The digest must be a hex-encoded SHA256
// string (no "sha256:" prefix). It returns the bundle(s) as
// newline-delimited JSON (JSONL).
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
// file-verification path (VerifyFile); the OCI path verifies
// a manifest digest and never reaches it.
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
