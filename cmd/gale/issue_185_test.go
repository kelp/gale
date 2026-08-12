package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kelp/gale/internal/farm"
	"github.com/kelp/gale/internal/projects"
)

// gh#185 asked for two properties of the shared farm, and both hold
// on main already: the rebuild takes the guarded UNION of the
// proposed closure and every registered scope's claim
// (guardedRebuildDirs -> FarmClaimants -> projects.Scopes), and
// there is one farm, reached from the store root
// (farm.DirFromStoreRoot), with no accessor that can be handed a
// scope's own gale dir.
//
// This is the regression pin for the first of them, at the layer
// where a project scope actually exists: a global rebuild that has
// never heard of the project must leave the project's soname
// resolvable, because the project's binaries resolve it through the
// one shared farm right now.
//
// The acknowledged residue stays out of scope: an UNREGISTERED
// project claims nothing, and neither does an already-open shell
// whose generation has moved on (farmclaims.go:28-29).

// projectSoname is the versioned dylib basename this test farms,
// spelled for the running OS.
func projectSoname() string {
	if runtime.GOOS == "linux" {
		return "libcurl.so.4"
	}
	return "libcurl.4.dylib"
}

// TestGlobalRebuildKeepsARegisteredProjectsFarmEntry pins gh#185.
func TestGlobalRebuildKeepsARegisteredProjectsFarmEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALE_OFFLINE", "1")
	globalDir := filepath.Join(home, ".gale")
	storeRoot := filepath.Join(globalDir, "pkg")

	// The project's package provides the dylib; the global package
	// provides none and shares nothing with it.
	curlDir := filepath.Join(storeRoot, "curl", "8.19.0-1")
	if err := os.MkdirAll(filepath.Join(curlDir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(curlDir, "lib", projectSoname()), []byte("x"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(
		filepath.Join(storeRoot, "jq", "1.7.1-1", "bin"), 0o755,
	); err != nil {
		t.Fatal(err)
	}

	proj := makeRegisteredProject(
		t, storeRoot, "[packages]\ncurl = \"8.19.0-1\"\n",
		"curl", "8.19.0-1", "curl",
	)
	if err := projects.Register(globalDir, proj); err != nil {
		t.Fatal(err)
	}

	// The farm as the project left it.
	farmDir := farm.DirFromStoreRoot(storeRoot)
	if err := farm.Populate(curlDir, farmDir); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(globalDir, "gale.toml")
	if err := os.WriteFile(
		configPath, []byte("[packages]\njq = \"1.7.1-1\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := rebuildGeneration(globalDir, storeRoot, configPath, nil); err != nil {
		t.Fatalf("rebuildGeneration: %v", err)
	}

	entry := filepath.Join(farmDir, projectSoname())
	target, err := os.Readlink(entry)
	if err != nil {
		t.Fatalf("a global rebuild deleted the registered project's "+
			"farm entry: %v", err)
	}
	if want := filepath.Join(curlDir, "lib", projectSoname()); target != want {
		t.Errorf("farm entry -> %q, want %q", target, want)
	}
	if _, err := os.Stat(entry); err != nil {
		t.Errorf("the project's soname no longer resolves: %v", err)
	}
}
