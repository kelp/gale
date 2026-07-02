package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupBareRepo creates a bare git repo with recipe TOML files.
// It returns the path to the bare repo. The recipes parameter is a
// map of filename to file content placed under recipes/.
func setupBareRepo(t *testing.T, recipes map[string]string) string {
	t.Helper()

	bare := filepath.Join(t.TempDir(), "bare.git")
	run(t, "git", "init", "--bare", bare)

	// Create a temporary working clone to add files.
	work := filepath.Join(t.TempDir(), "work")
	run(t, "git", "clone", bare, work)

	recipesDir := filepath.Join(work, "recipes")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatalf("failed to create recipes dir: %v", err)
	}

	for name, content := range recipes {
		p := filepath.Join(recipesDir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	gitIn(t, work, "add", ".")
	gitIn(t, work, "-c", "user.name=test",
		"-c", "user.email=test@test",
		"-c", "commit.gpgsign=false", "commit", "-m", "init")
	gitIn(t, work, "push")

	return bare
}

// run executes a command and fails the test on error.
func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed: %v\n%s", cmd.Args, err, out)
	}
}

// gitIn executes a git command in a specific directory.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s",
			args, dir, err, out)
	}
}

// --- Behavior 1: Clone recipe repo ---

func TestCloneClonesRepoToCache(t *testing.T) {
	bareURL := setupBareRepo(t, map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	cacheRoot := t.TempDir()
	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name: "core",
		URL:  bareURL,
	})

	if err := m.Clone("core"); err != nil {
		t.Fatalf("Clone error: %v", err)
	}

	// Verify the recipes directory exists in the cache.
	recipesDir := filepath.Join(cacheRoot, "core", "recipes")
	info, err := os.Stat(recipesDir)
	if err != nil {
		t.Fatalf("recipes dir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", recipesDir)
	}
}

func TestCloneCreatesRecipeFile(t *testing.T) {
	bareURL := setupBareRepo(t, map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	cacheRoot := t.TempDir()
	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name: "core",
		URL:  bareURL,
	})

	if err := m.Clone("core"); err != nil {
		t.Fatalf("Clone error: %v", err)
	}

	recipeFile := filepath.Join(cacheRoot, "core", "recipes", "jq.toml")
	if _, err := os.Stat(recipeFile); err != nil {
		t.Errorf("expected recipe file %q to exist: %v",
			recipeFile, err)
	}
}

func TestCloneUnknownRepoReturnsError(t *testing.T) {
	cacheRoot := t.TempDir()
	m := NewManager(cacheRoot)

	err := m.Clone("nonexistent")
	if err == nil {
		t.Fatal("expected error when cloning unknown repo")
	}
}

// --- Behavior 2: Fetch updates ---

func TestFetchPicksUpNewRecipe(t *testing.T) {
	bareURL := setupBareRepo(t, map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	cacheRoot := t.TempDir()
	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name: "core",
		URL:  bareURL,
	})

	if err := m.Clone("core"); err != nil {
		t.Fatalf("Clone error: %v", err)
	}

	// Add a new recipe to the bare repo via a temporary clone.
	work := filepath.Join(t.TempDir(), "work2")
	run(t, "git", "clone", bareURL, work)
	recipesDir := filepath.Join(work, "recipes")
	newFile := filepath.Join(recipesDir, "ripgrep.toml")
	if err := os.WriteFile(newFile,
		[]byte("[package]\nname = \"ripgrep\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write new recipe: %v", err)
	}
	gitIn(t, work, "add", ".")
	gitIn(t, work, "-c", "user.name=test",
		"-c", "user.email=test@test",
		"-c", "commit.gpgsign=false", "commit", "-m", "add ripgrep")
	gitIn(t, work, "push")

	// Fetch should pick up the new recipe.
	if err := m.Fetch("core"); err != nil {
		t.Fatalf("Fetch error: %v", err)
	}

	newRecipe := filepath.Join(cacheRoot, "core", "recipes", "ripgrep.toml")
	if _, err := os.Stat(newRecipe); err != nil {
		t.Errorf("expected new recipe %q after fetch: %v",
			newRecipe, err)
	}
}

func TestFetchUnknownRepoReturnsError(t *testing.T) {
	cacheRoot := t.TempDir()
	m := NewManager(cacheRoot)

	err := m.Fetch("nonexistent")
	if err == nil {
		t.Fatal("expected error when fetching unknown repo")
	}
}
