package registry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// validTOML is a minimal recipe that recipe.Parse accepts.
const validTOML = `[package]
name = "testpkg"
version = "1.0.0"
description = "A test package"
license = "MIT"
homepage = "https://example.com"

[source]
url = "https://example.com/testpkg-1.0.0.tar.gz"
sha256 = "abc123def456"
`

// --- Behavior 1: FetchRecipe constructs correct URL ---

func TestFetchRecipeConstructsCorrectURL(t *testing.T) {
	tests := []struct {
		name     string
		pkg      string
		wantPath string
	}{
		{"jq", "jq", "/recipes/j/jq.toml"},
		{"ripgrep", "ripgrep", "/recipes/r/ripgrep.toml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var gotPath string
			var set bool

			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					if !set {
						gotPath = r.URL.Path
						set = true
					}
					mu.Unlock()
					fmt.Fprint(w, validTOML)
				}))
			defer srv.Close()

			reg := &Registry{BaseURL: srv.URL}
			_, _ = reg.FetchRecipe(tt.pkg)

			mu.Lock()
			defer mu.Unlock()

			if gotPath != tt.wantPath {
				t.Errorf("request path = %q, want %q",
					gotPath, tt.wantPath)
			}
		})
	}
}

// --- Behavior 2: FetchRecipe downloads and parses recipe ---

func TestFetchRecipeParsesValidTOML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, validTOML)
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	rec, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil recipe")
	}
	if rec.Package.Name != "testpkg" {
		t.Errorf("Name = %q, want %q",
			rec.Package.Name, "testpkg")
	}
	if rec.Package.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q",
			rec.Package.Version, "1.0.0")
	}
}

// --- Behavior 3: FetchRecipe returns error for 404 ---

func TestFetchRecipeErrorsOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found",
				http.StatusNotFound)
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	_, err := reg.FetchRecipe("nonexistent")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// --- Behavior 4: FetchRecipe returns error for malformed TOML ---

func TestFetchRecipeErrorsOnMalformedTOML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "this is not valid toml [[[")
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	_, err := reg.FetchRecipe("badpkg")
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

// --- Behavior 5: New() uses default URL ---

func TestNewUsesDefaultURL(t *testing.T) {
	reg := New()
	if reg.BaseURL != DefaultURL {
		t.Errorf("BaseURL = %q, want %q",
			reg.BaseURL, DefaultURL)
	}
}

// --- Behavior 6: Custom BaseURL works ---

func TestFetchRecipeUsesCustomBaseURL(t *testing.T) {
	var mu sync.Mutex
	var called bool

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			called = true
			mu.Unlock()
			fmt.Fprint(w, validTOML)
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	rec, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !called {
		t.Error("custom server was not called")
	}
	if rec == nil {
		t.Fatal("expected non-nil recipe from custom URL")
	}
	if rec.Package.Name != "testpkg" {
		t.Errorf("Name = %q, want %q",
			rec.Package.Name, "testpkg")
	}
}

// --- Behavior 7: parseVersionIndex parses version→commit map ---

func TestParseVersionIndex(t *testing.T) {
	input := "1.7.1 abc1234def5678901234567890abcdef12345678\n" +
		"1.8.1 9876543210abcdef9876543210abcdef98765432\n"

	idx, err := parseVersionIndex(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx) != 2 {
		t.Fatalf("got %d entries, want 2", len(idx))
	}
	if idx["1.7.1"] != "abc1234def5678901234567890abcdef12345678" {
		t.Errorf("1.7.1 = %q", idx["1.7.1"])
	}
	if idx["1.8.1"] != "9876543210abcdef9876543210abcdef98765432" {
		t.Errorf("1.8.1 = %q", idx["1.8.1"])
	}
}

func TestParseVersionIndexSkipsBlanks(t *testing.T) {
	input := "1.0.0 aaaa\n\n  \n2.0.0 bbbb\n"
	idx, err := parseVersionIndex(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx) != 2 {
		t.Fatalf("got %d entries, want 2", len(idx))
	}
}

func TestParseVersionIndexErrorsOnBadLine(t *testing.T) {
	input := "1.0.0\n"
	_, err := parseVersionIndex(input)
	if err == nil {
		t.Fatal("expected error for malformed line")
	}
}

// --- Behavior 8: FetchRecipeVersion fetches pinned version ---

func TestFetchRecipeVersion(t *testing.T) {
	// Serve both the .versions index and the recipe at a
	// specific commit.
	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.7.1 " + commit + "\n" +
		"1.8.1 9876543210abcdef9876543210abcdef98765432\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.versions":
				fmt.Fprint(w, versionsBody)
			case "/" + commit + "/recipes/j/jq.toml":
				fmt.Fprint(w, validTOML)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	rec, err := reg.FetchRecipeVersion("jq", "1.7.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Name != "testpkg" {
		t.Errorf("Name = %q, want %q",
			rec.Package.Name, "testpkg")
	}
}

func TestFetchRecipeVersionNotFound(t *testing.T) {
	versionsBody := "1.8.1 abc123\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, versionsBody)
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	_, err := reg.FetchRecipeVersion("jq", "1.7.1")
	if err == nil {
		t.Fatal("expected error for version not in index")
	}
}

// --- Behavior 9: fuzzyMatch scores strings ---

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		query   string
		target  string
		isMatch bool
	}{
		{"jq", "jq", true},
		{"jq", "yq", false},
		{"json", "jq", false},
		{"json", "lightweight and flexible command-line json processor", true},
		{"fzf", "fzf", true},
		{"fuzzy", "fzf", false},
		{"fuzzy", "Command-line fuzzy finder written in Go", true},
		{"git", "git-delta", true},
		{"git", "lazygit", true},
		{"ls", "eza", false},
		{"ls", "Modern, maintained replacement for ls", true},
		{"jq", "jq", true}, // Search lowercases both sides
	}

	for _, tt := range tests {
		t.Run(tt.query+"→"+tt.target, func(t *testing.T) {
			score := fuzzyScore(tt.query, tt.target)
			if tt.isMatch && score == 0 {
				t.Errorf("expected match for %q in %q",
					tt.query, tt.target)
			}
			if !tt.isMatch && score > 0 {
				t.Errorf("unexpected match for %q in %q (score=%d)",
					tt.query, tt.target, score)
			}
		})
	}
}

func TestFuzzyScoreRanking(t *testing.T) {
	// Exact name match should rank higher than description match.
	nameScore := fuzzyScore("jq", "jq")
	descScore := fuzzyScore("jq", "not-jq")
	if nameScore <= descScore {
		t.Errorf("name match (%d) should rank higher than "+
			"non-match (%d)", nameScore, descScore)
	}
}

// --- Behavior 10: FetchRecipe errors on connection failure ---

func TestFetchRecipeErrorsOnConnectionFailure(t *testing.T) {
	// Start a server then immediately close it to get an
	// address that refuses connections.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	reg := &Registry{BaseURL: addr}
	_, err := reg.FetchRecipe("jq")
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
}

// --- Behavior 11: FetchRecipe merges .binaries.toml ---

const recipeNoBinaries = `[package]
name = "jq"
version = "1.8.1"
description = "JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://example.com/jq-1.8.1.tar.gz"
sha256 = "abc123"
`

const binariesToml = `version = "1.8.1"

[darwin-arm64]
sha256 = "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"

[linux-amd64]
sha256 = "a903b0ca428c174e611ad78ee6508fefeab7a8b2eb60e55b554280679b2c07c6"
`

func TestFetchRecipeMergesBinariesToml(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.toml":
				fmt.Fprint(w, recipeNoBinaries)
			case "/recipes/j/jq.binaries.toml":
				fmt.Fprint(w, binariesToml)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2", len(rec.Binary))
	}
	b := rec.Binary["darwin-arm64"]
	if b.SHA256 != "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e" {
		t.Errorf("SHA256 = %q", b.SHA256)
	}
}

func TestFetchRecipeBinaries404NoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.toml":
				fmt.Fprint(w, recipeNoBinaries)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) != 0 {
		t.Errorf("Binary count = %d, want 0",
			len(rec.Binary))
	}
}

func TestFetchRecipeInlineBinariesSkipsFetch(t *testing.T) {
	var binariesFetched bool
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.toml":
				fmt.Fprint(w, validTOML+`
[binary.darwin-arm64]
url = "https://example.com/jq-darwin"
sha256 = "inline123"
`)
			case "/recipes/j/jq.binaries.toml":
				binariesFetched = true
				fmt.Fprint(w, binariesToml)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binariesFetched {
		t.Error(".binaries.toml fetched despite inline binaries")
	}
	if rec.Binary["darwin-arm64"].SHA256 != "inline123" {
		t.Errorf("expected inline binary SHA256")
	}
}

// --- Behavior 12: ghcrBaseFromURL parses owner/repo ---

func TestGhcrBaseFromRawGitHubURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			"standard",
			"https://raw.githubusercontent.com/kelp/gale-recipes/main",
			"kelp/gale-recipes",
		},
		{
			"with refs",
			"https://raw.githubusercontent.com/org/repo/refs/heads/main",
			"org/repo",
		},
		{
			"trailing slash",
			"https://raw.githubusercontent.com/kelp/gale-recipes/main/",
			"kelp/gale-recipes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ghcrBaseFromURL(tt.url)
			if got != tt.want {
				t.Errorf("ghcrBaseFromURL(%q) = %q, want %q",
					tt.url, got, tt.want)
			}
		})
	}
}

func TestGhcrBaseFromNonGitHubURL(t *testing.T) {
	got := ghcrBaseFromURL("https://example.com/recipes")
	if got != defaultGHCRBase {
		t.Errorf("ghcrBaseFromURL(non-github) = %q, want %q",
			got, defaultGHCRBase)
	}
}
