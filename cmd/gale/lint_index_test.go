package main

import (
	"strings"
	"testing"
)

const (
	lintIndexSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	lintIndexDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func lintIndexTOML() string {
	return `[package]
name = "just"
description = "Save and run project-specific commands"
license = "CC0-1.0"
homepage = "https://github.com/casey/just"
repo = "casey/just"
latest = "1.56.0"

[versions."1.56.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"
format = "tar.gz"
sha256 = "` + lintIndexSHA + `"
tree_digest = "` + lintIndexDigest + `"
hash_source = "upstream-sha256sums"
strip = 1

[[versions."1.56.0".artifacts."darwin/arm64".files]]
src = "just"
dest = "bin/just"
mode = 0o755
`
}

func TestLintIndexDocumentOK(t *testing.T) {
	path := writeLintRecipe(t, "just.toml", lintIndexTOML())
	if err := runLintCmd(t, path, false); err != nil {
		t.Fatalf("gale lint index: %v", err)
	}
}

func TestLintIndexMissingTreeDigestFails(t *testing.T) {
	raw := strings.Replace(lintIndexTOML(),
		`tree_digest = "`+lintIndexDigest+`"`,
		`tree_digest = ""`, 1)
	path := writeLintRecipe(t, "just.toml", raw)
	err := runLintCmd(t, path, false)
	if err == nil {
		t.Fatal("gale lint index: want error, got nil")
	}
	out := captureStderr(t, func() {
		_ = runLintCmd(t, path, false)
	})
	if !strings.Contains(out, "tree_digest") {
		t.Fatalf("error should name tree_digest, got %q", out)
	}
}

func TestLintIndexStemMismatchFails(t *testing.T) {
	path := writeLintRecipe(t, "evil.toml", lintIndexTOML())
	if err := runLintCmd(t, path, false); err == nil {
		t.Fatal("gale lint stem mismatch: want error, got nil")
	}
}

func TestLintRecipeWithVersionsCommentStaysRecipe(t *testing.T) {
	raw := lintCleanRecipe + "\n# index uses [versions.\"1.56.0\"] tables\n"
	path := writeLintRecipe(t, "j/jq.toml", raw)
	err := runLintCmd(t, path, false)
	if err == nil {
		t.Fatal("source recipe: want not-an-index error, got nil")
	}
	if !strings.Contains(err.Error(), "not an index document") {
		t.Fatalf("source recipe: %v, want not an index document", err)
	}
}

func TestLintIndexBaseRejectsAddedPlatform(t *testing.T) {
	old := writeLintRecipe(t, "old/just.toml", lintIndexTOML())
	newer := lintIndexTOML() + `
[versions."1.56.0".artifacts."linux/amd64"]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-x86_64-unknown-linux-musl.tar.gz"
format = "tar.gz"
sha256 = "` + lintIndexSHA + `"
tree_digest = "` + lintIndexDigest + `"
hash_source = "upstream-sha256sums"
strip = 1

[[versions."1.56.0".artifacts."linux/amd64".files]]
src = "just"
dest = "bin/just"
mode = 0o755
`
	path := writeLintRecipe(t, "just.toml", newer)
	err := runLintWithBase(t, old, path)
	if err == nil {
		t.Fatal("lint --base added platform: want error, got nil")
	}
}

func TestLintIndexBaseAllowsNewVersion(t *testing.T) {
	old := writeLintRecipe(t, "old/just.toml", lintIndexTOML())
	newer := lintIndexTOML() + `
[versions."1.57.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.57.0/just-1.57.0-aarch64-apple-darwin.tar.gz"
format = "tar.gz"
sha256 = "` + lintIndexSHA + `"
tree_digest = "` + lintIndexDigest + `"
hash_source = "upstream-sha256sums"
strip = 1

[[versions."1.57.0".artifacts."darwin/arm64".files]]
src = "just"
dest = "bin/just"
mode = 0o755
`
	path := writeLintRecipe(t, "just.toml", newer)
	if err := runLintWithBase(t, old, path); err != nil {
		t.Fatalf("lint --base new version: %v", err)
	}
}

func TestLintIndexBaseMalformedReportsParseError(t *testing.T) {
	old := writeLintRecipe(t, "old/just.toml", "this is not toml [")
	path := writeLintRecipe(t, "just.toml", lintIndexTOML())
	err := runLintWithBase(t, old, path)
	if err == nil {
		t.Fatal("lint --base malformed: want error, got nil")
	}
	got := err.Error()
	if strings.Contains(got, "requires index documents") {
		t.Fatalf("masked TOML error as wrong type: %v", err)
	}
	if !strings.Contains(got, "parsing") && !strings.Contains(got, "decode") {
		t.Fatalf("error should name parse failure, got %v", err)
	}
}

func TestLintBaseRequiresOneFile(t *testing.T) {
	a := writeLintRecipe(t, "just.toml", lintIndexTOML())
	b := writeLintRecipe(t, "other/just.toml", lintIndexTOML())
	resetLintFlags(t)
	if err := lintCmd.Flags().Set("base", a); err != nil {
		t.Fatalf("setting --base: %v", err)
	}
	if err := lintCmd.RunE(lintCmd, []string{a, b}); err == nil {
		t.Fatal("lint --base with two files: want error, got nil")
	}
}

func runLintWithBase(t *testing.T, base, path string) error {
	t.Helper()
	resetLintFlags(t)
	if err := lintCmd.Flags().Set("base", base); err != nil {
		t.Fatalf("setting --base: %v", err)
	}
	return lintCmd.RunE(lintCmd, []string{path})
}

func resetLintFlags(t *testing.T) {
	t.Helper()
	if flag := lintCmd.Flags().Lookup("base"); flag != nil {
		if err := lintCmd.Flags().Set("base", ""); err != nil {
			t.Fatalf("reset --base: %v", err)
		}
	}
	t.Cleanup(func() {
		if flag := lintCmd.Flags().Lookup("base"); flag != nil {
			_ = lintCmd.Flags().Set("base", "")
		}
	})
}
