package attestation_test

// Release parity validation for native Sigstore verification: proves
// the in-process verifier (SigstoreVerifier) accepts a real, signed
// gale-recipes artifact and rejects a tampered digest, using the full
// production configuration — real TUF trusted root, full SCT option
// set, real GHCR referrers.
//
// The test needs the network, so it is gated behind GALE_TEST_NETWORK=1
// and skips in normal runs (`just check`, CI unit jobs). CI runs it in
// .github/workflows/attestation-parity.yml, which compares its verdict
// against `gh attestation verify` on the same artifact.
//
// By default it verifies a pinned known-good artifact (see the pinned*
// constants). The parity workflow overrides the target per matrix
// entry via GALE_PARITY_PACKAGE / GALE_PARITY_VERSION, and reads the
// resolved coordinates from the file named by GALE_PARITY_OUT so the
// gh CLI verifies the exact same artifact.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kelp/gale/internal/attestation"
	"github.com/kelp/gale/internal/download"
	"github.com/kelp/gale/internal/ghcr"
	"github.com/kelp/gale/internal/httpclient"
	"github.com/kelp/gale/internal/recipe"
)

const (
	// networkEnv gates the test: unset (or != "1") means skip.
	networkEnv = "GALE_TEST_NETWORK"
	// parityPackageEnv overrides the target package; empty means the
	// pinned default below.
	parityPackageEnv = "GALE_PARITY_PACKAGE"
	// parityVersionEnv pins a ledger version for the override
	// package; empty means the flat head of its .binaries.toml.
	parityVersionEnv = "GALE_PARITY_VERSION"
	// parityOutEnv names a file to receive the resolved artifact
	// coordinates as KEY=value lines (for the CI parity workflow).
	parityOutEnv = "GALE_PARITY_OUT"

	// parityGHCRBase is the GHCR namespace holding recipe images
	// (ghcr.io/kelp/gale-recipes/<pkg>). It equals the attesting
	// repo identity (attestation.DefaultRepo) by construction, but
	// names a registry path, not a repo.
	parityGHCRBase = "kelp/gale-recipes"
	// parityPlatform is the artifact platform to verify. Any
	// platform works — the test verifies digests, it never runs the
	// binary — so linux-amd64 is fixed for determinism.
	parityPlatform = "linux-amd64"

	// Pinned known-good artifact: jq 1.8.1-5 linux-amd64 from the
	// gale-recipes [[history]] ledger, verified good on 2026-07-01.
	// The ledger is append-only and entries are immutable, so these
	// coordinates stay resolvable; pinnedDigest guards against a
	// ledger-parsing regression silently switching artifacts.
	pinnedPackage = "jq"
	pinnedVersion = "1.8.1-5"
	pinnedDigest  = "sha256:9f35d79850663818a8be0eca27bb9680af73b3c6a79d08f17c49d5f336bc4ac0"
)

// parityCoords is one artifact to verify: its package, ledger
// version, archive blob sha256, and OCI manifest digest.
type parityCoords struct {
	pkg     string
	version string
	sha256  string
	digest  string
}

func TestNetworkParity(t *testing.T) {
	if os.Getenv(networkEnv) != "1" {
		t.Skipf("network parity test skipped: set %s=1 to verify real gale-recipes artifacts over the network", networkEnv)
	}
	forceProductionEnv(t)

	c := resolveParityCoords(t)
	writeParityCoords(t, c)
	t.Logf("target %s@%s: manifest digest %s, archive sha256 %s",
		c.pkg, c.version, c.digest, c.sha256)

	v := attestation.NewVerifier()
	bundles, legacy := fetchParityBundle(t, c)
	if legacy {
		verifyLegacyArchive(t, v, c)
		return
	}

	if err := v.VerifyOCI(c.digest, attestation.DefaultRepo, bundles); err != nil {
		t.Fatalf("VerifyOCI(%s@%s) = %v, want success", c.pkg, c.version, err)
	}
	t.Logf("native OCI verification succeeded for %s@%s", c.pkg, c.version)

	tampered := flipHexDigit(c.digest)
	if err := v.VerifyOCI(tampered, attestation.DefaultRepo, bundles); err == nil {
		t.Fatalf("VerifyOCI accepted tampered digest %s, want failure", tampered)
	}
	t.Logf("native OCI verification rejected tampered digest %s", tampered)
}

// forceProductionEnv clears verifier and registry overrides so the
// test exercises the exact production configuration: TUF-fetched
// trusted root, SCT + tlog + observer timestamps, real GHCR.
func forceProductionEnv(t *testing.T) {
	t.Helper()
	t.Setenv(attestation.TrustedRootEnv, "")
	t.Setenv("GALE_SIGSTORE_TEST_NO_SCT", "")
	t.Setenv("GALE_GHCR_URL", "")
}

// resolveParityCoords picks the target artifact (env override or the
// pinned default) and resolves its coordinates from the package's
// .binaries.toml in gale-recipes.
func resolveParityCoords(t *testing.T) parityCoords {
	t.Helper()
	pkg := os.Getenv(parityPackageEnv)
	version := os.Getenv(parityVersionEnv)
	wantDigest := ""
	if pkg == "" {
		pkg, version, wantDigest = pinnedPackage, pinnedVersion, pinnedDigest
	}

	url := fmt.Sprintf(
		"https://raw.githubusercontent.com/%s/main/recipes/%s/%s.binaries.toml",
		parityGHCRBase, pkg[:1], pkg,
	)
	idx, err := recipe.ParseBinaryIndex(string(httpGetBody(t, url)))
	if err != nil {
		t.Fatalf("parse %s: %v", url, err)
	}

	c := lookupPlatform(t, idx, pkg, version)
	if wantDigest != "" && c.digest != wantDigest {
		t.Fatalf("pinned artifact drifted: ledger has digest %s for %s@%s %s, test pins %s",
			c.digest, c.pkg, c.version, parityPlatform, wantDigest)
	}
	return c
}

// lookupPlatform resolves parityPlatform coordinates for version from
// the parsed index: the flat head when version is empty (or matches
// the head), else the matching [[history]] ledger entry.
func lookupPlatform(t *testing.T, idx *recipe.BinaryIndex, pkg, version string) parityCoords {
	t.Helper()
	if version == "" || version == idx.Version {
		return coordsFrom(t, pkg, idx.Version, idx.Platforms, idx.Digests)
	}
	for _, e := range idx.History {
		if e.Version == version {
			return coordsFrom(t, pkg, e.Version, e.Platforms, e.Digests)
		}
	}
	t.Fatalf("version %s not found in %s binaries ledger", version, pkg)
	return parityCoords{}
}

// coordsFrom builds parityCoords from one version's platform maps,
// failing when the platform or its manifest digest is absent.
func coordsFrom(t *testing.T, pkg, version string, platforms, digests map[string]string) parityCoords {
	t.Helper()
	sha, ok := platforms[parityPlatform]
	if !ok || sha == "" {
		t.Fatalf("%s@%s has no %s binary", pkg, version, parityPlatform)
	}
	dig, ok := digests[parityPlatform]
	if !ok || dig == "" {
		t.Fatalf("%s@%s has no %s manifest digest", pkg, version, parityPlatform)
	}
	return parityCoords{pkg: pkg, version: version, sha256: sha, digest: dig}
}

// writeParityCoords writes the resolved coordinates as KEY=value
// lines when GALE_PARITY_OUT names a file, so the CI parity job can
// feed the exact same artifact to `gh attestation verify`. Written
// before verification so the gh side runs even when the native side
// fails — that divergence is the whole point of the workflow.
func writeParityCoords(t *testing.T, c parityCoords) {
	t.Helper()
	path := os.Getenv(parityOutEnv)
	if path == "" {
		return
	}
	data := fmt.Sprintf(
		"PARITY_PACKAGE=%s\nPARITY_VERSION=%s\nPARITY_ARCHIVE_SHA256=%s\nPARITY_MANIFEST_DIGEST=%s\n",
		c.pkg, c.version, c.sha256, c.digest,
	)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write parity coords to %s: %v", path, err)
	}
}

// fetchParityBundle pulls the Sigstore bundle(s) attached as OCI
// referrers. legacy=true means the artifact has no referrer (it was
// published before bundles were pushed as referrers), so verification
// must take the GitHub Attestations API file path instead — the same
// fallback attestation.VerifyPrebuilt takes in production.
func fetchParityBundle(t *testing.T, c parityCoords) (bundles []byte, legacy bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bundles, err := ghcr.FetchReferrerBundle(ctx, parityBlobURL(c), c.digest, parityToken(t, c.pkg))
	if errors.Is(err, ghcr.ErrNoReferrer) {
		return nil, true
	}
	if err != nil {
		t.Fatalf("fetch referrer bundle for %s@%s: %v", c.pkg, c.version, err)
	}
	return bundles, false
}

// verifyLegacyArchive downloads the package archive blob and runs
// VerifyFile — the Attestations API fallback for artifacts published
// before OCI referrers. No tampered-digest case here: VerifyFile
// derives the subject digest from the file itself.
func verifyLegacyArchive(t *testing.T, v *attestation.SigstoreVerifier, c parityCoords) {
	t.Helper()
	t.Logf("%s@%s has no OCI referrer; exercising the Attestations API file fallback", c.pkg, c.version)

	path := downloadParityArchive(t, c)
	if err := v.VerifyFile(path, attestation.DefaultRepo); err != nil {
		t.Fatalf("VerifyFile(%s@%s) = %v, want success", c.pkg, c.version, err)
	}
	t.Logf("native file-fallback verification succeeded for %s@%s", c.pkg, c.version)
}

// downloadParityArchive fetches the artifact's tar.zst blob from GHCR
// into the test temp dir and checks it against the ledger sha256.
func downloadParityArchive(t *testing.T, c parityCoords) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), c.pkg+".tar.zst")
	if err := download.FetchWithAuthNamed(parityBlobURL(c), path, parityToken(t, c.pkg), ""); err != nil {
		t.Fatalf("download archive for %s@%s: %v", c.pkg, c.version, err)
	}
	got, err := download.HashFile(path)
	if err != nil {
		t.Fatalf("hash downloaded archive: %v", err)
	}
	if got != c.sha256 {
		t.Fatalf("downloaded archive sha256 = %s, want %s", got, c.sha256)
	}
	return path
}

// parityToken exchanges an anonymous GHCR pull token for pkg.
func parityToken(t *testing.T, pkg string) string {
	t.Helper()
	token, err := ghcr.Token(parityGHCRBase + "/" + pkg)
	if err != nil {
		t.Fatalf("fetch ghcr token for %s: %v", pkg, err)
	}
	return token
}

// parityBlobURL builds the GHCR blob URL for the artifact archive.
func parityBlobURL(c parityCoords) string {
	return fmt.Sprintf("%s/v2/%s/%s/blobs/sha256:%s",
		ghcr.BaseURL(), parityGHCRBase, c.pkg, c.sha256)
}

// httpGetBody GETs url and returns the body, failing the test on any
// error or non-200 status.
func httpGetBody(t *testing.T, url string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request for %s: %v", url, err)
	}
	resp, err := httpclient.Default().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return body
}

// flipHexDigit returns digest with its final hex digit changed — a
// syntactically valid digest that names a different artifact.
func flipHexDigit(digest string) string {
	b := []byte(digest)
	last := len(b) - 1
	if b[last] == '0' {
		b[last] = '1'
	} else {
		b[last] = '0'
	}
	return string(b)
}
