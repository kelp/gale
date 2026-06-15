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

// The mispin rescue must consult the [[history]] ledger, not just the
// flat head section. Scenario: .versions pins the latest version X
// (1.8.1) at commit C; recipe@C is version X but ships NO binary at C
// (the @commit .binaries.toml is absent — a mispin). The ref-tip recipe
// is AHEAD (2.0.0) and the ref-tip .binaries.toml's flat head is at yet
// another version (1.9.0, so flat won't match X), but its [[history]]
// ledger carries an entry matching X. A flat-only mispin rescue misses
// the ledger and degrades to a source build; the ledger-aware rescue
// finds X's entry and keeps the prebuilt binary.
func TestFetchRecipeMispinRescueConsultsLedger(t *testing.T) {
	commit := "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.8.1 " + commit + "\n"

	// recipe@C is version 1.8.1 (= X). It has no inline binaries, and
	// no .binaries.toml is served at the commit — the mispin.
	pinnedRecipe := recipeNoBinaries // version 1.8.1

	// Ref-tip recipe is ahead at 2.0.0, so fetchLatestRefTip can't
	// satisfy it from the ref-tip binaries and FetchRecipe proceeds to
	// the .versions pinned path.
	tipRecipe := strings.Replace(recipeNoBinaries,
		`version = "1.8.1"`, `version = "2.0.0"`, 1)

	// Ref-tip .binaries.toml: flat head at 1.9.0 (matches neither the
	// 2.0.0 ref-tip recipe nor X=1.8.1). The ledger carries a 1.8.1
	// entry covering all three platforms — this is what the rescue must
	// find.
	tipBinaries := `version = "1.9.0"

[darwin-arm64]
sha256 = "9999999999999999999999999999999999999999999999999999999999999999"

[linux-amd64]
sha256 = "9999999999999999999999999999999999999999999999999999999999999999"

[[history]]
version = "1.8.1"
darwin-arm64 = { sha256 = "13ee22e3d3a77d25d89cd1a8d7e4d4f8d37cbfa230313f0c1e865fcbff17b089", manifest_digest = "sha256:c58a902b972e03ba83c1fe66af2dbb53a24b1d71da14dc089783d9ba2442658b" }
linux-amd64 = { sha256 = "4a7ddc31de1c4b8330565d1dbf671bd8f60867dde02b40bd04f455bc55d74788", manifest_digest = "sha256:9f35d79850663818a8be0eca27bb9680af73b3c6a79d08f17c49d5f336bc4ac0" }
linux-arm64 = { sha256 = "62a2c004ef2ed6f2c17cf94e61598f82c717a79b3f648392a5f467fee2b0e4da", manifest_digest = "sha256:148c92fbecb0938286cb1a46791de4a7cf230b0bbda90fe0fd4719577a2ef0ef" }

[[history]]
version = "1.9.0"
darwin-arm64 = { sha256 = "9999999999999999999999999999999999999999999999999999999999999999" }
linux-amd64 = { sha256 = "9999999999999999999999999999999999999999999999999999999999999999" }
`

	srv := httptest.NewServer(fileHandler(map[string]string{
		"/recipes/j/jq.versions":            versionsBody,
		"/recipes/j/jq.toml":                tipRecipe,
		"/recipes/j/jq.binaries.toml":       tipBinaries,
		"/" + commit + "/recipes/j/jq.toml": pinnedRecipe,
		// No /<commit>/recipes/j/jq.binaries.toml — the mispin.
	}))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Version != "1.8.1" {
		t.Fatalf("version = %q, want 1.8.1 (the pinned X)", rec.Package.Version)
	}
	b, ok := rec.Binary[ledgerPlatform()]
	if !ok {
		t.Fatalf("no binary for %s — mispin rescue ignored the ledger "+
			"and degraded to a source build", ledgerPlatform())
	}
	// Must be the 1.8.1 ledger sha, not the 1.9.0 flat-head sha (9999...).
	if strings.HasPrefix(b.SHA256, "9999") {
		t.Errorf("SHA256 = %q, want the 1.8.1 ledger sha (used flat head?)",
			b.SHA256)
	}
}

// A ref-tip recipe that LAGS the ledger head: version 1.7.1 rev 3, while
// the ref-tip .binaries.toml ledger's newest entry is 1.8.1-5. The non-head
// 1.7.1-3 entry matches this recipe via binaryIndexMatchesRecipe.
const staleRefTipRecipe = `[package]
name = "jq"
version = "1.7.1"
revision = 3
description = "JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://example.com/jq-1.7.1.tar.gz"
sha256 = "abc123"
`

// A ref-tip .binaries.toml whose [[history]] ledger HEAD (1.8.1-5) is newer
// than the ref-tip recipe (1.7.1-3). The stale 1.7.1-3 entry carries the
// "7171..." sha so the test can tell the stale binary apart from the head's
// "8181..." sha. The flat head is at 1.7.1 so it does not rescue 1.8.1.
const staleRefTipBinaries = `version = "1.7.1"

[darwin-arm64]
sha256 = "7171717171717171717171717171717171717171717171717171717171717171"
manifest_digest = "sha256:7171717171717171717171717171717171717171717171717171717171717171"

[[history]]
version = "1.7.1-3"
darwin-arm64 = { sha256 = "7171717171717171717171717171717171717171717171717171717171717171", manifest_digest = "sha256:7171717171717171717171717171717171717171717171717171717171717171" }
linux-amd64 = { sha256 = "7171717171717171717171717171717171717171717171717171717171717171", manifest_digest = "sha256:7171717171717171717171717171717171717171717171717171717171717171" }
linux-arm64 = { sha256 = "7171717171717171717171717171717171717171717171717171717171717171", manifest_digest = "sha256:7171717171717171717171717171717171717171717171717171717171717171" }

[[history]]
version = "1.8.1-5"
darwin-arm64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181", manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181" }
linux-amd64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181", manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181" }
linux-arm64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181", manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181" }
`

// The recipe served at the .versions-pinned commit: jq 1.8.1 rev 5, the
// real latest. Its .binaries.toml carries the matching 1.8.1-5 binary.
const pinnedRecipe181 = `[package]
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

const pinnedBinaries181 = `version = "1.8.1-5"

[darwin-arm64]
sha256 = "8181818181818181818181818181818181818181818181818181818181818181"
manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181"

[linux-amd64]
sha256 = "8181818181818181818181818181818181818181818181818181818181818181"
manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181"

[linux-arm64]
sha256 = "8181818181818181818181818181818181818181818181818181818181818181"
manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181"

[[history]]
version = "1.8.1-5"
darwin-arm64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181" }
linux-amd64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181" }
linux-arm64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181" }
`

// When the ref-tip recipe LAGS the ledger head, FetchRecipe must defer to
// the .versions commit pin. fetchLatestRefTip's MergeBinariesForRecipe
// matches the non-head 1.7.1-3 entry (it matches the ref-tip recipe's own
// version) and reports ledgerOK=true, but the ledger is authoritative only
// when its HEAD matches the ref-tip recipe. Here the head is 1.8.1-5, so the
// resolver must consult .versions and return 1.8.1, not the stale 1.7.1.
func TestFetchRecipeStaleRefTipDefersToVersions(t *testing.T) {
	commit := "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.8.1-5 " + commit + "\n"

	srv := httptest.NewServer(fileHandler(map[string]string{
		"/recipes/j/jq.versions":                     versionsBody,
		"/recipes/j/jq.toml":                         staleRefTipRecipe,
		"/recipes/j/jq.binaries.toml":                staleRefTipBinaries,
		"/" + commit + "/recipes/j/jq.toml":          pinnedRecipe181,
		"/" + commit + "/recipes/j/jq.binaries.toml": pinnedBinaries181,
	}))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Version != "1.8.1" {
		t.Fatalf("version = %q, want 1.8.1 (trusted stale non-head ledger "+
			"match instead of deferring to .versions?)", rec.Package.Version)
	}
	b, ok := rec.Binary[ledgerPlatform()]
	if !ok {
		t.Fatalf("no binary for %s", ledgerPlatform())
	}
	// Must be the 1.8.1 sha (8181...), not the stale 1.7.1 sha (7171...).
	if strings.HasPrefix(b.SHA256, "7171") {
		t.Errorf("SHA256 = %q, want the 1.8.1 sha (used stale ledger entry?)",
			b.SHA256)
	}
}

// A ref-tip recipe that LAGS the ledger head AND carries inline [binary]
// entries: version 1.7.1 rev 1, while the ref-tip ledger head is 1.8.1-5.
// The inline binaries (the "7171..." stale shas) survive fetchRecipe and
// must NOT be mistaken for a head match when a pinned install resolves to
// the ledger head (1.8.1).
const inlineRefTipRecipe = `[package]
name = "jq"
version = "1.7.1"
revision = 1
description = "JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://example.com/jq-1.7.1.tar.gz"
sha256 = "abc123"

[binary.darwin-arm64]
url = "https://example.com/jq-1.7.1-darwin-arm64"
sha256 = "7171717171717171717171717171717171717171717171717171717171717171"

[binary.linux-amd64]
url = "https://example.com/jq-1.7.1-linux-amd64"
sha256 = "7171717171717171717171717171717171717171717171717171717171717171"

[binary.linux-arm64]
url = "https://example.com/jq-1.7.1-linux-arm64"
sha256 = "7171717171717171717171717171717171717171717171717171717171717171"
`

// A ref-tip .binaries.toml whose [[history]] ledger HEAD (1.8.1-5) is newer
// than the inline ref-tip recipe (1.7.1-1). The head carries the "8181..."
// shas. MergeBinariesFromHistory is a no-op against the 1.7.1 recipe, so the
// inline "7171..." binaries are all that survive on the ledger path.
const inlineRefTipBinaries = `version = "1.8.1-5"

[darwin-arm64]
sha256 = "8181818181818181818181818181818181818181818181818181818181818181"
manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181"

[[history]]
version = "1.8.1-5"
darwin-arm64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181", manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181" }
linux-amd64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181", manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181" }
linux-arm64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181", manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181" }
`

// The recipe served at the .versions-pinned commit: jq 1.8.1 rev 5 with NO
// inline binary. Its .binaries.toml carries the matching 1.8.1-5 head.
const inlinePinnedBinaries181 = `version = "1.8.1-5"

[darwin-arm64]
sha256 = "8181818181818181818181818181818181818181818181818181818181818181"
manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181"

[linux-amd64]
sha256 = "8181818181818181818181818181818181818181818181818181818181818181"
manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181"

[linux-arm64]
sha256 = "8181818181818181818181818181818181818181818181818181818181818181"
manifest_digest = "sha256:8181818181818181818181818181818181818181818181818181818181818181"

[[history]]
version = "1.8.1-5"
darwin-arm64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181" }
linux-amd64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181" }
linux-arm64 = { sha256 = "8181818181818181818181818181818181818181818181818181818181818181" }
`

// When a pinned install resolves to the ledger HEAD (1.8.1) but the ref-tip
// recipe LAGS it (1.7.1) and carries inline [binary] entries, fetchVersionFromLedger
// must NOT return the stale ref-tip recipe. MergeBinariesFromHistory is a no-op
// (head 1.8.1-5 mismatches the 1.7.1 recipe), but the inline "7171..." binaries
// leave rec.Binary non-empty and defeat the len==0 guard. The resolver must
// reject the head mismatch and fall through to the .versions commit pin, which
// fetches the historically-correct 1.8.1 head recipe and its "8181..." binary.
func TestFetchRecipeVersionInlineRefTipDefersToVersions(t *testing.T) {
	commit := "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.8.1-5 " + commit + "\n"

	srv := httptest.NewServer(fileHandler(map[string]string{
		"/recipes/j/jq.versions":                     versionsBody,
		"/recipes/j/jq.toml":                         inlineRefTipRecipe,
		"/recipes/j/jq.binaries.toml":                inlineRefTipBinaries,
		"/" + commit + "/recipes/j/jq.toml":          pinnedRecipe181,
		"/" + commit + "/recipes/j/jq.binaries.toml": inlinePinnedBinaries181,
	}))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipeVersion("jq", "1.8.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Version != "1.8.1" {
		t.Fatalf("version = %q, want 1.8.1 (returned stale ref-tip inline "+
			"recipe instead of deferring to .versions?)", rec.Package.Version)
	}
	b, ok := rec.Binary[ledgerPlatform()]
	if !ok {
		t.Fatalf("no binary for %s", ledgerPlatform())
	}
	// Must be the 1.8.1 head sha (8181...), not the stale inline sha (7171...).
	if strings.HasPrefix(b.SHA256, "7171") {
		t.Errorf("SHA256 = %q, want the 1.8.1 head sha (returned stale "+
			"inline ref-tip binary?)", b.SHA256)
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
