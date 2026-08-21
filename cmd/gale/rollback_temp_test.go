package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/generation"
)

// rollbackTempProject is an isolated HOME + project with two
// generations (jq 1.1 then 1.2) so rollback has somewhere to go.
func rollbackTempProject(t *testing.T) (galeDir, storeRoot, configPath string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	proj := t.TempDir()
	configPath = filepath.Join(proj, "gale.toml")
	galeDir = filepath.Join(proj, ".gale")
	storeRoot = filepath.Join(home, ".gale", "pkg")
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	const gens = 2
	for i := 1; i <= gens; i++ {
		version := "1." + string(rune('0'+i))
		mkStorePkg(t, storeRoot, "jq", version+"-1")
		if err := os.WriteFile(configPath,
			[]byte("[packages]\njq = \""+version+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
			t.Fatalf("rebuildGeneration %d: %v", i, err)
		}
	}

	t.Chdir(proj)
	generationsGlobal = false
	generationsProject = false
	dryRun = false
	t.Cleanup(func() {
		generationsGlobal = false
		generationsProject = false
		dryRun = false
	})
	return galeDir, storeRoot, configPath
}

func TestRollbackPrintsTemporary(t *testing.T) {
	rollbackTempProject(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	runErr := genRollbackCmd.RunE(genRollbackCmd, nil)
	_ = w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatal(copyErr)
	}
	if runErr != nil {
		t.Fatalf("rollback: %v", runErr)
	}

	got := buf.String()
	for _, want := range []string{"temporary", "sync", "lock", "git"} {
		if !strings.Contains(strings.ToLower(got), want) {
			t.Errorf("rollback output must mention %q, got:\n%s", want, got)
		}
	}
}

func TestRollbackDoesNotWriteLockOrManifest(t *testing.T) {
	_, _, configPath := rollbackTempProject(t)
	lockPath := filepath.Join(filepath.Dir(configPath), "gale.lock")
	lockBytes := []byte("schema = 1\n# fixture lock\n")
	if err := os.WriteFile(lockPath, lockBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := genRollbackCmd.RunE(genRollbackCmd, nil); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	gotLock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotLock, lockBytes) {
		t.Errorf("gale.lock changed:\n%s", gotLock)
	}
	gotManifest, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotManifest, manifest) {
		t.Errorf("gale.toml changed:\n%s", gotManifest)
	}
}

func TestRollbackInvalidatesSyncStamp(t *testing.T) {
	galeDir, _, _ := rollbackTempProject(t)
	if err := recordSyncOutcome(syncOutcomeRecord{
		galeDir:     galeDir,
		fingerprint: "sha256:test",
		complete:    true,
		now:         stampTime,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(syncStatePath(galeDir)); err != nil {
		t.Fatalf("setup stamp: %v", err)
	}

	if err := genRollbackCmd.RunE(genRollbackCmd, nil); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := os.Stat(syncStatePath(galeDir)); !os.IsNotExist(err) {
		t.Errorf("stamp must be gone after rollback, err=%v", err)
	}

	if err := genRollbackCmd.RunE(genRollbackCmd, []string{"2"}); err != nil {
		t.Fatalf("rollback with no stamp: %v", err)
	}
}

func TestRollbackLeavesTheOtherScopeStamp(t *testing.T) {
	projGale, _, _ := rollbackTempProject(t)
	globalGale := filepath.Join(os.Getenv("HOME"), ".gale")
	if err := os.MkdirAll(globalGale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordSyncOutcome(syncOutcomeRecord{
		galeDir: projGale, fingerprint: "sha256:proj", complete: true, now: stampTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordSyncOutcome(syncOutcomeRecord{
		galeDir: globalGale, fingerprint: "sha256:global", complete: true, now: stampTime,
	}); err != nil {
		t.Fatal(err)
	}

	if err := genRollbackCmd.RunE(genRollbackCmd, nil); err != nil {
		t.Fatalf("project rollback: %v", err)
	}
	if _, err := os.Stat(syncStatePath(projGale)); !os.IsNotExist(err) {
		t.Errorf("project stamp must be gone, err=%v", err)
	}
	if _, err := os.Stat(syncStatePath(globalGale)); err != nil {
		t.Errorf("global stamp must remain: %v", err)
	}
}

func TestRollbackDryRunLeavesStamp(t *testing.T) {
	galeDir, _, _ := rollbackTempProject(t)
	if err := recordSyncOutcome(syncOutcomeRecord{
		galeDir: galeDir, fingerprint: "sha256:test", complete: true, now: stampTime,
	}); err != nil {
		t.Fatal(err)
	}
	dryRun = true
	if err := genRollbackCmd.RunE(genRollbackCmd, nil); err != nil {
		t.Fatalf("dry-run rollback: %v", err)
	}
	if _, err := os.Stat(syncStatePath(galeDir)); err != nil {
		t.Errorf("dry-run must not delete the stamp: %v", err)
	}
}

func TestIfNeededAfterRollbackReturnsToLock(t *testing.T) {
	galeDir, storeRoot, configPath := rollbackTempProject(t)
	if err := recordSyncOutcome(syncOutcomeRecord{
		galeDir: galeDir, fingerprint: "sha256:test", complete: true, now: stampTime,
	}); err != nil {
		t.Fatal(err)
	}
	if cur, err := generation.Current(galeDir); err != nil || cur != 2 {
		t.Fatalf("setup: current = %d (err=%v), want 2", cur, err)
	}

	if err := genRollbackCmd.RunE(genRollbackCmd, nil); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if cur, err := generation.Current(galeDir); err != nil || cur != 1 {
		t.Fatalf("after rollback: current = %d (err=%v), want 1", cur, err)
	}

	check := syncNeeded(galeDir, "sha256:test", stampTime)
	if !check.Needed {
		t.Fatalf(" --if-needed must run after rollback, reason=%q", check.Reason)
	}

	if err := rebuildGeneration(galeDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("sync after rollback: %v", err)
	}
	cur, err := generation.Current(galeDir)
	if err != nil {
		t.Fatal(err)
	}
	if cur != 3 {
		t.Errorf("current = %d, want 3 (sync must leave the rolled-back gen)", cur)
	}
	got := readGenJQ(t, galeDir, cur)
	want := readGenJQ(t, galeDir, 2)
	if got != want {
		t.Errorf("current jq = %q, want the lock/manifest revision %q", got, want)
	}
}
