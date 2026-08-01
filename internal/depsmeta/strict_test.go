package depsmeta

import (
	"os"
	"path/filepath"
	"testing"
)

// ReadStrict separates the three states a caller may need to tell
// apart, which neither existing reader does. Read treats a missing
// file as an empty closure, and Has answers with os.Stat, which
// follows symlinks and says nothing about whether the target is a
// regular file or parses.
//
// Design §13's closure scan needs all three: a valid empty file is an
// explicitly recorded empty closure, an absent file is not, and
// anything unreadable, non-regular or malformed means the closure is
// unknown rather than empty.
func TestReadStrictSeparatesAbsentFromEmpty(t *testing.T) {
	absent := t.TempDir()
	if _, state := ReadStrict(absent); state != StateAbsent {
		t.Errorf("no file: state = %v, want StateAbsent", state)
	}

	empty := t.TempDir()
	if err := Write(empty, Metadata{}); err != nil {
		t.Fatal(err)
	}
	deps, state := ReadStrict(empty)
	if state != StateRecorded {
		t.Errorf("valid empty file: state = %v, want StateRecorded", state)
	}
	if len(deps) != 0 {
		t.Errorf("valid empty file: deps = %v, want none", deps)
	}
}

// A dangling symlink is the case that motivated the strict reader in
// the first place: os.Stat reports it as absent, and code that then
// writes through the path escapes the directory it was given.
func TestReadStrictRejectsASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.toml")
	if err := os.WriteFile(target, []byte("deps = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, File)); err != nil {
		t.Fatal(err)
	}

	if _, state := ReadStrict(dir); state != StateUnusable {
		t.Errorf("symlinked metadata: state = %v, want StateUnusable", state)
	}
}

// Malformed content is unusable, not empty. Reading it as a zero-dep
// package would report a package with dependencies as a leaf.
func TestReadStrictRejectsMalformedContent(t *testing.T) {
	for _, tt := range []struct {
		name, body string
	}{
		{"not toml", "this is not toml {{{"},
		{"unknown field", "deps = []\nsurprise = 1\n"},
		{"empty dep name", "[[deps]]\nname = \"\"\nversion = \"1.0\"\n"},
		{"negative revision", "[[deps]]\nname = \"jq\"\nversion = \"1.0\"\nrevision = -1\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(dir, File), []byte(tt.body), 0o644,
			); err != nil {
				t.Fatal(err)
			}
			if _, state := ReadStrict(dir); state != StateUnusable {
				t.Errorf("state = %v, want StateUnusable", state)
			}
		})
	}
}

// A well-formed file with dependencies returns them.
func TestReadStrictReturnsRecordedDeps(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Metadata{Deps: []ResolvedDep{
		{Name: "oniguruma", Version: "6.9.9", Revision: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	deps, state := ReadStrict(dir)
	if state != StateRecorded {
		t.Fatalf("state = %v, want StateRecorded", state)
	}
	if len(deps) != 1 || deps[0].Name != "oniguruma" {
		t.Errorf("deps = %v", deps)
	}
}

// A dependency name or version is joined onto the store root to build
// a path. A value that is not a single path component escapes the
// store, and the closure walk that consumes these would either follow
// it out of the store or treat the malformed edge as harmlessly
// missing — which for a destructive decision reads as "unreferenced".
func TestReadStrictRejectsUnsafeIdentities(t *testing.T) {
	for _, tt := range []struct {
		name, body string
	}{
		{
			"traversal in name",
			"[[deps]]\nname = \"../outside\"\nversion = \"1.0\"\nrevision = 1\n",
		},
		{
			"separator in name",
			"[[deps]]\nname = \"a/b\"\nversion = \"1.0\"\nrevision = 1\n",
		},
		{
			"traversal in version",
			"[[deps]]\nname = \"jq\"\nversion = \"../..\"\nrevision = 1\n",
		},
		{
			"separator in version",
			"[[deps]]\nname = \"jq\"\nversion = \"1.0/etc\"\nrevision = 1\n",
		},
		{
			"dot name",
			"[[deps]]\nname = \".\"\nversion = \"1.0\"\nrevision = 1\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(dir, File), []byte(tt.body), 0o644,
			); err != nil {
				t.Fatal(err)
			}
			if _, state := ReadStrict(dir); state != StateUnusable {
				t.Errorf("state = %v, want StateUnusable", state)
			}
		})
	}
}

// An authoritative record cannot have two readings. A name containing
// "@" and a version that already carries a revision suffix are each
// interpreted one way by the closure walk, which uses the version
// directly, and another by the installer's provenance path, which
// appends Revision — turning "1.7-2" into the identity "1.7-2-1".
func TestReadStrictRejectsAmbiguousIdentities(t *testing.T) {
	for _, tt := range []struct {
		name, body string
	}{
		{
			"at sign in name",
			"[[deps]]\nname = \"jq@1.7\"\nversion = \"1.0\"\nrevision = 1\n",
		},
		{
			"version already carries a revision",
			"[[deps]]\nname = \"jq\"\nversion = \"1.7-2\"\nrevision = 1\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(dir, File), []byte(tt.body), 0o644,
			); err != nil {
				t.Fatal(err)
			}
			if _, state := ReadStrict(dir); state != StateUnusable {
				t.Errorf("state = %v, want StateUnusable", state)
			}
		})
	}
}
