package lockfile

import (
	"errors"
	"path/filepath"
	"testing"
)

const entryPlatform = "darwin/arm64"

// TestViewEntryAnswersFromEitherSchema pins that a read-only consumer
// asks one question and never switches on Kind. audit, verify, and
// sbom each want one package's recorded hash; where that lives is this
// package's problem.
func TestViewEntryAnswersFromEitherSchema(t *testing.T) {
	legacy, err := Load(writeTemp(t, validLockTOML))
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	got, ok, err := legacy.Entry("jq", "", entryPlatform)
	if err != nil || !ok {
		t.Fatalf("Entry(jq) = %v, %v, %v", got, ok, err)
	}
	if got.Version != "1.7.1" || got.SHA256 != "abc123" {
		t.Errorf("legacy entry = %+v, want 1.7.1/abc123", got)
	}
	// The flat schema records no method, and inventing one would let a
	// caller believe a legacy lock says how the bytes were produced.
	if got.Method != "" {
		t.Errorf("legacy entry method = %q, want empty", got.Method)
	}

	v1, err := Load(writeTemp(t, v1Fixture))
	if err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	got, ok, err = v1.Entry("jq", "", entryPlatform)
	if err != nil || !ok {
		t.Fatalf("Entry(jq) = %v, %v, %v", got, ok, err)
	}
	// The version is the identity's, not the manifest's: it is the
	// canonical pin, which ResolveVersionedRecipe accepts.
	if got.Version != "1.8.1-2" {
		t.Errorf("v1 entry version = %q, want 1.8.1-2", got.Version)
	}
	if got.SHA256 != "aaaa" || got.ManifestDigest != "sha256:bbbb" {
		t.Errorf("v1 entry = %+v, want aaaa/sha256:bbbb", got)
	}
	if got.Method != "binary" {
		t.Errorf("v1 entry method = %q, want binary", got.Method)
	}
}

// TestViewEntryPlatformSelectsTheArtifact pins that platform is an
// artifact dimension: one v1 lock answers differently per platform,
// which is the whole reason one committed file can serve macOS and
// Linux.
func TestViewEntryPlatformSelectsTheArtifact(t *testing.T) {
	v1, err := Load(writeTemp(t, v1Fixture))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok, err := v1.Entry("jq", "", "linux/amd64")
	if err != nil || !ok {
		t.Fatalf("Entry(jq) = %v, %v, %v", got, ok, err)
	}
	if got.SHA256 != "dddd" || got.Method != "source" {
		t.Errorf("linux entry = %+v, want dddd/source", got)
	}
}

// TestViewEntryHostSelectsTheRoot pins that the host overlay applies:
// a root pinned for this machine wins over the shared one, and a root
// belonging to another machine is not visible here. The flat schema
// accumulated every host's entries in one map, so this is a deliberate
// change in what "locked" means.
func TestViewEntryHostSelectsTheRoot(t *testing.T) {
	v1, err := Load(writeTemp(t, v1Fixture))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// v1Fixture pins jq@1.9.0-1 for work-mbp, for which it records no
	// package node at all — proof the root came from the overlay.
	if _, _, err := v1.Entry("jq", "work-mbp", entryPlatform); !errors.Is(err, ErrMissingNode) {
		t.Errorf("Entry(jq, work-mbp) error = %v, want ErrMissingNode", err)
	}

	// zig is rooted only under the ci-*,build-* selector.
	if _, ok, err := v1.Entry("zig", "laptop", entryPlatform); ok || err != nil {
		t.Errorf("Entry(zig, laptop) = %v, %v, want not found", ok, err)
	}
}

// TestViewEntryMissingArtifactIsAnError separates "not locked" from
// "locked, but not for this platform". Folding the second into ok=false
// would make audit print "reinstall it", which does not fix a lock
// that never described this platform.
func TestViewEntryMissingArtifactIsAnError(t *testing.T) {
	v1, err := Load(writeTemp(t, v1Fixture))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, ok, err := v1.Entry("jq", "", "windows/amd64")
	if !errors.Is(err, ErrMissingArtifact) {
		t.Fatalf("Entry error = %v, want ErrMissingArtifact", err)
	}
	if ok {
		t.Error("ok = true alongside an error")
	}
}

// TestViewEntryAbsentAndUnlocked covers the two ordinary negatives: no
// lock at all, and a lock that simply does not carry the package.
// Neither is an error, because both are states a command reports
// rather than fails on.
func TestViewEntryAbsentAndUnlocked(t *testing.T) {
	absent, err := Load(filepath.Join(t.TempDir(), "gale.lock"))
	if err != nil {
		t.Fatalf("Load absent: %v", err)
	}
	if got, ok, err := absent.Entry("jq", "", entryPlatform); ok || err != nil {
		t.Errorf("absent Entry = %v, %v, %v, want zero/false/nil", got, ok, err)
	}

	for _, fixture := range []string{validLockTOML, v1Fixture} {
		v, err := Load(writeTemp(t, fixture))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if _, ok, err := v.Entry("absent-pkg", "", entryPlatform); ok || err != nil {
			t.Errorf("Entry(absent-pkg) = %v, %v, want false/nil", ok, err)
		}
	}
}
