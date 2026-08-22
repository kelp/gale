package lockfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// v2Guard is the mandatory v2 downgrade guard, written verbatim.
// It is kept separate from the fixture so the guard tests can
// remove or corrupt exactly these bytes.
const v2Guard = `[packages."!gale-lock-v2"]
version = 2
`

// v2Fixture is a hand-written v2 lockfile. It pins the on-disk key
// names the fetch schema will write: a rename here would break
// every later writer and every committed v2 lock.
const v2Fixture = "version = 2\n\n" + v2Guard + `
[targets.default]
roots = ["just@1.56.0"]

[targets.host."ci-*,build-*"]
roots = ["just@1.56.0", "jq@1.8.1"]

[packages."just@1.56.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"
format = "tar.gz"
sha256 = "aaaa"
tree_digest = "bbbb"
method = "fetch"
strip = 1
hash_source = "upstream-sha256sums"
index_commit = "deadbeef"

[packages."just@1.56.0".artifacts."darwin/arm64".attestation]
issuer = "https://token.actions.githubusercontent.com"
san = "https://github.com/casey/just/.github/workflows/release.yml@refs/tags/1.56.0"
repo = "casey/just"

[[packages."just@1.56.0".artifacts."darwin/arm64".files]]
src = "just"
dest = "bin/just"
mode = 0o755

[[packages."just@1.56.0".artifacts."darwin/arm64".files]]
src = "just.1"
dest = "man/man1/just.1"
mode = 0o644
`

func TestReadV2ParsesOnDiskKeys(t *testing.T) {
	lf, err := ReadV2(writeTemp(t, v2Fixture))
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}

	if lf.Version != 2 {
		t.Errorf("Version = %d, want 2", lf.Version)
	}
	wantDefault := []string{"just@1.56.0"}
	if lf.Targets.Default == nil {
		t.Fatal("default target missing")
	}
	if !reflect.DeepEqual(lf.Targets.Default.Roots, wantDefault) {
		t.Errorf("default roots = %v, want %v", lf.Targets.Default.Roots, wantDefault)
	}
	wildcard, ok := lf.Targets.Host["ci-*,build-*"]
	if !ok {
		t.Fatalf("wildcard selector missing, have %v", lf.Targets.Host)
	}
	if !reflect.DeepEqual(wildcard.Roots, []string{"just@1.56.0", "jq@1.8.1"}) {
		t.Errorf("wildcard roots = %v, want [just@1.56.0 jq@1.8.1]", wildcard.Roots)
	}

	pkg, ok := lf.Packages["just@1.56.0"]
	if !ok {
		t.Fatalf("package node missing, have %v", lf.Packages)
	}
	if _, ok := lf.Packages["just@1.56.0-1"]; ok {
		t.Error("v2 key was rewritten to a revisioned identity")
	}
	darwin, ok := pkg.Artifacts["darwin/arm64"]
	if !ok {
		t.Fatalf("darwin artifact missing, have %v", pkg.Artifacts)
	}
	if darwin.Attestation == nil {
		t.Fatal("attestation missing")
	}
	want := V2Artifact{
		URL:         "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz",
		Format:      "tar.gz",
		SHA256:      "aaaa",
		TreeDigest:  "bbbb",
		Method:      "fetch",
		Strip:       1,
		HashSource:  "upstream-sha256sums",
		IndexCommit: "deadbeef",
		Attestation: &V2Attestation{
			Issuer: "https://token.actions.githubusercontent.com",
			SAN:    "https://github.com/casey/just/.github/workflows/release.yml@refs/tags/1.56.0",
			Repo:   "casey/just",
		},
		Files: []V2File{
			{Src: "just", Dest: "bin/just", Mode: 0o755},
			{Src: "just.1", Dest: "man/man1/just.1", Mode: 0o644},
		},
	}
	if !reflect.DeepEqual(darwin, want) {
		t.Errorf("darwin artifact = %#v, want %#v", darwin, want)
	}
	if darwin.Files[0].Mode != 493 {
		t.Errorf("decoded mode = %d, want 493 (0o755)", darwin.Files[0].Mode)
	}
}

func TestReadV2MissingFile(t *testing.T) {
	_, err := ReadV2(filepath.Join(t.TempDir(), "absent.lock"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestReadV2DanglingSymlinkIsNotMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.Symlink(filepath.Join(dir, "gone.lock"), path); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := ReadV2(path)
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, which callers read as 'no lock'", err)
	}
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed", err)
	}
}

func TestReadV2LegacySchema(t *testing.T) {
	legacy := "[packages.jq]\nversion = \"1.8.1-2\"\nsha256 = \"aaaa\"\n"
	_, err := ReadV2(writeTemp(t, legacy))
	if !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("err = %v, want ErrLegacySchema", err)
	}
	if errors.Is(err, ErrUnknownVersion) {
		t.Error("legacy schema must not also report ErrUnknownVersion")
	}
}

func TestReadV2UnknownVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		found   string
	}{
		{name: "v1", content: v1Fixture, found: "1"},
		{name: "future", content: "version = 99\n", found: "99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadV2(writeTemp(t, tt.content))
			if !errors.Is(err, ErrUnknownVersion) {
				t.Fatalf("err = %v, want ErrUnknownVersion", err)
			}
			if !strings.Contains(err.Error(), tt.found) {
				t.Errorf("error does not name the version found: %v", err)
			}
			if errors.Is(err, ErrLegacySchema) {
				t.Error("unknown version must not also report ErrLegacySchema")
			}
		})
	}
}

func TestReadV2RejectsUnknownField(t *testing.T) {
	tests := []struct {
		name    string
		content string
		key     string
	}{
		{
			name:    "top level",
			content: "version = 2\nstray = true\n" + v2Fixture[len("version = 2\n"):],
			key:     "stray",
		},
		{
			name: "targets default",
			content: strings.Replace(v2Fixture,
				`roots = ["just@1.56.0"]`,
				"roots = [\"just@1.56.0\"]\nstray = true", 1),
			key: "targets.default.stray",
		},
		{
			name: "targets host",
			content: strings.Replace(v2Fixture,
				`roots = ["just@1.56.0", "jq@1.8.1"]`,
				"roots = [\"just@1.56.0\", \"jq@1.8.1\"]\nstray = true", 1),
			key: `targets.host."ci-*,build-*".stray`,
		},
		{
			name: "package",
			content: strings.Replace(v2Fixture,
				`[packages."just@1.56.0".artifacts."darwin/arm64"]`,
				"[packages.\"just@1.56.0\"]\nsurprise = true\n\n"+
					`[packages."just@1.56.0".artifacts."darwin/arm64"]`, 1),
			key: `packages."just@1.56.0".surprise`,
		},
		{
			name: "artifact graph_digest",
			content: strings.Replace(v2Fixture,
				`index_commit = "deadbeef"`,
				`index_commit = "deadbeef"`+"\ngraph_digest = \"sha256:cccc\"", 1),
			key: `packages."just@1.56.0".artifacts."darwin/arm64".graph_digest`,
		},
		{
			name: "attestation",
			content: strings.Replace(v2Fixture,
				`repo = "casey/just"`,
				`repo = "casey/just"`+"\nextra = true", 1),
			key: `packages."just@1.56.0".artifacts."darwin/arm64".attestation.extra`,
		},
		{
			name: "files",
			content: strings.Replace(v2Fixture,
				`mode = 0o755`,
				"mode = 0o755\nextra = true", 1),
			key: `packages."just@1.56.0".artifacts."darwin/arm64".files.extra`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadV2(writeTemp(t, tt.content))
			if !errors.Is(err, ErrUnknownField) {
				t.Fatalf("err = %v, want ErrUnknownField", err)
			}
			msg := err.Error()
			marker := ErrUnknownField.Error() + ": "
			idx := strings.Index(msg, marker)
			if idx < 0 {
				t.Fatalf("error is not shaped as expected: %v", err)
			}
			if got := msg[idx+len(marker):]; got != tt.key {
				t.Errorf("named keys = %q, want %q", got, tt.key)
			}
		})
	}
}

func TestReadV2RejectsBadGuard(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "absent",
			content: strings.Replace(v2Fixture, v2Guard, "", 1),
		},
		{
			name: "version key missing",
			content: strings.Replace(v2Fixture, v2Guard,
				"[packages.\"!gale-lock-v2\"]\n", 1),
		},
		{
			name: "wrong value",
			content: strings.Replace(v2Fixture, v2Guard,
				"[packages.\"!gale-lock-v2\"]\nversion = 1\n", 1),
		},
		{
			name: "string version",
			content: strings.Replace(v2Fixture, v2Guard,
				"[packages.\"!gale-lock-v2\"]\nversion = \"2\"\n", 1),
		},
		{
			name: "carries artifacts",
			content: strings.Replace(v2Fixture, v2Guard,
				v2Guard+"\n[packages.\"!gale-lock-v2\".artifacts.\"darwin/arm64\"]\n"+
					"url = \"https://example.com/x\"\nformat = \"tar.gz\"\n"+
					"sha256 = \"aaaa\"\ntree_digest = \"bbbb\"\nmethod = \"fetch\"\n", 1),
		},
		{
			name: "guard field on a real package",
			content: strings.Replace(v2Fixture,
				`[packages."just@1.56.0".artifacts."darwin/arm64"]`,
				"[packages.\"just@1.56.0\"]\nversion = 2\n\n"+
					`[packages."just@1.56.0".artifacts."darwin/arm64"]`, 1),
		},
		{
			// A document whose only package key is the reserved
			// name, carrying artifact fields: the key is the
			// guard, so this is a malformed guard, not a package.
			name: "package key is the guard",
			content: "version = 2\n\n" +
				"[packages.\"!gale-lock-v2\".artifacts.\"darwin/arm64\"]\n" +
				"url = \"https://example.com/x\"\nformat = \"tar.gz\"\n" +
				"sha256 = \"aaaa\"\ntree_digest = \"bbbb\"\nmethod = \"fetch\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadV2(writeTemp(t, tt.content))
			if !errors.Is(err, ErrDowngradeGuard) {
				t.Fatalf("err = %v, want ErrDowngradeGuard", err)
			}
		})
	}
}

func TestV2GuardIsWireOnly(t *testing.T) {
	lf, err := ReadV2(writeTemp(t, v2Fixture))
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	if _, ok := lf.Packages["!gale-lock-v2"]; ok {
		t.Errorf("guard leaked into the package map: %v", lf.Packages)
	}
	if len(lf.Packages) != 1 {
		t.Errorf("got %d package nodes, want 1", len(lf.Packages))
	}
}

// TestLegacyDecodeRefusesV2: the guard's whole point for
// pre-enforcement gale. A minimal v2 file — version key plus the
// integer guard, nothing else — must fail the flat decoder. The
// top-level version is ignored; the integer guard version is what
// stops the rewrite.
func TestLegacyDecodeRefusesV2(t *testing.T) {
	minimal := "version = 2\n\n" + v2Guard
	if _, err := decodeLegacy([]byte(minimal)); err == nil {
		t.Fatal("the legacy decoder accepted a minimal v2 lockfile")
	}
}

func TestReadV2Malformed(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "syntax error",
			content: "version = 2\n[targets\n",
		},
		{
			name: "type mismatch",
			content: strings.Replace(v2Fixture,
				`roots = ["just@1.56.0"]`,
				"roots = 5", 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadV2(writeTemp(t, tt.content))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			if !strings.Contains(err.Error(), "toml:") {
				t.Errorf("error dropped the decoder's detail: %v", err)
			}
		})
	}
}

func TestReadV1RefusesV2Fixture(t *testing.T) {
	_, err := ReadV1(writeTemp(t, v2Fixture))
	if !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
}

func TestReadV2OmitsAbsentAttestation(t *testing.T) {
	content := strings.Replace(v2Fixture,
		`[packages."just@1.56.0".artifacts."darwin/arm64".attestation]
issuer = "https://token.actions.githubusercontent.com"
san = "https://github.com/casey/just/.github/workflows/release.yml@refs/tags/1.56.0"
repo = "casey/just"

`, "", 1)
	lf, err := ReadV2(writeTemp(t, content))
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	art := lf.Packages["just@1.56.0"].Artifacts["darwin/arm64"]
	if art.Attestation != nil {
		t.Errorf("attestation = %#v, want nil", art.Attestation)
	}
}

func TestReadV2EmptyAttestationIsPresent(t *testing.T) {
	content := strings.Replace(v2Fixture,
		`[packages."just@1.56.0".artifacts."darwin/arm64".attestation]
issuer = "https://token.actions.githubusercontent.com"
san = "https://github.com/casey/just/.github/workflows/release.yml@refs/tags/1.56.0"
repo = "casey/just"
`,
		`[packages."just@1.56.0".artifacts."darwin/arm64".attestation]
`, 1)
	lf, err := ReadV2(writeTemp(t, content))
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}
	art := lf.Packages["just@1.56.0"].Artifacts["darwin/arm64"]
	if art.Attestation == nil {
		t.Fatal("empty attestation table decoded as absent")
	}
	if *art.Attestation != (V2Attestation{}) {
		t.Errorf("attestation = %#v, want zero value", art.Attestation)
	}
}

func TestWriteV2RoundTrip(t *testing.T) {
	original, err := ReadV2(writeTemp(t, v2Fixture))
	if err != nil {
		t.Fatalf("ReadV2: %v", err)
	}

	out := filepath.Join(t.TempDir(), "gale.lock")
	if err := WriteV2(out, original); err != nil {
		t.Fatalf("WriteV2: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read written lock: %v", err)
	}
	if !strings.Contains(string(data), `[packages."!gale-lock-v2"]`) {
		t.Errorf("WriteV2 omitted the downgrade guard:\n%s", data)
	}
	if !strings.Contains(string(data), "version = 2") {
		t.Errorf("WriteV2 omitted version = 2:\n%s", data)
	}

	reread, err := ReadV2(out)
	if err != nil {
		t.Fatalf("ReadV2(rewritten): %v", err)
	}
	if !reflect.DeepEqual(original, reread) {
		t.Errorf("round trip changed the lockfile:\n got %#v\nwant %#v", reread, original)
	}

	v, err := Load(out)
	if err != nil {
		t.Fatalf("Load after WriteV2: %v", err)
	}
	if v.Kind != KindV2 || v.V2 == nil {
		t.Errorf("Load after WriteV2: Kind=%v V2=%v, want KindV2", v.Kind, v.V2)
	}
}

func TestWriteV2RejectsWrongVersion(t *testing.T) {
	out := filepath.Join(t.TempDir(), "gale.lock")
	err := WriteV2(out, &V2{Version: 1})
	if !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("WriteV2 created a file despite rejecting the version")
	}
}

func TestWriteV2RejectsGuardKeyCollision(t *testing.T) {
	out := filepath.Join(t.TempDir(), "gale.lock")
	err := WriteV2(out, &V2{
		Version: SchemaV2,
		Packages: map[string]V2Package{
			guardKeyV2: {},
		},
	})
	if !errors.Is(err, ErrDowngradeGuard) {
		t.Fatalf("err = %v, want ErrDowngradeGuard", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("WriteV2 created a file despite rejecting the guard key")
	}
}

func TestSplitV2Root(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, name, version string
		ok                bool
	}{
		{in: "just@1.56.0", name: "just", version: "1.56.0", ok: true},
		{in: "jq@1.8.1", name: "jq", version: "1.8.1", ok: true},
		{in: "just", ok: false},
		{in: "@1.56.0", ok: false},
		{in: "just@", ok: false},
		{in: "", ok: false},
		{in: "just@1.56.0@extra", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			name, version, err := SplitV2Root(tt.in)
			if tt.ok {
				if err != nil {
					t.Fatalf("SplitV2Root(%q): %v", tt.in, err)
				}
				if name != tt.name || version != tt.version {
					t.Errorf("SplitV2Root(%q) = %q, %q, want %q, %q",
						tt.in, name, version, tt.name, tt.version)
				}
				return
			}
			if err == nil {
				t.Fatalf("SplitV2Root(%q) = %q, %q, want error", tt.in, name, version)
			}
		})
	}
}
