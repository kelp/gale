package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	if err := AddPackage(path, "", "ripgrep", "14.0"); err != nil {
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

	if err := AddPackage(path, "", "ripgrep", "14.0"); err != nil {
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

	if err := AddPackage(path, "", "jq", "1.8.0"); err != nil {
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
	if err := AddPackage(path, "", "jq", "1.7.1"); err != nil {
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

	if err := RemovePackage(path, "", "jq"); err != nil {
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

	if err := RemovePackage(path, "", "jq"); err != nil {
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

	err := RemovePackage(path, "", "nonexistent")
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

	if err := PinPackage(path, "", "jq"); err != nil {
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

	if err := PinPackage(path, "", "jq"); err != nil {
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

	if err := UnpinPackage(path, "", "jq"); err != nil {
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
	if err := UnpinPackage(path, "", "jq"); err != nil {
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
			errs[idx] = AddPackage(path, "", name, "1.0.0")
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

// --- BUG FIX 3: AddRepo/RemoveRepo missing file locking ---

func TestAddRepoConcurrentNoDataLoss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Start with an empty config.
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write initial config.toml: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			repo := Repo{
				Name: fmt.Sprintf("repo%d", idx),
				URL:  fmt.Sprintf("https://example.com/repo%d", idx),
			}
			errs[idx] = AddRepo(path, repo)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("AddRepo(repo%d) error: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	cfg, err := ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("ParseAppConfig error: %v", err)
	}

	if len(cfg.Repos) != n {
		t.Errorf("Repos count = %d, want %d: "+
			"concurrent writes lost data", len(cfg.Repos), n)
	}
}

// --- Behavior: Per-host packages ---

const galeWithHosts = `
[packages]
jq = "1.7.1"
ripgrep = "14.0"

[hosts.my-mac.packages]
fzf = "0.50"

[hosts.my-server.packages]
htop = "3.0"

[hosts.override-host.packages]
jq = "2.0.0"
`

func TestParseGaleConfigHostsNotNil(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithHosts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Hosts == nil {
		t.Fatal("expected non-nil Hosts map")
	}
	if _, ok := cfg.Hosts["my-mac"]; !ok {
		t.Error("expected hosts.my-mac to be present")
	}
}

func TestParseGaleConfigHostPackages(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithHosts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Hosts["my-mac"].Packages["fzf"] != "0.50" {
		t.Errorf("Hosts[my-mac].Packages[fzf] = %q, want %q",
			cfg.Hosts["my-mac"].Packages["fzf"], "0.50")
	}
}

func TestEffectivePackagesMergesSharedAndHost(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithHosts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pkgs := cfg.EffectivePackages("my-mac")
	if pkgs["jq"] != "1.7.1" {
		t.Errorf("EffectivePackages[jq] = %q, want %q",
			pkgs["jq"], "1.7.1")
	}
	if pkgs["fzf"] != "0.50" {
		t.Errorf("EffectivePackages[fzf] = %q, want %q",
			pkgs["fzf"], "0.50")
	}
	if _, has := pkgs["htop"]; has {
		t.Error("expected htop to be absent on my-mac")
	}
	if len(pkgs) != 3 {
		t.Errorf("EffectivePackages count = %d, want 3", len(pkgs))
	}
}

func TestEffectivePackagesUnknownHostReturnsSharedOnly(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithHosts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pkgs := cfg.EffectivePackages("nowhere")
	if len(pkgs) != 2 {
		t.Errorf("EffectivePackages count = %d, want 2 (shared only)",
			len(pkgs))
	}
	if pkgs["jq"] != "1.7.1" || pkgs["ripgrep"] != "14.0" {
		t.Error("expected shared packages only")
	}
}

func TestEffectivePackagesHostOverridesShared(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithHosts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pkgs := cfg.EffectivePackages("override-host")
	if pkgs["jq"] != "2.0.0" {
		t.Errorf("EffectivePackages[jq] = %q, want %q (host override)",
			pkgs["jq"], "2.0.0")
	}
}

func TestEffectivePackagesDoesNotMutateReceiver(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithHosts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = cfg.EffectivePackages("my-mac")
	if len(cfg.Packages) != 2 {
		t.Errorf("expected receiver Packages unchanged, got %d",
			len(cfg.Packages))
	}
}

const galeWithHostPinned = `
[packages]
jq = "1.7.1"

[pinned]
jq = true

[hosts.my-mac.packages]
fzf = "0.50"

[hosts.my-mac.pinned]
fzf = true
`

func TestEffectivePinnedMergesSharedAndHost(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithHostPinned)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pinned := cfg.EffectivePinned("my-mac")
	if !pinned["jq"] {
		t.Error("expected jq pinned (shared)")
	}
	if !pinned["fzf"] {
		t.Error("expected fzf pinned (host)")
	}
}

func TestCurrentHostHonorsEnvVar(t *testing.T) {
	t.Setenv("GALE_HOST", "test-host-xyz")
	if got := CurrentHost(); got != "test-host-xyz" {
		t.Errorf("CurrentHost() = %q, want %q", got, "test-host-xyz")
	}
}

func TestCurrentHostFallsBackToHostname(t *testing.T) {
	t.Setenv("GALE_HOST", "")
	got := CurrentHost()
	if got == "" {
		t.Error("CurrentHost() returned empty string")
	}
	osHost, err := os.Hostname()
	if err == nil && got != osHost {
		t.Errorf("CurrentHost() = %q, want os.Hostname() = %q",
			got, osHost)
	}
}

func TestAddPackageToHostSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")
	initial := "[packages]\njq = \"1.7.1\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AddPackage(path, "my-mac", "fzf", "0.50"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hosts["my-mac"].Packages["fzf"] != "0.50" {
		t.Errorf("expected fzf=0.50 under hosts.my-mac, got cfg=%+v",
			cfg)
	}
	if _, has := cfg.Packages["fzf"]; has {
		t.Error("expected fzf NOT to leak into top-level packages")
	}
	if cfg.Packages["jq"] != "1.7.1" {
		t.Error("expected top-level jq preserved")
	}
}

func TestAddPackageToHostSectionPreservesOtherHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")
	initial := "[hosts.my-server.packages]\nhtop = \"3.0\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AddPackage(path, "my-mac", "fzf", "0.50"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hosts["my-server"].Packages["htop"] != "3.0" {
		t.Error("expected my-server.htop preserved")
	}
	if cfg.Hosts["my-mac"].Packages["fzf"] != "0.50" {
		t.Error("expected my-mac.fzf added")
	}
}

func TestRemovePackageFromHostSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")
	initial := "[packages]\njq = \"1.7.1\"\n\n" +
		"[hosts.my-mac.packages]\nfzf = \"0.50\"\nbat = \"0.26\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RemovePackage(path, "my-mac", "fzf"); err != nil {
		t.Fatalf("RemovePackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := cfg.Hosts["my-mac"].Packages["fzf"]; has {
		t.Error("expected fzf removed from my-mac")
	}
	if cfg.Hosts["my-mac"].Packages["bat"] != "0.26" {
		t.Error("expected bat preserved in my-mac")
	}
	if cfg.Packages["jq"] != "1.7.1" {
		t.Error("expected top-level jq preserved")
	}
}

func TestRemovePackageFromHostSectionNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")
	// fzf exists at top level but not under host — must still
	// return ErrPackageNotFound when removing from host scope.
	initial := "[packages]\nfzf = \"0.50\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RemovePackage(path, "my-mac", "fzf")
	if !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("err = %v, want ErrPackageNotFound", err)
	}
}

func TestWriteGaleConfigRoundTripWithHosts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")
	cfg := &GaleConfig{
		Packages: map[string]string{"jq": "1.7.1"},
		Hosts: map[string]HostConfig{
			"my-mac": {
				Packages: map[string]string{"fzf": "0.50"},
			},
		},
	}
	if err := WriteGaleConfig(path, cfg); err != nil {
		t.Fatalf("WriteGaleConfig error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	round, err := ParseGaleConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if round.Hosts["my-mac"].Packages["fzf"] != "0.50" {
		t.Error("round-trip lost host package")
	}
	if round.Packages["jq"] != "1.7.1" {
		t.Error("round-trip lost shared package")
	}
}

// --- Behavior: Multi-host and wildcard host syntax ---

const galeWithMultiHost = `
[packages]
jq = "1.7.1"

[hosts."laptop,desktop".packages]
fzf = "0.50"

[hosts."work-*".packages]
slack = "1.0"

[hosts."*".packages]
common = "9.9"

[hosts.laptop.packages]
fzf = "0.60"
`

func TestEffectivePackagesMatchesCommaList(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithMultiHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pkgs := cfg.EffectivePackages("desktop")
	if pkgs["fzf"] != "0.50" {
		t.Errorf("desktop fzf = %q, want %q (matched via comma list)",
			pkgs["fzf"], "0.50")
	}
}

func TestEffectivePackagesMatchesGlob(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithMultiHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pkgs := cfg.EffectivePackages("work-mac")
	if pkgs["slack"] != "1.0" {
		t.Errorf("work-mac slack = %q, want %q (matched via glob)",
			pkgs["slack"], "1.0")
	}
}

func TestEffectivePackagesWildcardMatchesEveryHost(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithMultiHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, h := range []string{"laptop", "desktop", "work-mac", "nowhere"} {
		if got := cfg.EffectivePackages(h)["common"]; got != "9.9" {
			t.Errorf("%s common = %q, want %q (wildcard)", h, got, "9.9")
		}
	}
}

func TestEffectivePackagesExactOverridesGlob(t *testing.T) {
	// `laptop` is in both the comma-list "laptop,desktop" (fzf=0.50)
	// and the exact `[hosts.laptop]` section (fzf=0.60). The exact
	// section wins because it is the most specific match.
	cfg, err := ParseGaleConfig(galeWithMultiHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pkgs := cfg.EffectivePackages("laptop")
	if pkgs["fzf"] != "0.60" {
		t.Errorf("laptop fzf = %q, want %q (exact host overrides multi-list)",
			pkgs["fzf"], "0.60")
	}
}

func TestEffectivePackagesUnmatchedHostStillGetsShared(t *testing.T) {
	cfg, err := ParseGaleConfig(galeWithMultiHost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pkgs := cfg.EffectivePackages("nowhere")
	if pkgs["jq"] != "1.7.1" {
		t.Error("expected shared jq on unknown host")
	}
	if _, has := pkgs["fzf"]; has {
		t.Error("expected fzf absent on unknown host")
	}
	if _, has := pkgs["slack"]; has {
		t.Error("expected slack absent on non-work host")
	}
}

func TestEffectivePinnedMatchesGlob(t *testing.T) {
	src := `
[packages]
jq = "1.7.1"

[hosts."work-*".packages]
slack = "1.0"

[hosts."work-*".pinned]
slack = true
`
	cfg, err := ParseGaleConfig(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.EffectivePinned("work-mac")["slack"] {
		t.Error("expected slack pinned on work-mac (glob)")
	}
	if cfg.EffectivePinned("home")["slack"] {
		t.Error("expected slack not pinned on home")
	}
}

// --- Bug 0012: Config mutations strip TOML comments ---

func TestAddPackagePreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	// Write a gale.toml with both standalone and inline comments.
	initial := `# Managed by gale - do not edit manually
[packages]
# search tools
ripgrep = "14.0" # fast grep replacement
jq = "1.7.1"
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := AddPackage(path, "", "bat", "0.24.0"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file after mutation: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "# Managed by gale") {
		t.Error("AddPackage stripped the standalone header comment")
	}
	if !strings.Contains(content, "# search tools") {
		t.Error("AddPackage stripped the inline section comment")
	}
	if !strings.Contains(content, "# fast grep replacement") {
		t.Error("AddPackage stripped the inline value comment")
	}
}

// --- Bug 0013: Config mutations reorder package keys alphabetically ---

func TestAddPackagePreservesKeyOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	// Packages in a deliberate non-alphabetical order:
	// ripgrep, fd, bat, jq.
	// Alphabetically the order would be: bat, fd, jq, ripgrep.
	// So after a buggy alphabetical sort, ripgrep appears AFTER jq.
	// The test verifies ripgrep still appears BEFORE jq (original order).
	initial := `[packages]
ripgrep = "14.0"
fd = "9.0"
bat = "0.24.0"
jq = "1.7.1"
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := AddPackage(path, "", "fzf", "0.50"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file after mutation: %v", err)
	}
	content := string(data)

	idxRipgrep := strings.Index(content, "ripgrep")
	idxJq := strings.Index(content, "jq")

	if idxRipgrep < 0 {
		t.Fatal("ripgrep not found in output file")
	}
	if idxJq < 0 {
		t.Fatal("jq not found in output file")
	}
	if idxRipgrep >= idxJq {
		t.Errorf("key ordering changed: ripgrep (pos %d) should appear before jq (pos %d) but does not; AddPackage alphabetized the keys", idxRipgrep, idxJq)
	}
}

// --- Bug 0014: Config mutations drop unknown TOML sections ---

func TestAddPackagePreservesUnknownSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.toml")

	// Write a gale.toml with a custom section not known to GaleConfig.
	initial := `[packages]
jq = "1.7.1"

[custom]
owner = "my-team"
environment = "production"
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("failed to write initial gale.toml: %v", err)
	}

	if err := AddPackage(path, "", "ripgrep", "14.0"); err != nil {
		t.Fatalf("AddPackage error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file after mutation: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "[custom]") {
		t.Error("AddPackage dropped the unknown [custom] section header")
	}
	if !strings.Contains(content, "owner") {
		t.Error("AddPackage dropped the 'owner' key from the unknown [custom] section")
	}
	if !strings.Contains(content, "my-team") {
		t.Error("AddPackage dropped the 'my-team' value from the unknown [custom] section")
	}
}
