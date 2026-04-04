package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrependPATHReplacesDuplicateEntry(t *testing.T) {
	// prependPATH must replace the existing PATH entry,
	// not append a second one. If two PATH entries exist,
	// getenv(3) returns the first (original) one, making
	// the gale bin dir invisible.
	env := prependPATH("/gale/bin")

	var pathEntries []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			pathEntries = append(pathEntries, entry)
		}
	}

	if len(pathEntries) != 1 {
		t.Fatalf("expected 1 PATH entry, got %d: %v",
			len(pathEntries), pathEntries)
	}
	if !strings.HasPrefix(pathEntries[0], "PATH=/gale/bin:") {
		t.Errorf("PATH should start with /gale/bin: got %q",
			pathEntries[0])
	}
}

func TestSyncIfNeededUsesProjectDir(t *testing.T) {
	// When projectDir is specified, syncIfNeeded must
	// look for gale.toml there instead of os.Getwd().
	// Create a project dir with an invalid config to
	// verify it reads from projectDir (not cwd).
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("this is not valid toml {{{\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// cwd has no gale.toml — if syncIfNeeded ignores
	// projectDir it would silently return.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	emptyDir := t.TempDir()
	os.Chdir(emptyDir)

	var buf bytes.Buffer
	syncIfNeeded(&buf, projDir)

	if buf.Len() == 0 {
		t.Error("syncIfNeeded should read config from " +
			"projectDir, got no output")
	}
}

func TestSyncIfNeededSyncsTargetProject(t *testing.T) {
	// When projectDir is specified and the lockfile is stale,
	// syncIfNeeded must sync against the target project —
	// not the cwd or global scope. We verify by setting cwd
	// to a directory with NO gale.toml, while projectDir
	// has a valid gale.toml with packages. If sync targets
	// the wrong scope (cwd/global), it will either skip
	// (no config) or sync the wrong config.
	//
	// We create a project with a package that won't be in
	// the store, making sync attempt to install it. The
	// install will fail (no registry), but the error message
	// should reference the package name from the PROJECT's
	// config — proving sync used the right scope.
	projDir := t.TempDir()
	projConfig := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(projConfig,
		[]byte("[packages]\ntest-project-pkg = \"9.9.9\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// cwd has no gale.toml.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	emptyDir := t.TempDir()
	os.Chdir(emptyDir)

	var buf bytes.Buffer
	syncIfNeeded(&buf, projDir)

	output := buf.String()
	// syncIfNeeded writes "sync failed: ..." to buf when
	// runSync returns an error. The package-specific errors
	// go to os.Stderr inside runSync. We verify sync was
	// attempted against the project (not skipped) by
	// checking for the "sync failed" warning.
	if !strings.Contains(output, "sync failed") {
		t.Errorf("syncIfNeeded should attempt sync for the "+
			"target project, got output: %q", output)
	}
}

func TestSyncIfNeededNestedSubdirectory(t *testing.T) {
	// When projectDir is a nested subdirectory inside a
	// project, syncIfNeeded must walk up to find gale.toml
	// at the project root and sync against that root —
	// not the nested path.
	projDir := t.TempDir()
	projConfig := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(projConfig,
		[]byte("[packages]\nnested-test-pkg = \"1.0.0\"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Create a nested subdirectory with no gale.toml.
	nestedDir := filepath.Join(projDir, "src", "pkg")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// cwd has no gale.toml.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	emptyDir := t.TempDir()
	os.Chdir(emptyDir)

	var buf bytes.Buffer
	syncIfNeeded(&buf, nestedDir)

	output := buf.String()
	// syncIfNeeded should walk up from nestedDir to
	// projDir, find gale.toml there, and sync that
	// project. Verify sync was attempted (not skipped)
	// by checking for the "sync failed" warning that
	// syncIfNeeded writes to buf.
	if !strings.Contains(output, "sync failed") {
		t.Errorf("syncIfNeeded from nested dir should "+
			"attempt sync for the project root, "+
			"got output: %q", output)
	}
}

func TestResolveProjectRootToolVersionsNestedSubdir(t *testing.T) {
	// When --project points to a nested subdirectory of a
	// project that uses .tool-versions (no gale.toml), the
	// resolved root must be the directory containing
	// .tool-versions, not the raw nested path.
	projDir := t.TempDir()
	tvPath := filepath.Join(projDir, ".tool-versions")
	if err := os.WriteFile(tvPath,
		[]byte("jq 1.7.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	nestedDir := filepath.Join(projDir, "src", "pkg")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got := resolveProjectRoot(nestedDir)
	if got != projDir {
		t.Errorf("resolveProjectRoot(%q) = %q, want %q",
			nestedDir, got, projDir)
	}
}

func TestResolveProjectRootGaleTomlNestedSubdir(t *testing.T) {
	// When --project points to a nested subdirectory of a
	// project with gale.toml, the resolved root must be the
	// directory containing gale.toml.
	projDir := t.TempDir()
	configPath := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	nestedDir := filepath.Join(projDir, "src", "pkg")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got := resolveProjectRoot(nestedDir)
	if got != projDir {
		t.Errorf("resolveProjectRoot(%q) = %q, want %q",
			nestedDir, got, projDir)
	}
}

func TestResolveProjectRootNoConfigKeepsRawPath(t *testing.T) {
	// When --project points to a directory with no gale.toml
	// and no .tool-versions anywhere up the tree, keep the
	// raw path as-is.
	dir := t.TempDir()
	got := resolveProjectRoot(dir)
	if got != dir {
		t.Errorf("resolveProjectRoot(%q) = %q, want %q",
			dir, got, dir)
	}
}

func TestSyncIfNeededWarnsOnBadConfig(t *testing.T) {
	// When gale.toml exists but contains invalid TOML,
	// syncIfNeeded must write a warning rather than
	// silently swallowing the error.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(configPath,
		[]byte("this is not valid toml {{{\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Change to the directory so syncIfNeeded finds
	// the config.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	os.Chdir(dir)

	var buf bytes.Buffer
	syncIfNeeded(&buf, "")

	if buf.Len() == 0 {
		t.Error("syncIfNeeded should warn on invalid " +
			"config, got no output")
	}
}
