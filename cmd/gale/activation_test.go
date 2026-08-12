package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kelp/gale/internal/lockfile"
)

// writeGateFixture lays out a project with a gale.toml and a lockfile
// body, and returns the project dir. galeDir is created so the scope
// resolves the same way an activated project does.
func writeGateFixture(t *testing.T, lockBody string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".gale"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("gale.toml", "[packages]\nhello = \"1.0\"\n")
	if lockBody != "" {
		write("gale.lock", lockBody)
	}
	return proj
}

// TestGateActivationRefusesALegacyLock is acceptance 21's wiring half:
// the gate reaches the project's lockfile through the ordinary scope
// resolution, and a legacy lock refuses activation. No mtime is
// involved anywhere in the path.
func TestGateActivationRefusesALegacyLock(t *testing.T) {
	proj := writeGateFixture(t, legacyLockBody)
	chdirTo(t, proj)

	err := gateActivation(filepath.Join(proj, ".gale"), filepath.Join(proj, "gale.toml"))
	if !errors.Is(err, lockfile.ErrLegacySchema) {
		t.Fatalf("want lockfile.ErrLegacySchema, got %v", err)
	}
	if code := exitCodeFor(err); code != exitLockUnusable {
		t.Errorf("exit code = %d, want %d", code, exitLockUnusable)
	}
}

// TestGateActivationPassesWithNoLock pins that an unlocked project
// activates. The gate runs on every cd, so a project that never ran
// `gale lock` must not be refused its own PATH.
func TestGateActivationPassesWithNoLock(t *testing.T) {
	proj := writeGateFixture(t, "")
	chdirTo(t, proj)

	if err := gateActivation(
		filepath.Join(proj, ".gale"), filepath.Join(proj, "gale.toml"),
	); err != nil {
		t.Fatalf("gateActivation with no lock: %v", err)
	}
}

// TestGateActivationSkipsGlobalScope pins design §12's deliberate
// asymmetry: ~/.gale/current/bin reaches PATH from the user's shell rc
// with no gale invocation, so there is nothing to hook and no gate to
// run. Global relies on sync-time enforcement, which is why a locked
// global plan forbids carry-forward instead.
func TestGateActivationSkipsGlobalScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A legacy global lock, which the project gate would refuse.
	if err := os.WriteFile(
		filepath.Join(galeDir, "gale.lock"), []byte(legacyLockBody), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := gateActivation(
		galeDir, filepath.Join(galeDir, "gale.toml"),
	); err != nil {
		t.Fatalf("global scope must not be gated, got %v", err)
	}
}

// TestEnvCheckFlagIsRegistered pins the flag the direnv hook invokes.
// The hook is a string in internal/env, so nothing else would catch a
// rename that leaves every project unable to activate.
func TestEnvCheckFlagIsRegistered(t *testing.T) {
	if envCmd.Flags().Lookup("check") == nil {
		t.Fatal("gale env has no --check flag; the direnv hook's " +
			"activation gate cannot run")
	}
}
