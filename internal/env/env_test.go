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
