package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// --- Behavior 1: Parse gale.toml with packages ---

const galeWithPackages = `
[packages]
jq = "1.7.1"
ripgrep = "latest"
`

func TestParseGaleConfigPackagesNotNil(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithPackages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Packages == nil {
		t.Fatal("expected non-nil Packages map")
	}
}

func TestParseGaleConfigPackagesCount(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithPackages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Packages) != 2 {
		t.Errorf("Packages length = %d, want 2", len(cfg.Packages))
	}
}

func TestParseGaleConfigPackageJqVersion(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithPackages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Packages["jq"] != "1.7.1" {
		t.Errorf("Packages[jq] = %q, want %q",
			cfg.Packages["jq"], "1.7.1")
	}
}

func TestParseGaleConfigPackageRipgrepVersion(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithPackages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Packages["ripgrep"] != "latest" {
		t.Errorf("Packages[ripgrep] = %q, want %q",
			cfg.Packages["ripgrep"], "latest")
	}
}

// --- Behavior 2: Parse gale.toml with vars ---

const galeWithVars = `
[vars]
DATABASE_URL = "postgres://localhost/myapp"
LOG_LEVEL = "debug"
`

func TestParseGaleConfigVarsNotNil(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithVars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Vars == nil {
		t.Fatal("expected non-nil Vars map")
	}
}

func TestParseGaleConfigVarsCount(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithVars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Vars) != 2 {
		t.Errorf("Vars length = %d, want 2", len(cfg.Vars))
	}
}

func TestParseGaleConfigVarsDatabaseURL(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithVars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	want := "postgres://localhost/myapp"
	if cfg.Vars["DATABASE_URL"] != want {
		t.Errorf("Vars[DATABASE_URL] = %q, want %q",
			cfg.Vars["DATABASE_URL"], want)
	}
}

func TestParseGaleConfigVarsLogLevel(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithVars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Vars["LOG_LEVEL"] != "debug" {
		t.Errorf("Vars[LOG_LEVEL] = %q, want %q",
			cfg.Vars["LOG_LEVEL"], "debug")
	}
}

// --- Behavior 3: Parse config.toml with repos ---

const appConfigWithRepos = `
[[repos]]
name = "core"
url = "https://github.com/kelp/gale-recipes"
key = "gale-ed25519:abc123"
priority = 1

[[repos]]
name = "community"
url = "https://github.com/acme/gale-recipes"
key = "gale-ed25519:def456..."
priority = 2
`

func TestParseAppConfigReposCount(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithRepos)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Repos) != 2 {
		t.Errorf("Repos length = %d, want 2", len(cfg.Repos))
	}
}

func TestParseAppConfigRepoName(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithRepos)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Repos[0].Name != "core" {
		t.Errorf("Repos[0].Name = %q, want %q",
			cfg.Repos[0].Name, "core")
	}
}

func TestParseAppConfigRepoURL(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithRepos)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	want := "https://github.com/kelp/gale-recipes"
	if cfg.Repos[0].URL != want {
		t.Errorf("Repos[0].URL = %q, want %q",
			cfg.Repos[0].URL, want)
	}
}

func TestParseAppConfigRepoKey(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithRepos)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Repos[0].Key != "gale-ed25519:abc123" {
		t.Errorf("Repos[0].Key = %q, want %q",
			cfg.Repos[0].Key, "gale-ed25519:abc123")
	}
}

func TestParseAppConfigRepoPriority(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithRepos)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Repos[0].Priority != 1 {
		t.Errorf("Repos[0].Priority = %d, want 1",
			cfg.Repos[0].Priority)
	}
}

func TestParseAppConfigSecondRepo(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithRepos)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Repos[1].Name != "community" {
		t.Errorf("Repos[1].Name = %q, want %q",
			cfg.Repos[1].Name, "community")
	}
	wantURL := "https://github.com/acme/gale-recipes"
	if cfg.Repos[1].URL != wantURL {
		t.Errorf("Repos[1].URL = %q, want %q",
			cfg.Repos[1].URL, wantURL)
	}
	wantKey := "gale-ed25519:def456..."
	if cfg.Repos[1].Key != wantKey {
		t.Errorf("Repos[1].Key = %q, want %q",
			cfg.Repos[1].Key, wantKey)
	}
	if cfg.Repos[1].Priority != 2 {
		t.Errorf("Repos[1].Priority = %d, want 2",
			cfg.Repos[1].Priority)
	}
}

// --- Behavior 4: Parse config.toml with AI settings ---

const appConfigWithAnthropic = `
[anthropic]
api_key = "sk-ant-test123"
prompt_file = "~/.gale/recipe-prompt.md"
`

func TestParseAppConfigAnthropicAPIKey(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithAnthropic)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Anthropic.APIKey != "sk-ant-test123" {
		t.Errorf("Anthropic.APIKey = %q, want %q",
			cfg.Anthropic.APIKey, "sk-ant-test123")
	}
}

func TestParseAppConfigAnthropicPromptFile(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithAnthropic)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Anthropic.PromptFile != "~/.gale/recipe-prompt.md" {
		t.Errorf("Anthropic.PromptFile = %q, want %q",
			cfg.Anthropic.PromptFile,
			"~/.gale/recipe-prompt.md")
	}
}

// --- Behavior 5: Find gale.toml by walking up directories ---

func TestFindGaleConfigInCurrentDir(t *testing.T) {
	dir := t.TempDir()
	galePath := filepath.Join(dir, "gale.toml")
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	found, err := FindGaleConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != galePath {
		t.Errorf("FindGaleConfig = %q, want %q", found, galePath)
	}
}

func TestFindGaleConfigInParentDir(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "subdir")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	galePath := filepath.Join(parent, "gale.toml")
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	found, err := FindGaleConfig(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != galePath {
		t.Errorf("FindGaleConfig = %q, want %q", found, galePath)
	}
}

func TestFindGaleConfigInGrandparentDir(t *testing.T) {
	grandparent := t.TempDir()
	parent := filepath.Join(grandparent, "a")
	child := filepath.Join(parent, "b")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}
	galePath := filepath.Join(grandparent, "gale.toml")
	if err := os.WriteFile(galePath, []byte("[packages]\n"), 0o644); err != nil {
		t.Fatalf("failed to write gale.toml: %v", err)
	}

	found, err := FindGaleConfig(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != galePath {
		t.Errorf("FindGaleConfig = %q, want %q", found, galePath)
	}
}

func TestFindGaleConfigNotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := FindGaleConfig(dir)
	if err == nil {
		t.Fatal("expected error when no gale.toml exists")
	}
	if !errors.Is(err, ErrGaleConfigNotFound) {
		t.Errorf("error = %v, want ErrGaleConfigNotFound", err)
	}
}

// --- Behavior 6: Write gale.toml ---

func TestWriteGaleConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	original := &GaleConfig{
		Packages: map[string]string{
			"jq":      "1.7.1",
			"ripgrep": "latest",
		},
		Vars: map[string]string{
			"DATABASE_URL": "postgres://localhost/myapp",
		},
	}

	if err := WriteGaleConfig(path, original); err != nil {
		t.Fatalf("WriteGaleConfig error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	parsed, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error on written file: %v", err)
	}
	if parsed == nil {
		t.Fatal("expected non-nil parsed config")
	}

	if parsed.Packages["jq"] != "1.7.1" {
		t.Errorf("round-trip Packages[jq] = %q, want %q",
			parsed.Packages["jq"], "1.7.1")
	}
	if parsed.Packages["ripgrep"] != "latest" {
		t.Errorf("round-trip Packages[ripgrep] = %q, want %q",
			parsed.Packages["ripgrep"], "latest")
	}
	if parsed.Vars["DATABASE_URL"] != "postgres://localhost/myapp" {
		t.Errorf("round-trip Vars[DATABASE_URL] = %q, want %q",
			parsed.Vars["DATABASE_URL"], "postgres://localhost/myapp")
	}
}

func TestWriteGaleConfigCreatesValidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	cfg := &GaleConfig{
		Packages: map[string]string{"curl": "8.0"},
	}

	if err := WriteGaleConfig(path, cfg); err != nil {
		t.Fatalf("WriteGaleConfig error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("written file is empty")
	}

	// Verify it parses as valid TOML via round-trip.
	parsed, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("written file is not valid TOML: %v", err)
	}
	if parsed.Packages["curl"] != "8.0" {
		t.Errorf("Packages[curl] = %q, want %q",
			parsed.Packages["curl"], "8.0")
	}
}

// --- Behavior 7: Add package to gale.toml ---

func TestAddPackageToExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := AddPackage(path, "ripgrep", "14.0"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.Packages["ripgrep"] != "14.0" {
		t.Errorf("Packages[ripgrep] = %q, want %q",
			cfg.Packages["ripgrep"], "14.0")
	}
}

func TestAddPackagePreservesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := AddPackage(path, "ripgrep", "14.0"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.Packages["jq"] != "1.7.1" {
		t.Errorf("Packages[jq] = %q, want %q",
			cfg.Packages["jq"], "1.7.1")
	}
}

func TestAddPackageUpdatesExistingVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := AddPackage(path, "jq", "1.8.0"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.Packages["jq"] != "1.8.0" {
		t.Errorf("Packages[jq] = %q, want %q",
			cfg.Packages["jq"], "1.8.0")
	}
}

func TestAddPackageBootstrapsNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	// File does not exist yet
	if err := AddPackage(path, "jq", "1.7.1"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}
	if cfg.Packages["jq"] != "1.7.1" {
		t.Errorf("Packages[jq] = %q, want %q",
			cfg.Packages["jq"], "1.7.1")
	}
}

// --- Behavior 8: Remove package from gale.toml ---

func TestRemovePackageFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\nripgrep = \"14.0\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := RemovePackage(path, "jq"); err != nil {
		t.Fatalf("RemovePackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if _, exists := cfg.Packages["jq"]; exists {
		t.Error("expected jq to be removed from Packages")
	}
}

func TestRemovePackagePreservesOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\nripgrep = \"14.0\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := RemovePackage(path, "jq"); err != nil {
		t.Fatalf("RemovePackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.Packages["ripgrep"] != "14.0" {
		t.Errorf("Packages[ripgrep] = %q, want %q",
			cfg.Packages["ripgrep"], "14.0")
	}
}

func TestRemovePackageNonexistentReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	err := RemovePackage(path, "nonexistent")
	if err == nil {
		t.Fatal("expected error when removing nonexistent package")
	}
	if !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("error = %v, want ErrPackageNotFound", err)
	}
}

// --- Error paths: malformed TOML ---

func TestParseGaleConfigMalformedTOML(t *testing.T) {
	_, err := ParseGaleConfig("this is not [valid toml")
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

func TestParseAppConfigMalformedTOML(t *testing.T) {
	_, err := ParseAppConfig("this is not [valid toml")
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

// --- Behavior: Registry URL in AppConfig ---

const appConfigWithRegistry = `
[registry]
url = "https://example.com/recipes"
`

func TestParseAppConfigRegistryURL(t *testing.T) {
	cfg, err := ParseAppConfig(appConfigWithRegistry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Registry.URL != "https://example.com/recipes" {
		t.Errorf("Registry.URL = %q, want %q",
			cfg.Registry.URL, "https://example.com/recipes")
	}
}

func TestParseAppConfigRegistryURLEmpty(t *testing.T) {
	cfg, err := ParseAppConfig("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Registry.URL != "" {
		t.Errorf("Registry.URL = %q, want empty",
			cfg.Registry.URL)
	}
}

// --- Behavior: Pin package in gale.toml ---

func TestPinPackageAddsPinnedSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := PinPackage(path, "jq"); err != nil {
		t.Fatalf("PinPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}

	if !cfg.Pinned["jq"] {
		t.Error("expected jq to be pinned")
	}
}

func TestPinPackagePreservesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\nripgrep = \"14.0\"\n\n[vars]\nFOO = \"bar\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := PinPackage(path, "jq"); err != nil {
		t.Fatalf("PinPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}

	if cfg.Packages["jq"] != "1.7.1" {
		t.Errorf("Packages[jq] = %q, want %q",
			cfg.Packages["jq"], "1.7.1")
	}
	if cfg.Packages["ripgrep"] != "14.0" {
		t.Errorf("Packages[ripgrep] = %q, want %q",
			cfg.Packages["ripgrep"], "14.0")
	}
	if cfg.Vars["FOO"] != "bar" {
		t.Errorf("Vars[FOO] = %q, want %q",
			cfg.Vars["FOO"], "bar")
	}
}

// --- Behavior: Unpin package from gale.toml ---

func TestUnpinPackageRemovesPin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\n\n[pinned]\njq = true\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := UnpinPackage(path, "jq"); err != nil {
		t.Fatalf("UnpinPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}

	if cfg.Pinned["jq"] {
		t.Error("expected jq to be unpinned")
	}
}

func TestUnpinPackageNonexistentIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	initial := "[packages]\njq = \"1.7.1\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	// Unpinning something that isn't pinned should not error.
	if err := UnpinPackage(path, "jq"); err != nil {
		t.Fatalf("UnpinPackage error: %v", err)
	}
}

// --- Behavior: Parse gale.toml with pinned section ---

const galeWithPinned = `
[packages]
jq = "1.7.1"
ripgrep = "14.0"

[pinned]
jq = true
`

func TestParseGaleConfigPinnedNotNil(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithPinned)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Pinned == nil {
		t.Fatal("expected non-nil Pinned map")
	}
}

func TestParseGaleConfigPinnedValue(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithPinned)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Pinned["jq"] {
		t.Error("expected Pinned[jq] to be true")
	}
}

func TestParseGaleConfigUnpinnedPackage(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithPinned)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Pinned["ripgrep"] {
		t.Error("expected Pinned[ripgrep] to be false")
	}
}

// --- Behavior: Write and read AppConfig ---

func TestWriteAppConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := &AppConfig{
		Repos: []Repo{
			{Name: "core", URL: "https://example.com/recipes", Priority: 1},
		},
	}

	if err := WriteAppConfig(path, cfg); err != nil {
		t.Fatalf("WriteAppConfig error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	parsed, err := ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("ParseAppConfig error: %v", err)
	}

	if len(parsed.Repos) != 1 {
		t.Fatalf("Repos length = %d, want 1", len(parsed.Repos))
	}
	if parsed.Repos[0].Name != "core" {
		t.Errorf("Repos[0].Name = %q, want %q",
			parsed.Repos[0].Name, "core")
	}
}

// --- Behavior: Add repo to config.toml ---

func TestAddRepoToEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	err := AddRepo(path, Repo{
		Name: "core",
		URL:  "https://example.com/recipes",
	})
	if err != nil {
		t.Fatalf("AddRepo error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("ParseAppConfig error: %v", err)
	}

	if len(cfg.Repos) != 1 {
		t.Fatalf("Repos length = %d, want 1", len(cfg.Repos))
	}
	if cfg.Repos[0].Name != "core" {
		t.Errorf("Repos[0].Name = %q, want %q",
			cfg.Repos[0].Name, "core")
	}
	if cfg.Repos[0].URL != "https://example.com/recipes" {
		t.Errorf("Repos[0].URL = %q, want %q",
			cfg.Repos[0].URL, "https://example.com/recipes")
	}
}

func TestAddRepoToExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	initial := "[[repos]]\nname = \"core\"\nurl = \"https://example.com/core\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("writing initial: %v", err)
	}

	err := AddRepo(path, Repo{
		Name: "community",
		URL:  "https://example.com/community",
	})
	if err != nil {
		t.Fatalf("AddRepo error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	cfg, err := ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("ParseAppConfig error: %v", err)
	}

	if len(cfg.Repos) != 2 {
		t.Fatalf("Repos length = %d, want 2", len(cfg.Repos))
	}
	if cfg.Repos[1].Name != "community" {
		t.Errorf("Repos[1].Name = %q, want %q",
			cfg.Repos[1].Name, "community")
	}
}

// --- Behavior: Remove repo from config.toml ---

func TestRemoveRepoFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	initial := "[[repos]]\nname = \"core\"\nurl = \"https://example.com/core\"\n\n" +
		"[[repos]]\nname = \"community\"\nurl = \"https://example.com/community\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("writing initial: %v", err)
	}

	err := RemoveRepo(path, "core")
	if err != nil {
		t.Fatalf("RemoveRepo error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	cfg, err := ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("ParseAppConfig error: %v", err)
	}

	if len(cfg.Repos) != 1 {
		t.Fatalf("Repos length = %d, want 1", len(cfg.Repos))
	}
	if cfg.Repos[0].Name != "community" {
		t.Errorf("Repos[0].Name = %q, want %q",
			cfg.Repos[0].Name, "community")
	}
}

func TestRemoveRepoNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	initial := "[[repos]]\nname = \"core\"\nurl = \"https://example.com/core\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("writing initial: %v", err)
	}

	err := RemoveRepo(path, "nonexistent")
	if err == nil {
		t.Fatal("expected error when removing nonexistent repo")
	}
}

// --- Behavior: Concurrent config writes are serialized ---

func TestConcurrentAddPackageNoLostWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	// Start with an empty config.
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("pkg%d", idx)
			errs[idx] = AddPackage(path, name, "1.0.0")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("AddPackage(pkg%d) error: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatalf("ParseGaleConfig error: %v", err)
	}

	if len(cfg.Packages) != n {
		t.Errorf("Packages count = %d, want %d: "+
			"concurrent writes lost data", len(cfg.Packages), n)
	}
}
