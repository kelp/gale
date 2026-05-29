package attestation

import (
	"bytes"
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
	mock := writeMockGH(t, mockOpts{verifyExit: 0})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	if err := v.VerifyFile("test.tar.zst", "owner/repo"); err != nil {
		t.Errorf("expected success, got %v", err)
	}
}

func TestVerifyFileFailure(t *testing.T) {
	isolate(t)
	mock := writeMockGH(t, mockOpts{
		verifyExit:   1,
		verifyStderr: "verification failed",
	})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	err := v.VerifyFile("test.tar.zst", "owner/repo")
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestVerifyFileGHNotAvailable(t *testing.T) {
	isolate(t)
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return "", &os.PathError{Op: "lookpath", Path: "gh"}
	}
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	err := v.VerifyFile("test.tar.zst", "owner/repo")
	if err == nil {
		t.Error("expected error when gh not available")
	}
}

// TestVerifyOldGhReportsUpgradeGuidance covers the
// defence-in-depth branch in runVerify for the case where
// probe was bypassed (e.g. direct VerifyOCI call) and gh
// returns "unknown command" mid-verify.
func TestVerifyOldGhReportsUpgradeGuidance(t *testing.T) {
	isolate(t)
	mock := writeMockGH(t, mockOpts{
		verifyExit:   1,
		verifyStderr: `unknown command "attestation" for "gh"`,
	})
	orig := lookPath
	lookPath = func(name string) (string, error) {
		return mock, nil
	}
	defer func() { lookPath = orig }()

	v := &GHVerifier{}
	err := v.VerifyFile("test.tar.zst", "owner/repo")
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
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
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
