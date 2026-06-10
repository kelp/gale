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
