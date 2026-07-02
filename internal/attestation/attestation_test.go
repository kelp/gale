package attestation

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/ghcr"
)

// TestVerifyFileRejectsDirectory pins the file-subject guard:
// VerifyFile must reject a directory before any bundle fetch or
// signature work happens.
func TestVerifyFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	v := NewVerifier()
	err := v.VerifyFile(dir, "owner/repo")
	if err == nil {
		t.Fatal("expected error for directory subject, got nil")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error %q should mention 'directory'", err)
	}
}

// writeTempFile creates a temporary file with data and returns its
// path. The file is cleaned up when the test finishes.
func writeTempFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "attest-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

// fakeVerifier records which Verifier methods VerifyPrebuilt
// invokes so routing tests can assert the decision path without
// real signature verification.
type fakeVerifier struct {
	ociCalled  bool
	fileCalled bool
	ociErr     error
	fileErr    error
}

func (f *fakeVerifier) VerifyFile(filePath, repo string) error {
	f.fileCalled = true
	return f.fileErr
}

func (f *fakeVerifier) VerifyOCI(manifestDigest, repo string, bundles []byte) error {
	f.ociCalled = true
	return f.ociErr
}

// TestVerifyPrebuiltUsesReferrerWhenBundleFound asserts that a
// successful FetchBundle routes to VerifyOCI and never touches
// the file fallback.
func TestVerifyPrebuiltUsesReferrerWhenBundleFound(t *testing.T) {
	fv := &fakeVerifier{}
	archiveCalled := false
	err := VerifyPrebuilt(fv, PrebuiltParams{
		Repo:           "owner/repo",
		ManifestDigest: "sha256:abc",
		FetchBundle:    func() ([]byte, error) { return []byte(`{"x":1}`), nil },
		Archive: func() (string, func(), error) {
			archiveCalled = true
			return "", nil, nil
		},
	})
	if err != nil {
		t.Fatalf("VerifyPrebuilt: %v", err)
	}
	if !fv.ociCalled {
		t.Error("expected VerifyOCI to be called")
	}
	if fv.fileCalled || archiveCalled {
		t.Error("file fallback must not run when referrer bundle found")
	}
}

// TestVerifyPrebuiltFallsBackOnNoReferrer asserts ErrNoReferrer
// from FetchBundle routes to the file path.
func TestVerifyPrebuiltFallsBackOnNoReferrer(t *testing.T) {
	fv := &fakeVerifier{}
	err := VerifyPrebuilt(fv, PrebuiltParams{
		Repo:           "owner/repo",
		ManifestDigest: "sha256:abc",
		FetchBundle:    func() ([]byte, error) { return nil, ghcr.ErrNoReferrer },
		Archive: func() (string, func(), error) {
			return "/tmp/archive", nil, nil
		},
	})
	if err != nil {
		t.Fatalf("VerifyPrebuilt: %v", err)
	}
	if fv.ociCalled {
		t.Error("VerifyOCI must not run after ErrNoReferrer")
	}
	if !fv.fileCalled {
		t.Error("expected VerifyFile fallback")
	}
}

// TestVerifyPrebuiltPropagatesFetchError asserts a non-ErrNoReferrer
// fetch error fails closed: it propagates and never falls back.
func TestVerifyPrebuiltPropagatesFetchError(t *testing.T) {
	fv := &fakeVerifier{}
	want := errors.New("network down")
	err := VerifyPrebuilt(fv, PrebuiltParams{
		Repo:           "owner/repo",
		ManifestDigest: "sha256:abc",
		FetchBundle:    func() ([]byte, error) { return nil, want },
		Archive: func() (string, func(), error) {
			return "/tmp/archive", nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, want) {
		t.Errorf("error %v should wrap %v", err, want)
	}
	if fv.fileCalled {
		t.Error("file fallback must not run after a non-ErrNoReferrer fetch error")
	}
}

// TestVerifyPrebuiltFileWhenNoManifestDigest asserts an empty
// ManifestDigest skips the referrer path entirely.
func TestVerifyPrebuiltFileWhenNoManifestDigest(t *testing.T) {
	fv := &fakeVerifier{}
	fetchCalled := false
	err := VerifyPrebuilt(fv, PrebuiltParams{
		Repo: "owner/repo",
		FetchBundle: func() ([]byte, error) {
			fetchCalled = true
			return []byte(`{"x":1}`), nil
		},
		Archive: func() (string, func(), error) {
			return "/tmp/archive", nil, nil
		},
	})
	if err != nil {
		t.Fatalf("VerifyPrebuilt: %v", err)
	}
	if fetchCalled || fv.ociCalled {
		t.Error("empty ManifestDigest must skip the referrer path")
	}
	if !fv.fileCalled {
		t.Error("expected VerifyFile when ManifestDigest empty")
	}
}

// TestVerifyPrebuiltReferrerErrorFailsClosed asserts that once a
// referrer bundle is found, a VerifyOCI failure propagates without
// any file fallback.
func TestVerifyPrebuiltReferrerErrorFailsClosed(t *testing.T) {
	want := errors.New("cert identity mismatch")
	fv := &fakeVerifier{ociErr: want}
	err := VerifyPrebuilt(fv, PrebuiltParams{
		Repo:           "owner/repo",
		ManifestDigest: "sha256:abc",
		FetchBundle:    func() ([]byte, error) { return []byte(`{"x":1}`), nil },
		Archive: func() (string, func(), error) {
			return "/tmp/archive", nil, nil
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
	if fv.fileCalled {
		t.Error("file fallback must not run after a found referrer fails")
	}
}

// TestVerifyPrebuiltRunsArchiveCleanup asserts the file fallback
// invokes the cleanup returned by Archive.
func TestVerifyPrebuiltRunsArchiveCleanup(t *testing.T) {
	fv := &fakeVerifier{}
	cleaned := false
	err := VerifyPrebuilt(fv, PrebuiltParams{
		Repo: "owner/repo",
		Archive: func() (string, func(), error) {
			return "/tmp/archive", func() { cleaned = true }, nil
		},
	})
	if err != nil {
		t.Fatalf("VerifyPrebuilt: %v", err)
	}
	if !cleaned {
		t.Error("expected Archive cleanup to run")
	}
}

func TestFetchBundle(t *testing.T) {
	want := []byte(`{"foo":"bar"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/attestations/sha256:deadbeef" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"attestations":[{"bundle":%s}]}`, want)
	}))
	defer srv.Close()

	t.Setenv("GALE_GITHUB_TOKEN", "token")
	orig := attestationsEndpoint
	attestationsEndpoint = srv.URL + "/repos/%s/attestations/%s"
	defer func() { attestationsEndpoint = orig }()

	got, err := FetchBundle("deadbeef", "owner/repo")
	if err != nil {
		t.Fatalf("FetchBundle: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("FetchBundle = %q, want %q", got, want)
	}
}

func TestFetchBundleNoAttestations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"attestations":[]}`)
	}))
	defer srv.Close()

	orig := attestationsEndpoint
	attestationsEndpoint = srv.URL + "/repos/%s/attestations/%s"
	defer func() { attestationsEndpoint = orig }()

	_, err := FetchBundle("deadbeef", "owner/repo")
	if err == nil {
		t.Fatal("expected error for empty attestations, got nil")
	}
	if !strings.Contains(err.Error(), "no attestations found") {
		t.Errorf("error %q should mention 'no attestations found'", err.Error())
	}
}

func TestBareVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"1.8.1-4", "1.8.1"},
		{"1.8.1", "1.8.1"},
		{"0.10.0-2", "0.10.0"},
		{"1.0-rc1", "1.0-rc1"},
		{"1.2-0", "1.2-0"},
		{"2.0-1-2", "2.0-1"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			got := BareVersion(c.in)
			if got != c.want {
				t.Errorf("BareVersion(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
