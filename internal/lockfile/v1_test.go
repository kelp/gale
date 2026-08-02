package lockfile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// v1Guard is the mandatory downgrade guard, written verbatim. It is
// kept separate from the fixture so the guard tests can remove or
// corrupt exactly these bytes.
const v1Guard = `[packages."!gale-lock-v1"]
version = 1
`

// v1Fixture is a hand-written v1 lockfile. It pins the on-disk key
// names: this file is a persisted format shared with committed
// repositories, so a rename here breaks every checked-in lockfile.
const v1Fixture = "version = 1\n\n" + v1Guard + `
[targets.default]
roots = ["jq@1.8.1-2", "ripgrep@14.1.1-1"]

[targets.host."ci-*,build-*"]
roots = ["jq@1.8.1-2", "zig@0.14.1-1"]

[targets.host."work-mbp"]
roots = ["jq@1.9.0-1"]

[packages."jq@1.8.1-2".artifacts."darwin/arm64"]
sha256 = "aaaa"
manifest_digest = "sha256:bbbb"
method = "binary"
runtime_deps = ["oniguruma@6.9.10-1"]
graph_digest = "sha256:cccc"

[packages."jq@1.8.1-2".artifacts."linux/amd64"]
sha256 = "dddd"
method = "source"
runtime_deps = ["oniguruma@6.9.10-1"]
build_deps = ["autoconf@2.72-1"]
graph_digest = "sha256:eeee"
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gale.lock")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestReadV1ParsesOnDiskKeys(t *testing.T) {
	lf, err := ReadV1(writeTemp(t, v1Fixture))
	if err != nil {
		t.Fatalf("ReadV1: %v", err)
	}

	if lf.Version != 1 {
		t.Errorf("Version = %d, want 1", lf.Version)
	}
	wantDefault := []string{"jq@1.8.1-2", "ripgrep@14.1.1-1"}
	if lf.Targets.Default == nil {
		t.Fatal("default target missing")
	}
	if !reflect.DeepEqual(lf.Targets.Default.Roots, wantDefault) {
		t.Errorf("default roots = %v, want %v", lf.Targets.Default.Roots, wantDefault)
	}
	// Selector keys are stored verbatim, commas and wildcards
	// included: they are gale.toml's strings, not resolved hosts.
	wildcard, ok := lf.Targets.Host["ci-*,build-*"]
	if !ok {
		t.Fatalf("wildcard selector missing, have %v", lf.Targets.Host)
	}
	if len(wildcard.Roots) != 2 {
		t.Errorf("wildcard roots = %v, want 2 entries", wildcard.Roots)
	}

	pkg, ok := lf.Packages["jq@1.8.1-2"]
	if !ok {
		t.Fatalf("package node missing, have %v", lf.Packages)
	}
	darwin, ok := pkg.Artifacts["darwin/arm64"]
	if !ok {
		t.Fatalf("darwin artifact missing, have %v", pkg.Artifacts)
	}
	want := Artifact{
		SHA256:         "aaaa",
		ManifestDigest: "sha256:bbbb",
		Method:         "binary",
		RuntimeDeps:    []string{"oniguruma@6.9.10-1"},
		GraphDigest:    "sha256:cccc",
	}
	if !reflect.DeepEqual(darwin, want) {
		t.Errorf("darwin artifact = %#v, want %#v", darwin, want)
	}
	linux := pkg.Artifacts["linux/amd64"]
	if !reflect.DeepEqual(linux.BuildDeps, []string{"autoconf@2.72-1"}) {
		t.Errorf("build_deps = %v, want [autoconf@2.72-1]", linux.BuildDeps)
	}
}

func TestReadV1RoundTrip(t *testing.T) {
	path := writeTemp(t, v1Fixture)
	original, err := ReadV1(path)
	if err != nil {
		t.Fatalf("ReadV1: %v", err)
	}

	out := filepath.Join(t.TempDir(), "gale.lock")
	if err := WriteV1(out, original); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	reread, err := ReadV1(out)
	if err != nil {
		t.Fatalf("ReadV1(rewritten): %v", err)
	}
	if !reflect.DeepEqual(original, reread) {
		t.Errorf("round trip changed the lockfile:\n got %#v\nwant %#v", reread, original)
	}
}

func TestReadV1MissingFile(t *testing.T) {
	_, err := ReadV1(filepath.Join(t.TempDir(), "absent.lock"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

// TestReadV1LegacySchema: a lockfile with no version key was
// written by a pre-enforcement gale. It is a distinct case from an
// unknown version, because the remedy differs.
func TestReadV1LegacySchema(t *testing.T) {
	legacy := "[packages.jq]\nversion = \"1.8.1-2\"\nsha256 = \"aaaa\"\n"
	_, err := ReadV1(writeTemp(t, legacy))
	if !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("err = %v, want ErrLegacySchema", err)
	}
	if errors.Is(err, ErrUnknownVersion) {
		t.Error("legacy schema must not also report ErrUnknownVersion")
	}
}

func TestReadV1UnknownVersion(t *testing.T) {
	_, err := ReadV1(writeTemp(t, "version = 2\n"))
	if !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error does not name the version found: %v", err)
	}
	if errors.Is(err, ErrLegacySchema) {
		t.Error("unknown version must not also report ErrLegacySchema")
	}
}

// TestWriteV1RejectsUnknownVersion guards the amendment that
// forward compatibility requires a version bump: toml.Decode drops
// fields it does not model, so writing a version this build does
// not fully model would silently destroy data.
func TestWriteV1RejectsUnknownVersion(t *testing.T) {
	out := filepath.Join(t.TempDir(), "gale.lock")
	err := WriteV1(out, &V1{Version: 2})
	if !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("WriteV1 created a file despite rejecting the version")
	}
}

// TestWriteV1PreservesUntouchedNodes: a writer regenerating one
// target must not drop package nodes another target references.
func TestWriteV1PreservesUntouchedNodes(t *testing.T) {
	lf, err := ReadV1(writeTemp(t, v1Fixture))
	if err != nil {
		t.Fatalf("ReadV1: %v", err)
	}
	lf.Targets.Default.Roots = []string{"jq@1.8.1-2"}

	out := filepath.Join(t.TempDir(), "gale.lock")
	if err := WriteV1(out, lf); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	reread, err := ReadV1(out)
	if err != nil {
		t.Fatalf("ReadV1: %v", err)
	}
	if _, ok := reread.Targets.Host["work-mbp"]; !ok {
		t.Error("rewriting the default target dropped a host target")
	}
	if _, ok := reread.Packages["jq@1.8.1-2"]; !ok {
		t.Error("rewriting the default target dropped a package node")
	}
}

// TestWriteV1HostOnlyOmitsDefault: a project that declares only
// host overlays has no default target. Emitting an empty one
// would both invent a target no writer was asked to touch and
// erase the difference between absent and empty.
func TestWriteV1HostOnlyOmitsDefault(t *testing.T) {
	lf := &V1{
		Version: SchemaVersion,
		Targets: Targets{
			Host: map[string]Target{
				"work-mbp": {Roots: []string{"jq@1.8.1-2"}},
			},
		},
		Packages: map[string]Package{},
	}
	out := filepath.Join(t.TempDir(), "gale.lock")
	if err := WriteV1(out, lf); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(data), "targets.default") {
		t.Errorf("host-only lock emitted a default target:\n%s", data)
	}

	reread, err := ReadV1(out)
	if err != nil {
		t.Fatalf("ReadV1: %v", err)
	}
	if reread.Targets.Default != nil {
		t.Errorf("default target = %#v, want nil", reread.Targets.Default)
	}
}

// TestReadV1RejectsUnknownField: a v1 file carrying a field this
// build does not model would be silently dropped by the next
// write, which is the data loss the version check exists to
// prevent. Reject it instead.
func TestReadV1RejectsUnknownField(t *testing.T) {
	tests := []struct {
		name    string
		content string
		// key is the full dotted path the error must name.
		// Asserting the whole path, not a substring, is what
		// proves which TOML scope the stray field landed in.
		key string
	}{
		{
			// Placed before any table header: a key appended to
			// the end of the fixture would scope into the last
			// table, not the document root.
			name:    "top level",
			content: "version = 1\nstray = true\n" + v1Fixture[len("version = 1\n"):],
			key:     "stray",
		},
		{
			name: "nested in artifact",
			content: strings.Replace(v1Fixture,
				`graph_digest = "sha256:cccc"`,
				`graph_digest = "sha256:cccc"`+"\nsigning_key = \"k\"", 1),
			key: `packages."jq@1.8.1-2".artifacts."darwin/arm64".signing_key`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadV1(writeTemp(t, tt.content))
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

// TestV1SelectorKeyRoundTrip pins the encoder's quoting for the
// awkward selector strings gale.toml permits. These are table
// keys in a persisted file, so a quoting change would silently
// re-key every host overlay.
func TestV1SelectorKeyRoundTrip(t *testing.T) {
	selectors := []string{
		"ci-*,build-*",
		"host.with.dots",
		`has"quote`,
		`has\backslash`,
		"work-mbp",
	}
	hosts := make(map[string]Target, len(selectors))
	for i, s := range selectors {
		hosts[s] = Target{Roots: []string{fmt.Sprintf("jq@1.8.%d-1", i)}}
	}
	lf := &V1{
		Version:  SchemaVersion,
		Targets:  Targets{Host: hosts},
		Packages: map[string]Package{},
	}

	out := filepath.Join(t.TempDir(), "gale.lock")
	if err := WriteV1(out, lf); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	reread, err := ReadV1(out)
	if err != nil {
		t.Fatalf("ReadV1: %v", err)
	}
	if !reflect.DeepEqual(reread.Targets.Host, hosts) {
		t.Errorf("selector keys changed across a round trip:\n got %#v\nwant %#v",
			reread.Targets.Host, hosts)
	}
}

// TestReadV1Malformed: a lock that is present but unparseable is
// not an ordinary failure. It belongs with the schema errors, since
// in every one of those cases the lock exists and cannot be
// modeled, and a bare TOML error would leave a pipeline unable to
// tell it from a build break.
func TestReadV1Malformed(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "syntax error",
			content: "version = 1\n[targets\n",
		},
		{
			// Caught by the body decode rather than the probe, so
			// this pins the second parse site as well.
			name: "type mismatch",
			content: strings.Replace(v1Fixture,
				`roots = ["jq@1.8.1-2", "ripgrep@14.1.1-1"]`,
				"roots = 5", 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadV1(writeTemp(t, tt.content))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			// The decoder's own message names the line, which is
			// the only actionable part for a hand-edited file.
			if !strings.Contains(err.Error(), "toml:") {
				t.Errorf("error dropped the decoder's detail: %v", err)
			}
		})
	}
}

// TestReadV1RejectsBadGuard: the guard is what stops an
// already-shipped gale from rewriting a v1 lock in the flat schema.
// A file that claims version 1 without a well-formed guard is still
// downgrade-destructible, so accepting one would leave exactly the
// hole the guard exists to close.
func TestReadV1RejectsBadGuard(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "absent",
			content: strings.Replace(v1Fixture, v1Guard, "", 1),
		},
		{
			name: "wrong value",
			content: strings.Replace(v1Fixture, v1Guard,
				"[packages.\"!gale-lock-v1\"]\nversion = 2\n", 1),
		},
		{
			// A string here is precisely what the legacy decoder
			// accepts, so it would not stop an old gale at all.
			name: "string version",
			content: strings.Replace(v1Fixture, v1Guard,
				"[packages.\"!gale-lock-v1\"]\nversion = \"1\"\n", 1),
		},
		{
			name: "carries artifacts",
			content: strings.Replace(v1Fixture, v1Guard,
				v1Guard+"\n[packages.\"!gale-lock-v1\".artifacts.\"darwin/arm64\"]\n"+
					"sha256 = \"aaaa\"\nmethod = \"binary\"\ngraph_digest = \"sha256:x\"\n", 1),
		},
		{
			// The guard field on a real node would make that node
			// ambiguous with the guard and is never written.
			name: "guard field on a real package",
			content: strings.Replace(v1Fixture,
				`[packages."jq@1.8.1-2".artifacts."darwin/arm64"]`,
				"[packages.\"jq@1.8.1-2\"]\nversion = 1\n\n"+
					`[packages."jq@1.8.1-2".artifacts."darwin/arm64"]`, 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadV1(writeTemp(t, tt.content))
			if !errors.Is(err, ErrDowngradeGuard) {
				t.Fatalf("err = %v, want ErrDowngradeGuard", err)
			}
		})
	}
}

// TestV1GuardIsWireOnly: the guard exists on disk and nowhere else.
// Plan construction must never see it as a package node, and must
// never have to remember to skip one.
func TestV1GuardIsWireOnly(t *testing.T) {
	lf, err := ReadV1(writeTemp(t, v1Fixture))
	if err != nil {
		t.Fatalf("ReadV1: %v", err)
	}
	if _, ok := lf.Packages["!gale-lock-v1"]; ok {
		t.Errorf("guard leaked into the package map: %v", lf.Packages)
	}

	out := filepath.Join(t.TempDir(), "gale.lock")
	if err := WriteV1(out, lf); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), `[packages."!gale-lock-v1"]`) {
		t.Errorf("WriteV1 omitted the downgrade guard:\n%s", data)
	}
}

// TestLegacyReadRefusesV1: the guard's whole point. An old gale
// reading a v1 lock must stop, not decode it into near-empty
// packages and rewrite it in the flat schema. The assertion is
// refusal plus byte-identity, not destruction.
func TestLegacyReadRefusesV1(t *testing.T) {
	path := writeTemp(t, v1Fixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	if _, err := Read(path); err == nil {
		t.Fatal("legacy Read accepted a v1 lockfile")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("legacy read modified the lockfile")
	}
}

// TestLegacyReadStillWorks: existing callers keep working until
// they are migrated, so the legacy reader must be untouched by the
// v1 addition.
func TestLegacyReadStillWorks(t *testing.T) {
	legacy := "[packages.jq]\nversion = \"1.8.1-2\"\nsha256 = \"aaaa\"\n"
	lf, err := Read(writeTemp(t, legacy))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if lf.Packages["jq"].SHA256 != "aaaa" {
		t.Errorf("legacy read lost data: %#v", lf.Packages)
	}
}
