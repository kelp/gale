package lint

import (
	"strings"
	"testing"
)

const validRecipe = `
[package]
name = "jq"
version = "1.8.1"
description = "Lightweight JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
repo = "jqlang/jq"
url = "https://github.com/jqlang/jq/releases/download/jq-1.8.1/jq-1.8.1.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
released_at = "2025-07-01"

[build]
steps = [
  "./configure --prefix=${PREFIX}",
  "make -j${JOBS}",
  "make install",
]
`

// --- Valid recipe produces no issues ---

func TestLintValidRecipeNoIssues(t *testing.T) {
	issues := Lint(validRecipe, "recipes/j/jq.toml")
	if len(issues) > 0 {
		t.Errorf("expected no issues, got %d: %v",
			len(issues), issues)
	}
}

// --- Error: missing required fields ---

func TestLintMissingPackageName(t *testing.T) {
	data := `
[package]
version = "1.0"
[source]
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasError(issues, "name") {
		t.Errorf("expected error about missing name, got %v", issues)
	}
}

func TestLintMissingPackageVersion(t *testing.T) {
	data := `
[package]
name = "foo"
[source]
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasError(issues, "version") {
		t.Errorf("expected error about missing version, got %v", issues)
	}
}

func TestLintMissingSourceURL(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
[source]
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasError(issues, "url") {
		t.Errorf("expected error about missing url, got %v", issues)
	}
}

func TestLintMissingSourceSHA256(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
[source]
url = "https://example.com/foo.tar.gz"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasError(issues, "sha256") {
		t.Errorf("expected error about missing sha256, got %v", issues)
	}
}

func TestLintMissingBuildSteps(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
[source]
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
`
	issues := Lint(data, "")
	if !hasError(issues, "steps") {
		t.Errorf("expected error about missing steps, got %v", issues)
	}
}

// --- Error: bad SHA256 format ---

func TestLintBadSourceSHA256Format(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
[source]
url = "https://example.com/foo.tar.gz"
sha256 = "not-a-valid-hash"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasError(issues, "sha256") {
		t.Errorf("expected error about invalid sha256, got %v", issues)
	}
}

func TestLintBadBinarySHA256Format(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
[source]
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
[binary.darwin-arm64]
url = "https://example.com/foo.tar.zst"
sha256 = "bad"
`
	issues := Lint(data, "")
	if !hasError(issues, "sha256") {
		t.Errorf("expected error about invalid binary sha256, got %v", issues)
	}
}

// --- Error: file path mismatch ---

func TestLintFilePathMismatch(t *testing.T) {
	issues := Lint(validRecipe, "recipes/x/wrong.toml")
	if !hasError(issues, "path") {
		t.Errorf("expected error about file path, got %v", issues)
	}
}

func TestLintFilePathLetterBucketWrong(t *testing.T) {
	issues := Lint(validRecipe, "recipes/x/jq.toml")
	if !hasError(issues, "path") {
		t.Errorf("expected error about path letter, got %v", issues)
	}
}

// --- Warning: missing optional fields ---

func TestLintMissingDescriptionWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
[source]
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "description") {
		t.Errorf("expected warning about description, got %v", issues)
	}
}

func TestLintMissingRepoWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "repo") {
		t.Errorf("expected warning about missing repo, got %v", issues)
	}
}

func TestLintRepoFullURLNoWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "https://gitlab.freedesktop.org/pkgconf/pkgconf"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if hasWarning(issues, "repo") {
		t.Errorf("full URL repo should not warn, got %v", issues)
	}
}

func TestLintRepoBadFormatWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "just-a-name"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "repo") {
		t.Errorf("expected warning about repo format, got %v", issues)
	}
}

// --- Warning: bad released_at ---

func TestLintBadReleasedAtWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
released_at = "not-a-date"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "released_at") {
		t.Errorf("expected warning about released_at, got %v", issues)
	}
}

// --- Warning: no ${PREFIX} in build steps ---

func TestLintNoPrefixInStepsWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make", "make install"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "prefix") {
		t.Errorf("expected warning about PREFIX, got %v", issues)
	}
}

// --- Warning: missing build deps ---

func TestLintGoBuildMissingGoDep(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["go build -o ${PREFIX}/bin/foo"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "go") {
		t.Errorf("expected warning about missing go dep, got %v",
			issues)
	}
}

func TestLintGoBuildWithGoDep(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[dependencies]
build = ["go"]
[build]
steps = ["go build -o ${PREFIX}/bin/foo"]
`
	issues := Lint(data, "")
	if hasWarning(issues, "missing build dep") {
		t.Errorf("should not warn when go dep is present, got %v",
			issues)
	}
}

func TestLintCargoBuildMissingRustDep(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["cargo install --root ${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "rust") {
		t.Errorf("expected warning about missing rust dep, got %v",
			issues)
	}
}

func TestLintCargoInstallNoFalseGoWarning(t *testing.T) {
	data := `
[package]
name = "zoxide"
version = "0.9.6"
description = "Shell extension"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "ajeetdsouza/zoxide"
url = "https://example.com/zoxide.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[dependencies]
build = ["rust"]
[build]
steps = ["cargo install --path . --root ${PREFIX}"]
`
	issues := Lint(data, "")
	for _, issue := range issues {
		if strings.Contains(issue.Message, "go") &&
			strings.Contains(issue.Message, "not in build deps") {
			t.Errorf(
				"false positive: cargo install matched as go install: %s",
				issue.Message)
		}
	}
}

func TestLintMakeMissingGnumakeDep(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make -j${JOBS}", "make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "gnumake") {
		t.Errorf("expected warning about missing gnumake dep, got %v",
			issues)
	}
}

func TestLintConfigureMakeNoGnumakeWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = [
  "./configure --prefix=${PREFIX}",
  "make -j${JOBS}",
  "make install",
]
`
	issues := Lint(data, "")
	if hasWarning(issues, "gnumake") {
		t.Errorf(
			"should not warn about gnumake with ./configure, got %v",
			issues)
	}
}

// --- Warning: invalid platform strings ---

func TestLintValidPlatformNoWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
platforms = ["linux-amd64"]
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if hasWarning(issues, "platform") {
		t.Errorf("should not warn for valid platform, got %v",
			issues)
	}
}

func TestLintInvalidPlatformWarning(t *testing.T) {
	data := `
[package]
name = "foo"
version = "1.0"
description = "A tool"
license = "MIT"
homepage = "https://example.com"
platforms = ["invalid-platform"]
[source]
repo = "owner/foo"
url = "https://example.com/foo.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
[build]
steps = ["make install PREFIX=${PREFIX}"]
`
	issues := Lint(data, "")
	if !hasWarning(issues, "platform") {
		t.Errorf("expected warning about invalid platform, got %v",
			issues)
	}
}

// --- helpers ---

func hasError(issues []Issue, substr string) bool {
	for _, i := range issues {
		if i.Level == "error" &&
			strings.Contains(
				strings.ToLower(i.Message), substr) {
			return true
		}
	}
	return false
}

func hasWarning(issues []Issue, substr string) bool {
	for _, i := range issues {
		if i.Level == "warning" &&
			strings.Contains(
				strings.ToLower(i.Message), substr) {
			return true
		}
	}
	return false
}
