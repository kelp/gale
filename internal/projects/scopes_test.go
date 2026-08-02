package projects

import (
	"os"
	"path/filepath"
	"testing"
)

// Every cross-scope rule in the lock design — the farm claimant guard
// (§4) and the migration veto (§13) — asks a different question of the
// same set: the global scope plus every registered project. The set
// itself must have exactly one definition, because a scan that misses
// a scope does not fail, it silently approves.
func TestScopesIncludesGlobalAndEveryRegisteredProject(t *testing.T) {
	home := t.TempDir()
	projA := filepath.Join(t.TempDir(), "a")
	projB := filepath.Join(t.TempDir(), "b")
	for _, p := range []string{projA, projB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := Register(home, p); err != nil {
			t.Fatal(err)
		}
	}

	scopes, err := Scopes(home)
	if err != nil {
		t.Fatalf("Scopes: %v", err)
	}
	if len(scopes) != 3 {
		t.Fatalf("len(scopes) = %d, want 3 (global + two projects): %+v",
			len(scopes), scopes)
	}

	// The global scope comes first and is never a registered project:
	// registerProject skips it by design, so a registry-only scan
	// misses it entirely.
	if scopes[0].GaleDir != home {
		t.Errorf("scopes[0].GaleDir = %q, want the global dir %q",
			scopes[0].GaleDir, home)
	}
	if scopes[0].LockPath != filepath.Join(home, "gale.lock") {
		t.Errorf("global LockPath = %q", scopes[0].LockPath)
	}

	byDir := map[string]Scope{}
	for _, s := range scopes {
		byDir[s.GaleDir] = s
	}
	for _, p := range []string{projA, projB} {
		// The registry canonicalizes, and on macOS /var is a symlink
		// to /private/var, so the raw temp path never matches what was
		// stored.
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		s, ok := byDir[filepath.Join(resolved, ".gale")]
		if !ok {
			t.Fatalf("project %s missing from scopes", p)
		}
		if s.LockPath != filepath.Join(resolved, "gale.lock") {
			t.Errorf("%s LockPath = %q", p, s.LockPath)
		}
		// The label is what a fail-closed error names, so it has to
		// identify the scope to a human, not just to the code.
		if s.Label == "" {
			t.Errorf("%s has no label", p)
		}
	}
}

// An unreadable registry names scopes the walk cannot see, so it must
// surface as an error rather than an empty set. Callers fail closed on
// it; returning nothing would silently shrink every cross-scope check
// to "the global scope agrees".
func TestScopesFailsOnAnUnreadableRegistry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads regardless of mode")
	}
	home := t.TempDir()
	proj := t.TempDir()
	if err := Register(home, proj); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registryPath(home), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(registryPath(home), 0o644) })

	if _, err := Scopes(home); err == nil {
		t.Error("unreadable registry must fail the scan, not shrink it")
	}
}
