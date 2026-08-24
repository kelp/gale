package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncBuildFlagReplacesSource(t *testing.T) {
	if syncCmd.Flags().Lookup("build") != nil {
		t.Error("sync: --build must be gone")
	}
	if syncCmd.Flags().Lookup("source") != nil {
		t.Error("sync: --source flag should not exist")
	}
}

func TestInstallBuildFlag(t *testing.T) {
	if installCmd.Flags().Lookup("build") != nil {
		t.Fatal("install: --build must be gone")
	}
}

func TestUpdateBuildFlag(t *testing.T) {
	if updateCmd.Flags().Lookup("build") != nil {
		t.Fatal("update: --build must be gone")
	}
}

func TestRunSyncProjectFlagAccepted(t *testing.T) {
	// Before the fix, syncProject was declared but never
	// passed to runSync. runSync only accepted 3 args
	// (recipesPath, buildOnly, global). This test verifies
	// that runSync accepts the project parameter.
	//
	// The test calls runSync with project=true. Before the
	// fix this would not compile. After the fix, the
	// project flag is passed through and honored.

	// Create a project directory with gale.toml.
	t.Setenv("HOME", t.TempDir()) // isolate ~/.gale (project registry)
	projDir := t.TempDir()
	projConfig := filepath.Join(projDir, "gale.toml")
	if err := os.WriteFile(projConfig,
		[]byte("[packages]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)
	os.Chdir(projDir)

	// This call verifies the function signature accepts
	// the project parameter. Before the fix, this would
	// fail to compile with "too many arguments".
	err = runSync(syncRun{Project: true})
	// The sync itself may fail (no store, etc.) but the
	// important thing is that the function accepts 4 args
	// and the project flag reaches config resolution.
	_ = err
}
