package attestation

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points HOME at a tempdir so findGh's bundled
// lookup misses and falls through to the mocked lookPath,
// and silences warnWriter so test output stays clean.
func isolate(t *testing.T) *bytes.Buffer {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	buf := &bytes.Buffer{}
	orig := warnWriter
	warnWriter = buf
	t.Cleanup(func() { warnWriter = orig })
	return buf
}

// withAttestationServer returns an attestation-endpoint format
// string backed by a test server that returns a static bundle for
// any subject. The server is closed on test cleanup.
func withAttestationServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"attestations":[{"bundle":{"foo":"bar"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/repos/%s/attestations/%s"
}

func TestAvailableWhenGHOnPath(t *testing.T) {
	isolate(t)
	mock := writeMockGH(t, mockOpts{
		attestationHelpExit: 0,
	})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	if !v.Available() {
		t.Errorf("expected Available true, reason=%q",
			v.UnavailableReason())
	}
}

func TestAvailableWhenGHMissing(t *testing.T) {
	warn := isolate(t)
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "", &os.PathError{Op: "lookpath", Path: "gh"}
	}
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	if v.Available() {
		t.Error("expected Available false")
	}
	if !strings.Contains(warn.String(),
		"attestation verification disabled") {
		t.Errorf("expected warning, got %q", warn.String())
	}
	if !strings.Contains(v.UnavailableReason(), "gh CLI not found") {
		t.Errorf("unexpected reason: %q", v.UnavailableReason())
	}
}

// TestAvailableWhenGHTooOld is the regression test for the
// dust@1.2.4 install failure: an older system gh on PATH
// without the "attestation" subcommand must be treated as
// "attestation unavailable" — never silently skipped —
// with a warning that names the path and the fix.
func TestAvailableWhenGHTooOld(t *testing.T) {
	warn := isolate(t)
	mock := writeMockGH(t, mockOpts{
		attestationHelpExit: 1,
	})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	if v.Available() {
		t.Error("expected Available false for old gh")
	}
	got := warn.String()
	if !strings.Contains(got,
		"attestation verification disabled") {
		t.Errorf("missing warning header in %q", got)
	}
	if !strings.Contains(got, "gh >= 2.49.0") {
		t.Errorf("warning missing version hint: %q", got)
	}
	if !strings.Contains(got, "gale install gh") {
		t.Errorf("warning missing fix suggestion: %q", got)
	}
}

// TestAvailableWarnsOnceAcrossCalls protects against
// noisy logs during multi-package syncs: a single gale
// process should warn once per Verifier, not per call.
func TestAvailableWarnsOnceAcrossCalls(t *testing.T) {
	warn := isolate(t)
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "", &os.PathError{Op: "lookpath", Path: "gh"}
	}
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	for i := 0; i < 5; i++ {
		v.Available()
	}
	if n := strings.Count(warn.String(), "warning:"); n != 1 {
		t.Errorf("got %d warnings, want 1: %q", n, warn.String())
	}
}

func TestVerifyFileSuccess(t *testing.T) {
	isolate(t)
	origEndpoint := attestationsEndpoint
	attestationsEndpoint = withAttestationServer(t)
	defer func() { attestationsEndpoint = origEndpoint }()

	mock := writeMockGH(t, mockOpts{verifyExit: 0})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	file := writeTempFile(t, []byte("archive bytes"))
	v := &GHVerifier{}
	if err := v.VerifyFile(file, "owner/repo"); err != nil {
		t.Errorf("expected success, got %v", err)
	}
}

func TestVerifyFileFailure(t *testing.T) {
	isolate(t)
	origEndpoint := attestationsEndpoint
	attestationsEndpoint = withAttestationServer(t)
	defer func() { attestationsEndpoint = origEndpoint }()

	mock := writeMockGH(t, mockOpts{
		verifyExit:   1,
		verifyStderr: "verification failed",
	})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	file := writeTempFile(t, []byte("archive bytes"))
	v := &GHVerifier{}
	err := v.VerifyFile(file, "owner/repo")
	if err == nil {
		t.Error("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "verification failed") {
		t.Errorf("error %q missing gh stderr", err.Error())
	}
}

func TestVerifyFileGHNotAvailable(t *testing.T) {
	isolate(t)
	origEndpoint := attestationsEndpoint
	attestationsEndpoint = withAttestationServer(t)
	defer func() { attestationsEndpoint = origEndpoint }()

	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "", &os.PathError{Op: "lookpath", Path: "gh"}
	}
	defer func() { lookPath = orig }()

	file := writeTempFile(t, []byte("archive bytes"))
	v := &GHVerifier{}
	err := v.VerifyFile(file, "owner/repo")
	if err == nil {
		t.Error("expected error when gh not available")
	}
	if !strings.Contains(err.Error(), "gh CLI not found") {
		t.Errorf("error %q missing gh not found reason", err.Error())
	}
}

// TestVerifyOldGhReportsUpgradeGuidance covers the
// defence-in-depth branch in runVerify for the case where
// probe was bypassed (e.g. direct VerifyOCI call) and gh
// returns "unknown command" mid-verify.
func TestVerifyOldGhReportsUpgradeGuidance(t *testing.T) {
	isolate(t)
	origEndpoint := attestationsEndpoint
	attestationsEndpoint = withAttestationServer(t)
	defer func() { attestationsEndpoint = origEndpoint }()

	mock := writeMockGH(t, mockOpts{
		verifyExit:   1,
		verifyStderr: `unknown command "attestation" for "gh"`,
	})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	file := writeTempFile(t, []byte("archive bytes"))
	v := &GHVerifier{}
	err := v.VerifyFile(file, "owner/repo")
	if err == nil {
		t.Fatal("expected error for old gh, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "gh >= 2.49.0") {
		t.Errorf("error %q missing version hint", msg)
	}
	if !strings.Contains(msg, "gale install gh") {
		t.Errorf("error %q missing fix suggestion", msg)
	}
}

// TestFindGhPrefersBundled verifies findGh picks the
// bundled gale gh over whatever lookPath returns. This
// is the defence against an older system gh on PATH
// taking precedence over gale's own current gh.
func TestFindGhPrefersBundled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bundledDir := filepath.Join(home, ".gale", "current", "bin")
	if err := os.MkdirAll(bundledDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bundled := filepath.Join(bundledDir, "gh")
	if err := os.WriteFile(bundled, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "/usr/bin/gh", nil
	}
	defer func() { lookPath = orig }()

	got, err := findGh()
	if err != nil {
		t.Fatal(err)
	}
	if got != bundled {
		t.Errorf("findGh = %q, want bundled %q", got, bundled)
	}
}

// TestFindGhFallsBackToPath checks the legacy behaviour
// is preserved when no bundled gh is present.
func TestFindGhFallsBackToPath(t *testing.T) {
	isolate(t)
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "/usr/bin/gh", nil
	}
	defer func() { lookPath = orig }()

	got, err := findGh()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/usr/bin/gh" {
		t.Errorf("findGh = %q, want /usr/bin/gh", got)
	}
}

func TestVerifyFileRejectsDirectory(t *testing.T) {
	isolate(t)
	mock := writeMockGH(t, mockOpts{verifyExit: 0})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	dir := t.TempDir()
	v := &GHVerifier{}
	err := v.VerifyFile(dir, "owner/repo")
	if err == nil {
		t.Fatal("expected error for directory subject, got nil")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error %q should mention 'directory'", err)
	}
}

func TestNewVerifierReturnsGHVerifier(t *testing.T) {
	v := NewVerifier()
	if _, ok := v.(*GHVerifier); !ok {
		t.Errorf("NewVerifier() returned %T, want *GHVerifier", v)
	}
}

// mockOpts configures the shell-script gh stand-in.
type mockOpts struct {
	attestationHelpExit int    // exit for `gh attestation --help`
	verifyExit          int    // exit for `gh attestation verify ...`
	verifyStderr        string // stderr for the verify call
	verifyOCIExit       int    // exit for verify with --bundle-from-oci
	verifyOCIStderr     string // stderr for the OCI verify call
}

// writeMockGH creates a shell script that dispatches on
// the second arg: "--help" returns attestationHelpExit;
// "verify" returns verifyExit (writing verifyStderr first).
// When --bundle-from-oci is present it returns verifyOCIExit
// instead. Any other invocation returns the help exit code so
// the probe stays the source of truth.
func writeMockGH(t *testing.T, opts mockOpts) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
		"  --help) exit " + itoa(opts.attestationHelpExit) + " ;;\n" +
		"  verify)\n" +
		"    if echo \"$*\" | grep -q -e '--bundle-from-oci'; then\n"
	if opts.verifyOCIStderr != "" {
		script += "      echo '" + opts.verifyOCIStderr + "' >&2\n"
	}
	script += "      exit " + itoa(opts.verifyOCIExit) + "\n" +
		"    fi\n"
	if opts.verifyStderr != "" {
		script += "    echo '" + opts.verifyStderr + "' >&2\n"
	}
	script += "    exit " + itoa(opts.verifyExit) + " ;;\n" +
		"  *) exit " + itoa(opts.attestationHelpExit) + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
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

func TestVerifyOCISuccess(t *testing.T) {
	isolate(t)
	mock := writeMockGH(t, mockOpts{
		attestationHelpExit: 0,
		verifyOCIExit:       0,
	})
	orig := lookPath
	lookPath = func(name string) (string, error) { return mock, nil }
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	if err := v.VerifyOCI("oci://ghcr.io/owner/repo/img:1.0-linux-amd64", "owner/repo"); err != nil {
		t.Errorf("expected success, got %v", err)
	}
}

func TestVerifyOCIFailure(t *testing.T) {
	isolate(t)
	mock := writeMockGH(t, mockOpts{
		attestationHelpExit: 0,
		verifyOCIExit:       1,
		verifyOCIStderr:     "no OCI attestation found",
	})
	orig := lookPath
	lookPath = func(name string) (string, error) { return mock, nil }
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	err := v.VerifyOCI("oci://ghcr.io/owner/repo/img:1.0-linux-amd64", "owner/repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no OCI attestation found") {
		t.Errorf("error %q missing OCI stderr", err.Error())
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

func TestOCIURI(t *testing.T) {
	cases := []struct {
		repoPath, version, platform, digest, want string
	}{
		{
			repoPath: "owner/repo/pkg",
			version:  "1.8.1-4",
			platform: "darwin-arm64",
			digest:   "sha256:abc123",
			want:     "oci://ghcr.io/owner/repo/pkg@sha256:abc123",
		},
		{
			repoPath: "owner/repo/pkg",
			version:  "1.8.1-4",
			platform: "darwin-arm64",
			want:     "oci://ghcr.io/owner/repo/pkg:1.8.1-darwin-arm64",
		},
		{
			repoPath: "owner/repo/pkg",
			version:  "1.0-rc1",
			platform: "linux-amd64",
			want:     "oci://ghcr.io/owner/repo/pkg:1.0-rc1-linux-amd64",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.want, func(t *testing.T) {
			t.Parallel()
			got := OCIURI(c.repoPath, c.version, c.platform, c.digest)
			if got != c.want {
				t.Errorf("OCIURI = %q, want %q", got, c.want)
			}
		})
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
