package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInitDryRunDoesNotCreateFiles verifies that `gale init
// --dry-run` does not create gale.toml, .envrc, or modify
// .gitignore.
func TestInitDryRunDoesNotCreateFiles(t *testing.T) {
	dir := t.TempDir()

	orig, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(orig) })

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	if err := initCmd.RunE(initCmd, nil); err != nil {
		t.Fatalf("init command failed: %v", err)
	}

	for _, name := range []string{"gale.toml", ".envrc", ".gitignore"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after --dry-run init", name)
		}
	}
}
