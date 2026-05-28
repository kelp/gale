package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/registry"
)

func TestInfoCommandRegistered(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "info" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'info' command")
	}
}

// withTestRegistry points the default registry at the given URL
// for the duration of the test. Restores the previous override
// on cleanup.
func withTestRegistry(t *testing.T, url string) {
	t.Helper()
	prev := registryOverride
	registryOverride = func() *registry.Registry {
		return registry.NewWithURL(url)
	}
	t.Cleanup(func() { registryOverride = prev })
}

// withIsolatedHome points HOME at a temp dir so info doesn't
// see the developer's real ~/.gale config.
func withIsolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Run from an empty directory so no project config is picked
	// up either.
	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return home
}

const infoTestRecipe = `[package]
name = "testpkg"
version = "1.0.0"
description = "Test package"
license = "MIT"
homepage = "https://example.com"

[source]
url = "https://example.com/testpkg-1.0.0.tar.gz"
sha256 = "deadbeef"
`

const infoTestRecipeV2 = `[package]
name = "testpkg"
version = "2.0.0"
description = "Test package v2"
license = "MIT"
homepage = "https://example.com"

[source]
url = "https://example.com/testpkg-2.0.0.tar.gz"
sha256 = "cafef00d"
`

// TestInfoParsesAtVersion confirms that `gale info testpkg@1.0.0`
// resolves the version via FetchRecipeVersion instead of treating
// "testpkg@1.0.0" as the literal recipe name.
//
// Reproduces: audit/readonly/bad-input/findings/0001-info-no-version-parsing.md
func TestInfoParsesAtVersion(t *testing.T) {
	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.0.0 " + commit + "\n2.0.0 " +
		"9876543210abcdef9876543210abcdef98765432\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/t/testpkg.versions":
				fmt.Fprint(w, versionsBody)
			case "/" + commit + "/recipes/t/testpkg.toml":
				fmt.Fprint(w, infoTestRecipe)
			case "/recipes/t/testpkg.toml":
				fmt.Fprint(w, infoTestRecipeV2)
			default:
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	withIsolatedHome(t)
	withTestRegistry(t, srv.URL)

	var buf bytes.Buffer
	if err := runInfo(&buf, "testpkg@1.0.0"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1.0.0") {
		t.Errorf("output missing 1.0.0:\n%s", out)
	}
	if strings.Contains(out, "2.0.0") {
		t.Errorf("output contains v2 metadata, expected pinned 1.0.0:\n%s", out)
	}
}

// TestInfoRejectsInvalidName confirms that registry-side
// validation surfaces through `info` as a clear error before any
// HTTP request goes out.
//
// Reproduces: audit/readonly/bad-input/findings/0002-recipe-name-injected-into-url.md
func TestInfoRejectsInvalidName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected HTTP request for bad name: %s", r.URL.Path)
		},
	))
	defer srv.Close()

	withIsolatedHome(t)
	withTestRegistry(t, srv.URL)

	bad := []string{
		"jq?foo=bar", "%2e%2e/etc", "jq/sub", "../etc",
		"jq with space", "JQ", "-jq", "jq@", "",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			err := runInfo(&buf, name)
			if err == nil {
				t.Fatalf("expected validation error for %q", name)
			}
		})
	}
}

// TestInfoWritesThroughCmdStdout confirms info writes through
// the provided writer so tests (and future quiet/no-color modes)
// can capture and gate output.
//
// Reproduces: audit/readonly/tty-vs-nontty/findings/0002-info-bypasses-tty-and-color-mode.md
func TestInfoWritesThroughCmdStdout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/recipes/t/testpkg.toml" {
				fmt.Fprint(w, infoTestRecipe)
				return
			}
			http.NotFound(w, r)
		},
	))
	defer srv.Close()

	withIsolatedHome(t)
	withTestRegistry(t, srv.URL)

	var buf bytes.Buffer
	if err := runInfo(&buf, "testpkg"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "testpkg") {
		t.Errorf("output missing 'testpkg':\n%s", out)
	}
	// No ANSI escape sequences should appear when writing to a
	// non-TTY io.Writer (a bytes.Buffer).
	if strings.Contains(out, "\x1b[") {
		t.Errorf("output contains ANSI escapes when writing to "+
			"non-TTY buffer:\n%q", out)
	}
}

// TestInfoMakesOneRequest confirms the registry-not-installed
// branch of info issues a single HTTP request — the legacy code
// made an extra .binaries.toml roundtrip every invocation.
//
// Reproduces: audit/readonly/network-perf/findings/0005-info-binaries-extra-roundtrip.md
func TestInfoMakesOneRequest(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			count++
			if r.URL.Path == "/recipes/t/testpkg.toml" {
				fmt.Fprint(w, infoTestRecipe)
				return
			}
			http.NotFound(w, r)
		},
	))
	defer srv.Close()

	withIsolatedHome(t)
	withTestRegistry(t, srv.URL)

	var buf bytes.Buffer
	if err := runInfo(&buf, "testpkg"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	if count != 1 {
		t.Errorf("HTTP request count = %d, want 1", count)
	}
}

// TestInfoInstalledFromConfig confirms that `info <pkg>` for a
// package already declared in gale.toml prints config metadata
// without calling the registry at all.
func TestInfoInstalledFromConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected HTTP request: %s", r.URL.Path)
		},
	))
	defer srv.Close()

	home := withIsolatedHome(t)
	withTestRegistry(t, srv.URL)

	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.toml"),
		[]byte(`[packages]
testpkg = "1.0.0"
`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runInfo(&buf, "testpkg"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "testpkg") || !strings.Contains(out, "1.0.0") {
		t.Errorf("output missing testpkg/1.0.0:\n%s", out)
	}
	// Silence the unused import linter if config isn't used.
	_ = config.CurrentHost
}
