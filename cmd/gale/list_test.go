package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/store"
)

// TestListGroupsByScope verifies that the default output of
// `gale list` separates shared packages from host overlays so
// host-scoped entries are not invisible.
func TestListGroupsByScope(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "h1")
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8\"\n\n"+
			"[hosts.h1.packages]\n  actionlint = \"1.7\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Run from a dir with no project gale.toml so list falls
	// back to global.
	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	os.Chdir(cwd)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "all"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Shared") {
		t.Errorf("missing Shared header: %q", out)
	}
	if !strings.Contains(out, "Host (h1)") {
		t.Errorf("missing Host (h1) header: %q", out)
	}
	if !strings.Contains(out, "jq@1.8") {
		t.Errorf("missing jq line: %q", out)
	}
	if !strings.Contains(out, "actionlint@1.7") {
		t.Errorf("missing actionlint line: %q", out)
	}
	// Shared header should precede host header.
	if i, j := strings.Index(out, "Shared"),
		strings.Index(out, "Host"); j >= 0 && i > j {
		t.Errorf("shared should appear before host: %q", out)
	}
}

// TestListMarksOverriddenSharedEntry verifies that a shared
// package shadowed by a host overlay is flagged in the
// shared section so the user knows the shared value is dead
// on this machine.
func TestListMarksOverriddenSharedEntry(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "h1")
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  ripgrep = \"15.0\"\n\n"+
			"[hosts.h1.packages]\n  ripgrep = \"14.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	os.Chdir(cwd)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "all"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ripgrep@15.0") {
		t.Errorf("missing shared ripgrep@15.0: %q", out)
	}
	if !strings.Contains(out, "overridden") {
		t.Errorf("missing override marker: %q", out)
	}
	if !strings.Contains(out, "ripgrep@14.0") {
		t.Errorf("missing host ripgrep@14.0: %q", out)
	}
}

// TestListScopeSharedHidesHostOverlay verifies that
// --scope=shared lists only the shared section.
func TestListScopeSharedHidesHostOverlay(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "h1")
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8\"\n\n"+
			"[hosts.h1.packages]\n  actionlint = \"1.7\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	os.Chdir(cwd)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "shared"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "jq@1.8") {
		t.Errorf("missing jq line: %q", out)
	}
	if strings.Contains(out, "actionlint") {
		t.Errorf("actionlint should be hidden under --scope=shared: %q",
			out)
	}
}

// TestListScopeHostHidesShared verifies that --scope=host
// lists only the current host's overlay.
func TestListScopeHostHidesShared(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GALE_HOST", "h1")
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8\"\n\n"+
			"[hosts.h1.packages]\n  actionlint = \"1.7\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	os.Chdir(cwd)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "host"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "actionlint@1.7") {
		t.Errorf("missing actionlint line: %q", out)
	}
	if strings.Contains(out, "jq") {
		t.Errorf("jq should be hidden under --scope=host: %q", out)
	}
}

// TestListStableFormatWithoutHostOverlays verifies that the
// grouped Shared / Host schema is used even when no host
// overlays apply, so shell pipelines don't break the day a
// user adds their first overlay. Regression test for
// audit/readonly/output-format/findings/0003-list-format-changes-with-overlays.md.
func TestListStableFormatWithoutHostOverlays(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8\"\n  ripgrep = \"15.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	os.Chdir(cwd)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "all"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Shared:") {
		t.Errorf("expected Shared header (stable schema): %q", out)
	}
	if !strings.Contains(out, "  jq@1.8") {
		t.Errorf("jq should be indented under Shared: %q", out)
	}
	if !strings.Contains(out, "  ripgrep@15.0") {
		t.Errorf("ripgrep should be indented under Shared: %q", out)
	}
}

// TestListEmptyStateExitsZeroAndUsesStderr verifies that when
// no gale.toml exists, list exits cleanly (no error) and the
// empty-state notice goes to stderr — stdout stays empty so
// pipelines like `gale list | wc -l` see 0 lines. Regression
// for audit/readonly/exit-codes/findings/0003-list-vs-sbom-empty-state-mismatch.md.
func TestListEmptyStateExitsZeroAndUsesStderr(t *testing.T) {
	home := t.TempDir()
	// Deliberately do NOT create .gale/gale.toml.
	t.Setenv("HOME", home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	os.Chdir(home)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "all"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("stdout should be empty in empty state, got %q",
			got)
	}
	if !strings.Contains(errBuf.String(), "No packages declared") {
		t.Errorf("stderr should explain empty state, got %q",
			errBuf.String())
	}
}

// TestListEmptyStateWithEmptyConfig verifies that an empty
// gale.toml also produces an empty stdout and a stderr
// notice. Same exit-code/stream contract as no config at all.
func TestListEmptyStateWithEmptyConfig(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"), []byte(""),
		0o644); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	os.Chdir(cwd)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "all"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("stdout should be empty, got %q", got)
	}
	if !strings.Contains(errBuf.String(), "No packages declared") {
		t.Errorf("stderr should explain empty state, got %q",
			errBuf.String())
	}
}

// TestListMarksDeclaredButNotInstalled verifies that
// packages declared in gale.toml but absent from the store
// are flagged so list doesn't lie about installation state.
// Regression for audit/readonly/empty-state/findings/0004-list-reports-declared-as-installed.md.
func TestListMarksDeclaredButNotInstalled(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	// No store entries — package is declared but not installed.

	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	os.Chdir(cwd)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "all"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "jq@1.8") {
		t.Errorf("missing jq entry: %q", out)
	}
	if !strings.Contains(out, "(not installed)") {
		t.Errorf("missing (not installed) marker: %q", out)
	}
}

// TestListInstalledPackageHasNoMarker verifies the
// (not installed) marker is gated on store presence: an
// installed package shows clean.
func TestListInstalledPackageHasNoMarker(t *testing.T) {
	home := t.TempDir()
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte("[packages]\n  jq = \"1.8\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	// Fake the store entry: a non-empty version dir is what
	// IsInstalled checks.
	storeRoot := filepath.Join(home, ".gale", "pkg")
	pkgDir := filepath.Join(storeRoot, "jq", "1.8")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pkgDir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Sanity check: store agrees the pkg is installed.
	s := store.NewStore(storeRoot)
	if !s.IsInstalled("jq", "1.8") {
		t.Fatalf("test setup: store should report jq@1.8 installed")
	}

	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	os.Chdir(cwd)
	t.Cleanup(func() { os.Chdir(orig) })

	listScope = "all"
	t.Cleanup(func() { listScope = "all" })

	var buf, errBuf bytes.Buffer
	if err := runList(&buf, &errBuf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "jq@1.8") {
		t.Errorf("missing jq line: %q", out)
	}
	if strings.Contains(out, "(not installed)") {
		t.Errorf("installed pkg should not be flagged: %q", out)
	}
}
