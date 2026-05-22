package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	var buf bytes.Buffer
	if err := runList(&buf); err != nil {
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

	var buf bytes.Buffer
	if err := runList(&buf); err != nil {
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

	var buf bytes.Buffer
	if err := runList(&buf); err != nil {
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

	var buf bytes.Buffer
	if err := runList(&buf); err != nil {
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

// TestListBackwardCompatibleWhenNoHosts verifies that when no
// host overlays exist, list output stays simple — one
// `name@version` per line. Adding scope headers for users who
// never used hosts would be needless noise.
func TestListBackwardCompatibleWhenNoHosts(t *testing.T) {
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

	var buf bytes.Buffer
	if err := runList(&buf); err != nil {
		t.Fatalf("runList: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "Shared") {
		t.Errorf("should not show Shared header when no hosts: %q",
			out)
	}
	if !strings.Contains(out, "jq@1.8") {
		t.Errorf("missing jq line: %q", out)
	}
}
