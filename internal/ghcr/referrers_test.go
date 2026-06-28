package ghcr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureRepoPath is the repository path served by every referrer fixture.
const fixtureRepoPath = "kelp/gale-recipes/hello"

// blobJSON returns bytes and their "sha256:<hex>" digest.
func blobJSON(b []byte) (string, []byte) {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", sum[:]), b
}

// referrerFixture wires an httptest server that serves a referrers
// index, the referrer manifests, and the bundle blobs. bundles maps
// an artifactType to the bundle JSON it should ultimately yield.
type referrerFixture struct {
	repoPath       string
	manifestDigest string
	// referrers in index order: artifactType -> bundle bytes.
	referrers []fixtureReferrer
	server    *httptest.Server
	gotAuth   []string
}

type fixtureReferrer struct {
	artifactType string
	bundle       []byte
}

type builtReferrer struct {
	refDigest    string
	refManifest  []byte
	blobDigest   string
	blobBody     []byte
	artifactType string
}

// buildReferrers precomputes each referrer's manifest + blob digests
// and the referrers index that lists them.
func buildReferrers(refs []fixtureReferrer) ([]builtReferrer, []byte) {
	builts := make([]builtReferrer, len(refs))
	for i, r := range refs {
		blobDigest, blobBody := blobJSON(r.bundle)
		man := map[string]any{
			"schemaVersion": 2,
			"mediaType":     "application/vnd.oci.image.manifest.v1+json",
			"artifactType":  r.artifactType,
			"layers": []map[string]any{
				{"digest": blobDigest, "mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json"},
			},
		}
		manBytes, _ := json.Marshal(man)
		refDigest, _ := blobJSON(manBytes)
		builts[i] = builtReferrer{
			refDigest:    refDigest,
			refManifest:  manBytes,
			blobDigest:   blobDigest,
			blobBody:     blobBody,
			artifactType: r.artifactType,
		}
	}

	idxManifests := make([]map[string]any, len(builts))
	for i, b := range builts {
		idxManifests[i] = map[string]any{
			"digest":       b.refDigest,
			"artifactType": b.artifactType,
			"mediaType":    "application/vnd.oci.image.manifest.v1+json",
		}
	}
	index := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     idxManifests,
	}
	indexBytes, _ := json.Marshal(index)
	return builts, indexBytes
}

func newReferrerFixture(t *testing.T, manifestDigest string, refs []fixtureReferrer) *referrerFixture {
	t.Helper()
	repoPath := fixtureRepoPath
	f := &referrerFixture{
		repoPath:       repoPath,
		manifestDigest: manifestDigest,
		referrers:      refs,
	}

	builts, indexBytes := buildReferrers(refs)

	f.server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			f.gotAuth = append(f.gotAuth, r.Header.Get("Authorization"))
			switch {
			case r.URL.Path == "/v2/"+repoPath+"/referrers/"+manifestDigest:
				w.Write(indexBytes)
			case strings.HasPrefix(r.URL.Path, "/v2/"+repoPath+"/manifests/"):
				d := strings.TrimPrefix(r.URL.Path, "/v2/"+repoPath+"/manifests/")
				for _, b := range builts {
					if b.refDigest == d {
						w.Write(b.refManifest)
						return
					}
				}
				http.Error(w, "no such manifest", http.StatusNotFound)
			case strings.HasPrefix(r.URL.Path, "/v2/"+repoPath+"/blobs/"):
				d := strings.TrimPrefix(r.URL.Path, "/v2/"+repoPath+"/blobs/")
				for _, b := range builts {
					if b.blobDigest == d {
						w.Write(b.blobBody)
						return
					}
				}
				http.Error(w, "no such blob", http.StatusNotFound)
			default:
				http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			}
		},
	))
	t.Cleanup(f.server.Close)
	return f
}

// blobURL returns the package blob URL on the fixture server.
func (f *referrerFixture) blobURL() string {
	return f.server.URL + "/v2/" + f.repoPath + "/blobs/sha256:" + strings.Repeat("e", 64)
}

func TestFetchReferrerBundleReturnsBundle(t *testing.T) {
	bundle := []byte(`{"bundle":"one"}`)
	manifestDigest := "sha256:" + strings.Repeat("0", 64)
	f := newReferrerFixture(t, manifestDigest, []fixtureReferrer{
		{artifactType: "application/vnd.dev.sigstore.bundle.v0.3+json", bundle: bundle},
	})

	got, err := FetchReferrerBundle(context.Background(), f.blobURL(), manifestDigest, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(got)) != string(bundle) {
		t.Errorf("bundle = %q, want %q", got, bundle)
	}
}

func TestFetchReferrerBundleAnonymous(t *testing.T) {
	manifestDigest := "sha256:" + strings.Repeat("0", 64)
	f := newReferrerFixture(t, manifestDigest, []fixtureReferrer{
		{artifactType: "application/vnd.dev.sigstore.bundle.v0.3+json", bundle: []byte(`{"b":1}`)},
	})

	if _, err := FetchReferrerBundle(context.Background(), f.blobURL(), manifestDigest, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range f.gotAuth {
		if a != "" {
			t.Errorf("expected no Authorization header, got %q", a)
		}
	}
}

func TestFetchReferrerBundleEmptyIndexReturnsErrNoReferrer(t *testing.T) {
	manifestDigest := "sha256:" + strings.Repeat("0", 64)
	f := newReferrerFixture(t, manifestDigest, nil)

	_, err := FetchReferrerBundle(context.Background(), f.blobURL(), manifestDigest, "")
	if !errors.Is(err, ErrNoReferrer) {
		t.Fatalf("error = %v, want ErrNoReferrer", err)
	}
}

func TestFetchReferrerBundleMultipleBundlesJSONL(t *testing.T) {
	manifestDigest := "sha256:" + strings.Repeat("0", 64)
	f := newReferrerFixture(t, manifestDigest, []fixtureReferrer{
		{artifactType: "application/vnd.dev.sigstore.bundle.v0.3+json", bundle: []byte(`{"b":1}`)},
		{artifactType: "application/vnd.dev.sigstore.bundle.v0.3+json", bundle: []byte(`{"b":2}`)},
	})

	got, err := FetchReferrerBundle(context.Background(), f.blobURL(), manifestDigest, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d JSONL lines, want 2: %q", len(lines), got)
	}
}

func TestFetchReferrerBundleSelectsBundleArtifactType(t *testing.T) {
	manifestDigest := "sha256:" + strings.Repeat("0", 64)
	bundle := []byte(`{"sig":"yes"}`)
	f := newReferrerFixture(t, manifestDigest, []fixtureReferrer{
		{artifactType: "application/vnd.example.other", bundle: []byte(`{"other":1}`)},
		{artifactType: "application/vnd.dev.sigstore.bundle.v0.3+json", bundle: bundle},
	})

	got, err := FetchReferrerBundle(context.Background(), f.blobURL(), manifestDigest, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d JSONL lines, want 1 (only the bundle): %q", len(lines), got)
	}
	if strings.TrimSpace(lines[0]) != string(bundle) {
		t.Errorf("line = %q, want %q", lines[0], bundle)
	}
}

func TestFetchReferrerBundleFallsBackToAllWhenNoBundlePrefix(t *testing.T) {
	manifestDigest := "sha256:" + strings.Repeat("0", 64)
	f := newReferrerFixture(t, manifestDigest, []fixtureReferrer{
		{artifactType: "application/vnd.example.other", bundle: []byte(`{"a":1}`)},
		{artifactType: "application/vnd.example.thing", bundle: []byte(`{"b":2}`)},
	})

	got, err := FetchReferrerBundle(context.Background(), f.blobURL(), manifestDigest, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d JSONL lines, want 2 (all referrers): %q", len(lines), got)
	}
}

func TestFetchReferrerBundle404ReturnsErrNoReferrer(t *testing.T) {
	// GHCR 303-redirects the referrers endpoint to a backing store
	// that 404s when a subject has no referrer. Go's http client
	// follows the redirect, so gale sees a 404. That must map to
	// ErrNoReferrer so verification falls back to the GitHub
	// Attestations API file path rather than failing fatally (which
	// would source-build every not-yet-republished package — #131).
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		},
	))
	t.Cleanup(srv.Close)

	blobURL := srv.URL + "/v2/kelp/gale-recipes/jq/blobs/sha256:" +
		strings.Repeat("e", 64)
	manifestDigest := "sha256:" + strings.Repeat("0", 64)

	_, err := FetchReferrerBundle(
		context.Background(), blobURL, manifestDigest, "",
	)
	if !errors.Is(err, ErrNoReferrer) {
		t.Fatalf("error = %v, want ErrNoReferrer", err)
	}
}

// TestFetchReferrerBundleAllFallbackSkipsBadSibling pins that in the
// all-referrers fallback (no Sigstore artifactType matched), a sibling
// referrer that fails to resolve is skipped rather than aborting the
// whole fetch, so one malformed sibling can't mask a valid bundle.
func TestFetchReferrerBundleAllFallbackSkipsBadSibling(t *testing.T) {
	good := []byte(`{"good":1}`)
	goodBlobDigest, goodBlob := blobJSON(good)
	goodMan, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"artifactType":  "application/vnd.example.thing",
		"layers": []map[string]any{
			{"digest": goodBlobDigest},
		},
	})
	goodRefDigest, _ := blobJSON(goodMan)

	// A sibling manifest with zero layers — fetchOneBundle errors on it.
	badMan, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"artifactType":  "application/vnd.example.other",
		"layers":        []map[string]any{},
	})
	badRefDigest, _ := blobJSON(badMan)

	repoPath := "kelp/gale-recipes/hello"
	manifestDigest := "sha256:" + strings.Repeat("0", 64)
	index, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{"digest": badRefDigest, "artifactType": "application/vnd.example.other"},
			{"digest": goodRefDigest, "artifactType": "application/vnd.example.thing"},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/" + repoPath + "/referrers/" + manifestDigest:
				w.Write(index)
			case "/v2/" + repoPath + "/manifests/" + goodRefDigest:
				w.Write(goodMan)
			case "/v2/" + repoPath + "/manifests/" + badRefDigest:
				w.Write(badMan)
			case "/v2/" + repoPath + "/blobs/" + goodBlobDigest:
				w.Write(goodBlob)
			default:
				http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			}
		},
	))
	t.Cleanup(srv.Close)

	blobURL := srv.URL + "/v2/" + repoPath + "/blobs/sha256:" + strings.Repeat("e", 64)
	got, err := FetchReferrerBundle(context.Background(), blobURL, manifestDigest, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(got)) != string(good) {
		t.Errorf("bundle = %q, want %q (bad sibling should be skipped)", got, good)
	}
}

// TestFetchReferrerBundleTagSchemaFallback pins the real GHCR shape:
// the /referrers/<digest> API 303-redirects to a backing store that
// 404s even when a referrer exists, but the OCI tag-schema fallback
// (/manifests/sha256-<hex>, the subject digest with ':' -> '-') serves
// the index. gale must fall back to the tag schema and return the
// bundle, not give up with ErrNoReferrer.
func TestFetchReferrerBundleTagSchemaFallback(t *testing.T) {
	bundle := []byte(`{"bundle":"viatag"}`)
	manifestDigest := "sha256:" + strings.Repeat("a", 64)
	tagRef := "sha256-" + strings.Repeat("a", 64)

	builts, indexBytes := buildReferrers([]fixtureReferrer{
		{artifactType: "application/vnd.dev.sigstore.bundle.v0.3+json", bundle: bundle},
	})
	repoPath := "kelp/gale-recipes/jq"

	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotAuth = append(gotAuth, r.Header.Get("Authorization"))
			switch {
			case r.URL.Path == "/v2/"+repoPath+"/referrers/"+manifestDigest:
				// GHCR's broken redirect surfaces here as a 404.
				http.Error(w, "Not Found", http.StatusNotFound)
			case r.URL.Path == "/v2/"+repoPath+"/manifests/"+tagRef:
				w.Write(indexBytes)
			case strings.HasPrefix(r.URL.Path, "/v2/"+repoPath+"/manifests/"):
				d := strings.TrimPrefix(r.URL.Path, "/v2/"+repoPath+"/manifests/")
				for _, b := range builts {
					if b.refDigest == d {
						w.Write(b.refManifest)
						return
					}
				}
				http.Error(w, "no such manifest", http.StatusNotFound)
			case strings.HasPrefix(r.URL.Path, "/v2/"+repoPath+"/blobs/"):
				d := strings.TrimPrefix(r.URL.Path, "/v2/"+repoPath+"/blobs/")
				for _, b := range builts {
					if b.blobDigest == d {
						w.Write(b.blobBody)
						return
					}
				}
				http.Error(w, "no such blob", http.StatusNotFound)
			default:
				http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			}
		},
	))
	t.Cleanup(srv.Close)

	blobURL := srv.URL + "/v2/" + repoPath + "/blobs/sha256:" + strings.Repeat("e", 64)
	got, err := FetchReferrerBundle(context.Background(), blobURL, manifestDigest, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(got)) != string(bundle) {
		t.Errorf("bundle = %q, want %q", got, bundle)
	}
	for _, a := range gotAuth {
		if a != "" {
			t.Errorf("expected anonymous requests, got Authorization %q", a)
		}
	}
}

func TestReferrersTagURLForBlob(t *testing.T) {
	blob := "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc123"
	digest := "sha256:" + strings.Repeat("d", 64)
	got, err := ReferrersTagURLForBlob(blob, digest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://ghcr.io/v2/kelp/gale-recipes/jq/manifests/sha256-" + strings.Repeat("d", 64)
	if got != want {
		t.Errorf("ReferrersTagURLForBlob = %q, want %q", got, want)
	}
}

func TestReferrersURLForBlob(t *testing.T) {
	blob := "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc123"
	digest := "sha256:" + strings.Repeat("d", 64)
	got, err := ReferrersURLForBlob(blob, digest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://ghcr.io/v2/kelp/gale-recipes/jq/referrers/" + digest
	if got != want {
		t.Errorf("ReferrersURLForBlob = %q, want %q", got, want)
	}
}

func TestReferrersURLForBlobRejectsNonBlobURL(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	if _, err := ReferrersURLForBlob("https://example.com/foo", digest); err == nil {
		t.Fatal("expected error for URL without /blobs/ segment, got nil")
	}
}

func TestBaseURLDefault(t *testing.T) {
	t.Setenv("GALE_GHCR_URL", "")
	if got := BaseURL(); got != "https://ghcr.io" {
		t.Errorf("BaseURL = %q, want https://ghcr.io", got)
	}
}

func TestBaseURLHonorsEnv(t *testing.T) {
	t.Setenv("GALE_GHCR_URL", "http://127.0.0.1:5000")
	if got := BaseURL(); got != "http://127.0.0.1:5000" {
		t.Errorf("BaseURL = %q, want override", got)
	}
}
