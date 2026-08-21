package attestation

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
