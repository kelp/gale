package ghcr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kelp/gale/internal/httpclient"
	"github.com/kelp/gale/internal/timing"
)

// manifestAccept is the Accept header sent when pulling an OCI
// manifest. Both the OCI and the Docker v2 media types are
// advertised for registry compatibility.
const manifestAccept = "application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

// ociManifest is the subset of an OCI image manifest gale reads:
// the layer descriptors. gale artifacts are a single tar+zstd
// layer.
type ociManifest struct {
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
}

// ManifestURLForBlob derives the OCI manifest URL for a given blob
// URL by replacing the `/blobs/<ref>` tail with
// `/manifests/<manifestDigest>`. The blob and its manifest live on
// the same registry host and repository path, so deriving the
// manifest URL keeps both pointing at the same place (including a
// test server that overrides the host). Returns an error when the
// URL has no `/blobs/` segment.
func ManifestURLForBlob(blobURL, manifestDigest string) (string, error) {
	base, _, ok := strings.Cut(blobURL, "/blobs/")
	if !ok {
		return "", fmt.Errorf("not a GHCR blob URL: %q", blobURL)
	}
	return base + "/manifests/" + manifestDigest, nil
}

// FetchManifestLayer pulls the OCI manifest at manifestURL, verifies
// the manifest bytes hash to expectedDigest, and returns the single
// layer descriptor's digest. It is the resolution half of
// digest-based fetch (gh#121): the caller cross-checks the returned
// layer digest against the ledger's recorded sha256 before pulling
// the blob.
//
// Errors when the manifest does not hash to expectedDigest or does
// not have exactly one layer — gale artifacts are single-layer, and
// a mismatch means the registry served something other than the
// attested manifest.
func FetchManifestLayer(ctx context.Context, manifestURL, expectedDigest, token string) (string, error) {
	defer timing.Phase("ghcr-manifest")()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", fmt.Errorf("build manifest request: %w", err)
	}
	req.Header.Set("Accept", manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpclient.Default().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch manifest: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read manifest: %w", err)
	}

	gotDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	if !strings.EqualFold(gotDigest, expectedDigest) {
		return "", fmt.Errorf(
			"manifest digest mismatch: expected %s, got %s",
			expectedDigest, gotDigest,
		)
	}

	var m ociManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return "", fmt.Errorf("parse manifest: %w", err)
	}
	if len(m.Layers) != 1 {
		return "", fmt.Errorf(
			"manifest has %d layers, want exactly 1", len(m.Layers),
		)
	}
	return m.Layers[0].Digest, nil
}
