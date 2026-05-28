package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRepoAddDryRunDoesNotMutate verifies that `gale repo add
// --dry-run` neither writes the config nor clones the cache.
func TestRepoAddDryRunDoesNotMutate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	args := []string{"mytap", "https://example.invalid/mytap.git"}
	if err := repoAddCmd.RunE(repoAddCmd, args); err != nil {
		t.Fatalf("repo add failed: %v", err)
	}

	cfg := filepath.Join(home, ".gale", "config.toml")
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("%s exists after --dry-run", cfg)
	}
	cache := filepath.Join(home, ".gale", "repos", "mytap")
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("%s exists after --dry-run", cache)
	}
}

// TestRepoRemoveDryRunDoesNotMutate verifies that `gale repo
// remove --dry-run` leaves the config and cache intact.
func TestRepoRemoveDryRunDoesNotMutate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	galeDir := filepath.Join(home, ".gale")
	if err := os.MkdirAll(galeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(galeDir, "config.toml")
	body := "[[repos]]\nname = \"mytap\"\n" +
		"url = \"https://example.invalid/mytap.git\"\n" +
		"priority = 0\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(galeDir, "repos", "mytap")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(cacheDir, "marker")
	if err := os.WriteFile(sentinel, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	if err := repoRemoveCmd.RunE(
		repoRemoveCmd, []string{"mytap"},
	); err != nil {
		t.Fatalf("repo remove failed: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Errorf("config.toml mutated under --dry-run:\n%s",
			string(data))
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Error("cache dir removed under --dry-run")
	}
}

// TestRepoInitDryRunDoesNotCreateDir verifies that `gale repo
// init --dry-run` does not create the repo directory.
func TestRepoInitDryRunDoesNotCreateDir(t *testing.T) {
	dir := t.TempDir()

	orig, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(orig) })

	dryRun = true
	t.Cleanup(func() { dryRun = false })

	if err := repoInitCmd.RunE(
		repoInitCmd, []string{"newrepo"},
	); err != nil {
		t.Fatalf("repo init failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "newrepo")); !os.IsNotExist(err) {
		t.Error("newrepo dir created under --dry-run")
	}
}
