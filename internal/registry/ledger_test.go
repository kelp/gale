package registry

import (
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// A revision-5 recipe whose head matches the ledger's latest entry.
const ledgerRecipeRev5 = `[package]
name = "jq"
version = "1.8.1"
revision = 5
description = "JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://example.com/jq-1.8.1.tar.gz"
sha256 = "abc123"
`

// A ref-tip .binaries.toml carrying a [[history]] ledger. The head
// entry (1.8.1-5) covers darwin-arm64, linux-amd64, and linux-arm64.
const ledgerBinariesToml = `version = "1.8.1-5"

[darwin-arm64]
sha256 = "13ee22e3d3a77d25d89cd1a8d7e4d4f8d37cbfa230313f0c1e865fcbff17b089"
manifest_digest = "sha256:c58a902b972e03ba83c1fe66af2dbb53a24b1d71da14dc089783d9ba2442658b"

[[history]]
version = "1.7.1-1"
darwin-arm64 = { sha256 = "1111111111111111111111111111111111111111111111111111111111111111", manifest_digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111" }

[[history]]
version = "1.8.1-5"
darwin-arm64 = { sha256 = "13ee22e3d3a77d25d89cd1a8d7e4d4f8d37cbfa230313f0c1e865fcbff17b089", manifest_digest = "sha256:c58a902b972e03ba83c1fe66af2dbb53a24b1d71da14dc089783d9ba2442658b" }
linux-amd64 = { sha256 = "4a7ddc31de1c4b8330565d1dbf671bd8f60867dde02b40bd04f455bc55d74788", manifest_digest = "sha256:9f35d79850663818a8be0eca27bb9680af73b3c6a79d08f17c49d5f336bc4ac0" }
linux-arm64 = { sha256 = "62a2c004ef2ed6f2c17cf94e61598f82c717a79b3f648392a5f467fee2b0e4da", manifest_digest = "sha256:148c92fbecb0938286cb1a46791de4a7cf230b0bbda90fe0fd4719577a2ef0ef" }
`

// ledgerPlatform returns a platform present in the ledger fixture
// so the test asserts a binary regardless of host arch.
func ledgerPlatform() string {
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "linux-amd64":
		return "linux-amd64"
	case "linux-arm64":
		return "linux-arm64"
	default:
		return "darwin-arm64"
	}
}

// FetchRecipe resolves the latest version from the [[history]]
// ledger and ignores .versions when the ledger is present. The
// decoy commit in .versions must not be consulted.
func TestFetchRecipeUsesLedger(t *testing.T) {
	decoyCommit := "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.8.1-5 " + decoyCommit + "\n"

	srv := httptest.NewServer(fileHandler(map[string]string{
		"/recipes/j/jq.versions":      versionsBody,
		"/recipes/j/jq.toml":          ledgerRecipeRev5,
		"/recipes/j/jq.binaries.toml": ledgerBinariesToml,
		// Deliberately do NOT serve the decoy commit's files; if the
		// ledger path works, they are never requested.
	}))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Full() != "1.8.1-5" {
		t.Fatalf("version = %q, want 1.8.1-5", rec.Package.Full())
	}
	b, ok := rec.Binary[ledgerPlatform()]
	if !ok {
		t.Fatalf("no binary for %s", ledgerPlatform())
	}
	if b.ManifestDigest == "" {
		t.Error("ManifestDigest empty, want it set from the ledger")
	}
	if !strings.HasPrefix(b.URL, "https://ghcr.io/v2/") {
		t.Errorf("URL = %q, want a GHCR blob URL", b.URL)
	}
}

// When the ref-tip .binaries.toml has no [[history]] ledger,
// FetchRecipe falls through to the legacy .versions commit-pin path.
func TestFetchRecipeFallsThroughWithoutLedger(t *testing.T) {
	commit := "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.8.1 " + commit + "\n"

	srv := httptest.NewServer(fileHandler(map[string]string{
		"/recipes/j/jq.versions":                     versionsBody,
		"/recipes/j/jq.toml":                         recipeNoBinaries,
		"/recipes/j/jq.binaries.toml":                binariesToml, // no [[history]]
		"/" + commit + "/recipes/j/jq.toml":          recipeNoBinaries,
		"/" + commit + "/recipes/j/jq.binaries.toml": binariesToml,
	}))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Version != "1.8.1" {
		t.Fatalf("version = %q, want 1.8.1", rec.Package.Version)
	}
	if len(rec.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2 (legacy path)", len(rec.Binary))
	}
}

// When no .versions index is served and the ref-tip ledger head's
// version mismatches the ref-tip recipe, FetchRecipe must fall back to
// the flat head section so the recipe keeps its prebuilt binaries
// instead of degrading to a source build.
func TestFetchRecipeLedgerMismatchUsesFlat(t *testing.T) {
	// rev-1 recipe at version 1.8.1; the flat head matches it, but the
	// ledger head (1.8.1-2) does not (rev-1 accepts only 1.8.1/1.8.1-1).
	mismatchBinaries := `version = "1.8.1"

[darwin-arm64]
sha256 = "13ee22e3d3a77d25d89cd1a8d7e4d4f8d37cbfa230313f0c1e865fcbff17b089"
manifest_digest = "sha256:c58a902b972e03ba83c1fe66af2dbb53a24b1d71da14dc089783d9ba2442658b"

[linux-amd64]
sha256 = "4a7ddc31de1c4b8330565d1dbf671bd8f60867dde02b40bd04f455bc55d74788"
manifest_digest = "sha256:9f35d79850663818a8be0eca27bb9680af73b3c6a79d08f17c49d5f336bc4ac0"

[linux-arm64]
sha256 = "62a2c004ef2ed6f2c17cf94e61598f82c717a79b3f648392a5f467fee2b0e4da"
manifest_digest = "sha256:148c92fbecb0938286cb1a46791de4a7cf230b0bbda90fe0fd4719577a2ef0ef"

[[history]]
version = "1.8.1-2"
darwin-arm64 = { sha256 = "1111111111111111111111111111111111111111111111111111111111111111" }
linux-amd64 = { sha256 = "2222222222222222222222222222222222222222222222222222222222222222" }
linux-arm64 = { sha256 = "3333333333333333333333333333333333333333333333333333333333333333" }
`

	srv := httptest.NewServer(fileHandler(map[string]string{
		// recipeNoBinaries is a rev-1 recipe at version 1.8.1.
		"/recipes/j/jq.toml":          recipeNoBinaries,
		"/recipes/j/jq.binaries.toml": mismatchBinaries,
		// No .versions served: fetchLatestPinned returns
		// errNoVersionIndex, so the ref-tip recipe must carry binaries.
	}))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) == 0 {
		t.Fatalf("Binary count = 0, want flat-head binaries (degraded " +
			"to source build because ledger head mismatched?)")
	}
	b, ok := rec.Binary[ledgerPlatform()]
	if !ok {
		t.Fatalf("no binary for %s", ledgerPlatform())
	}
	// Must be the flat-head sha, not the ledger sha (1111.../2222.../3333...).
	if strings.HasPrefix(b.SHA256, "1111") ||
		strings.HasPrefix(b.SHA256, "2222") ||
		strings.HasPrefix(b.SHA256, "3333") {
		t.Errorf("SHA256 = %q, want flat-head sha (used mismatched ledger?)",
			b.SHA256)
	}
}

// FetchRecipeVersion resolves the head version via the ledger.
func TestFetchRecipeVersionLedgerHead(t *testing.T) {
	srv := httptest.NewServer(fileHandler(map[string]string{
		"/recipes/j/jq.toml":          ledgerRecipeRev5,
		"/recipes/j/jq.binaries.toml": ledgerBinariesToml,
		// No .versions served: head must resolve from the ledger.
	}))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipeVersion("jq", "1.8.1-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := rec.Binary[ledgerPlatform()]; !ok {
		t.Fatalf("no binary for %s", ledgerPlatform())
	}
}

// A historical version (not the ledger head) is resolved via the
// .versions commit-pin path, fetching the recipe at the pinned
// commit — NOT the ref-tip recipe.
func TestFetchRecipeVersionHistoricalUsesVersions(t *testing.T) {
	commit := "fedcba9876543210fedcba9876543210fedcba98"
	versionsBody := "1.7.1-1 " + commit + "\n1.8.1-5 " +
		"abc1234def5678901234567890abcdef12345678\n"

	// Pinned commit serves the historical recipe (version 1.7.1).
	histRecipe := strings.Replace(recipeNoBinaries,
		`version = "1.8.1"`, `version = "1.7.1"`, 1)

	srv := httptest.NewServer(fileHandler(map[string]string{
		"/recipes/j/jq.versions":            versionsBody,
		"/recipes/j/jq.toml":                ledgerRecipeRev5,
		"/recipes/j/jq.binaries.toml":       ledgerBinariesToml,
		"/" + commit + "/recipes/j/jq.toml": histRecipe,
		// no binaries at the pinned commit → source build, fine here.
	}))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipeVersion("jq", "1.7.1-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Version != "1.7.1" {
		t.Fatalf("version = %q, want 1.7.1 (used ref-tip recipe instead "+
			"of the pinned commit?)", rec.Package.Version)
	}
}
