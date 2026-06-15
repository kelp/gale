package ghcr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// manifestJSON builds a minimal single-layer OCI image manifest
// referencing the given layer digest, and returns the JSON bytes
// plus their "sha256:<hex>" digest (the manifest digest GHCR
// would serve it under).
func manifestJSON(layerDigest string) ([]byte, string) {
	m := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    "sha256:" + strings.Repeat("c", 64),
			"size":      2,
		},
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.oci.image.layer.v1.tar+zstd",
				"digest":    layerDigest,
				"size":      1024,
			},
		},
	}
	body, _ := json.Marshal(m)
	sum := sha256.Sum256(body)
	return body, fmt.Sprintf("sha256:%x", sum[:])
}

func TestFetchManifestLayerReturnsLayerDigest(t *testing.T) {
	layer := "sha256:" + strings.Repeat("a", 64)
	body, digest := manifestJSON(layer)

	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotAccept = r.Header.Get("Accept")
			w.Write(body)
		},
	))
	defer srv.Close()

	got, err := FetchManifestLayer(context.Background(), srv.URL, digest, "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != layer {
		t.Errorf("layer digest = %q, want %q", got, layer)
	}
	if !strings.Contains(gotAccept, "oci.image.manifest") {
		t.Errorf("Accept = %q, want it to advertise OCI manifest", gotAccept)
	}
}

func TestFetchManifestLayerRejectsBodyDigestMismatch(t *testing.T) {
	layer := "sha256:" + strings.Repeat("a", 64)
	body, _ := manifestJSON(layer)
	wrongDigest := "sha256:" + strings.Repeat("b", 64)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.Write(body) },
	))
	defer srv.Close()

	if _, err := FetchManifestLayer(context.Background(), srv.URL, wrongDigest, "tok"); err == nil {
		t.Fatal("expected error on manifest body digest mismatch, got nil")
	}
}

func TestFetchManifestLayerRejectsMultiLayer(t *testing.T) {
	m := map[string]any{
		"schemaVersion": 2,
		"layers": []map[string]any{
			{"digest": "sha256:" + strings.Repeat("a", 64)},
			{"digest": "sha256:" + strings.Repeat("b", 64)},
		},
	}
	body, _ := json.Marshal(m)
	sum := sha256.Sum256(body)
	digest := fmt.Sprintf("sha256:%x", sum[:])

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.Write(body) },
	))
	defer srv.Close()

	if _, err := FetchManifestLayer(context.Background(), srv.URL, digest, "tok"); err == nil {
		t.Fatal("expected error on multi-layer manifest, got nil")
	}
}

func TestFetchManifestLayerErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusNotFound)
		},
	))
	defer srv.Close()

	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := FetchManifestLayer(context.Background(), srv.URL, digest, "tok"); err == nil {
		t.Fatal("expected error on HTTP 404, got nil")
	}
}

func TestManifestURLForBlob(t *testing.T) {
	blob := "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc123"
	digest := "sha256:" + strings.Repeat("d", 64)
	got, err := ManifestURLForBlob(blob, digest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://ghcr.io/v2/kelp/gale-recipes/jq/manifests/" + digest
	if got != want {
		t.Errorf("ManifestURLForBlob = %q, want %q", got, want)
	}
}

func TestManifestURLForBlobRejectsNonBlobURL(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	if _, err := ManifestURLForBlob("https://example.com/foo", digest); err == nil {
		t.Fatal("expected error for URL without /blobs/ segment, got nil")
	}
}
