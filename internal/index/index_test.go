package index

import (
	"strings"
	"testing"
)

const (
	goldenSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	goldenDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func goldenTOML() string {
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
sha256 = "` + goldenSHA + `"
tree_digest = "` + goldenDigest + `"
hash_source = "upstream-sha256sums"
strip = 1
attestation = true

[[versions."1.56.0".artifacts."darwin/arm64".files]]
src = "just"
dest = "bin/just"
mode = 0o755
`
}

func TestParseReadsProposalShape(t *testing.T) {
	f, err := Parse([]byte(goldenTOML()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Package.Name != "just" {
		t.Errorf("name = %q, want just", f.Package.Name)
	}
	if f.Package.Latest != "1.56.0" {
		t.Errorf("latest = %q, want 1.56.0", f.Package.Latest)
	}
	ver, ok := f.Versions["1.56.0"]
	if !ok {
		t.Fatal("version 1.56.0 missing")
	}
	art, ok := ver.Artifacts["darwin/arm64"]
	if !ok {
		t.Fatal("darwin/arm64 missing")
	}
	if art.Format != "tar.gz" {
		t.Errorf("format = %q, want tar.gz", art.Format)
	}
	if art.SHA256 != goldenSHA {
		t.Errorf("sha256 = %q", art.SHA256)
	}
	if art.TreeDigest != goldenDigest {
		t.Errorf("tree_digest = %q", art.TreeDigest)
	}
	if art.HashSource != "upstream-sha256sums" {
		t.Errorf("hash_source = %q", art.HashSource)
	}
	if art.Strip != 1 {
		t.Errorf("strip = %d, want 1", art.Strip)
	}
	if art.Attestation == nil || !*art.Attestation {
		t.Fatal("attestation: want pointer to true")
	}
	if len(art.Files) != 1 || art.Files[0].Src != "just" ||
		art.Files[0].Dest != "bin/just" || art.Files[0].Mode != 0o755 {
		t.Errorf("files = %+v", art.Files)
	}
}

func TestParseAbsentAttestationIsNil(t *testing.T) {
	raw := strings.Replace(goldenTOML(), "attestation = true\n", "", 1)
	f, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	art := f.Versions["1.56.0"].Artifacts["darwin/arm64"]
	if art.Attestation != nil {
		t.Fatalf("attestation = %v, want nil", art.Attestation)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	raw := goldenTOML() + "\nextra = 1\n"
	if _, err := Parse([]byte(raw)); err == nil {
		t.Fatal("Parse: want error for unknown field")
	}
}

func TestParseRejectsEmptyDocument(t *testing.T) {
	if _, err := Parse([]byte("")); err == nil {
		t.Fatal("Parse: want error for empty document")
	}
}

func TestParseRejectsMissingVersions(t *testing.T) {
	raw := `[package]
name = "just"
latest = "1.56.0"
`
	if _, err := Parse([]byte(raw)); err == nil {
		t.Fatal("Parse: want error for missing versions")
	}
}
