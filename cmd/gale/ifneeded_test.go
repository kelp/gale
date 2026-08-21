package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kelp/gale/internal/generation"
)

// useCanceledIfNeeded replaces the --if-needed parent with an
// already-canceled context so tests never wait on the compiled
// 15s deadline.
func useCanceledIfNeeded(t *testing.T) {
	t.Helper()
	orig := ifNeededContext
	t.Cleanup(func() { ifNeededContext = orig })
	ifNeededContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, func() {}
	}
}

// ifNeededProject is an isolated HOME + project that --if-needed
// would otherwise try to sync. Isolate HOME so nothing touches
// the real store.
func ifNeededProject(t *testing.T, manifest string) (proj, galeDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	proj = t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "gale.toml"),
		[]byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return proj, filepath.Join(proj, ".gale")
}

func TestIfNeededDeadlineIsFixed(t *testing.T) {
	if ifNeededDeadline != 15*time.Second {
		t.Errorf("ifNeededDeadline = %s, want 15s", ifNeededDeadline)
	}
	ctx, cancel := defaultIfNeededContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("--if-needed context must carry a deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 14*time.Second || remaining > 16*time.Second {
		t.Errorf("deadline remaining %s, want ~15s", remaining)
	}
}

func TestTypedSyncHasNoOverallDeadline(t *testing.T) {
	proj, _ := ifNeededProject(t, "[packages]\n")
	orig := ifNeededContext
	t.Cleanup(func() { ifNeededContext = orig })
	ifNeededContext = func() (context.Context, context.CancelFunc) {
		t.Fatal("typed gale sync must not wrap an overall deadline")
		return context.Background(), func() {}
	}
	t.Chdir(proj)
	if err := runSync(syncRun{Project: true}); err != nil {
		t.Fatalf("typed sync: %v", err)
	}
}

func TestIfNeededTimeoutRecordsIncomplete(t *testing.T) {
	useCanceledIfNeeded(t)
	proj, galeDir := ifNeededProject(t, "[packages]\n  jq = \"1.7\"\n")

	err := runSync(syncRun{ProjectDir: proj, IfNeeded: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runSync: %v, want context.Canceled", err)
	}

	data, readErr := os.ReadFile(filepath.Join(galeDir, syncStateFile))
	if readErr != nil {
		t.Fatalf("reading sync-state.toml: %v", readErr)
	}
	if !strings.Contains(string(data), `status = "incomplete"`) {
		t.Errorf("stamp must be incomplete, got:\n%s", data)
	}
	if _, statErr := os.Lstat(filepath.Join(galeDir, "current")); !os.IsNotExist(statErr) {
		t.Errorf("current must stay absent on timeout, err=%v", statErr)
	}
}

func TestIfNeededTimeoutLeavesCurrent(t *testing.T) {
	useCanceledIfNeeded(t)
	proj, galeDir := ifNeededProject(t, "[packages]\n  jq = \"1.7\"\n")
	storeRoot := filepath.Join(os.Getenv("HOME"), ".gale", "pkg")
	seedStore(t, storeRoot, "jq", "1.7-1")
	if err := generation.Build(
		map[string]string{"jq": "1.7-1"}, galeDir, storeRoot,
	); err != nil {
		t.Fatalf("seed generation: %v", err)
	}
	if cur, err := generation.Current(galeDir); err != nil || cur != 1 {
		t.Fatalf("setup: current = %d (err=%v), want 1", cur, err)
	}

	err := runSync(syncRun{ProjectDir: proj, IfNeeded: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runSync: %v, want context.Canceled", err)
	}
	cur, curErr := generation.Current(galeDir)
	if curErr != nil {
		t.Fatalf("read current: %v", curErr)
	}
	if cur != 1 {
		t.Errorf("current = %d, want 1 (timeout must not swap)", cur)
	}
}

// A completed no-op must not become incomplete just because the
// deadline expired after every package was already up to date.
// Stamping incomplete would withhold the next --if-needed for
// syncRetryInterval even though current is already correct.
func TestFinishSyncNoOpIgnoresExpiredContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rebuilt := false
	err := finishSync(ctx, syncFinish{}, func() error {
		rebuilt = true
		return nil
	})
	if err != nil {
		t.Fatalf("completed no-op: %v, want nil", err)
	}
	if rebuilt {
		t.Error("no-op must not rebuild")
	}
}

// Work that still needs a rebuild must not swap current after
// the deadline. Return the context error so the stamp is incomplete.
func TestFinishSyncExpiredContextSkipsRebuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rebuilt := false
	err := finishSync(ctx, syncFinish{installed: 1}, func() error {
		rebuilt = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("finishSync: %v, want context.Canceled", err)
	}
	if rebuilt {
		t.Error("timeout must not rebuild (leave current unchanged)")
	}
}

func TestSyncIfNeededInheritsBound(t *testing.T) {
	useCanceledIfNeeded(t)
	proj, galeDir := ifNeededProject(t, "[packages]\n  jq = \"1.7\"\n")
	t.Chdir(t.TempDir())

	var buf bytes.Buffer
	syncIfNeeded(&buf, proj)

	if !strings.Contains(buf.String(), "sync failed") {
		t.Errorf("syncIfNeeded must run the bound sync, got %q", buf.String())
	}
	data, err := os.ReadFile(filepath.Join(galeDir, syncStateFile))
	if err != nil {
		t.Fatalf("reading sync-state.toml: %v", err)
	}
	if !strings.Contains(string(data), `status = "incomplete"`) {
		t.Errorf("shell/run path must stamp incomplete, got:\n%s", data)
	}
}
