package installer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// manifestBytes builds a single-layer OCI manifest referencing
// layerDigest and returns the JSON plus its "sha256:<hex>" digest.
func manifestBytes(layerDigest string) ([]byte, string) {
	m := map[string]any{
		"schemaVersion": 2,
		"layers": []map[string]any{
			{"digest": layerDigest},
		},
	}
	body, _ := json.Marshal(m)
	return body, fmt.Sprintf("sha256:%x", sha256.Sum256(body))
}

func TestVerifyManifestDigestEmptyIsNoop(t *testing.T) {
	// No digest declared (legacy recipe) → no network, no error.
	bin := &recipe.Binary{URL: "https://ghcr.io/v2/x/y/blobs/sha256:abc", SHA256: "abc"}
	if err := verifyManifestDigest(bin, ""); err != nil {
		t.Errorf("verifyManifestDigest = %v, want nil", err)
	}
}

func TestVerifyManifestDigestSucceeds(t *testing.T) {
	layerSHA := strings.Repeat("a", 64)
	body, manifestDigest := manifestBytes("sha256:" + layerSHA)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.Path, "/manifests/") {
				http.Error(w, "not a manifest req", http.StatusBadRequest)
				return
			}
			w.Write(body)
		},
	))
	defer srv.Close()

	bin := &recipe.Binary{
		URL:            srv.URL + "/v2/base/pkg/blobs/sha256:" + layerSHA,
		SHA256:         layerSHA,
		ManifestDigest: manifestDigest,
	}
	if err := verifyManifestDigest(bin, ""); err != nil {
		t.Errorf("verifyManifestDigest = %v, want nil", err)
	}
}

func TestVerifyManifestDigestRejectsLayerMismatch(t *testing.T) {
	// Manifest references a layer other than the ledger's sha256.
	otherLayer := strings.Repeat("b", 64)
	body, manifestDigest := manifestBytes("sha256:" + otherLayer)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.Write(body) },
	))
	defer srv.Close()

	ledgerSHA := strings.Repeat("a", 64)
	bin := &recipe.Binary{
		URL:            srv.URL + "/v2/base/pkg/blobs/sha256:" + ledgerSHA,
		SHA256:         ledgerSHA,
		ManifestDigest: manifestDigest,
	}
	if err := verifyManifestDigest(bin, ""); err == nil {
		t.Fatal("expected error on layer/ledger mismatch, got nil")
	}
}

// A manifest digest on a non-blob URL is inert metadata, so
// verification is skipped (not an error): such a binary is either
// sigstore (rejected for non-GHCR upstream) or sha256-only.
func TestVerifyManifestDigestSkipsNonBlobURL(t *testing.T) {
	bin := &recipe.Binary{
		URL:            "https://example.com/no-blobs-here",
		SHA256:         "abc",
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
	}
	if err := verifyManifestDigest(bin, ""); err != nil {
		t.Errorf("verifyManifestDigest = %v, want nil (skip)", err)
	}
}

// Full install path: a binary carrying a valid manifest digest is
// verified (manifest → layer cross-check) and installed.
func TestInstallBinaryWithManifestDigest(t *testing.T) {
	binContent := "#!/bin/sh\necho from-binary"
	tarzst := createTestTarZstd(t, "bin/testpkg", binContent)
	hash := hashFile(t, tarzst)
	blobData, err := os.ReadFile(tarzst)
	if err != nil {
		t.Fatalf("read tar.zst: %v", err)
	}
	manifest, manifestDigest := manifestBytes("sha256:" + hash)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/manifests/") {
				w.Write(manifest)
				return
			}
			w.Write(blobData)
		},
	))
	defer srv.Close()

	restore := download.SetHTTPClient(srv.Client())
	defer restore()

	storeRoot := t.TempDir()
	inst := &Installer{Store: store.NewStore(storeRoot)}

	r := &recipe.Recipe{
		Package: recipe.Package{Name: "testpkg", Version: "1.0"},
		Source:  recipe.Source{URL: "http://unused", SHA256: "unused"},
		Binary: map[string]recipe.Binary{
			fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH): {
				URL:            srv.URL + "/v2/base/testpkg/blobs/sha256:" + hash,
				SHA256:         hash,
				ManifestDigest: manifestDigest,
				Trust:          recipe.TrustSHA256Only,
			},
		},
	}

	result, err := inst.Install(r)
	if err != nil {
		t.Fatalf("Install error: %v", err)
	}
	if result.Method != "binary" {
		t.Errorf("Method = %q, want binary", result.Method)
	}
	if result.ManifestDigest != manifestDigest {
		t.Errorf("ManifestDigest = %q, want %q", result.ManifestDigest, manifestDigest)
	}
	storeBin := filepath.Join(storeRoot, "testpkg", "1.0-1", "bin", "testpkg")
	if _, err := os.Stat(storeBin); err != nil {
		t.Errorf("binary not in store: %v", err)
	}
}
