package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Behavior 1: Build PATH from packages ---

func TestBuildPATHSinglePackage(t *testing.T) {
	got := BuildPATH("/store", map[string]string{"jq": "1.7.1"})
	want := "/store/jq/1.7.1/bin"
	if got != want {
		t.Errorf("BuildPATH = %q, want %q", got, want)
	}
}

func TestBuildPATHMultiplePackages(t *testing.T) {
	pkgs := map[string]string{
		"jq":      "1.7.1",
		"ripgrep": "14.0",
	}
	got := BuildPATH("/store", pkgs)

	if !strings.Contains(got, "/store/jq/1.7.1/bin") {
		t.Errorf("PATH missing jq entry: %q", got)
	}
	if !strings.Contains(got, "/store/ripgrep/14.0/bin") {
		t.Errorf("PATH missing ripgrep entry: %q", got)
	}
}

func TestBuildPATHSeparator(t *testing.T) {
	pkgs := map[string]string{
		"jq":      "1.7.1",
		"ripgrep": "14.0",
	}
	got := BuildPATH("/store", pkgs)
	if !strings.Contains(got, string(os.PathListSeparator)) {
		t.Errorf("PATH missing separator: %q", got)
	}
}

func TestBuildPATHEmptyPackagesAndNonEmpty(t *testing.T) {
	// Empty input should produce empty output.
	got := BuildPATH("/store", map[string]string{})
	if got != "" {
		t.Errorf("BuildPATH(empty) = %q, want empty string", got)
	}

	// Non-empty input must produce non-empty output.
	got = BuildPATH("/store", map[string]string{"jq": "1.7.1"})
	if !strings.Contains(got, "/store/jq/1.7.1/bin") {
		t.Errorf("BuildPATH(jq) = %q, want containing %q",
			got, "/store/jq/1.7.1/bin")
	}
}

func TestBuildPATHNilPackagesAndNonNil(t *testing.T) {
	// Nil input should produce empty output.
	got := BuildPATH("/store", nil)
	if got != "" {
		t.Errorf("BuildPATH(nil) = %q, want empty string", got)
	}

	// Non-nil input must produce non-empty output.
	got = BuildPATH("/store", map[string]string{"fd": "9.0"})
	if !strings.Contains(got, "/store/fd/9.0/bin") {
		t.Errorf("BuildPATH(fd) = %q, want containing %q",
			got, "/store/fd/9.0/bin")
	}
}

// --- Behavior 2: Merge global and project packages ---

func TestMergePackagesProjectOverridesGlobal(t *testing.T) {
	global := map[string]string{"python": "3.12"}
	project := map[string]string{"python": "3.11"}

	merged := MergePackages(global, project)
	if merged["python"] != "3.11" {
		t.Errorf("merged[python] = %q, want %q",
			merged["python"], "3.11")
	}
}

func TestMergePackagesIncludesGlobalOnly(t *testing.T) {
	global := map[string]string{"jq": "1.7.1"}
	project := map[string]string{"ripgrep": "14.0"}

	merged := MergePackages(global, project)
	if merged["jq"] != "1.7.1" {
		t.Errorf("merged[jq] = %q, want %q",
			merged["jq"], "1.7.1")
	}
}

func TestMergePackagesIncludesProjectOnly(t *testing.T) {
	global := map[string]string{"jq": "1.7.1"}
	project := map[string]string{"ripgrep": "14.0"}

	merged := MergePackages(global, project)
	if merged["ripgrep"] != "14.0" {
		t.Errorf("merged[ripgrep] = %q, want %q",
			merged["ripgrep"], "14.0")
	}
}

func TestMergePackagesCount(t *testing.T) {
	global := map[string]string{"jq": "1.7.1", "python": "3.12"}
	project := map[string]string{"ripgrep": "14.0", "python": "3.11"}

	merged := MergePackages(global, project)
	if len(merged) != 3 {
		t.Errorf("merged length = %d, want 3", len(merged))
	}
}

func TestMergePackagesNilGlobal(t *testing.T) {
	project := map[string]string{"jq": "1.7.1"}

	merged := MergePackages(nil, project)
	if merged["jq"] != "1.7.1" {
		t.Errorf("merged[jq] = %q, want %q",
			merged["jq"], "1.7.1")
	}
}

func TestMergePackagesNilProject(t *testing.T) {
	global := map[string]string{"jq": "1.7.1"}

	merged := MergePackages(global, nil)
	if merged["jq"] != "1.7.1" {
		t.Errorf("merged[jq] = %q, want %q",
			merged["jq"], "1.7.1")
	}
}

func TestMergePackagesDoesNotMutateGlobal(t *testing.T) {
	global := map[string]string{"python": "3.12"}
	project := map[string]string{"python": "3.11"}

	merged := MergePackages(global, project)
	if global["python"] != "3.12" {
		t.Errorf("global was mutated: python = %q, want %q",
			global["python"], "3.12")
	}
	// Also verify the merge result is correct.
	if merged["python"] != "3.11" {
		t.Errorf("merged[python] = %q, want %q",
			merged["python"], "3.11")
	}
}

// --- Behavior 3: Merge environment variables ---

func TestBuildEnvironmentVars(t *testing.T) {
	vars := map[string]string{
		"DATABASE_URL": "postgres://localhost/myapp",
		"LOG_LEVEL":    "debug",
	}
	env := BuildEnvironment("/store", nil, nil, vars)
	if env == nil {
		t.Fatal("expected non-nil Environment")
	}
	if env.Vars["DATABASE_URL"] != "postgres://localhost/myapp" {
		t.Errorf("Vars[DATABASE_URL] = %q, want %q",
			env.Vars["DATABASE_URL"],
			"postgres://localhost/myapp")
	}
}

func TestBuildEnvironmentVarsLogLevel(t *testing.T) {
	vars := map[string]string{
		"LOG_LEVEL": "debug",
	}
	env := BuildEnvironment("/store", nil, nil, vars)
	if env == nil {
		t.Fatal("expected non-nil Environment")
	}
	if env.Vars["LOG_LEVEL"] != "debug" {
		t.Errorf("Vars[LOG_LEVEL] = %q, want %q",
			env.Vars["LOG_LEVEL"], "debug")
	}
}

func TestBuildEnvironmentPATH(t *testing.T) {
	global := map[string]string{"jq": "1.7.1"}
	project := map[string]string{"ripgrep": "14.0"}
	env := BuildEnvironment("/store", global, project, nil)
	if env == nil {
		t.Fatal("expected non-nil Environment")
	}
	if !strings.Contains(env.PATH, "/store/jq/1.7.1/bin") {
		t.Errorf("PATH missing jq entry: %q", env.PATH)
	}
	if !strings.Contains(env.PATH, "/store/ripgrep/14.0/bin") {
		t.Errorf("PATH missing ripgrep entry: %q", env.PATH)
	}
}

func TestBuildEnvironmentProjectOverridesGlobalInPATH(t *testing.T) {
	global := map[string]string{"python": "3.12"}
	project := map[string]string{"python": "3.11"}
	env := BuildEnvironment("/store", global, project, nil)
	if env == nil {
		t.Fatal("expected non-nil Environment")
	}
	if !strings.Contains(env.PATH, "/store/python/3.11/bin") {
		t.Errorf("PATH should contain project version: %q",
			env.PATH)
	}
	if strings.Contains(env.PATH, "/store/python/3.12/bin") {
		t.Errorf("PATH should not contain global version: %q",
			env.PATH)
	}
}

func TestBuildEnvironmentNilVarsProducesEmptyMap(t *testing.T) {
	env := BuildEnvironment("/store", nil, nil, nil)
	if env == nil {
		t.Fatal("expected non-nil Environment")
	}
	if env.Vars == nil {
		t.Fatal("expected non-nil Vars map")
	}
	if len(env.Vars) != 0 {
		t.Errorf("Vars length = %d, want 0", len(env.Vars))
	}
}

// --- Behavior 4: Generate fish shell hook ---

func TestGenerateHookFishNoError(t *testing.T) {
	hook, err := GenerateHook("fish")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook == "" {
		t.Error("expected non-empty fish hook output")
	}
}

func TestGenerateHookFishContainsSetGx(t *testing.T) {
	hook, err := GenerateHook("fish")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "set -gx") {
		t.Errorf("fish hook missing 'set -gx': %q", hook)
	}
}

func TestGenerateHookFishContainsHookFunction(t *testing.T) {
	hook, err := GenerateHook("fish")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "_gale_hook") {
		t.Errorf("fish hook missing '_gale_hook': %q", hook)
	}
}

func TestGenerateHookFishContainsPATH(t *testing.T) {
	hook, err := GenerateHook("fish")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "PATH") {
		t.Errorf("fish hook missing 'PATH': %q", hook)
	}
}

// --- Behavior 5: Generate zsh shell hook ---

func TestGenerateHookZshNoError(t *testing.T) {
	hook, err := GenerateHook("zsh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook == "" {
		t.Error("expected non-empty zsh hook output")
	}
}

func TestGenerateHookZshContainsExport(t *testing.T) {
	hook, err := GenerateHook("zsh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "export") {
		t.Errorf("zsh hook missing 'export': %q", hook)
	}
}

func TestGenerateHookZshContainsHookFunction(t *testing.T) {
	hook, err := GenerateHook("zsh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "_gale_hook") {
		t.Errorf("zsh hook missing '_gale_hook': %q", hook)
	}
}

func TestGenerateHookZshContainsPATH(t *testing.T) {
	hook, err := GenerateHook("zsh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "PATH") {
		t.Errorf("zsh hook missing 'PATH': %q", hook)
	}
}

// --- Behavior 6: Generate bash shell hook ---

func TestGenerateHookBashNoError(t *testing.T) {
	hook, err := GenerateHook("bash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook == "" {
		t.Error("expected non-empty bash hook output")
	}
}

func TestGenerateHookBashContainsExport(t *testing.T) {
	hook, err := GenerateHook("bash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "export") {
		t.Errorf("bash hook missing 'export': %q", hook)
	}
}

func TestGenerateHookBashContainsHookFunction(t *testing.T) {
	hook, err := GenerateHook("bash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "_gale_hook") {
		t.Errorf("bash hook missing '_gale_hook': %q", hook)
	}
}

func TestGenerateHookBashContainsPATH(t *testing.T) {
	hook, err := GenerateHook("bash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hook, "PATH") {
		t.Errorf("bash hook missing 'PATH': %q", hook)
	}
}

func TestGenerateHookUnsupportedShellReturnsError(t *testing.T) {
	_, err := GenerateHook("powershell")
	if err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}

// --- Behavior 7: Detect gale.toml in directory ---

func TestDetectGaleConfigInCurrentDir(t *testing.T) {
	dir := t.TempDir()
	galePath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	found, err := DetectConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != galePath {
		t.Errorf("DetectConfig = %q, want %q", found, galePath)
	}
}

func TestDetectGaleConfigInParentDir(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "subdir")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	galePath := filepath.Join(parent, "gale.toml")
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	found, err := DetectConfig(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != galePath {
		t.Errorf("DetectConfig = %q, want %q", found, galePath)
	}
}

func TestDetectGaleConfigNotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := DetectConfig(dir)
	if err == nil {
		t.Fatal("expected error when no gale.toml exists")
	}
}
