package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
		"-c", "user.email=test@test", "commit", "-m", "init")
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

// setupCachedRepo creates a directory under cacheRoot/name/recipes
// with the given recipe files, simulating an already-cloned repo.
func setupCachedRepo(
	t *testing.T, cacheRoot, name string,
	recipes map[string]string,
) {
	t.Helper()
	recipesDir := filepath.Join(cacheRoot, name, "recipes")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatalf("failed to create recipes dir: %v", err)
	}
	for fname, content := range recipes {
		p := filepath.Join(recipesDir, fname)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", fname, err)
		}
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
		Name:     "core",
		URL:      bareURL,
		Priority: 1,
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
		Name:     "core",
		URL:      bareURL,
		Priority: 1,
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
		Name:     "core",
		URL:      bareURL,
		Priority: 1,
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
		"-c", "user.email=test@test", "commit", "-m", "add ripgrep")
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

// --- Behavior 3: Search recipes by name ---

func TestSearchFindsExactMatch(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml":      "[package]\nname = \"jq\"\n",
		"ripgrep.toml": "[package]\nname = \"ripgrep\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.Search("jq")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search results = %d, want 1", len(results))
	}
	if results[0].Package != "jq" {
		t.Errorf("Package = %q, want %q", results[0].Package, "jq")
	}
}

func TestSearchFindsSubstringMatch(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"python3.toml": "[package]\nname = \"python3\"\n",
		"python2.toml": "[package]\nname = \"python2\"\n",
		"ruby.toml":    "[package]\nname = \"ruby\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.Search("py")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search results = %d, want 2", len(results))
	}
}

func TestSearchReturnsEmptyForNoMatch(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.Search("nonexistent")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Search results = %d, want 0", len(results))
	}
}

func TestSearchAcrossMultipleRepos(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})
	setupCachedRepo(t, cacheRoot, "community", map[string]string{
		"jq-extras.toml": "[package]\nname = \"jq-extras\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})
	m.AddRepo(RepoConfig{
		Name:     "community",
		CacheDir: filepath.Join(cacheRoot, "community"),
		Priority: 2,
	})

	results, err := m.Search("jq")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search results = %d, want 2", len(results))
	}
}

func TestSearchResultIncludesRepoName(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.Search("jq")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search results = %d, want 1", len(results))
	}
	if results[0].RepoName != "core" {
		t.Errorf("RepoName = %q, want %q",
			results[0].RepoName, "core")
	}
}

func TestSearchResultIncludesFilePath(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.Search("jq")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search results = %d, want 1", len(results))
	}
	want := filepath.Join(cacheRoot, "core", "recipes", "jq.toml")
	if results[0].FilePath != want {
		t.Errorf("FilePath = %q, want %q",
			results[0].FilePath, want)
	}
}

// --- Behavior 4: Priority resolution ---

func TestResolveReturnsHighestPriority(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})
	setupCachedRepo(t, cacheRoot, "community", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})
	m.AddRepo(RepoConfig{
		Name:     "community",
		CacheDir: filepath.Join(cacheRoot, "community"),
		Priority: 2,
	})

	result, err := m.Resolve("jq")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.RepoName != "core" {
		t.Errorf("RepoName = %q, want %q",
			result.RepoName, "core")
	}
}

func TestResolveReturnsLowerPriorityNumber(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "alpha", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})
	setupCachedRepo(t, cacheRoot, "beta", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	m := NewManager(cacheRoot)
	// Add beta (priority 5) first, then alpha (priority 2).
	// Resolve should return alpha regardless of add order.
	m.AddRepo(RepoConfig{
		Name:     "beta",
		CacheDir: filepath.Join(cacheRoot, "beta"),
		Priority: 5,
	})
	m.AddRepo(RepoConfig{
		Name:     "alpha",
		CacheDir: filepath.Join(cacheRoot, "alpha"),
		Priority: 2,
	})

	result, err := m.Resolve("jq")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.RepoName != "alpha" {
		t.Errorf("RepoName = %q, want %q",
			result.RepoName, "alpha")
	}
	if result.Priority != 2 {
		t.Errorf("Priority = %d, want 2", result.Priority)
	}
}

func TestResolveReturnsNilForNotFound(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	result, err := m.Resolve("nonexistent")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
}

func TestResolveUsesExactFilenameMatch(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml":      "[package]\nname = \"jq\"\n",
		"jq-next.toml": "[package]\nname = \"jq-next\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	result, err := m.Resolve("jq")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Package != "jq" {
		t.Errorf("Package = %q, want %q", result.Package, "jq")
	}
}

// --- Behavior 5: List available recipes ---

func TestListAllReturnsAllRecipes(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml":      "[package]\nname = \"jq\"\n",
		"ripgrep.toml": "[package]\nname = \"ripgrep\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.ListAll()
	if err != nil {
		t.Fatalf("ListAll error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("ListAll results = %d, want 2", len(results))
	}
}

func TestListAllAcrossMultipleRepos(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})
	setupCachedRepo(t, cacheRoot, "community", map[string]string{
		"ripgrep.toml": "[package]\nname = \"ripgrep\"\n",
		"fd.toml":      "[package]\nname = \"fd\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})
	m.AddRepo(RepoConfig{
		Name:     "community",
		CacheDir: filepath.Join(cacheRoot, "community"),
		Priority: 2,
	})

	results, err := m.ListAll()
	if err != nil {
		t.Fatalf("ListAll error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("ListAll results = %d, want 3", len(results))
	}
}

func TestListAllReturnsEmptyForNoRepos(t *testing.T) {
	cacheRoot := t.TempDir()
	m := NewManager(cacheRoot)

	results, err := m.ListAll()
	if err != nil {
		t.Fatalf("ListAll error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("ListAll results = %d, want 0", len(results))
	}
}

func TestListAllIncludesRepoName(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.ListAll()
	if err != nil {
		t.Fatalf("ListAll error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ListAll results = %d, want 1", len(results))
	}
	if results[0].RepoName != "core" {
		t.Errorf("RepoName = %q, want %q",
			results[0].RepoName, "core")
	}
}

func TestListAllIncludesPriority(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"jq.toml": "[package]\nname = \"jq\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 3,
	})

	results, err := m.ListAll()
	if err != nil {
		t.Fatalf("ListAll error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ListAll results = %d, want 1", len(results))
	}
	if results[0].Priority != 3 {
		t.Errorf("Priority = %d, want 3", results[0].Priority)
	}
}

// --- Behavior 6: Letter subdirectory support ---

func setupCachedRepoWithLetterDirs(
	t *testing.T, cacheRoot, name string,
	recipes map[string]string,
) {
	t.Helper()
	for fname, content := range recipes {
		letter := string(fname[0])
		dir := filepath.Join(cacheRoot, name, "recipes", letter)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		p := filepath.Join(dir, fname)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", fname, err)
		}
	}
}

func TestSearchFindsRecipesInLetterSubdirs(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepoWithLetterDirs(t, cacheRoot, "core", map[string]string{
		"jq.toml":      "[package]\nname = \"jq\"\n",
		"ripgrep.toml": "[package]\nname = \"ripgrep\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.Search("jq")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search results = %d, want 1", len(results))
	}
	if results[0].Package != "jq" {
		t.Errorf("Package = %q, want %q", results[0].Package, "jq")
	}
}

func TestListAllFindsRecipesInLetterSubdirs(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepoWithLetterDirs(t, cacheRoot, "core", map[string]string{
		"jq.toml":      "[package]\nname = \"jq\"\n",
		"ripgrep.toml": "[package]\nname = \"ripgrep\"\n",
		"bat.toml":     "[package]\nname = \"bat\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.ListAll()
	if err != nil {
		t.Fatalf("ListAll error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("ListAll results = %d, want 3", len(results))
	}
}

func TestListAllSortsByPackageName(t *testing.T) {
	cacheRoot := t.TempDir()
	setupCachedRepo(t, cacheRoot, "core", map[string]string{
		"zsh.toml":  "[package]\nname = \"zsh\"\n",
		"awk.toml":  "[package]\nname = \"awk\"\n",
		"make.toml": "[package]\nname = \"make\"\n",
	})

	m := NewManager(cacheRoot)
	m.AddRepo(RepoConfig{
		Name:     "core",
		CacheDir: filepath.Join(cacheRoot, "core"),
		Priority: 1,
	})

	results, err := m.ListAll()
	if err != nil {
		t.Fatalf("ListAll error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("ListAll results = %d, want 3", len(results))
	}

	// Verify sorted by package name.
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Package
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("results not sorted by package name: %v", names)
	}
}
