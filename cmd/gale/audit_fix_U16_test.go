package main

import (
	"bytes"
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

// noticeHostCase is a single row in the noticeNewHostSection
// table: the env to set, TOML to write, the --host argument,
// whether --dry-run is active, and whether a notice is expected.
type noticeHostCase struct {
	name        string
	toml        string
	galehostEnv string
	host        string
	dryRun      bool
	wantNotice  bool
}

// noticeHostCases returns the full table of noticeNewHostSection
// test cases covering typos, current host, exact/glob/comma-list
// key matches, legacy dotted headers, and dry-run suppression.
func noticeHostCases() []noticeHostCase {
	return []noticeHostCase{
		{
			name: "typo produces notice",
			toml: "[packages]\n  jq = \"1.8.1\"\n\n" +
				"[hosts.\"travis-mb.local\".packages]\n" +
				"  rg = \"14.1.0\"\n",
			galehostEnv: "testhost",
			host:        "travis-macbok.local",
			wantNotice:  true,
		},
		{
			name:        "current host never warns",
			toml:        "[packages]\n  jq = \"1.8.1\"\n",
			galehostEnv: "testhost",
			host:        "testhost",
			wantNotice:  false,
		},
		{
			name: "existing exact key no notice",
			toml: "[hosts.\"travis-mb.local\".packages]\n" +
				"  rg = \"14.1.0\"\n",
			galehostEnv: "testhost",
			host:        "travis-mb.local",
			wantNotice:  false,
		},
		{
			name: "existing glob key no notice",
			toml: "[hosts.\"travis-mb*\".packages]\n" +
				"  rg = \"14.1.0\"\n",
			galehostEnv: "testhost",
			host:        "travis-mb.local",
			wantNotice:  false,
		},
		{
			name: "glob flag targeting identical glob key no notice",
			toml: "[hosts.\"mac-*\".packages]\n" +
				"  rg = \"14.1.0\"\n",
			galehostEnv: "testhost",
			host:        "mac-*",
			wantNotice:  false,
		},
		{
			name: "comma-list flag targeting identical key no notice",
			toml: "[hosts.\"mac-1,mac-2\".packages]\n" +
				"  rg = \"14.1.0\"\n",
			galehostEnv: "testhost",
			host:        "mac-1,mac-2",
			wantNotice:  false,
		},
		{
			name:        "glob flag covering current host no notice",
			toml:        "[packages]\n  jq = \"1.8.1\"\n",
			galehostEnv: "mac-mini",
			host:        "mac-*",
			wantNotice:  false,
		},
		{
			name: "legacy dotted header no notice",
			toml: "[hosts.travis-mb.local.packages]\n" +
				"  rg = \"14.1.0\"\n",
			galehostEnv: "testhost",
			host:        "travis-mb.local",
			wantNotice:  false,
		},
		{
			name:        "dry-run never warns",
			toml:        "[packages]\n  jq = \"1.8.1\"\n",
			galehostEnv: "testhost",
			host:        "travis-macbok.local",
			dryRun:      true,
			wantNotice:  false,
		},
	}
}

// checkNoticeOutput asserts notice output matches wantNotice,
// verifying host and path are named when a notice is expected.
func checkNoticeOutput(t *testing.T, got, host, path string, wantNotice bool) {
	t.Helper()
	if wantNotice {
		if !strings.Contains(got, "creating new host section") {
			t.Errorf("expected new-host-section notice, got %q", got)
		}
		if !strings.Contains(got, host) {
			t.Errorf("notice should name the host %q, got %q", host, got)
		}
		if !strings.Contains(got, path) {
			t.Errorf("notice should name the config path, got %q", got)
		}
	} else if got != "" {
		t.Errorf("expected no notice, got %q", got)
	}
}

// TestNoticeNewHostSection tests all noticeNewHostSection cases
// in a table driven by noticeHostCases.
func TestNoticeNewHostSection(t *testing.T) {
	for _, tt := range noticeHostCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GALE_HOST", tt.galehostEnv)
			path := writeU16GaleToml(t, t.TempDir(), tt.toml)

			if tt.dryRun {
				dryRun = true
				t.Cleanup(func() { dryRun = false })
			}

			var buf bytes.Buffer
			out := output.NewWithOptions(&buf, output.Options{})
			noticeNewHostSection(out, path, tt.host)

			checkNoticeOutput(t, buf.String(), tt.host, path, tt.wantNotice)
		})
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

	chdirTo(t, projDir)

	addProject = true
	addHost = "travis-macbok.local"
	t.Cleanup(func() {
		addProject = false
		addHost = ""
	})

	// @version avoids the network resolver.
	var runErr error
	stderr := captureStderr(t, func() {
		runErr = addCmd.RunE(addCmd, []string{"jq@1.8.1"})
	})

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

	chdirTo(t, projDir)

	addProject = true
	addHost = "travis-mb.local"
	t.Cleanup(func() {
		addProject = false
		addHost = ""
	})

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = addCmd.RunE(addCmd, []string{"jq@1.8.1"})
	})

	if runErr != nil {
		t.Fatalf("add command failed: %v", runErr)
	}
	if strings.Contains(stderr, "creating new host section") {
		t.Errorf("existing host section must not warn, got %q", stderr)
	}
}
