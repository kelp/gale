package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/store"
)

func TestRepairDoctorRebuildsGlobalGeneration(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(home, ".gale", "pkg")
	configPath := filepath.Join(galeDir, "gale.toml")

	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n  jq = \"1.8.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("jq", "1.8.1")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "jq"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       home,
		out:       output.NewWithOptions(&bytes.Buffer{}, output.Options{}),
	}

	if err := repairDoctor(ctx); err != nil {
		t.Fatalf("repairDoctor: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(galeDir, "current", "bin", "jq")); err != nil {
		t.Fatalf("jq symlink missing after repair: %v", err)
	}
}

// TestCheckPackagesInstalledOffersRemove verifies that when
// the store is missing a package the config lists, the
// remediation message points the user at BOTH `gale sync`
// and `gale remove`. Before the fix, only `gale sync` was
// suggested — so a user who had just tried (and failed,
// because of the host-overlay bug) to remove the package
// had no discoverable path forward and would reinstall the
// thing they wanted gone.
func TestCheckPackagesInstalledOffersRemove(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, ".gale", "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	s := store.NewStore(storeRoot)

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:    filepath.Join(home, ".gale"),
		storeRoot:  storeRoot,
		cwd:        home,
		store:      s,
		globalPkgs: map[string]string{"foo": "1.0"},
		out:        output.NewWithOptions(&buf, output.Options{}),
	}

	if checkPackagesInstalled(ctx) {
		t.Fatal("expected checkPackagesInstalled to return false")
	}

	out := buf.String()
	if !strings.Contains(out, "gale sync") {
		t.Errorf("missing `gale sync` suggestion: %q", out)
	}
	if !strings.Contains(out, "gale remove foo") {
		t.Errorf("missing `gale remove foo` suggestion: %q", out)
	}
}

// TestCheckHostOverridesReportsShadowedShared verifies that
// when a package appears in both shared [packages] and a
// matching [hosts.<host>.packages] overlay, doctor surfaces
// it. Host-wins is intentional but easy to miss; this check
// makes the redundancy discoverable.
func TestCheckHostOverridesReportsShadowedShared(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GALE_HOST", "h1")
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  ripgrep = \"15.0\"\n\n"+
			"[hosts.h1.packages]\n  ripgrep = \"14.0\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir: galeDir,
		cwd:     home,
		out:     output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkHostOverrides(ctx) {
		t.Fatal("checkHostOverrides should warn (not fail)")
	}

	out := buf.String()
	if !strings.Contains(out, "ripgrep") {
		t.Errorf("expected ripgrep mentioned: %q", out)
	}
	if !strings.Contains(out, "h1") {
		t.Errorf("expected host h1 mentioned: %q", out)
	}
}

// TestCheckHostOverridesSilentWhenNoOverlap verifies the
// check stays quiet when shared and host overlays don't
// shadow each other.
func TestCheckHostOverridesSilentWhenNoOverlap(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GALE_HOST", "h1")
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  ripgrep = \"15.0\"\n\n"+
			"[hosts.h1.packages]\n  fzf = \"0.50\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir: galeDir,
		cwd:     home,
		out:     output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkHostOverrides(ctx) {
		t.Fatal("checkHostOverrides should pass")
	}
	if strings.Contains(buf.String(), "overrides") {
		t.Errorf("unexpected override warning: %q", buf.String())
	}
}

// TestCheckOrphansIgnoresResolvedRevisions verifies that when
// config carries a bare version (`bat = "0.26.1"`) and the
// store holds the canonical revision dir (`bat/0.26.1-2`),
// checkOrphans does NOT flag the active package as orphaned.
// Before the fix, checkOrphans built the referenced set with
// the bare config key and compared against the store's revision
// key — strings never matched, so every active package looked
// orphaned and the count was wildly inflated.
func TestCheckOrphansIgnoresResolvedRevisions(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  bat = \"0.26.1\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("bat", "0.26.1-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(
		filepath.Join(pkgDir, "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkgDir, "bin", "bat"),
		[]byte("#!/bin/sh\n"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	installed, err := s.List()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       home,
		store:     s,
		installed: installed,
		out:       output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkOrphans(ctx) {
		t.Fatal("checkOrphans returned false (should warn-only)")
	}

	if bytes.Contains(buf.Bytes(), []byte("orphaned version(s)")) {
		t.Errorf("checkOrphans reported orphans for an active "+
			"package: %q", buf.String())
	}
}

// TestCheckOrphansCountsOldRevisions verifies that once an old
// revision is no longer referenced by config (bare version
// resolves to a newer revision), checkOrphans correctly flags
// the stale revision as orphaned.
func TestCheckOrphansCountsOldRevisions(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(galeDir, "pkg")

	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8.1\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	// -3 is the highest, so bare jq = "1.8.1" resolves to it.
	// -2 is an old revision that should be flagged orphaned.
	for _, ver := range []string{"1.8.1-2", "1.8.1-3"} {
		d, err := s.Create("jq", ver)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(
			filepath.Join(d, "bin"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(d, "bin", "jq"),
			[]byte("#!/bin/sh\n"), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	installed, err := s.List()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       home,
		store:     s,
		installed: installed,
		out:       output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkOrphans(ctx) {
		t.Fatal("checkOrphans returned false (should warn-only)")
	}

	if !bytes.Contains(buf.Bytes(), []byte("1 orphaned version(s)")) {
		t.Errorf("expected 1 orphaned version (old jq-2), "+
			"got: %q", buf.String())
	}
}

// TestCheckGenerationFailsOnDanglingCurrentSymlink pins the
// marquee doctor bug: when ~/.gale/current points to a gen
// directory that no longer exists, checkGeneration must fail
// loudly (red xxx) instead of reporting a green checkmark.
// Doctor exists specifically to catch this corruption.
func TestCheckGenerationFailsOnDanglingCurrentSymlink(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Point current at gen/9 without creating gen/9.
	if err := os.Symlink(
		filepath.Join("gen", "9"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir: galeDir,
		cwd:     home,
		out:     output.NewWithOptions(&buf, output.Options{}),
	}

	if checkGeneration(ctx) {
		t.Fatalf("checkGeneration should fail on dangling "+
			"current symlink; output: %q", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "xxx ") {
		t.Errorf("expected error prefix, got: %q", out)
	}
	if !strings.Contains(out, "gale sync") {
		t.Errorf("expected actionable `gale sync` suggestion, "+
			"got: %q", out)
	}
}

// TestCheckGenerationPassesWhenTargetExists verifies the happy
// path still works after we tightened the check: a current
// symlink to an existing gen dir gives a green success.
func TestCheckGenerationPassesWhenTargetExists(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(
		filepath.Join(galeDir, "gen", "1", "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join("gen", "1"),
		filepath.Join(galeDir, "current"),
	); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := &doctorContext{
		galeDir: galeDir,
		cwd:     home,
		out:     output.NewWithOptions(&buf, output.Options{}),
	}

	if !checkGeneration(ctx) {
		t.Fatalf("checkGeneration should pass; output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "==> ") {
		t.Errorf("expected success prefix, got: %q", buf.String())
	}
}

// TestDoctorRunWritesSummaryToStdout pins the stdout discipline:
// per-check progress lines go to stderr (an Output writer), but
// the final summary block ("OK" or "N issues") goes to stdout
// so `gale doctor > status.txt` captures the answer. Without
// this contract, the file would be zero bytes.
func TestDoctorRunWritesSummaryToStdout(t *testing.T) {
	home := t.TempDir()
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
		// We expect failures (no current symlink, no PATH, etc.)
		// — the point is the summary line still goes to stdout.
		t.Log("runDoctor returned nil; test still checks summary")
	}

	if stdout.Len() == 0 {
		t.Fatalf("stdout was empty; doctor must emit a summary "+
			"to stdout. stderr: %q", stderr.String())
	}
	// Summary should contain a structured marker so users can
	// grep it. Either "OK" (all green) or "issues" (some failed).
	s := stdout.String()
	if !strings.Contains(s, "OK") && !strings.Contains(s, "issue") {
		t.Errorf("stdout should contain a summary line "+
			"(OK or issues), got: %q", s)
	}
}

func TestRepairDoctorRebuildsToolVersionsProjectGeneration(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(home, ".gale", "pkg")
	globalConfig := filepath.Join(galeDir, "gale.toml")
	projectDir := filepath.Join(home, "project")
	projectGaleDir := filepath.Join(projectDir, ".gale")

	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalConfig, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".tool-versions"),
		[]byte("golang 1.26.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := store.NewStore(storeRoot)
	pkgDir, err := s.Create("go", "1.26.1")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(pkgDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "go"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := &doctorContext{
		galeDir:   galeDir,
		storeRoot: storeRoot,
		cwd:       projectDir,
		out:       output.NewWithOptions(&bytes.Buffer{}, output.Options{}),
	}

	if err := repairDoctor(ctx); err != nil {
		t.Fatalf("repairDoctor: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(projectGaleDir, "current", "bin", "go")); err != nil {
		t.Fatalf("go symlink missing after project repair: %v", err)
	}
}
