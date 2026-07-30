package env

import (
	"strings"
	"testing"
)

func TestDirenvHookNonEmpty(t *testing.T) {
	hook := DirenvHook()
	if hook == "" {
		t.Error("expected non-empty direnv hook output")
	}
}

func TestDirenvHookContainsUseGale(t *testing.T) {
	hook := DirenvHook()
	if !strings.Contains(hook, "use_gale") {
		t.Errorf("direnv hook missing 'use_gale': %q", hook)
	}
}

func TestDirenvHookContainsPATHAdd(t *testing.T) {
	hook := DirenvHook()
	if !strings.Contains(hook, "PATH_add") {
		t.Errorf("direnv hook missing 'PATH_add': %q", hook)
	}
}

func TestDirenvHookWatchesManifest(t *testing.T) {
	hook := DirenvHook()
	if !strings.Contains(hook, "watch_file") {
		t.Errorf("direnv hook missing 'watch_file': %q", hook)
	}
	if !strings.Contains(hook, "gale.toml") {
		t.Errorf("direnv hook missing 'gale.toml': %q", hook)
	}
}

func TestDirenvHookSkipsSyncWhenFresh(t *testing.T) {
	hook := DirenvHook()
	// The freshness check guards `gale sync` behind a mtime
	// comparison so activation is a no-op when nothing changed.
	if !strings.Contains(hook, "-nt") {
		t.Errorf("direnv hook missing '-nt' freshness check: %q", hook)
	}
}

// TestDirenvHook_RunsGateUnconditionally pins the activation
// gate into the hook (design §12). The mtime guard cannot see a
// gale upgrade: upgrading the binary modifies no file in the
// project, so a lockfile this build refuses to honor would reach
// PATH unexamined. The gate therefore runs on every activation,
// outside the `-nt` conditional, before PATH_add, with its stderr
// intact.
func TestDirenvHook_RunsGateUnconditionally(t *testing.T) {
	hook := DirenvHook()

	gate := strings.Index(hook, gateCommand)
	if gate < 0 {
		t.Fatalf("direnv hook never runs the activation gate "+
			"(%q): %q", gateCommand, hook)
	}

	// The invocation, not the bare word: the surrounding comments
	// name PATH_add too, and only the call site orders the gate.
	pathAdd := strings.Index(hook, `PATH_add "`)
	if pathAdd < 0 {
		t.Fatal("direnv hook has no PATH_add call")
	}
	if gate > pathAdd {
		t.Errorf("activation gate runs after PATH_add — the "+
			"project's binaries reach PATH before anything "+
			"checks them: %q", hook)
	}

	// Unconditional means the gate is not nested in the mtime
	// guard's `if`. The guard opens with `if [ ! -L` and closes
	// with the next `fi`, so a gate between them is conditional.
	guard := strings.Index(hook, "if [ ! -L")
	if guard < 0 {
		t.Fatal("direnv hook has no mtime guard to compare against")
	}
	guardEnd := strings.Index(hook[guard:], "\n  fi\n")
	if guardEnd < 0 {
		t.Fatal("direnv hook's mtime guard has no closing fi")
	}
	if gate > guard && gate < guard+guardEnd {
		t.Errorf("activation gate is nested inside the mtime "+
			"guard, so a gale upgrade skips it: %q", hook)
	}

	// A failed gate must stop activation rather than warn.
	if !strings.Contains(hook, gateCommand+" || return 1") {
		t.Errorf("direnv hook does not refuse activation when the "+
			"gate fails: %q", hook)
	}
}

// TestDirenvHook_NeverSuppressesStderr pins design §12's
// "integrity failures are never suppressed". Every gale
// invocation in the hook must keep its stderr: a discarded
// integrity error is a silently downgraded security control.
func TestDirenvHook_NeverSuppressesStderr(t *testing.T) {
	hook := DirenvHook()
	if strings.Contains(hook, "2>/dev/null") {
		t.Errorf("direnv hook still discards stderr, hiding "+
			"integrity failures: %q", hook)
	}
}

// TestDirenvHook_WatchesLockfile pins that an edited or replaced
// gale.lock re-triggers activation. The lock is what the gate
// checks against, so watching only the manifest would let a lock
// change go unnoticed until something else touched gale.toml.
func TestDirenvHook_WatchesLockfile(t *testing.T) {
	hook := DirenvHook()
	if !strings.Contains(hook, `watch_file "$lockfile"`) {
		t.Errorf("direnv hook does not watch gale.lock: %q", hook)
	}
}

// TestDirenvHookSurfacesEnvErrors pins that the direnv hook
// surfaces errors from `gale env --vars-only` instead of
// swallowing them. A user with a broken [vars] section should
// see parse errors during direnv activation and get a failed
// activation, not silently exported nothing.
func TestDirenvHookSurfacesEnvErrors(t *testing.T) {
	hook := DirenvHook()
	if strings.Contains(hook, "gale env --vars-only 2>/dev/null") {
		t.Errorf("direnv hook still redirects gale env stderr "+
			"to /dev/null, hiding parse errors: %q", hook)
	}
	if strings.Contains(hook,
		`eval "$(gale env --vars-only`+
			` 2>/dev/null)" || true`) {
		t.Errorf("direnv hook still suppresses gale env exit "+
			"status with '|| true': %q", hook)
	}
	// Positive assertion: the expected new shape.
	if !strings.Contains(hook,
		`eval "$(gale env --vars-only)"`) {
		t.Errorf("direnv hook missing bare "+
			"`eval \"$(gale env --vars-only)\"`: %q", hook)
	}
}
