package main

import "testing"

// verifyTestDigest is a well-formed OCI manifest digest for URI
// construction assertions.
const verifyTestDigest = "sha256:" +
	"2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"

// TestVerifyOCIURIUsesDigest pins that when a manifest digest is
// available, the verify URI pins the manifest by digest instead of
// by tag. Digest pinning verifies the exact artifact installed,
// immune to tag moves on GHCR.
func TestVerifyOCIURIUsesDigest(t *testing.T) {
	got := verifyOCIURI(
		"kelp/gale-recipes", "jq", "1.8.1-4",
		"darwin-arm64", verifyTestDigest,
	)
	want := "oci://ghcr.io/kelp/gale-recipes/jq@" + verifyTestDigest
	if got != want {
		t.Errorf("verifyOCIURI with digest = %q, want %q", got, want)
	}
}

// TestVerifyOCIURIFallsBackToTag pins the no-digest fallback: the
// tag form gale-recipes CI pushes, "<bareVersion>-<platform>". The
// canonical lockfile version "1.8.1-4" carries a revision; GHCR
// tags use the bare "1.8.1", so the revision must be stripped.
func TestVerifyOCIURIFallsBackToTag(t *testing.T) {
	got := verifyOCIURI(
		"kelp/gale-recipes", "jq", "1.8.1-4", "darwin-arm64", "",
	)
	want := "oci://ghcr.io/kelp/gale-recipes/jq:1.8.1-darwin-arm64"
	if got != want {
		t.Errorf("verifyOCIURI without digest = %q, want %q", got, want)
	}
}
