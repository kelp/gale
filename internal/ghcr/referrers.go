package ghcr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/kelp/gale/internal/httpclient"
	"github.com/kelp/gale/internal/timing"
)

// ErrNoReferrer signals the manifest has no attestation referrer.
var ErrNoReferrer = errors.New("ghcr: no attestation referrer found")

// statusError is a non-200 HTTP response from the registry. It carries
// the status code so callers can distinguish a 404 (no referrer) from
// other failures.
type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// indexAccept is the Accept header sent when pulling an OCI referrers
// index (an image index of the attestations attached to a subject).
const indexAccept = "application/vnd.oci.image.index.v1+json"

// bundleArtifactPrefix marks a Sigstore bundle referrer. gale prefers
// referrers whose artifactType has this prefix and falls back to all
// referrers only when none match.
const bundleArtifactPrefix = "application/vnd.dev.sigstore.bundle"

// ociIndex is the subset of an OCI image index gale reads: the list
// of referrer descriptors with their digest and artifactType.
type ociIndex struct {
	Manifests []struct {
		Digest       string `json:"digest"`
		ArtifactType string `json:"artifactType"`
		MediaType    string `json:"mediaType"`
	} `json:"manifests"`
}

// BaseURL returns the GHCR base, overridable via GALE_GHCR_URL
// (default "https://ghcr.io"). The override exists for integration
// hermeticity, letting tests point gale at a fake registry.
func BaseURL() string {
	if u := os.Getenv("GALE_GHCR_URL"); u != "" {
		return u
	}
	return "https://ghcr.io"
}

// ReferrersURLForBlob rewrites a ".../v2/<repoPath>/blobs/<ref>" URL
// to ".../v2/<repoPath>/referrers/<manifestDigest>". It mirrors
// ManifestURLForBlob so both point at the same registry host and
// repository path (including a test server that overrides the host).
// Returns an error when the URL has no "/blobs/" segment.
func ReferrersURLForBlob(blobURL, manifestDigest string) (string, error) {
	base, _, ok := strings.Cut(blobURL, "/blobs/")
	if !ok {
		return "", fmt.Errorf("not a GHCR blob URL: %q", blobURL)
	}
	return base + "/referrers/" + manifestDigest, nil
}

// ReferrersTagURLForBlob builds the OCI referrers tag-schema fallback
// URL: ".../manifests/sha256-<hex>" (the subject digest with ':'
// replaced by '-'). The OCI spec defines this tag as how clients list
// referrers on registries that do not serve the /referrers/ API.
// GHCR's /referrers/ endpoint 303-redirects to a backing store that
// 404s (it truncates the digest at the colon), so this tag schema is
// the path that actually resolves referrers there.
func ReferrersTagURLForBlob(blobURL, manifestDigest string) (string, error) {
	base, _, ok := strings.Cut(blobURL, "/blobs/")
	if !ok {
		return "", fmt.Errorf("not a GHCR blob URL: %q", blobURL)
	}
	return base + "/manifests/" + strings.Replace(manifestDigest, ":", "-", 1), nil
}

// FetchReferrerBundle fetches the Sigstore attestation bundle(s)
// attached as OCI referrers to the image manifest, as JSONL ready for
// gh --bundle. blobURL is the package's ".../blobs/sha256:<hex>" URL;
// token may be "" (anonymous). Returns ErrNoReferrer when the
// referrers index has no usable bundle referrer.
func FetchReferrerBundle(ctx context.Context, blobURL, manifestDigest, token string) ([]byte, error) {
	defer timing.Phase("ghcr-referrers")()

	idx, err := fetchReferrersIndex(ctx, blobURL, manifestDigest, token)
	if err != nil {
		return nil, err
	}

	selected, strict := selectReferrers(idx)
	if len(selected) == 0 {
		return nil, ErrNoReferrer
	}

	bundles, err := collectBundles(ctx, blobURL, selected, strict, token)
	if err != nil {
		return nil, err
	}
	if len(bundles) == 0 {
		return nil, ErrNoReferrer
	}

	var out bytes.Buffer
	for _, b := range bundles {
		out.Write(b)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// collectBundles fetches each selected referrer's bundle. In strict
// mode (the referrers carried a Sigstore artifactType) any fetch
// failure is fatal — fail closed, since a real bundle that won't
// resolve is a genuine problem. In best-effort mode (the all-
// referrers fallback for an unrecognized index shape) a failing
// referrer is skipped so one malformed sibling can't mask a valid
// bundle.
func collectBundles(ctx context.Context, blobURL string, digests []string, strict bool, token string) ([][]byte, error) {
	var out [][]byte
	for _, d := range digests {
		bundle, err := fetchOneBundle(ctx, blobURL, d, token)
		if err != nil {
			if strict {
				return nil, err
			}
			continue
		}
		out = append(out, bytes.TrimRight(bundle, "\n"))
	}
	return out, nil
}

// fetchReferrersIndex resolves the referrers index, preferring the
// /referrers/ API and falling back to the OCI tag schema. GHCR's API
// 303-redirects to a store that 404s (whether or not a referrer
// exists), so the tag schema is the path that resolves real
// referrers; a 404 from both means no referrer.
func fetchReferrersIndex(ctx context.Context, blobURL, manifestDigest, token string) (*ociIndex, error) {
	apiURL, err := ReferrersURLForBlob(blobURL, manifestDigest)
	if err != nil {
		return nil, err
	}
	body, err := getOCI(ctx, apiURL, indexAccept, token)
	if err != nil {
		if !isNotFound(err) {
			return nil, fmt.Errorf("fetch referrers index: %w", err)
		}
		// API 404 (GHCR's broken redirect): try the tag schema.
		body, err = fetchReferrersTag(ctx, blobURL, manifestDigest, token)
		if err != nil {
			return nil, err
		}
	}
	var idx ociIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("parse referrers index: %w", err)
	}
	return &idx, nil
}

// fetchReferrersTag GETs the OCI tag-schema referrers index. A 404
// here means the subject truly has no referrer; verification then
// falls back to the GitHub Attestations API file path rather than
// failing, which would source-build every package not yet republished
// with a referrer (#131).
func fetchReferrersTag(ctx context.Context, blobURL, manifestDigest, token string) ([]byte, error) {
	tagURL, err := ReferrersTagURLForBlob(blobURL, manifestDigest)
	if err != nil {
		return nil, err
	}
	body, err := getOCI(ctx, tagURL, indexAccept, token)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNoReferrer
		}
		return nil, fmt.Errorf("fetch referrers tag: %w", err)
	}
	return body, nil
}

// isNotFound reports whether err is a registry 404.
func isNotFound(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == http.StatusNotFound
}

// selectReferrers returns the referrer manifest digests to pull and
// whether the selection is strict. strict is true when at least one
// referrer carried a Sigstore bundle artifactType (those are the real
// attestations, fetched fail-closed). When none match, it returns all
// referrers with strict=false — a best-effort fallback robust to the
// unconfirmed actions/attest index shape.
func selectReferrers(idx *ociIndex) (digests []string, strict bool) {
	var bundles, all []string
	for _, m := range idx.Manifests {
		if m.Digest == "" {
			continue
		}
		all = append(all, m.Digest)
		if strings.HasPrefix(m.ArtifactType, bundleArtifactPrefix) {
			bundles = append(bundles, m.Digest)
		}
	}
	if len(bundles) > 0 {
		return bundles, true
	}
	return all, false
}

// fetchOneBundle pulls the referrer manifest at refDigest, takes its
// first layer's digest, and returns that blob (the bundle JSON).
func fetchOneBundle(ctx context.Context, blobURL, refDigest, token string) ([]byte, error) {
	manURL, err := ManifestURLForBlob(blobURL, refDigest)
	if err != nil {
		return nil, err
	}
	manBody, err := getOCI(ctx, manURL, manifestAccept, token)
	if err != nil {
		return nil, fmt.Errorf("fetch referrer manifest: %w", err)
	}
	var m ociManifest
	if err := json.Unmarshal(manBody, &m); err != nil {
		return nil, fmt.Errorf("parse referrer manifest: %w", err)
	}
	if len(m.Layers) == 0 {
		return nil, fmt.Errorf("referrer manifest %s has no layers", refDigest)
	}

	base, _, _ := strings.Cut(blobURL, "/blobs/")
	bundleURL := base + "/blobs/" + m.Layers[0].Digest
	bundle, err := getOCI(ctx, bundleURL, "", token)
	if err != nil {
		return nil, fmt.Errorf("fetch bundle blob: %w", err)
	}
	return bundle, nil
}

// getOCI performs an authenticated (or anonymous) GET and returns the
// body, erroring on non-200.
func getOCI(ctx context.Context, url, accept, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpclient.Default().Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{code: resp.StatusCode}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
