package attestation

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/ghcr"
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
// probe was bypassed and gh returns "unknown command"
// mid-verify.
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
	argvLog             string // if set, append each call's argv here
}

// writeMockGH creates a shell script that dispatches on
// the second arg: "--help" returns attestationHelpExit;
// "verify" returns verifyExit (writing verifyStderr first).
// Any other invocation returns the help exit code so the
// probe stays the source of truth.
func writeMockGH(t *testing.T, opts mockOpts) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	script := "#!/bin/sh\n"
	if opts.argvLog != "" {
		script += "echo \"$*\" >> '" + opts.argvLog + "'\n"
	}
	script += "case \"$2\" in\n" +
		"  --help) exit " + itoa(opts.attestationHelpExit) + " ;;\n" +
		"  verify)\n"
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

// TestVerifyOCIReferrerPassesBundleArg pins the new tokenless
// OCI contract: VerifyOCIReferrer hands gh the oci:// subject
// plus "--bundle <path>" (an offline verify against the bundle
// gale already fetched) and NEVER --bundle-from-oci.
func TestVerifyOCIReferrerPassesBundleArg(t *testing.T) {
	isolate(t)
	log := filepath.Join(t.TempDir(), "argv.log")
	mock := writeMockGH(t, mockOpts{verifyExit: 0, argvLog: log})
	orig := lookPath
	lookPath = func(name string) (string, error) { return mock, nil }
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	uri := "oci://ghcr.io/owner/repo/img@sha256:abc"
	if err := v.VerifyOCIReferrer(uri, "owner/repo", []byte(`{"x":1}`)); err != nil {
		t.Fatalf("VerifyOCIReferrer: %v", err)
	}

	argv := readFile(t, log)
	if !strings.Contains(argv, uri) {
		t.Errorf("argv %q missing oci subject", argv)
	}
	if !strings.Contains(argv, "--bundle ") {
		t.Errorf("argv %q missing --bundle", argv)
	}
	if strings.Contains(argv, "--bundle-from-oci") {
		t.Errorf("argv %q must not use --bundle-from-oci", argv)
	}
}

// TestVerifyOCIReferrerFailurePropagates checks a real gh
// mismatch (exit 1) surfaces as an error.
func TestVerifyOCIReferrerFailurePropagates(t *testing.T) {
	isolate(t)
	mock := writeMockGH(t, mockOpts{
		verifyExit:   1,
		verifyStderr: "verification failed",
	})
	orig := lookPath
	lookPath = func(name string) (string, error) { return mock, nil }
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	err := v.VerifyOCIReferrer(
		"oci://ghcr.io/owner/repo/img@sha256:abc",
		"owner/repo", []byte(`{"x":1}`),
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "verification failed") {
		t.Errorf("error %q missing gh stderr", err.Error())
	}
}

// fakeVerifier records which Verifier methods VerifyPrebuilt
// invokes so routing tests can assert the decision path
// without spawning gh.
type fakeVerifier struct {
	ociCalled  bool
	fileCalled bool
	ociErr     error
	fileErr    error
}

func (f *fakeVerifier) Available() bool           { return true }
func (f *fakeVerifier) UnavailableReason() string { return "" }

func (f *fakeVerifier) VerifyFile(filePath, repo string) error {
	f.fileCalled = true
	return f.fileErr
}

func (f *fakeVerifier) VerifyOCIReferrer(ociURI, repo string, bundle []byte) error {
	f.ociCalled = true
	return f.ociErr
}

// TestVerifyPrebuiltUsesReferrerWhenBundleFound asserts that a
// successful FetchBundle routes to VerifyOCIReferrer and never
// touches the file fallback.
func TestVerifyPrebuiltUsesReferrerWhenBundleFound(t *testing.T) {
	fv := &fakeVerifier{}
	archiveCalled := false
	err := VerifyPrebuilt(fv, PrebuiltParams{
		Repo:           "owner/repo",
		OCIURI:         "oci://ghcr.io/owner/repo/img@sha256:abc",
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
		t.Error("expected VerifyOCIReferrer to be called")
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
		OCIURI:         "oci://ghcr.io/owner/repo/img@sha256:abc",
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
		t.Error("VerifyOCIReferrer must not run after ErrNoReferrer")
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
		OCIURI:         "oci://ghcr.io/owner/repo/img@sha256:abc",
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
		Repo:   "owner/repo",
		OCIURI: "oci://ghcr.io/owner/repo/img@sha256:abc",
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
// referrer bundle is found, a VerifyOCIReferrer failure propagates
// without any file fallback.
func TestVerifyPrebuiltReferrerErrorFailsClosed(t *testing.T) {
	want := errors.New("cert identity mismatch")
	fv := &fakeVerifier{ociErr: want}
	err := VerifyPrebuilt(fv, PrebuiltParams{
		Repo:           "owner/repo",
		OCIURI:         "oci://ghcr.io/owner/repo/img@sha256:abc",
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

// TestVerifyFilePassesBundleArg asserts the file path hands
// gh "--bundle <path>" (and never --bundle-from-oci), pinning
// the argv contract for the token-free file fallback.
func TestVerifyFilePassesBundleArg(t *testing.T) {
	isolate(t)
	origEndpoint := attestationsEndpoint
	attestationsEndpoint = withAttestationServer(t)
	defer func() { attestationsEndpoint = origEndpoint }()

	log := filepath.Join(t.TempDir(), "argv.log")
	mock := writeMockGH(t, mockOpts{verifyExit: 0, argvLog: log})
	orig := lookPath
	lookPath = func(name string) (string, error) { return mock, nil }
	defer func() { lookPath = orig }()

	file := writeTempFile(t, []byte("archive bytes"))
	v := &GHVerifier{}
	if err := v.VerifyFile(file, "owner/repo"); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}

	argv := readFile(t, log)
	if !strings.Contains(argv, "--bundle ") {
		t.Errorf("argv %q missing --bundle", argv)
	}
	if strings.Contains(argv, "--bundle-from-oci") {
		t.Errorf("argv %q must not use --bundle-from-oci on file path", argv)
	}
}

// readFile returns the contents of path or fails the test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	return string(b)
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
