package recipe

import (
	"strings"
	"testing"
)

const validRecipe = `
[package]
name = "jq"
version = "1.7.1"
description = "Command-line JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
sha256 = "478c9ca129fd2e3443fe27314b455e211e0d8c60bc8ff7df703f25571c92f12e"

[build]
system = "autotools"
steps = [
  "./configure --prefix=${PREFIX} --disable-docs",
  "make -j${JOBS}",
  "make install",
]

[dependencies]
build = ["autoconf", "automake", "libtool"]
runtime = ["oniguruma"]
`

// --- Behavior 1: Parse valid recipe TOML ---

func TestParseValidRecipe(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
}

func TestParseValidRecipePackageName(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	if r.Package.Name != "jq" {
		t.Errorf("Package.Name = %q, want %q", r.Package.Name, "jq")
	}
}

func TestParseValidRecipePackageVersion(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	if r.Package.Version != "1.7.1" {
		t.Errorf("Package.Version = %q, want %q", r.Package.Version, "1.7.1")
	}
}

func TestParseValidRecipePackageDescription(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	if r.Package.Description != "Command-line JSON processor" {
		t.Errorf("Package.Description = %q, want %q",
			r.Package.Description, "Command-line JSON processor")
	}
}

func TestParseValidRecipePackageLicense(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	if r.Package.License != "MIT" {
		t.Errorf("Package.License = %q, want %q", r.Package.License, "MIT")
	}
}

func TestParseValidRecipePackageHomepage(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	if r.Package.Homepage != "https://jqlang.github.io/jq" {
		t.Errorf("Package.Homepage = %q, want %q",
			r.Package.Homepage, "https://jqlang.github.io/jq")
	}
}

func TestParseValidRecipeSourceURL(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	want := "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
	if r.Source.URL != want {
		t.Errorf("Source.URL = %q, want %q", r.Source.URL, want)
	}
}

func TestParseValidRecipeSourceSHA256(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	want := "478c9ca129fd2e3443fe27314b455e211e0d8c60bc8ff7df703f25571c92f12e"
	if r.Source.SHA256 != want {
		t.Errorf("Source.SHA256 = %q, want %q", r.Source.SHA256, want)
	}
}

func TestParseValidRecipeBuildSystem(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	if r.Build.System != "autotools" {
		t.Errorf("Build.System = %q, want %q", r.Build.System, "autotools")
	}
}

func TestParseValidRecipeBuildSteps(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	wantSteps := []string{
		"./configure --prefix=${PREFIX} --disable-docs",
		"make -j${JOBS}",
		"make install",
	}
	if len(r.Build.Steps) != len(wantSteps) {
		t.Fatalf("Build.Steps length = %d, want %d",
			len(r.Build.Steps), len(wantSteps))
	}
	for i, s := range r.Build.Steps {
		if s != wantSteps[i] {
			t.Errorf("Build.Steps[%d] = %q, want %q", i, s, wantSteps[i])
		}
	}
}

func TestParseValidRecipeBuildDeps(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	wantBuild := []string{"autoconf", "automake", "libtool"}
	if len(r.Dependencies.Build) != len(wantBuild) {
		t.Fatalf("Dependencies.Build length = %d, want %d",
			len(r.Dependencies.Build), len(wantBuild))
	}
	for i, d := range r.Dependencies.Build {
		if d != wantBuild[i] {
			t.Errorf("Dependencies.Build[%d] = %q, want %q",
				i, d, wantBuild[i])
		}
	}
}

func TestParseValidRecipeRuntimeDeps(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	wantRuntime := []string{"oniguruma"}
	if len(r.Dependencies.Runtime) != len(wantRuntime) {
		t.Fatalf("Dependencies.Runtime length = %d, want %d",
			len(r.Dependencies.Runtime), len(wantRuntime))
	}
	if r.Dependencies.Runtime[0] != "oniguruma" {
		t.Errorf("Dependencies.Runtime[0] = %q, want %q",
			r.Dependencies.Runtime[0], "oniguruma")
	}
}

// --- Behavior 2: Validate required fields ---

func TestParseMissingPackageName(t *testing.T) {
	input := `
[package]
version = "1.0.0"

[source]
url = "https://example.com/foo.tar.gz"
sha256 = "abc123"
`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for missing package.name")
	}
	if !containsField(err.Error(), "name") {
		t.Errorf("error %q should mention missing field 'name'", err)
	}
}

func TestParseMissingPackageVersion(t *testing.T) {
	input := `
[package]
name = "foo"

[source]
url = "https://example.com/foo.tar.gz"
sha256 = "abc123"
`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for missing package.version")
	}
	if !containsField(err.Error(), "version") {
		t.Errorf("error %q should mention missing field 'version'", err)
	}
}

func TestParseMissingSourceURL(t *testing.T) {
	input := `
[package]
name = "foo"
version = "1.0.0"

[source]
sha256 = "abc123"
`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for missing source.url")
	}
	if !containsField(err.Error(), "url") {
		t.Errorf("error %q should mention missing field 'url'", err)
	}
}

func TestParseMissingSourceSHA256(t *testing.T) {
	input := `
[package]
name = "foo"
version = "1.0.0"

[source]
url = "https://example.com/foo.tar.gz"
`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for missing source.sha256")
	}
	if !containsField(err.Error(), "sha256") {
		t.Errorf("error %q should mention missing field 'sha256'", err)
	}
}

// TestParseEmptyStringReturnsError tests Behavior 2: an empty string is
// valid TOML (no syntax error) but lacks all required fields, so Parse
// must return a validation error.
func TestParseEmptyStringReturnsError(t *testing.T) {
	_, err := Parse("")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

// --- Behavior 3: Handle optional fields ---

func TestParseMinimalRecipe(t *testing.T) {
	input := `
[package]
name = "foo"
version = "1.0.0"

[source]
url = "https://example.com/foo.tar.gz"
sha256 = "abc123"
`
	r, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
}

func TestParseMinimalRecipeOptionalStringsEmpty(t *testing.T) {
	input := `
[package]
name = "foo"
version = "1.0.0"

[source]
url = "https://example.com/foo.tar.gz"
sha256 = "abc123"
`
	r, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	if r.Package.License != "" {
		t.Errorf("Package.License = %q, want empty", r.Package.License)
	}
	if r.Package.Homepage != "" {
		t.Errorf("Package.Homepage = %q, want empty", r.Package.Homepage)
	}
	if r.Build.System != "" {
		t.Errorf("Build.System = %q, want empty", r.Build.System)
	}
}

func TestParseMinimalRecipeOptionalSlicesNil(t *testing.T) {
	input := `
[package]
name = "foo"
version = "1.0.0"

[source]
url = "https://example.com/foo.tar.gz"
sha256 = "abc123"
`
	r, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recipe")
	}
	if len(r.Build.Steps) != 0 {
		t.Errorf("Build.Steps has %d elements, want 0", len(r.Build.Steps))
	}
	if len(r.Dependencies.Build) != 0 {
		t.Errorf("Dependencies.Build has %d elements, want 0",
			len(r.Dependencies.Build))
	}
	if len(r.Dependencies.Runtime) != 0 {
		t.Errorf("Dependencies.Runtime has %d elements, want 0",
			len(r.Dependencies.Runtime))
	}
}

// --- Behavior 4: Meaningful errors for malformed TOML ---

func TestParseMalformedTOML(t *testing.T) {
	input := `this is not valid TOML [[[`
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

func TestParseMalformedTOMLNoPanic(t *testing.T) {
	inputs := []string{
		`[package`,
		`name = `,
		`= "value"`,
	}
	for _, input := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Parse panicked on input %q: %v", input, r)
				}
			}()
			// We don't care about the result, just that it doesn't panic.
			Parse(input)
		}()
	}
}

// --- Behavior 5: Binary sections ---

const recipeWithBinaries = `
[package]
name = "jq"
version = "1.7.1"
description = "Command-line JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
sha256 = "478c9ca129fd2e3443fe27314b455e211e0d8c60bc8ff7df703f25571c92f12e"

[build]
steps = ["make install"]

[binary.darwin-arm64]
url = "ghcr.io/kelp/gale-recipes/jq:1.7.1-darwin-arm64"
sha256 = "abc123"

[binary.linux-amd64]
url = "ghcr.io/kelp/gale-recipes/jq:1.7.1-linux-amd64"
sha256 = "def456"

[dependencies]
build = ["autoconf"]
`

func TestParseBinarySection(t *testing.T) {
	r, err := Parse(recipeWithBinaries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2", len(r.Binary))
	}
}

func TestParseBinaryDarwinArm64URL(t *testing.T) {
	r, err := Parse(recipeWithBinaries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, ok := r.Binary["darwin-arm64"]
	if !ok {
		t.Fatal("missing binary for darwin-arm64")
	}
	if b.URL != "ghcr.io/kelp/gale-recipes/jq:1.7.1-darwin-arm64" {
		t.Errorf("URL = %q", b.URL)
	}
}

func TestParseBinaryDarwinArm64SHA256(t *testing.T) {
	r, err := Parse(recipeWithBinaries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b := r.Binary["darwin-arm64"]
	if b.SHA256 != "abc123" {
		t.Errorf("SHA256 = %q, want %q", b.SHA256, "abc123")
	}
}

func TestParseNoBinarySectionIsValid(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Binary) != 0 {
		t.Errorf("Binary should be nil or empty, got %d entries",
			len(r.Binary))
	}
}

func TestBinaryForPlatformFound(t *testing.T) {
	r, err := Parse(recipeWithBinaries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b := r.BinaryForPlatform("darwin", "arm64")
	if b == nil {
		t.Fatal("expected non-nil binary for darwin-arm64")
	}
	if b.SHA256 != "abc123" {
		t.Errorf("SHA256 = %q, want %q", b.SHA256, "abc123")
	}
}

func TestBinaryForPlatformNotFound(t *testing.T) {
	r, err := Parse(recipeWithBinaries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b := r.BinaryForPlatform("windows", "amd64")
	if b != nil {
		t.Error("expected nil binary for windows-amd64")
	}
}

// containsField checks if the error message contains the field name
// in a case-insensitive manner.
func containsField(msg, field string) bool {
	return strings.Contains(strings.ToLower(msg), strings.ToLower(field))
}

// --- Source repo and released_at fields ---

const recipeWithSourceMeta = `
[package]
name = "jq"
version = "1.7.1"

[source]
url = "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
sha256 = "478c9ca129fd2e3443fe27314b455e211e0d8c60bc8ff7df703f25571c92f12e"
repo = "jqlang/jq"
released_at = "2024-12-15"
`

func TestParseSourceRepo(t *testing.T) {
	r, err := Parse(recipeWithSourceMeta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Source.Repo != "jqlang/jq" {
		t.Errorf("Source.Repo = %q, want %q",
			r.Source.Repo, "jqlang/jq")
	}
}

func TestParseSourceReleasedAt(t *testing.T) {
	r, err := Parse(recipeWithSourceMeta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Source.ReleasedAt != "2024-12-15" {
		t.Errorf("Source.ReleasedAt = %q, want %q",
			r.Source.ReleasedAt, "2024-12-15")
	}
}

func TestParseWithoutSourceMetaFieldsStillWorks(t *testing.T) {
	r, err := Parse(validRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Source.Repo != "" {
		t.Errorf("Source.Repo = %q, want empty", r.Source.Repo)
	}
	if r.Source.ReleasedAt != "" {
		t.Errorf("Source.ReleasedAt = %q, want empty",
			r.Source.ReleasedAt)
	}
}
