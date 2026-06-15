package recipe

import (
	"testing"
)

const validBinariesTOML = `version = "1.8.1"

[darwin-arm64]
sha256 = "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"

[linux-amd64]
sha256 = "a903b0ca428c174e611ad78ee6508fefeab7a8b2eb60e55b554280679b2c07c6"
`

// --- Behavior 1: ParseBinaryIndex parses valid file ---

func TestParseBinaryIndexVersion(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.Version != "1.8.1" {
		t.Errorf("Version = %q, want %q", idx.Version, "1.8.1")
	}
}

func TestParseBinaryIndexPlatforms(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.Platforms) != 2 {
		t.Fatalf("Platforms count = %d, want 2",
			len(idx.Platforms))
	}
}

func TestParseBinaryIndexDarwinHash(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"
	if idx.Platforms["darwin-arm64"] != want {
		t.Errorf("darwin-arm64 = %q, want %q",
			idx.Platforms["darwin-arm64"], want)
	}
}

func TestParseBinaryIndexLinuxHash(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "a903b0ca428c174e611ad78ee6508fefeab7a8b2eb60e55b554280679b2c07c6"
	if idx.Platforms["linux-amd64"] != want {
		t.Errorf("linux-amd64 = %q, want %q",
			idx.Platforms["linux-amd64"], want)
	}
}

// --- Behavior 2: ParseBinaryIndex handles empty input ---

func TestParseBinaryIndexEmpty(t *testing.T) {
	idx, err := ParseBinaryIndex("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.Version != "" {
		t.Errorf("Version = %q, want empty", idx.Version)
	}
	if len(idx.Platforms) != 0 {
		t.Errorf("Platforms count = %d, want 0",
			len(idx.Platforms))
	}
}

// --- Behavior 3: MergeBinaries populates recipe binary map ---

func TestMergeBinariesPopulatesMap(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1"},
	}
	idx := &BinaryIndex{
		Version: "1.8.1",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
			"linux-amd64":  "def456",
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	if len(r.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2", len(r.Binary))
	}
}

func TestMergeBinariesCorrectURL(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1"},
	}
	idx := &BinaryIndex{
		Version: "1.8.1",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	b, ok := r.Binary["darwin-arm64"]
	if !ok {
		t.Fatal("missing binary for darwin-arm64")
	}
	want := "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc123"
	if b.URL != want {
		t.Errorf("URL = %q, want %q", b.URL, want)
	}
}

func TestMergeBinariesCorrectSHA256(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1"},
	}
	idx := &BinaryIndex{
		Version: "1.8.1",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	b := r.Binary["darwin-arm64"]
	if b.SHA256 != "abc123" {
		t.Errorf("SHA256 = %q, want %q", b.SHA256, "abc123")
	}
}

// --- Behavior 4: MergeBinaries skips stale index ---

func TestMergeBinariesSkipsStaleVersion(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1"},
	}
	idx := &BinaryIndex{
		Version: "1.7.1", // stale — doesn't match recipe
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	if len(r.Binary) != 0 {
		t.Errorf("Binary count = %d, want 0 (stale index)",
			len(r.Binary))
	}
}

func TestMergeBinariesRevisionOneAcceptsBareVersion(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1", Revision: 1},
	}
	idx := &BinaryIndex{
		Version: "1.8.1",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	if len(r.Binary) != 1 {
		t.Fatalf("Binary count = %d, want 1", len(r.Binary))
	}
}

func TestMergeBinariesRevisionedRecipeRejectsBareVersion(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1", Revision: 2},
	}
	idx := &BinaryIndex{
		Version: "1.8.1",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	if len(r.Binary) != 0 {
		t.Fatalf("Binary count = %d, want 0", len(r.Binary))
	}
}

func TestMergeBinariesRevisionedRecipeAcceptsFullVersion(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1", Revision: 2},
	}
	idx := &BinaryIndex{
		Version: "1.8.1-2",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	if len(r.Binary) != 1 {
		t.Fatalf("Binary count = %d, want 1", len(r.Binary))
	}
}

// --- Behavior 5: MergeBinaries with nil index is a no-op ---

func TestMergeBinariesNilIndex(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1"},
	}
	MergeBinaries(r, nil, "kelp/gale-recipes")

	if len(r.Binary) != 0 {
		t.Errorf("Binary count = %d, want 0", len(r.Binary))
	}
}

// --- Behavior 6 (C4): .binaries.toml surfaces per-platform dep closure ---

// A .binaries.toml may carry a `deps` array-of-tables under each
// platform section recording the exact (name, version, revision)
// closure the prebuilt was linked against. This is informational
// — the archive's own .gale-deps.toml remains the authoritative
// record at install time — but exposing it at the registry layer
// lets `gale info` and audit tooling inspect closures without
// fetching and extracting the tarball. Old gale ignores the field.
const validBinariesTOMLWithDeps = `version = "2.53.0-2"

[darwin-arm64]
sha256 = "abc123"
deps = [
  { name = "curl", version = "8.11.0", revision = 1 },
  { name = "openssl", version = "3.5.4", revision = 2 },
]

[linux-amd64]
sha256 = "def456"
deps = [
  { name = "curl", version = "8.11.0", revision = 1 },
  { name = "openssl", version = "3.5.4", revision = 2 },
]
`

func TestParseBinaryIndexWithDepsParses(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOMLWithDeps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.Version != "2.53.0-2" {
		t.Errorf("Version = %q, want %q", idx.Version, "2.53.0-2")
	}
	if got := idx.Platforms["darwin-arm64"]; got != "abc123" {
		t.Errorf("darwin-arm64 sha = %q, want %q", got, "abc123")
	}
}

func TestParseBinaryIndexExtractsDeps(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOMLWithDeps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.Deps) != 2 {
		t.Fatalf("Deps platform count = %d, want 2", len(idx.Deps))
	}
	darwin := idx.Deps["darwin-arm64"]
	if len(darwin) != 2 {
		t.Fatalf("darwin deps count = %d, want 2", len(darwin))
	}
	if darwin[0].Name != "curl" || darwin[0].Version != "8.11.0" || darwin[0].Revision != 1 {
		t.Errorf("darwin[0] = %+v, want {curl 8.11.0 1}", darwin[0])
	}
	if darwin[1].Name != "openssl" || darwin[1].Version != "3.5.4" || darwin[1].Revision != 2 {
		t.Errorf("darwin[1] = %+v, want {openssl 3.5.4 2}", darwin[1])
	}
}

// A .binaries.toml without a `deps` field is still valid —
// backward compat with every file written before this change.
func TestParseBinaryIndexWithoutDepsParses(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.Deps) != 0 {
		t.Errorf("Deps = %v, want empty", idx.Deps)
	}
}

// --- Behavior 7: .binaries.toml surfaces per-platform manifest digest ---

// A .binaries.toml may carry a `manifest_digest` key under each
// platform section recording the OCI manifest digest CI observed
// when pushing the prebuilt. Informational like `deps`: malformed
// values are dropped silently, never failing the parse.
const validBinariesTOMLWithDigest = `version = "1.8.1"

[darwin-arm64]
sha256 = "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"
manifest_digest = "sha256:abababababababababababababababababababababababababababababababab"

[linux-amd64]
sha256 = "a903b0ca428c174e611ad78ee6508fefeab7a8b2eb60e55b554280679b2c07c6"
manifest_digest = "sha256:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
`

func TestParseBinaryIndexExtractsManifestDigest(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOMLWithDigest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantDarwin := "sha256:abababababababababababababababababababababababababababababababab"
	if got := idx.Digests["darwin-arm64"]; got != wantDarwin {
		t.Errorf("darwin-arm64 digest = %q, want %q",
			got, wantDarwin)
	}
	wantLinux := "sha256:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	if got := idx.Digests["linux-amd64"]; got != wantLinux {
		t.Errorf("linux-amd64 digest = %q, want %q",
			got, wantLinux)
	}
}

// A .binaries.toml without manifest_digest is still valid —
// backward compat with every file written before this change.
func TestParseBinaryIndexWithoutManifestDigest(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.Digests) != 0 {
		t.Errorf("Digests = %v, want empty", idx.Digests)
	}
}

// Malformed digests are dropped silently; the platform's sha256
// is still parsed. A valid digest is exactly "sha256:" followed
// by 64 lowercase hex characters.
const malformedDigestBinariesTOML = `version = "1.8.1"

[darwin-arm64]
sha256 = "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"
manifest_digest = "abababababababababababababababababababababababababababababababab"

[linux-amd64]
sha256 = "a903b0ca428c174e611ad78ee6508fefeab7a8b2eb60e55b554280679b2c07c6"
manifest_digest = "sha256:abababababababababababababababababababababababababababababababa"

[linux-arm64]
sha256 = "1111111111111111111111111111111111111111111111111111111111111111"
manifest_digest = "sha256:abababababababababababababababababababababababababababababababag"

[darwin-amd64]
sha256 = "2222222222222222222222222222222222222222222222222222222222222222"
manifest_digest = "sha256:ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB"
`

func TestParseBinaryIndexDropsMalformedDigest(t *testing.T) {
	idx, err := ParseBinaryIndex(malformedDigestBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cases := []struct {
		platform string
		reason   string
	}{
		{"darwin-arm64", "missing sha256: prefix"},
		{"linux-amd64", "63 hex chars"},
		{"linux-arm64", "non-hex character"},
		{"darwin-amd64", "uppercase hex"},
	}
	for _, c := range cases {
		if got, ok := idx.Digests[c.platform]; ok {
			t.Errorf("Digests[%q] = %q, want absent (%s)",
				c.platform, got, c.reason)
		}
	}
	if len(idx.Platforms) != 4 {
		t.Errorf("Platforms count = %d, want 4",
			len(idx.Platforms))
	}
}

// --- Behavior 8: MergeBinaries carries the manifest digest ---

func TestMergeBinariesSetsManifestDigest(t *testing.T) {
	digest := "sha256:efefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefef"
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1"},
	}
	idx := &BinaryIndex{
		Version: "1.8.1",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
		},
		Digests: map[string]string{
			"darwin-arm64": digest,
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	b, ok := r.Binary["darwin-arm64"]
	if !ok {
		t.Fatal("missing binary for darwin-arm64")
	}
	if b.ManifestDigest != digest {
		t.Errorf("ManifestDigest = %q, want %q",
			b.ManifestDigest, digest)
	}
}

func TestMergeBinariesEmptyManifestDigestWhenAbsent(t *testing.T) {
	r := &Recipe{
		Package: Package{Name: "jq", Version: "1.8.1"},
	}
	idx := &BinaryIndex{
		Version: "1.8.1",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
		},
	}
	MergeBinaries(r, idx, "kelp/gale-recipes")

	b, ok := r.Binary["darwin-arm64"]
	if !ok {
		t.Fatal("missing binary for darwin-arm64")
	}
	if b.ManifestDigest != "" {
		t.Errorf("ManifestDigest = %q, want empty",
			b.ManifestDigest)
	}
	wantURL := "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc123"
	if b.URL != wantURL {
		t.Errorf("URL = %q, want %q", b.URL, wantURL)
	}
	if b.SHA256 != "abc123" {
		t.Errorf("SHA256 = %q, want %q", b.SHA256, "abc123")
	}
	if b.Trust != TrustSigstore {
		t.Errorf("Trust = %q, want %q", b.Trust, TrustSigstore)
	}
}

// --- Behavior 11: MergeBinariesFromHistory populates from a ledger entry ---

func TestMergeBinariesFromHistoryPopulates(t *testing.T) {
	digest := "sha256:efefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefef"
	r := &Recipe{Package: Package{Name: "jq", Version: "1.8.1", Revision: 5}}
	entry := BinaryHistoryEntry{
		Version: "1.8.1-5",
		Platforms: map[string]string{
			"darwin-arm64": "abc123",
			"linux-amd64":  "def456",
		},
		Digests: map[string]string{
			"darwin-arm64": digest,
		},
	}
	MergeBinariesFromHistory(r, entry, "kelp/gale-recipes")

	if len(r.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2", len(r.Binary))
	}
	b := r.Binary["darwin-arm64"]
	if b.URL != "https://ghcr.io/v2/kelp/gale-recipes/jq/blobs/sha256:abc123" {
		t.Errorf("URL = %q", b.URL)
	}
	if b.SHA256 != "abc123" {
		t.Errorf("SHA256 = %q, want abc123", b.SHA256)
	}
	if b.ManifestDigest != digest {
		t.Errorf("ManifestDigest = %q, want %q", b.ManifestDigest, digest)
	}
	if b.Trust != TrustSigstore {
		t.Errorf("Trust = %q, want %q", b.Trust, TrustSigstore)
	}
	// A platform without a digest still merges, with empty digest.
	if r.Binary["linux-amd64"].ManifestDigest != "" {
		t.Errorf("linux-amd64 digest = %q, want empty",
			r.Binary["linux-amd64"].ManifestDigest)
	}
}

// A revisioned recipe requires the full <version>-<revision> on the
// ledger entry, matching binaryIndexMatchesRecipe.
func TestMergeBinariesFromHistoryRejectsVersionMismatch(t *testing.T) {
	r := &Recipe{Package: Package{Name: "jq", Version: "1.8.1", Revision: 5}}
	entry := BinaryHistoryEntry{
		Version:   "1.8.1-2", // wrong revision
		Platforms: map[string]string{"darwin-arm64": "abc123"},
	}
	MergeBinariesFromHistory(r, entry, "kelp/gale-recipes")
	if len(r.Binary) != 0 {
		t.Errorf("Binary count = %d, want 0 (version mismatch)", len(r.Binary))
	}
}

// --- Behavior 12: MergeBinariesPreferLedger picks ledger over flat ---

func TestMergeBinariesPreferLedgerUsesHistory(t *testing.T) {
	r := &Recipe{Package: Package{Name: "jq", Version: "1.8.1", Revision: 5}}
	idx := &BinaryIndex{
		Version:   "1.8.1-5",
		Platforms: map[string]string{"darwin-arm64": "flatsha"},
		History: []BinaryHistoryEntry{
			{
				Version:   "1.8.1-5",
				Platforms: map[string]string{"darwin-arm64": "ledgersha"},
			},
		},
	}
	used := MergeBinariesPreferLedger(r, idx, "kelp/gale-recipes")
	if !used {
		t.Error("used = false, want true (ledger present)")
	}
	if r.Binary["darwin-arm64"].SHA256 != "ledgersha" {
		t.Errorf("SHA256 = %q, want ledgersha (flat shadowed ledger?)",
			r.Binary["darwin-arm64"].SHA256)
	}
}

func TestMergeBinariesPreferLedgerFallsBackToFlat(t *testing.T) {
	r := &Recipe{Package: Package{Name: "jq", Version: "1.8.1"}}
	idx := &BinaryIndex{
		Version:   "1.8.1",
		Platforms: map[string]string{"darwin-arm64": "flatsha"},
	}
	used := MergeBinariesPreferLedger(r, idx, "kelp/gale-recipes")
	if used {
		t.Error("used = true, want false (no ledger)")
	}
	if r.Binary["darwin-arm64"].SHA256 != "flatsha" {
		t.Errorf("SHA256 = %q, want flatsha", r.Binary["darwin-arm64"].SHA256)
	}
}

func TestMergeBinariesPreferLedgerNil(t *testing.T) {
	r := &Recipe{Package: Package{Name: "jq", Version: "1.8.1"}}
	if MergeBinariesPreferLedger(r, nil, "kelp/gale-recipes") {
		t.Error("used = true for nil index, want false")
	}
}

// --- Behavior 9: ParseBinaryIndex reads the [[history]] ledger ---

// A .binaries.toml carries an append-only [[history]] ledger: one
// table per published <version>-<revision>, each with an inline
// table per platform recording {sha256, manifest_digest}. This is
// the registry-side source of truth for installable versions that
// replaces the .versions commit-pin file (gh#121). The flat head
// section is retained for older gale clients.
const validBinariesTOMLWithHistory = `version = "1.8.1-5"

[darwin-arm64]
sha256 = "13ee22e3d3a77d25d89cd1a8d7e4d4f8d37cbfa230313f0c1e865fcbff17b089"
manifest_digest = "sha256:c58a902b972e03ba83c1fe66af2dbb53a24b1d71da14dc089783d9ba2442658b"

[linux-amd64]
sha256 = "4a7ddc31de1c4b8330565d1dbf671bd8f60867dde02b40bd04f455bc55d74788"
manifest_digest = "sha256:9f35d79850663818a8be0eca27bb9680af73b3c6a79d08f17c49d5f336bc4ac0"

[[history]]
version = "1.8.1-5"
darwin-arm64 = { sha256 = "13ee22e3d3a77d25d89cd1a8d7e4d4f8d37cbfa230313f0c1e865fcbff17b089", manifest_digest = "sha256:c58a902b972e03ba83c1fe66af2dbb53a24b1d71da14dc089783d9ba2442658b" }
linux-amd64 = { sha256 = "4a7ddc31de1c4b8330565d1dbf671bd8f60867dde02b40bd04f455bc55d74788", manifest_digest = "sha256:9f35d79850663818a8be0eca27bb9680af73b3c6a79d08f17c49d5f336bc4ac0" }
linux-arm64 = { sha256 = "62a2c004ef2ed6f2c17cf94e61598f82c717a79b3f648392a5f467fee2b0e4da", manifest_digest = "sha256:148c92fbecb0938286cb1a46791de4a7cf230b0bbda90fe0fd4719577a2ef0ef" }
`

func TestParseBinaryIndexHistoryLength(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOMLWithHistory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.History) != 1 {
		t.Fatalf("History count = %d, want 1", len(idx.History))
	}
}

func TestParseBinaryIndexHistoryEntry(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOMLWithHistory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	e := idx.History[0]
	if e.Version != "1.8.1-5" {
		t.Errorf("Version = %q, want %q", e.Version, "1.8.1-5")
	}
	if len(e.Platforms) != 3 {
		t.Fatalf("Platforms count = %d, want 3", len(e.Platforms))
	}
	wantSHA := "62a2c004ef2ed6f2c17cf94e61598f82c717a79b3f648392a5f467fee2b0e4da"
	if got := e.Platforms["linux-arm64"]; got != wantSHA {
		t.Errorf("linux-arm64 sha = %q, want %q", got, wantSHA)
	}
	wantDigest := "sha256:148c92fbecb0938286cb1a46791de4a7cf230b0bbda90fe0fd4719577a2ef0ef"
	if got := e.Digests["linux-arm64"]; got != wantDigest {
		t.Errorf("linux-arm64 digest = %q, want %q", got, wantDigest)
	}
}

// A .binaries.toml without [[history]] yields a nil History slice —
// the flat head section still parses, so existing clients are
// unaffected.
func TestParseBinaryIndexNoHistory(t *testing.T) {
	idx, err := ParseBinaryIndex(validBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.History != nil {
		t.Errorf("History = %v, want nil", idx.History)
	}
}

// A history entry whose platform table lacks sha256, or carries a
// malformed manifest_digest, degrades gracefully: the platform sha
// is dropped when absent and the bad digest is dropped, but the
// parse never fails — mirroring the flat-section leniency.
const malformedHistoryBinariesTOML = `version = "2.0.0-1"

[[history]]
version = "2.0.0-1"
darwin-arm64 = { sha256 = "1111111111111111111111111111111111111111111111111111111111111111", manifest_digest = "not-a-digest" }
linux-amd64 = { manifest_digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222" }
linux-arm64 = { sha256 = "3333333333333333333333333333333333333333333333333333333333333333", manifest_digest = "sha256:3333333333333333333333333333333333333333333333333333333333333333" }
`

// --- Behavior 10: PickHistoryLatest selects the newest entry ---

// A multi-entry, multi-revision ledger locks in the total order
// ahead of the ledger accumulating real history.
const multiEntryHistoryTOML = `version = "1.8.1-5"

[[history]]
version = "1.7.1"
linux-amd64 = { sha256 = "1111111111111111111111111111111111111111111111111111111111111111" }

[[history]]
version = "1.8.1-2"
linux-amd64 = { sha256 = "2222222222222222222222222222222222222222222222222222222222222222" }

[[history]]
version = "1.8.1-5"
linux-amd64 = { sha256 = "5555555555555555555555555555555555555555555555555555555555555555" }

[[history]]
version = "1.8.0"
linux-amd64 = { sha256 = "0000000000000000000000000000000000000000000000000000000000000000" }
`

func TestPickHistoryLatest(t *testing.T) {
	idx, err := ParseBinaryIndex(multiEntryHistoryTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry, ok := idx.PickHistoryLatest()
	if !ok {
		t.Fatal("PickHistoryLatest ok = false")
	}
	if entry.Version != "1.8.1-5" {
		t.Errorf("Version = %q, want %q", entry.Version, "1.8.1-5")
	}
}

func TestPickHistoryLatestEmpty(t *testing.T) {
	idx := &BinaryIndex{}
	if _, ok := idx.PickHistoryLatest(); ok {
		t.Error("PickHistoryLatest on empty ledger ok = true, want false")
	}
}

func TestParseBinaryIndexHistoryDropsMalformed(t *testing.T) {
	idx, err := ParseBinaryIndex(malformedHistoryBinariesTOML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.History) != 1 {
		t.Fatalf("History count = %d, want 1", len(idx.History))
	}
	e := idx.History[0]
	// darwin-arm64: valid sha, malformed digest dropped.
	if got := e.Platforms["darwin-arm64"]; got == "" {
		t.Error("darwin-arm64 sha should be retained")
	}
	if _, ok := e.Digests["darwin-arm64"]; ok {
		t.Error("darwin-arm64 malformed digest should be dropped")
	}
	// linux-amd64: no sha — platform omitted entirely.
	if _, ok := e.Platforms["linux-amd64"]; ok {
		t.Error("linux-amd64 has no sha256, should be omitted")
	}
	// linux-arm64: fully valid.
	if got := e.Platforms["linux-arm64"]; got == "" {
		t.Error("linux-arm64 sha should be retained")
	}
	if _, ok := e.Digests["linux-arm64"]; !ok {
		t.Error("linux-arm64 digest should be retained")
	}
}
