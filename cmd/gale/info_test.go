package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/index"
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

func withIsolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(home, "empty")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return home
}

func infoIndexSrc(t *testing.T, files map[string]string) (index.Source, *int) {
	t.Helper()
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			count++
			body, ok := files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, body)
		},
	))
	t.Cleanup(srv.Close)
	return index.Source{
		BaseURL: srv.URL,
		Commit:  lockFetchPinA,
		HTTP:    srv.Client(),
	}, &count
}

func TestInfoParsesAtVersion(t *testing.T) {
	withIsolatedHome(t)
	doc := lockIndexTOML("testpkg", "1.0.0", false)
	src, _ := infoIndexSrc(t, map[string]string{
		"/" + lockFetchPinA + "/index/t/testpkg.toml": doc,
	})

	var buf bytes.Buffer
	if err := runInfo(context.Background(), &buf, src, "testpkg@1.0.0"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1.0.0") {
		t.Errorf("output missing 1.0.0:\n%s", out)
	}
}

func TestInfoRejectsInvalidName(t *testing.T) {
	withIsolatedHome(t)
	src, count := infoIndexSrc(t, map[string]string{})

	bad := []string{
		"jq?foo=bar", "%2e%2e/etc", "jq/sub", "../etc",
		"jq with space", "JQ", "-jq",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			err := runInfo(context.Background(), &buf, src, name)
			if err == nil {
				t.Fatalf("expected validation error for %q", name)
			}
		})
	}
	if *count != 0 {
		t.Errorf("invalid names hit the index %d times", *count)
	}
}

func TestInfoWritesThroughCmdStdout(t *testing.T) {
	withIsolatedHome(t)
	src, _ := infoIndexSrc(t, map[string]string{
		"/" + lockFetchPinA + "/index/t/testpkg.toml": lockIndexTOML("testpkg", "1.0.0", false),
	})

	var buf bytes.Buffer
	if err := runInfo(context.Background(), &buf, src, "testpkg"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "testpkg") {
		t.Errorf("output missing 'testpkg':\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("output contains ANSI escapes when writing to "+
			"non-TTY buffer:\n%q", out)
	}
}

func TestInfoMakesOneRequest(t *testing.T) {
	withIsolatedHome(t)
	src, count := infoIndexSrc(t, map[string]string{
		"/" + lockFetchPinA + "/index/t/testpkg.toml": lockIndexTOML("testpkg", "1.0.0", false),
	})

	var buf bytes.Buffer
	if err := runInfo(context.Background(), &buf, src, "testpkg"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	if *count != 1 {
		t.Errorf("HTTP request count = %d, want 1", *count)
	}
}

func TestInfoInstalledFromConfig(t *testing.T) {
	src, count := infoIndexSrc(t, map[string]string{})
	home := withIsolatedHome(t)

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
	if err := runInfo(context.Background(), &buf, src, "testpkg"); err != nil {
		t.Fatalf("runInfo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "testpkg") || !strings.Contains(out, "1.0.0") {
		t.Errorf("output missing testpkg/1.0.0:\n%s", out)
	}
	if *count != 0 {
		t.Errorf("installed lookup hit the index %d times", *count)
	}
}

func TestInfoOmitsPinnedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(path,
		[]byte("[packages]\njq = \"1.7.0\"\n\n[pinned]\njq = true\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	found, err := printConfigInfo(&buf, "jq", path, "project")
	if err != nil {
		t.Fatalf("printConfigInfo: %v", err)
	}
	if !found {
		t.Fatal("expected jq in gale.toml")
	}
	if strings.Contains(buf.String(), "Pinned:") {
		t.Errorf("info must not print Pinned:, got:\n%s", buf.String())
	}
}

func TestInfoHasIndexFlag(t *testing.T) {
	if infoCmd.Flags().Lookup("index") == nil {
		t.Fatal("info --index is missing")
	}
}
