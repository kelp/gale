package lockfile

import "testing"

// Tests for the flat pre-enforcement schema, which is read-only:
// decodeLegacy, LockFile.Stale, and VersionMatches. The reader,
// writer and file-level staleness check that used to live beside
// them (Read, Write, IsStale) were culled with gh#197 — Load is the
// reader, WriteV1 the writer, and lockIsStale the staleness check.

// --- decodeLegacy ---

const validLockTOML = `[packages.jq]
version = "1.7.1"
sha256 = "abc123"

[packages.ripgrep]
version = "14.1.0"
sha256 = "def456"
`

func TestDecodeLegacyParsesPackages(t *testing.T) {
	lf, err := decodeLegacy([]byte(validLockTOML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lf.Packages) != 2 {
		t.Fatalf("got %d packages, want 2", len(lf.Packages))
	}
	if lf.Packages["jq"].Version != "1.7.1" {
		t.Errorf("jq version = %q, want 1.7.1",
			lf.Packages["jq"].Version)
	}
	if lf.Packages["jq"].SHA256 != "abc123" {
		t.Errorf("jq sha256 = %q, want abc123",
			lf.Packages["jq"].SHA256)
	}
	if lf.Packages["ripgrep"].Version != "14.1.0" {
		t.Errorf("ripgrep version = %q, want 14.1.0",
			lf.Packages["ripgrep"].Version)
	}
}

func TestDecodeLegacyMalformedTOMLErrors(t *testing.T) {
	if _, err := decodeLegacy([]byte("not [valid toml")); err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

// TestDecodeLegacyNoPackagesSection pins the empty-map guarantee the
// doc comment makes: callers write into Packages without checking.
func TestDecodeLegacyNoPackagesSection(t *testing.T) {
	lf, err := decodeLegacy([]byte("# empty lock file\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lf.Packages == nil {
		t.Fatal("Packages map should be initialized")
	}
	if len(lf.Packages) != 0 {
		t.Errorf("got %d packages, want 0", len(lf.Packages))
	}
}

func TestDecodeLegacyIgnoresUnknownFields(t *testing.T) {
	const content = `[packages.jq]
version = "1.7.1"
sha256 = "abc123"
unknown_field = "should be ignored"
`
	lf, err := decodeLegacy([]byte(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lf.Packages["jq"].Version != "1.7.1" {
		t.Errorf("version = %q, want 1.7.1", lf.Packages["jq"].Version)
	}
	if lf.Packages["jq"].SHA256 != "abc123" {
		t.Errorf("sha256 = %q, want abc123", lf.Packages["jq"].SHA256)
	}
}

// --- manifest digest: the third field the flat schema carries ---

const testManifestDigest = "sha256:" +
	"a3f1c2d4e5b6978800112233445566778899aabbccddeeff0011223344556677"

func TestDecodeLegacyManifestDigest(t *testing.T) {
	const content = `[packages.jq]
version = "1.7.1"
sha256 = "abc123"
manifest_digest = "` + testManifestDigest + `"
`
	lf, err := decodeLegacy([]byte(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := lf.Packages["jq"].ManifestDigest; got != testManifestDigest {
		t.Errorf("manifest digest = %q, want %q", got, testManifestDigest)
	}
}

func TestDecodeLegacyWithoutManifestDigest(t *testing.T) {
	const content = `[packages.jq]
version = "1.7.1"
sha256 = "abc123"
`
	lf, err := decodeLegacy([]byte(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := lf.Packages["jq"].ManifestDigest; got != "" {
		t.Errorf("manifest digest = %q, want empty", got)
	}
	if lf.Packages["jq"].Version != "1.7.1" {
		t.Errorf("version = %q, want 1.7.1", lf.Packages["jq"].Version)
	}
}

// TestLoadReadsTheLegacySchema is the one file-level case, kept
// because a decoder test alone would not prove Load still classifies
// a flat file as KindLegacy and hands back a populated LockFile.
func TestLoadReadsTheLegacySchema(t *testing.T) {
	path := writeTemp(t, validLockTOML)

	v, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.Kind != KindLegacy {
		t.Fatalf("kind = %v, want %v", v.Kind, KindLegacy)
	}
	if v.Legacy.Packages["jq"].Version != "1.7.1" {
		t.Errorf("jq version = %q, want 1.7.1",
			v.Legacy.Packages["jq"].Version)
	}
}

// --- LockFile.Stale ---

func TestLockFileStale(t *testing.T) {
	tests := []struct {
		name      string
		locked    map[string]LockedPackage
		declared  map[string]string
		wantStale bool
	}{
		{
			name:      "in sync",
			locked:    map[string]LockedPackage{"jq": {Version: "1.7.1"}},
			declared:  map[string]string{"jq": "1.7.1"},
			wantStale: false,
		},
		{
			name:      "version differs",
			locked:    map[string]LockedPackage{"jq": {Version: "1.7.1"}},
			declared:  map[string]string{"jq": "1.8.0"},
			wantStale: true,
		},
		{
			name:   "gale.toml declares an extra package",
			locked: map[string]LockedPackage{"jq": {Version: "1.7.1"}},
			declared: map[string]string{
				"jq": "1.7.1", "ripgrep": "14.1.0",
			},
			wantStale: true,
		},
		{
			name: "the lock holds an extra package",
			locked: map[string]LockedPackage{
				"jq":      {Version: "1.7.1"},
				"ripgrep": {Version: "14.1.0"},
			},
			declared:  map[string]string{"jq": "1.7.1"},
			wantStale: true,
		},
		{
			name:      "declared package missing from the lock",
			locked:    map[string]LockedPackage{"ripgrep": {Version: "14.1.0"}},
			declared:  map[string]string{"jq": "1.7.1"},
			wantStale: true,
		},
		{
			// The normal state: install and update write the canonical
			// form through r.Package.Full(), gale.toml carries the bare
			// version. Reading these as different would make every
			// direnv `cd` sync a project that is already synced
			// (finding 0006).
			name:      "canonical lock against a bare manifest pin",
			locked:    map[string]LockedPackage{"jq": {Version: "1.8.1-1"}},
			declared:  map[string]string{"jq": "1.8.1"},
			wantStale: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lf := &LockFile{Packages: tt.locked}
			if got := lf.Stale(tt.declared); got != tt.wantStale {
				t.Errorf("Stale = %v, want %v", got, tt.wantStale)
			}
		})
	}
}

// --- VersionMatches ---

// TestVersionMatchesSurvivesTheCull is a guard, not a repro. It
// passes today and passed before this commit; its job is to fail
// loudly if a later change finishes gh#197's literal instruction.
//
// The issue asks for `LockedPackage` and `IsStale` to be culled and
// says only that `VersionMatches` must survive. Taking the first half
// literally reaches this function, because `LockedPackage` is what
// `LockFile.Stale` compares and `Stale` is VersionMatches' caller
// inside this package. Deleting it is silent: nothing fails to
// compile in a world where its callers went with it, and the user
// sees a `jq = "1.7"` → `"1.8"` pin edit leave direnv reporting a
// fresh lock — the exact silent failure gh#197 opens by naming.
//
// Five live callers, all verified against main at the time of the
// cull:
//
//   - internal/lockfile/lockfile.go, LockFile.Stale — the legacy
//     schema's comparison, reached from cmd/gale/lockstale.go for
//     KindLegacy on every direnv `cd`.
//   - internal/lockfile/roots.go, CheckDeclared — the enforced
//     schema's comparison, the same `cd` for KindV1.
//   - internal/lockwrite/lockwrite.go, the request's roots check.
//   - internal/lockwrite/lockwrite.go, the carry-forward agreement
//     check.
//   - cmd/gale/storereplace.go, legacySHA — a legacy lock's byte
//     claim keyed by a version spelled bare where the query's is
//     canonical.
func TestVersionMatchesSurvivesTheCull(t *testing.T) {
	tests := []struct {
		name   string
		locked string
		toml   string
		want   bool
	}{
		{
			// The bare-vs-canonical reconciliation itself: gale.toml
			// records the bare version so an entry tracks revision
			// bumps, the lock records the version-revision it resolved
			// to.
			name:   "canonical revision against a bare pin",
			locked: "1.8.1-2",
			toml:   "1.8.1",
			want:   true,
		},
		{
			// Revision 0 is not a canonical revision, so "-0" is part
			// of the version string and not a suffix to reconcile away.
			name:   "revision zero is not canonical",
			locked: "1.8.1-0",
			toml:   "1.8.1",
			want:   false,
		},
		{
			// The pin edit. This row is why the function cannot go:
			// answering true here is what makes a changed pin invisible.
			name:   "edited pin",
			locked: "1.8",
			toml:   "1.7",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VersionMatches(tt.locked, tt.toml); got != tt.want {
				t.Errorf("VersionMatches(%q, %q) = %v, want %v",
					tt.locked, tt.toml, got, tt.want)
			}
		})
	}
}
