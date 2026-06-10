package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/output"
)

// Issue #108: `gale install/add --host <name>` accepts any
// string. A typo'd hostname silently creates a brand-new
// [hosts.<typo>.packages] section in gale.toml instead of
// targeting the intended host — no error, no warning. The fix
// prints a visible notice when the named host is neither the
// current machine nor covered by any existing [hosts.<key>]
// section.

func writeU16GaleToml(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNoticeNewHostSectionTypo: a typo'd host that matches no
// existing section and is not the current host produces the
// notice.
func TestNoticeNewHostSectionTypo(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	path := writeU16GaleToml(t, t.TempDir(),
		"[packages]\n  jq = \"1.8.1\"\n\n"+
			"[hosts.\"travis-mb.local\".packages]\n  rg = \"14.1.0\"\n")

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "travis-macbok.local")

	got := buf.String()
	if !strings.Contains(got, "creating new host section") {
		t.Errorf("expected new-host-section notice, got %q", got)
	}
	if !strings.Contains(got, "travis-macbok.local") {
		t.Errorf("notice should name the host, got %q", got)
	}
	if !strings.Contains(got, path) {
		t.Errorf("notice should name the config path, got %q", got)
	}
}

// TestNoticeNewHostSectionCurrentHost: --host naming the current
// machine never warns, even with no existing section — installing
// to your own host overlay is the normal first-use flow.
func TestNoticeNewHostSectionCurrentHost(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	path := writeU16GaleToml(t, t.TempDir(),
		"[packages]\n  jq = \"1.8.1\"\n")

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "testhost")

	if got := buf.String(); got != "" {
		t.Errorf("current host must not warn, got %q", got)
	}
}

// TestNoticeNewHostSectionExactKey: --host matching an existing
// exact [hosts.<key>] section produces no notice.
func TestNoticeNewHostSectionExactKey(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	path := writeU16GaleToml(t, t.TempDir(),
		"[hosts.\"travis-mb.local\".packages]\n  rg = \"14.1.0\"\n")

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "travis-mb.local")

	if got := buf.String(); got != "" {
		t.Errorf("existing exact key must not warn, got %q", got)
	}
}

// TestNoticeNewHostSectionGlobKey: --host covered by an existing
// glob section key produces no notice — glob host keys must keep
// working (HostKeyMatches semantics).
func TestNoticeNewHostSectionGlobKey(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	path := writeU16GaleToml(t, t.TempDir(),
		"[hosts.\"travis-mb*\".packages]\n  rg = \"14.1.0\"\n")

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "travis-mb.local")

	if got := buf.String(); got != "" {
		t.Errorf("existing glob key must not warn, got %q", got)
	}
}

// TestNoticeNewHostSectionGlobFlagTargetingGlobKey: `--host mac-*`
// targeting an existing [hosts."mac-*"] key produces no notice.
func TestNoticeNewHostSectionGlobFlagTargetingGlobKey(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	path := writeU16GaleToml(t, t.TempDir(),
		"[hosts.\"mac-*\".packages]\n  rg = \"14.1.0\"\n")

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "mac-*")

	if got := buf.String(); got != "" {
		t.Errorf("glob flag targeting identical glob key must not warn, got %q", got)
	}
}

// TestNoticeNewHostSectionCommaListKey: `--host "mac-1,mac-2"`
// targeting an existing identical [hosts."mac-1,mac-2"] key
// produces no notice — AddPackage reuses the literal key, so the
// section is not new.
func TestNoticeNewHostSectionCommaListKey(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	path := writeU16GaleToml(t, t.TempDir(),
		"[hosts.\"mac-1,mac-2\".packages]\n  rg = \"14.1.0\"\n")

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "mac-1,mac-2")

	if got := buf.String(); got != "" {
		t.Errorf("comma-list flag targeting identical comma-list key must not warn, got %q", got)
	}
}

// TestNoticeNewHostSectionGlobFlagMatchingCurrentHost: a glob
// --host that covers the current machine is not foreign and
// produces no notice (HostKeyMatches direction: flag → current).
func TestNoticeNewHostSectionGlobFlagMatchingCurrentHost(t *testing.T) {
	t.Setenv("GALE_HOST", "mac-mini")
	path := writeU16GaleToml(t, t.TempDir(),
		"[packages]\n  jq = \"1.8.1\"\n")

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "mac-*")

	if got := buf.String(); got != "" {
		t.Errorf("glob flag covering current host must not warn, got %q", got)
	}
}

// TestNoticeNewHostSectionLegacyDottedHeader: a host present only
// as a pre-#59 unquoted dotted header ([hosts.h.local.packages])
// is an existing section on disk (the mutators normalize and
// reuse it) and produces no notice.
func TestNoticeNewHostSectionLegacyDottedHeader(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	path := writeU16GaleToml(t, t.TempDir(),
		"[hosts.travis-mb.local.packages]\n  rg = \"14.1.0\"\n")

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "travis-mb.local")

	if got := buf.String(); got != "" {
		t.Errorf("legacy dotted header must not warn, got %q", got)
	}
}

// TestAddHostTypoPrintsNotice: end-to-end `gale add --host
// <typo>` prints the notice on stderr and still performs the
// write (a notice, not an error — existing workflows keep
// working).
func TestAddHostTypoPrintsNotice(t *testing.T) {
	projDir := t.TempDir()
	t.Setenv("HOME", projDir)
	t.Setenv("GALE_HOST", "testhost")

	configPath := writeU16GaleToml(t, projDir,
		"[packages]\n  jq = \"1.8.1\"\n\n"+
			"[hosts.\"travis-mb.local\".packages]\n  rg = \"14.1.0\"\n")

	orig, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	addProject = true
	addHost = "travis-macbok.local"
	t.Cleanup(func() {
		addProject = false
		addHost = ""
	})

	// Capture stderr via a pipe drained on a goroutine so the
	// writer never blocks.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	stderrCh := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		stderrCh <- string(data)
	}()

	// @version avoids the network resolver.
	runErr := addCmd.RunE(addCmd, []string{"jq@1.8.1"})
	w.Close()
	stderr := <-stderrCh
	os.Stderr = origStderr

	if runErr != nil {
		t.Fatalf("add command failed: %v", runErr)
	}

	if !strings.Contains(stderr, "creating new host section") {
		t.Errorf("expected new-host-section notice on stderr, got %q",
			stderr)
	}

	// The write must still happen — notice, not error.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data),
		`[hosts."travis-macbok.local".packages]`) {
		t.Errorf("typo'd host section should still be written:\n%s",
			string(data))
	}
}

// TestAddHostExistingSectionNoNotice: end-to-end `gale add
// --host` targeting an existing section stays quiet.
func TestAddHostExistingSectionNoNotice(t *testing.T) {
	projDir := t.TempDir()
	t.Setenv("HOME", projDir)
	t.Setenv("GALE_HOST", "testhost")

	writeU16GaleToml(t, projDir,
		"[packages]\n  jq = \"1.8.1\"\n\n"+
			"[hosts.\"travis-mb.local\".packages]\n  rg = \"14.1.0\"\n")

	orig, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	addProject = true
	addHost = "travis-mb.local"
	t.Cleanup(func() {
		addProject = false
		addHost = ""
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	stderrCh := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		stderrCh <- string(data)
	}()

	runErr := addCmd.RunE(addCmd, []string{"jq@1.8.1"})
	w.Close()
	stderr := <-stderrCh
	os.Stderr = origStderr

	if runErr != nil {
		t.Fatalf("add command failed: %v", runErr)
	}
	if strings.Contains(stderr, "creating new host section") {
		t.Errorf("existing host section must not warn, got %q", stderr)
	}
}

// TestNoticeNewHostSectionDryRun: under --dry-run nothing is
// written, so no "creating" notice fires.
func TestNoticeNewHostSectionDryRun(t *testing.T) {
	t.Setenv("GALE_HOST", "testhost")
	path := writeU16GaleToml(t, t.TempDir(),
		"[packages]\n  jq = \"1.8.1\"\n")

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	var buf bytes.Buffer
	out := output.NewWithOptions(&buf, output.Options{})
	noticeNewHostSection(out, path, "travis-macbok.local")

	if got := buf.String(); got != "" {
		t.Errorf("dry-run must not warn, got %q", got)
	}
}
