package main

import (
	"os"
	"path/filepath"
	"testing"
)

// lintCleanRecipe is a source-era recipe used to prove
// gale lint no longer accepts non-index documents.
const lintCleanRecipe = `
[package]
name = "jq"
version = "1.8.1"
description = "Lightweight JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
repo = "jqlang/jq"
url = "https://github.com/jqlang/jq/releases/download/jq-1.8.1/jq-1.8.1.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"

[build]
steps = [
  "./configure --prefix=${PREFIX}",
  "make -j${JOBS}",
  "make install",
]
`

func runLintCmd(t *testing.T, path string, _ bool) error {
	t.Helper()
	resetLintFlags(t)
	return lintCmd.RunE(lintCmd, []string{path})
}

func writeLintRecipe(t *testing.T, rel, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating recipe dir: %v", err)
	}
	if err := os.WriteFile(
		path, []byte(data), 0o644,
	); err != nil {
		t.Fatalf("writing test recipe: %v", err)
	}
	return path
}
