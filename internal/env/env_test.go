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

// TestDirenvHook_DelegatesSyncFreshnessToGale replaces
// TestDirenvHookSkipsSyncWhenFresh, which pinned the shell-level
// mtime gate `[ "$manifest" -nt "$gale_dir/current" ]`.
//
// That gate is the gh#186 defect, not a property worth keeping. A
// partial sync rebuilds the generation (issue #20) and the swap gives
// `current` a now-mtime, so the comparison is false forever and the
// failed packages are never retried. The shell cannot tell a
// completed sync from an abandoned one; only gale can, so the
// decision moved into `gale sync --if-needed`, where
// cmd/gale/syncstate_test.go tests it.
//
// What the hook still owes the user is unchanged and asserted here:
// it calls the freshness-aware form, and it no longer decides
// freshness itself.
func TestDirenvHook_DelegatesSyncFreshnessToGale(t *testing.T) {
	hook := DirenvHook()

	if !strings.Contains(hook, "gale sync --if-needed") {
		t.Errorf("direnv hook does not call 'gale sync --if-needed': %q",
			hook)
	}
	// An unguarded `gale sync` would resolve recipes on every cd.
	if strings.Contains(hook, "gale sync\n") ||
		strings.Contains(hook, "gale sync |") ||
		strings.Contains(hook, "gale sync ||") {
		t.Errorf("direnv hook still calls a bare 'gale sync': %q", hook)
	}
	if strings.Contains(hook, "-nt") {
		t.Errorf("direnv hook still compares mtimes to decide "+
			"freshness; a partial sync's rebuilt generation defeats "+
			"that comparison (gh#186): %q", hook)
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

	// Unconditional means the gate sits at the top level of
	// use_gale, not inside any `if`. It used to be phrased as "not
	// between the mtime guard's `if [ ! -L` and its `fi`"; the mtime
	// guard is gone (gh#186), so the property is now checked
	// directly: every `if` opened before the gate must already be
	// closed by a `fi`.
	if depth := shellIfDepthAt(hook, gate); depth != 0 {
		t.Errorf("activation gate is nested %d level(s) inside an "+
			"if, so some activations skip it: %q", depth, hook)
	}

	// A failed gate must stop activation rather than warn.
	if !strings.Contains(hook, gateCommand+" || return 1") {
		t.Errorf("direnv hook does not refuse activation when the "+
			"gate fails: %q", hook)
	}
}

// shellIfDepthAt counts how many `if` blocks are still open at byte
// offset idx. Zero means the statement there runs on every call.
// Comment lines are skipped: the hook's prose names both `if` and
// the commands it describes.
func shellIfDepthAt(hook string, idx int) int {
	depth := 0
	for _, line := range strings.Split(hook[:idx], "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "if "):
			depth++
		case trimmed == "fi":
			depth--
		}
	}
	return depth
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
