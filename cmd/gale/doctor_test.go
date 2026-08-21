package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/store"
)

// executeDoctor runs `gale doctor` with argv through cobra so a
// leftover --repair / --force is an unknown-flag parse error, not a
// silent no-op of runDoctor.
func executeDoctor(t *testing.T, argv ...string) error {
	t.Helper()
	oldArgs := rootCmd.Flags().Args()
	oldOut := rootCmd.OutOrStdout()
	oldErr := rootCmd.ErrOrStderr()
	t.Cleanup(func() {
		rootCmd.SetArgs(oldArgs)
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
	})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"doctor"}, argv...))
	return executeRoot()
}

// TestDoctorRejectsRepairFlag is the red proof that --repair is gone:
// cobra refuses the flag, and a missing current is not rebuilt.
func TestDoctorRejectsRepairFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\njq = \"1.8.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir, err := store.NewStore(storeRoot).Create("jq", "1.8.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pkgDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "bin", "jq"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, home)

	err = executeDoctor(t, "--repair")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("doctor --repair must be an unknown flag, got: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(galeDir, "current")); !os.IsNotExist(statErr) {
		t.Fatalf("doctor --repair must not rebuild current, stat err=%v", statErr)
	}
}

// TestDoctorRejectsForceFlag is the same proof for --force.
func TestDoctorRejectsForceFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gale"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTo(t, home)

	err := executeDoctor(t, "--force")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("doctor --force must be an unknown flag, got: %v", err)
	}
}

// TestDoctorRejectsRepairDoesNotPurge pins that --repair cannot
// delete store dirs.
func TestDoctorRejectsRepairDoesNotPurge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")
	jqDir := mkStorePkg(t, storeRoot, "jq", "1.8.1-1")
	fzfDir := mkStorePkg(t, storeRoot, "fzf", "0.60.0-1")
	writeRawDepsMeta(t, jqDir, corruptDepsBody)
	writeDepsMeta(t, storeRoot, "fzf", "0.60.0-1",
		depsmeta.ResolvedDep{Name: "jq", Version: "1.8.1", Revision: 1})
	writeFile(t, filepath.Join(galeDir, "gale.toml"),
		"[packages]\njq = \"1.8.1\"\n")
	chdirTo(t, t.TempDir())

	err := executeDoctor(t, "--repair")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("doctor --repair must be an unknown flag, got: %v", err)
	}
	for _, dir := range []string{jqDir, fzfDir} {
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Errorf("%s must survive doctor --repair: %v", dir, statErr)
		}
	}
}

// TestDoctorRunWritesSummaryToStdout so `gale doctor > status.txt`
// captures the answer.
func TestDoctorRunWritesSummaryToStdout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runDoctor(&doctorIO{
		galeDir: galeDir,
		cwd:     home,
		stdout:  &stdout,
		stderr:  &stderr,
	}); err == nil {
		t.Log("runDoctor returned nil; test still checks summary")
	}

	if stdout.Len() == 0 {
		t.Fatalf("stdout was empty; doctor must emit a summary "+
			"to stdout. stderr: %q", stderr.String())
	}
	s := stdout.String()
	if !strings.Contains(s, "OK") && !strings.Contains(s, "issue") {
		t.Errorf("stdout should contain a summary line "+
			"(OK or issues), got: %q", s)
	}
}

// TestDoctorReportsUnreadableGenerationWithoutAborting: an
// unreadable generation fails the roots check; PATH still runs.
func TestDoctorReportsUnreadableGenerationWithoutAborting(t *testing.T) {
	h := newDoctorFourHome(t)
	plantDoctorFourFetch(t, h, doctorFourSHA, true)
	breakGenerationWalk(t, h.galeDir)
	setGalePATH(t, h.galeDir)

	stdout, stderr := runDoctorHome(t, h)
	roots := checkLine(t, stderr, "generation matches lock roots")
	if !strings.HasPrefix(roots, "xxx ") {
		t.Errorf("unreadable generation must fail roots, got: %q", roots)
	}
	if !strings.Contains(stderr, "PATH") {
		t.Errorf("PATH must still run after an unreadable generation; stderr:\n%s",
			stderr)
	}
	if want := "of 4 checks"; !strings.Contains(stdout, want) {
		t.Errorf("summary must count every check (%q), got: %q",
			want, stdout)
	}
}

const corruptDepsBody = "deps = [{name = \"../escape\", version = \"1\"}]\n"

func writeRawDepsMeta(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".gale-deps.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// checkLine returns the single progress line whose text contains
// substr, marker prefix included, and fails when there is not
// exactly one.
func checkLine(t *testing.T, logged, substr string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, substr) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one line containing %q, got %d:\n%s",
			substr, len(found), logged)
	}
	return found[0]
}
