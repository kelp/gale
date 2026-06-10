package store

import (
	"os"
	"path/filepath"
	"testing"
)

// auditU1Populate creates <root>/<name>/<version>/bin/<name>
// with content so the dir counts as a real install.
func auditU1Populate(t *testing.T, root, name, version string) {
	t.Helper()
	binDir := filepath.Join(root, name, version, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("setup %s/%s: %v", name, version, err)
	}
	if err := os.WriteFile(
		filepath.Join(binDir, name), []byte("fake"), 0o755,
	); err != nil {
		t.Fatalf("setup binary %s/%s: %v", name, version, err)
	}
}

// TestStorePathSkipsEmptyInFlightRevisionDir pins the gh#76
// contract: an empty canonical store dir — pre-created by
// Store.Create while a concurrent install of a new revision is
// still downloading, or left behind forever by a killed install
// — must not shadow the populated active revision when a bare
// version resolves to "the highest revision on disk".
func TestStorePathSkipsEmptyInFlightRevisionDir(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	auditU1Populate(t, root, "jq", "1.8.1-2")
	// Empty in-flight dir for the next revision.
	if err := os.MkdirAll(
		filepath.Join(root, "jq", "1.8.1-3"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	got, ok := s.StorePath("jq", "1.8.1")
	if !ok {
		t.Fatalf("StorePath ok = false, want true")
	}
	want := filepath.Join(root, "jq", "1.8.1-2")
	if got != want {
		t.Errorf("StorePath = %q, want %q (populated revision, "+
			"not the empty in-flight one)", got, want)
	}

	if !s.IsInstalled("jq", "1.8.1") {
		t.Errorf("IsInstalled = false, want true (1.8.1-2 is populated)")
	}
}

// TestStorePathAllEmptyRevisionsFallsBackToHighest preserves the
// pre-fix semantics when no revision dir has content at all (a
// truly fresh in-flight install): the highest revision is still
// the answer, and IsInstalled separately reports false.
func TestStorePathAllEmptyRevisionsFallsBackToHighest(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	for _, rev := range []string{"1.8.1-2", "1.8.1-3"} {
		if err := os.MkdirAll(
			filepath.Join(root, "jq", rev), 0o755,
		); err != nil {
			t.Fatal(err)
		}
	}

	got, ok := s.StorePath("jq", "1.8.1")
	if !ok {
		t.Fatalf("StorePath ok = false, want true")
	}
	want := filepath.Join(root, "jq", "1.8.1-3")
	if got != want {
		t.Errorf("StorePath = %q, want %q", got, want)
	}
	if s.IsInstalled("jq", "1.8.1") {
		t.Errorf("IsInstalled = true, want false (all dirs empty)")
	}
}

// TestStorePathPrefersPopulatedBareOverEmptyRevisions covers the
// legacy layout: a populated pre-revision bare dir plus an empty
// in-flight "<v>-<N>" sibling. The populated bare dir must win.
func TestStorePathPrefersPopulatedBareOverEmptyRevisions(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	auditU1Populate(t, root, "jq", "1.8.1")
	if err := os.MkdirAll(
		filepath.Join(root, "jq", "1.8.1-2"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	got, ok := s.StorePath("jq", "1.8.1")
	if !ok {
		t.Fatalf("StorePath ok = false, want true")
	}
	want := filepath.Join(root, "jq", "1.8.1")
	if got != want {
		t.Errorf("StorePath = %q, want %q (populated legacy bare dir)",
			got, want)
	}
}

// TestSplitRevision pins the canonical revision-splitting helper
// that generation, registry, and cmd/gale route through.
func TestSplitRevision(t *testing.T) {
	cases := []struct {
		in       string
		wantBase string
		wantRev  int
	}{
		{"1.8.1", "1.8.1", 1},
		{"1.8.1-2", "1.8.1", 2},
		{"1.8.1.1-2", "1.8.1.1", 2},
		{"1.0.0-rc1", "1.0.0-rc1", 1},
		{"0.16.2-dev.70+676b646", "0.16.2-dev.70+676b646", 1},
		{"0.16.2-dev.70+676b646-3", "0.16.2-dev.70+676b646", 3},
	}
	for _, tc := range cases {
		base, rev := SplitRevision(tc.in)
		if base != tc.wantBase || rev != tc.wantRev {
			t.Errorf("SplitRevision(%q) = (%q, %d), want (%q, %d)",
				tc.in, base, rev, tc.wantBase, tc.wantRev)
		}
	}
}
