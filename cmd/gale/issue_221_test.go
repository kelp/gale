package main

// gh#221: the sync completion stamp (gh#186) is read by
// `gale sync --if-needed` and by nothing a user can ask. A person who
// runs `gale doctor` because a binary is missing from PATH is told the
// package is missing; nothing tells them the last sync gave up on it,
// or that `gale sync` retries now rather than after the backoff.
//
// The check is advisory by construction, so these tests assert the
// report and the passing verdict together — see checkSyncState's own
// comment for why the exit code stays checkPackagesInstalled's.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// issue221Fingerprint stands in for the hash a real sync records. The
// check never compares it against anything — a stamp whose inputs have
// moved is `sync --if-needed`'s question, not doctor's — so its value
// is arbitrary and its presence is what keeps the fixture honest.
const issue221Fingerprint = "sha256:0000000000000000000000000000" +
	"000000000000000000000000000000000000"

// stampScope records one sync outcome in galeDir, making the directory
// first. The stamp is the only fixture these tests need: the check
// reads it and nothing else.
func stampScope(
	t *testing.T, galeDir string, complete bool, failed ...string,
) {
	t.Helper()
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp(t, galeDir, issue221Fingerprint, complete, failed...)
}

// TestDoctorReportsIncompleteSyncState is gh#221 itself. The stamp
// records that the last sync gave up on python, and doctor must say
// so — naming the package and the remedy that ignores the backoff.
func TestDoctorReportsIncompleteSyncState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	stampScope(t, galeDir, false, "python@3.13.1")
	cwd := t.TempDir()
	chdirTo(t, cwd)

	var buf bytes.Buffer
	if !checkSyncState(doctorCtx(galeDir, storeRoot, cwd, &buf)) {
		t.Errorf("an incomplete stamp must not fail the check — "+
			"checkPackagesInstalled owns the exit code, got: %q",
			buf.String())
	}
	msg := buf.String()
	for _, want := range []string{"python@3.13.1", "gale sync", "global"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report must name %q, got: %q", want, msg)
		}
	}
}

// TestDoctorSilentOnCompleteSyncState is the other half. A sync that
// completed is the ordinary state, and a check that talked about it
// the way it talks about a failure would train the user to skip both.
func TestDoctorSilentOnCompleteSyncState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	stampScope(t, galeDir, true)
	cwd := t.TempDir()
	chdirTo(t, cwd)

	var buf bytes.Buffer
	if !checkSyncState(doctorCtx(galeDir, storeRoot, cwd, &buf)) {
		t.Fatalf("a complete stamp must pass, got: %q", buf.String())
	}
	if msg := buf.String(); strings.Contains(msg, "gale sync") {
		t.Errorf("a completed sync must not send the user anywhere, "+
			"got: %q", msg)
	}
}

// TestDoctorDistinguishesMissingFromUnreadableSyncState carries
// gh#186's distinction into the report. A machine that has never
// synced under this gale has no stamp and no finding; one whose stamp
// cannot be read has a state worth looking at. Collapsing them would
// either invent a problem on every fresh install or hide a corrupt
// file.
func TestDoctorDistinguishesMissingFromUnreadableSyncState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	chdirTo(t, cwd)

	var missing bytes.Buffer
	if !checkSyncState(doctorCtx(galeDir, storeRoot, cwd, &missing)) {
		t.Fatalf("no stamp must pass, got: %q", missing.String())
	}
	if msg := missing.String(); strings.Contains(msg, "gale sync") {
		t.Errorf("a project that has never synced is not a finding, "+
			"got: %q", msg)
	}

	if err := os.WriteFile(filepath.Join(galeDir, syncStateFile),
		[]byte("schema = \"not an integer\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var unreadable bytes.Buffer
	if !checkSyncState(doctorCtx(galeDir, storeRoot, cwd, &unreadable)) {
		t.Fatalf("an unparsable stamp must not fail the check, got: %q",
			unreadable.String())
	}
	if missing.String() == unreadable.String() {
		t.Errorf("missing and unparsable report the same line %q; "+
			"on-disk state carries meaning and the two are not the "+
			"same condition", missing.String())
	}
	if msg := unreadable.String(); !strings.Contains(msg, syncStateFile) {
		t.Errorf("the report must name the unreadable file, got: %q", msg)
	}
}

// TestDoctorReadsProjectSyncState is the second scope. The stamp lives
// beside the generation it describes — <project>/.gale, not ~/.gale —
// so a check reading the global dir alone would stay silent in exactly
// the place gh#186 was reported from: a project whose activation is
// short a package.
func TestDoctorReadsProjectSyncState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	stampScope(t, galeDir, true)

	proj := filepath.Join(home, "proj")
	writeFile(t, filepath.Join(proj, "gale.toml"), "[packages]\n")
	stampScope(t, filepath.Join(proj, ".gale"), false, "node@22.0.0")
	chdirTo(t, proj)

	var buf bytes.Buffer
	if !checkSyncState(doctorCtx(galeDir, storeRoot, proj, &buf)) {
		t.Fatalf("the check is advisory in every scope, got: %q",
			buf.String())
	}
	msg := buf.String()
	for _, want := range []string{"node@22.0.0", "project"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the project scope's stamp must be reported, "+
				"want %q in: %q", want, msg)
		}
	}
}
