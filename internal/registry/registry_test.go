package registry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// fileHandler returns an http.HandlerFunc that serves file
// contents for the given map of URL path → content. Missing
// paths return 404.
func fileHandler(files map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		content, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, content)
	}
}

// testRegistry returns a Registry pointing at the given base URL.
func testRegistry(baseURL string) *Registry {
	return &Registry{BaseURL: baseURL}
}

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
			var paths []string

			// The .versions probe runs first and is served the
			// recipe body, which fails to parse as a version
			// index, so FetchRecipe falls back to the ref-tip
			// recipe fetch. We assert the correctly-bucketed
			// .toml URL is among the requests rather than that
			// it is the first one.
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					paths = append(paths, r.URL.Path)
					mu.Unlock()
					fmt.Fprint(w, validTOML)
				},
			))
			defer srv.Close()

			reg := testRegistry(srv.URL)
			_, _ = reg.FetchRecipe(tt.pkg)

			mu.Lock()
			defer mu.Unlock()

			found := false
			for _, p := range paths {
				if p == tt.wantPath {
					found = true
				}
			}
			if !found {
				t.Errorf("recipe path %q not requested; got %v",
					tt.wantPath, paths)
			}
		})
	}
}

// --- Behavior 2: FetchRecipe downloads and parses recipe ---

func TestFetchRecipeParsesValidTOML(t *testing.T) {
	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/t/testpkg.toml": validTOML,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
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
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	_, err := reg.FetchRecipe("nonexistent")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// --- Behavior 4: FetchRecipe returns error for malformed TOML ---

func TestFetchRecipeErrorsOnMalformedTOML(t *testing.T) {
	malformed := "this is not valid toml [[["
	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/b/badpkg.toml": malformed,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
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

	inner := fileHandler(map[string]string{
		"/recipes/t/testpkg.toml": validTOML,
	})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			called = true
			mu.Unlock()
			inner.ServeHTTP(w, r)
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
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
	input := "1.0.0 aaaaaaa\n\n  \n2.0.0 bbbbbbb\n"
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

func TestParseVersionIndexRejectsInvalidCommitHash(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"path traversal", "1.0.0 ../../etc/passwd\n"},
		{"non-hex characters", "1.0.0 xyz123ghijkl\n"},
		{"too short", "1.0.0 abc12\n"},
		{"too long", "1.0.0 " +
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"},
		{"uppercase hex", "1.0.0 ABC1234DEF5678901234567890ABCDEF12345678\n"},
		{"special characters", "1.0.0 abc123/def456\n"},
		{"empty hash", "1.0.0 \n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseVersionIndex(tt.input)
			if err == nil {
				t.Fatalf("expected error for %s commit hash",
					tt.name)
			}
		})
	}
}

func TestParseVersionIndexAcceptsValidCommitHashes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		commit string
	}{
		{
			"7-char short hash",
			"1.0.0 abc1234\n", "abc1234",
		},
		{
			"40-char full hash",
			"1.0.0 abc1234def5678901234567890abcdef12345678\n",
			"abc1234def5678901234567890abcdef12345678",
		},
		{
			"20-char hash",
			"1.0.0 0123456789abcdef0123\n",
			"0123456789abcdef0123",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, err := parseVersionIndex(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if idx["1.0.0"] != tt.commit {
				t.Errorf("commit = %q, want %q",
					idx["1.0.0"], tt.commit)
			}
		})
	}
}

// --- Behavior 8: FetchRecipeVersion fetches pinned version ---

func TestFetchRecipeVersion(t *testing.T) {
	// Serve both the .versions index and the recipe at a
	// specific commit.
	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.7.1 " + commit + "\n" +
		"1.8.1 9876543210abcdef9876543210abcdef98765432\n"

	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/j/jq.versions":            versionsBody,
			"/" + commit + "/recipes/j/jq.toml": validTOML,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipeVersion("jq", "1.7.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Name != "testpkg" {
		t.Errorf("Name = %q, want %q",
			rec.Package.Name, "testpkg")
	}
}

// TestFetchRecipeVersionStripsTrailingRefFromBaseURL guards
// against the production-URL bug where BaseURL already includes
// a ref segment (e.g. ".../kelp/gale-recipes/main") and
// FetchRecipeVersion appended /<commit>/ on top of /main/,
// producing a 404. The .versions index lives at the ref tip
// (BaseURL/recipes/...), but the per-commit recipe must drop
// the ref segment and substitute the commit.
func TestFetchRecipeVersionStripsTrailingRefFromBaseURL(t *testing.T) {
	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.7.1 " + commit + "\n"

	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/main/recipes/j/jq.versions":       versionsBody,
			"/" + commit + "/recipes/j/jq.toml": validTOML,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL + "/main")
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
	versionsBody := "1.8.1 abc1234\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, versionsBody)
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
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
		func(w http.ResponseWriter, r *http.Request) {},
	))
	addr := srv.URL
	srv.Close()

	reg := testRegistry(addr)
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
	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/j/jq.toml":          recipeNoBinaries,
			"/recipes/j/jq.binaries.toml": binariesToml,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
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
	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/j/jq.toml": recipeNoBinaries,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) != 0 {
		t.Errorf("Binary count = %d, want 0",
			len(rec.Binary))
	}
}

func TestFetchBinariesNetworkErrorLogsWarning(t *testing.T) {
	// Start a server and close it to cause a connection
	// error when fetching .binaries.toml.
	binSrv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {},
	))
	brokenAddr := binSrv.URL
	binSrv.Close()

	// Capture the warning via the warnf hook.
	var warnings []string
	reg := &Registry{BaseURL: brokenAddr}
	reg.warnf = func(format string, args ...any) {
		warnings = append(warnings,
			fmt.Sprintf(format, args...))
	}
	idx, err := reg.fetchBinaries("jq")
	// Should return (nil, nil) for graceful fallback.
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if idx != nil {
		t.Fatal("expected nil index on network error")
	}

	// A warning must be emitted.
	if len(warnings) == 0 {
		t.Fatal("expected a warning for network error, got none")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "binaries") {
			found = true
		}
	}
	if !found {
		t.Errorf("warning does not mention binaries: %v",
			warnings)
	}
}

func TestFetchRecipeInlineBinariesSkipsFetch(t *testing.T) {
	var binariesFetched bool

	inlineRecipe := validTOML + `
[binary.darwin-arm64]
url = "https://example.com/jq-darwin"
sha256 = "inline123"
`
	inner := fileHandler(map[string]string{
		"/recipes/j/jq.toml": inlineRecipe,
	})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/recipes/j/jq.binaries.toml" {
				binariesFetched = true
			}
			inner.ServeHTTP(w, r)
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
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

// --- Behavior 13: Search happy path ---

func TestSearchHappyPath(t *testing.T) {
	index := "jq\tLightweight and flexible command-line JSON processor\n" +
		"yq\tProcess YAML, JSON, XML, CSV and properties\n" +
		"ripgrep\tRecursively search directories for a regex\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/index.tsv" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, index)
		},
	))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	results, err := reg.Search("json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	// jq's description contains "JSON" so it should match.
	found := false
	for _, r := range results {
		if r.Name == "jq" {
			found = true
			if r.Score <= 0 {
				t.Errorf("jq score = %d, want > 0", r.Score)
			}
		}
	}
	if !found {
		t.Error("expected jq in search results")
	}

	// yq also mentions JSON in its description.
	foundYQ := false
	for _, r := range results {
		if r.Name == "yq" {
			foundYQ = true
		}
	}
	if !foundYQ {
		t.Error("expected yq in search results")
	}

	// ripgrep should NOT match "json".
	for _, r := range results {
		if r.Name == "ripgrep" {
			t.Errorf("ripgrep should not match 'json', score=%d",
				r.Score)
		}
	}
}

// --- Behavior 14: Search returns empty for no match ---

func TestSearchNoResults(t *testing.T) {
	index := "jq\tJSON processor\n" +
		"ripgrep\tSearch directories\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, index)
		},
	))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	results, err := reg.Search("zzzznotexist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// --- Behavior 15: Search errors on HTTP failure ---

func TestSearchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server error",
				http.StatusInternalServerError)
		},
	))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	_, err := reg.Search("anything")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

// --- Behavior 16: Search connection failure ---

func TestSearchConnectionFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {},
	))
	addr := srv.URL
	srv.Close()

	reg := &Registry{BaseURL: addr}
	_, err := reg.Search("anything")
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
}

// --- Behavior 17: FetchRecipeVersion with malformed .versions ---

func TestFetchRecipeVersionBadIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Return malformed data: three fields per line.
			fmt.Fprint(w, "1.0.0 abc123 extra-field\n")
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	_, err := reg.FetchRecipeVersion("jq", "1.0.0")
	if err == nil {
		t.Fatal("expected error for malformed version index")
	}
}

// --- Behavior 18: FetchRecipeVersion recipe fetch 404 ---

func TestFetchRecipeVersionRecipeFetchFails(t *testing.T) {
	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.7.1 " + commit + "\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.versions":
				fmt.Fprint(w, versionsBody)
			default:
				// The recipe at the commit URL is not found.
				http.NotFound(w, r)
			}
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	_, err := reg.FetchRecipeVersion("jq", "1.7.1")
	if err == nil {
		t.Fatal("expected error when recipe at commit returns 404")
	}
}

// --- Behavior 19: FetchRecipeVersion index 404 ---

func TestFetchRecipeVersionIndex404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	_, err := reg.FetchRecipeVersion("jq", "1.0.0")
	if err == nil {
		t.Fatal("expected error when .versions returns 404")
	}
}

// --- Behavior 20: Stale .binaries.toml version skips merge ---

func TestFetchRecipeBinariesStaleVersion(t *testing.T) {
	// Recipe is version 1.8.1; binaries.toml says 1.7.0.
	// MergeBinaries should skip because versions differ.
	const staleBinaries = `version = "1.7.0"

[darwin-arm64]
sha256 = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff0000000011111111"
`

	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/j/jq.toml":          recipeNoBinaries,
			"/recipes/j/jq.binaries.toml": staleBinaries,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) != 0 {
		t.Errorf("Binary count = %d, want 0 (stale version "+
			"should not merge)", len(rec.Binary))
	}
}

func TestFetchRecipeBinariesStaleRevision(t *testing.T) {
	const revisionedRecipe = `[package]
name = "jq"
version = "1.8.1"
revision = 2
description = "JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://example.com/jq-1.8.1.tar.gz"
sha256 = "abc123"
`
	const staleBinaries = `version = "1.8.1"

[darwin-arm64]
sha256 = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff0000000011111111"
`

	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/j/jq.toml":          revisionedRecipe,
			"/recipes/j/jq.binaries.toml": staleBinaries,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) != 0 {
		t.Errorf("Binary count = %d, want 0 (bare index "+
			"should not merge into revisioned recipe)", len(rec.Binary))
	}
}

// --- Behavior 21: parseIndex edge cases ---

func TestParseIndex(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []indexEntry
	}{
		{
			"normal entries",
			"jq\tJSON processor\nripgrep\tSearch tool\n",
			[]indexEntry{
				{Name: "jq", Description: "JSON processor"},
				{Name: "ripgrep", Description: "Search tool"},
			},
		},
		{
			"missing description",
			"jq\n",
			[]indexEntry{
				{Name: "jq", Description: ""},
			},
		},
		{
			"empty lines skipped",
			"jq\tJSON processor\n\n\nripgrep\tSearch\n",
			[]indexEntry{
				{Name: "jq", Description: "JSON processor"},
				{Name: "ripgrep", Description: "Search"},
			},
		},
		{
			"whitespace-only lines skipped",
			"jq\tJSON\n   \n  \t \n",
			[]indexEntry{
				{Name: "jq", Description: "JSON"},
			},
		},
		{
			"empty input",
			"",
			nil,
		},
		{
			"description with tabs",
			"jq\tJSON\tprocessor\ttool\n",
			[]indexEntry{
				{Name: "jq", Description: "JSON\tprocessor\ttool"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIndex(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d entries, want %d",
					len(got), len(tt.want))
			}
			for i, g := range got {
				if g.Name != tt.want[i].Name {
					t.Errorf("[%d] Name = %q, want %q",
						i, g.Name, tt.want[i].Name)
				}
				if g.Description != tt.want[i].Description {
					t.Errorf("[%d] Description = %q, want %q",
						i, g.Description, tt.want[i].Description)
				}
			}
		})
	}
}

// --- Behavior 22: FetchRecipe empty name ---

func TestFetchRecipeEmptyName(t *testing.T) {
	reg := New()
	_, err := reg.FetchRecipe("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

// --- Behavior 23: FetchRecipeVersion empty name ---

func TestFetchRecipeVersionEmptyName(t *testing.T) {
	reg := New()
	_, err := reg.FetchRecipeVersion("", "1.0.0")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

// --- Behavior 23a: FetchRecipe rejects invalid names without HTTP ---

// TestFetchRecipeRejectsInjectionNames guards against the bad-input
// finding that user-supplied recipe names are interpolated into the
// registry URL with zero validation. Each input below would, before
// the fix, produce an HTTP request against
// raw.githubusercontent.com with attacker-controlled path/query
// components. After the fix, validation must reject these before any
// network call (verified by the fact that we point the registry at
// an address that refuses connections — a real HTTP attempt would
// surface a connection error, not the validation error).
func TestFetchRecipeRejectsInjectionNames(t *testing.T) {
	// Use a closed server to make any actual HTTP request fail
	// loudly. We assert errors are returned for validation
	// reasons, not network reasons.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected HTTP request for invalid name: %s", r.URL.Path)
		},
	))
	defer srv.Close()

	cases := []string{
		"",              // empty
		"jq?foo=bar",    // query injection
		"jq#frag",       // fragment
		"%2e%2e/etc",    // url-encoded traversal
		"..",            // literal traversal
		"../etc",        // traversal
		"jq/sub",        // slash
		"jq with space", // whitespace
		"jq$shell",      // dollar
		";rm -rf /",     // shell metachars (leading non-alnum)
		"-jq",           // leading dash
		".jq",           // leading dot
		"JQ",            // uppercase
		"jq@1.7",        // version marker leaks through
		"jq\nfoo",       // newline
		"jq\x00null",    // NUL byte
		"é-utf8",        // multi-byte first rune
		"jq+plus",       // plus
		"jq:colon",      // colon
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			reg := testRegistry(srv.URL)
			_, err := reg.FetchRecipe(name)
			if err == nil {
				t.Fatalf("expected validation error for %q", name)
			}
		})
	}
}

func TestFetchRecipeVersionRejectsInjectionNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unexpected HTTP request for invalid name: %s", r.URL.Path)
		},
	))
	defer srv.Close()

	cases := []string{
		"jq?x=y", "jq/sub", "../etc", "%2e%2e", "JQ",
		"jq@v", " jq", "jq with space",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			reg := testRegistry(srv.URL)
			_, err := reg.FetchRecipeVersion(name, "1.0.0")
			if err == nil {
				t.Fatalf("expected validation error for %q", name)
			}
		})
	}
}

// TestValidNameAcceptsRealRecipeNames pins the validation rule
// against the actual gale-recipes naming scheme: lowercase
// alphanumerics, hyphens, leading digit allowed (e.g.
// "1password-cli"). If this regresses, an installed package would
// fail to refetch.
func TestValidNameAcceptsRealRecipeNames(t *testing.T) {
	good := []string{
		"jq", "ripgrep", "1password-cli", "arm-none-eabi-gcc",
		"awscli", "git-delta", "a", "0",
	}
	for _, name := range good {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidNameRejectsBadNames(t *testing.T) {
	bad := []string{
		"", "..", "jq/x", "jq?x", "JQ", "-jq", ".jq",
		"jq with space", "é", "jq@1.0", "jq+x",
	}
	for _, name := range bad {
		if err := ValidName(name); err == nil {
			t.Errorf("ValidName(%q) = nil, want error", name)
		}
	}
}

// --- Behavior 23b: FetchRecipeMetadata skips binaries roundtrip ---

// TestFetchRecipeMetadataSkipsBinariesFetch confirms that a
// metadata-only fetch (used by `gale info`) makes exactly one
// HTTP request for the .toml and never probes .binaries.toml.
func TestFetchRecipeMetadataSkipsBinariesFetch(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			if r.URL.Path == "/recipes/j/jq.toml" {
				fmt.Fprint(w, recipeNoBinaries)
				return
			}
			http.NotFound(w, r)
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipeMetadata("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Name != "jq" {
		t.Errorf("Name = %q, want jq", rec.Package.Name)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("expected 1 HTTP request, got %d: %v", len(paths), paths)
	}
	if paths[0] != "/recipes/j/jq.toml" {
		t.Errorf("path = %q, want /recipes/j/jq.toml", paths[0])
	}
}

// --- Behavior 24: NewWithURL empty string uses default ---

func TestNewWithURLEmpty(t *testing.T) {
	reg := NewWithURL("")
	if reg.BaseURL != DefaultURL {
		t.Errorf("BaseURL = %q, want %q",
			reg.BaseURL, DefaultURL)
	}
}

func TestNewWithURLCustom(t *testing.T) {
	reg := NewWithURL("https://example.com/recipes")
	if reg.BaseURL != "https://example.com/recipes" {
		t.Errorf("BaseURL = %q, want %q",
			reg.BaseURL, "https://example.com/recipes")
	}
}

// --- Behavior 25: Search sorts by score descending ---

func TestSearchResultsSortedByScore(t *testing.T) {
	// "jq" should score highest for query "jq" (exact name
	// match). "jqp" has prefix match. An entry with "jq" only
	// in description scores lower.
	index := "jq\tJSON processor\n" +
		"jqp\tA TUI playground for jq\n" +
		"yq\tYAML processor like jq\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, index)
		},
	))
	defer srv.Close()

	reg := &Registry{BaseURL: srv.URL}
	results, err := reg.Search("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d",
			len(results))
	}
	// Results should be in descending score order.
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted: [%d].Score=%d > "+
				"[%d].Score=%d",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
	// First result should be "jq" (exact match).
	if results[0].Name != "jq" {
		t.Errorf("first result = %q, want %q",
			results[0].Name, "jq")
	}
}

// --- Behavior 26: FetchRecipeVersion connection failure ---

func TestFetchRecipeVersionConnectionFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {},
	))
	addr := srv.URL
	srv.Close()

	reg := &Registry{BaseURL: addr}
	_, err := reg.FetchRecipeVersion("jq", "1.0.0")
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
}

// --- pickVersion behaviors ---

// Behavior 1: exact match in index returns the key as-is.
func TestPickVersionExactMatch(t *testing.T) {
	idx := map[string]string{"8.19.0-2": "abc"}
	got, ok := pickVersion(idx, "8.19.0-2")
	if !ok {
		t.Fatal("expected ok=true for exact match")
	}
	if got != "8.19.0-2" {
		t.Errorf("got %q, want %q", got, "8.19.0-2")
	}
}

// Behavior 2: no revision suffix, multiple revisions present,
// returns the highest numbered revision.
func TestPickVersionHighestRevision(t *testing.T) {
	idx := map[string]string{
		"8.19.0-1": "abc",
		"8.19.0-2": "def",
		"8.19.0-3": "ghi",
	}
	got, ok := pickVersion(idx, "8.19.0")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "8.19.0-3" {
		t.Errorf("got %q, want %q", got, "8.19.0-3")
	}
}

// Behavior 3: no revision suffix, single revision present,
// returns that revision.
func TestPickVersionSingleRevision(t *testing.T) {
	idx := map[string]string{"8.19.0-1": "abc"}
	got, ok := pickVersion(idx, "8.19.0")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "8.19.0-1" {
		t.Errorf("got %q, want %q", got, "8.19.0-1")
	}
}

// Behavior 4: no revision suffix, only bare version in index
// (legacy), returns via exact match.
func TestPickVersionBareVersionExactMatch(t *testing.T) {
	idx := map[string]string{"8.19.0": "abc"}
	got, ok := pickVersion(idx, "8.19.0")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "8.19.0" {
		t.Errorf("got %q, want %q", got, "8.19.0")
	}
}

// Behavior 5: bare + revisioned mix; exact match wins.
func TestPickVersionBareAndRevisionedExactMatchWins(t *testing.T) {
	idx := map[string]string{
		"8.19.0":   "abc",
		"8.19.0-2": "def",
	}
	got, ok := pickVersion(idx, "8.19.0")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "8.19.0" {
		t.Errorf("got %q, want %q", got, "8.19.0")
	}
}

// Behavior 6: no match at all returns ("", false).
// The index has 8.20.0-1 (matches 8.20.0) but not 8.19.0.
// Verify 8.20.0 resolves (proving the function runs), then
// verify 8.19.0 does not.
func TestPickVersionNoMatch(t *testing.T) {
	idx := map[string]string{"8.20.0-1": "abc"}

	// Sanity: 8.20.0 must resolve to prove the function works.
	got, ok := pickVersion(idx, "8.20.0")
	if !ok || got != "8.20.0-1" {
		t.Fatalf("expected 8.20.0 → 8.20.0-1, got (%q, %v)",
			got, ok)
	}

	// 8.19.0 is absent; must return ("", false).
	got, ok = pickVersion(idx, "8.19.0")
	if ok {
		t.Fatalf("expected ok=false for absent version, got key %q", got)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// Behavior 7: requested has revision suffix but is absent
// from index returns ("", false).
// The index has 8.19.0-1 (not 8.19.0-5).
// Verify 8.19.0-1 resolves exactly, then verify 8.19.0-5 does not.
func TestPickVersionRevisionSuffixNotPresent(t *testing.T) {
	idx := map[string]string{"8.19.0-1": "abc"}

	// Sanity: exact key must resolve.
	got, ok := pickVersion(idx, "8.19.0-1")
	if !ok || got != "8.19.0-1" {
		t.Fatalf("expected 8.19.0-1 → 8.19.0-1, got (%q, %v)",
			got, ok)
	}

	// 8.19.0-5 is absent; must return ("", false).
	got, ok = pickVersion(idx, "8.19.0-5")
	if ok {
		t.Fatalf("expected ok=false for absent revision, got key %q", got)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// Behavior 8: different base versions are not picked up by
// prefix scan (8.19.10 is not a revision of 8.19.0).
func TestPickVersionDifferentBaseVersionsFiltered(t *testing.T) {
	idx := map[string]string{
		"8.19.0-1":  "abc",
		"8.20.0-1":  "def",
		"8.19.10-1": "ghi",
	}
	got, ok := pickVersion(idx, "8.19.0")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "8.19.0-1" {
		t.Errorf("got %q, want %q", got, "8.19.0-1")
	}
}

// Behavior 8b: a "-1" suffix falls back to the bare version
// when the index only carries the bare entry. Pre-revision
// .versions indexes record the bare version, but bare recipes
// resolve to revision 1 (default), so a "-1" lookup must find
// them. Without this, gale update against legacy recipes
// reports "not found" even though the recipe exists.
func TestPickVersionRev1FallsBackToBare(t *testing.T) {
	idx := map[string]string{"0.12.3": "abc1234"}

	got, ok := pickVersion(idx, "0.12.3-1")
	if !ok {
		t.Fatal("expected ok=true for -1 → bare fallback")
	}
	if got != "0.12.3" {
		t.Errorf("got %q, want %q", got, "0.12.3")
	}
}

// Behavior 9: multi-digit revision numbers are compared
// numerically, not lexicographically.
func TestPickVersionMultiDigitRevisionNumericComparison(t *testing.T) {
	idx := map[string]string{
		"8.19.0-9":  "a",
		"8.19.0-10": "b",
		"8.19.0-2":  "c",
	}
	got, ok := pickVersion(idx, "8.19.0")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "8.19.0-10" {
		t.Errorf("got %q, want %q", got, "8.19.0-10")
	}
}

// --- Part 1: commit-pinned atomic resolution ---

func TestPickLatest(t *testing.T) {
	tests := []struct {
		name string
		idx  map[string]string
		want string
		ok   bool
	}{
		{
			name: "highest semver wins",
			idx: map[string]string{
				"1.7.1": "aaaaaaa",
				"1.8.1": "bbbbbbb",
			},
			want: "1.8.1",
			ok:   true,
		},
		{
			name: "revision beats bare same version",
			idx: map[string]string{
				"1.8.1":   "aaaaaaa",
				"1.8.1-2": "bbbbbbb",
			},
			want: "1.8.1-2",
			ok:   true,
		},
		{
			name: "single entry",
			idx:  map[string]string{"2.0.0": "ccccccc"},
			want: "2.0.0",
			ok:   true,
		},
		{
			name: "empty index",
			idx:  map[string]string{},
			want: "",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pickLatest(tt.idx)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("pickLatest = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFetchRecipeCommitPinned proves the default resolver
// reads recipe AND binaries from the single commit named by
// the latest .versions entry — never from the mutable main
// ref. The main-ref files are decoys with the WRONG version;
// if FetchRecipe touched them the assertion would fail.
func TestFetchRecipeCommitPinned(t *testing.T) {
	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.7.0 0000000000000000000000000000000000000000\n" +
		"1.8.1 " + commit + "\n"

	// Decoys served at the mutable main ref: an older recipe
	// and a stale binaries index. Commit-pinning must ignore
	// these.
	staleRecipe := strings.Replace(recipeNoBinaries,
		`version = "1.8.1"`, `version = "1.7.0"`, 1)
	staleBinaries := strings.Replace(binariesToml,
		`version = "1.8.1"`, `version = "1.7.0"`, 1)

	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/j/jq.versions":                     versionsBody,
			"/recipes/j/jq.toml":                         staleRecipe,
			"/recipes/j/jq.binaries.toml":                staleBinaries,
			"/" + commit + "/recipes/j/jq.toml":          recipeNoBinaries,
			"/" + commit + "/recipes/j/jq.binaries.toml": binariesToml,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Version != "1.8.1" {
		t.Fatalf("Version = %q, want 1.8.1 (read the mutable main ref?)",
			rec.Package.Version)
	}
	if len(rec.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2 (binaries not pinned to commit)",
			len(rec.Binary))
	}
}

// TestFetchRecipeFallsBackWhenNoVersions verifies that a
// recipe without a .versions index still resolves via the
// legacy two-file main-ref fetch.
func TestFetchRecipeFallsBackWhenNoVersions(t *testing.T) {
	srv := httptest.NewServer(fileHandler(
		map[string]string{
			// No .versions served -> 404 -> fallback.
			"/recipes/j/jq.toml":          recipeNoBinaries,
			"/recipes/j/jq.binaries.toml": binariesToml,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Version != "1.8.1" {
		t.Errorf("Version = %q, want 1.8.1", rec.Package.Version)
	}
	if len(rec.Binary) != 2 {
		t.Errorf("Binary count = %d, want 2", len(rec.Binary))
	}
}

// TestFetchRecipeVersionMergesBinaries verifies that the
// pinned-version path now also fetches .binaries.toml at the
// resolved commit and populates the binary map.
func TestFetchRecipeVersionMergesBinaries(t *testing.T) {
	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.8.1 " + commit + "\n"

	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/j/jq.versions":                     versionsBody,
			"/" + commit + "/recipes/j/jq.toml":          recipeNoBinaries,
			"/" + commit + "/recipes/j/jq.binaries.toml": binariesToml,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipeVersion("jq", "1.8.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2", len(rec.Binary))
	}
}

// recipeRev4 is a revisioned recipe whose Full() is "1.8.1-4".
const recipeRev4 = `[package]
name = "jq"
version = "1.8.1"
revision = 4
description = "JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://example.com/jq-1.8.1.tar.gz"
sha256 = "abc123"
`

// staleBinaries1_8_1 is the index as it exists at the recipe-BUMP
// commit: version still the bare "1.8.1", before CI re-committed
// the "1.8.1-4" entry. This is the real-world gale-recipes shape
// (binaries committed a commit or two after the recipe bump).
const staleBinaries1_8_1 = `version = "1.8.1"

[darwin-arm64]
sha256 = "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"
`

// tipBinaries1_8_1_4 is the up-to-date index at the ref tip,
// matching the rev-4 recipe.
const tipBinaries1_8_1_4 = `version = "1.8.1-4"

[darwin-arm64]
sha256 = "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"

[linux-amd64]
sha256 = "a903b0ca428c174e611ad78ee6508fefeab7a8b2eb60e55b554280679b2c07c6"
`

// TestFetchRecipeFallsBackToTipBinariesOnPinnedMismatch pins the
// real gale-recipes failure mode: .versions maps the revisioned
// version to the recipe-bump commit, where .binaries.toml is still
// the prior (bare) version. Pinning binaries to that commit would
// mismatch and silently source-build. The resolver must fall back
// to the ref-tip binaries (which DO match) rather than regress.
func TestFetchRecipeFallsBackToTipBinariesOnPinnedMismatch(t *testing.T) {
	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.8.1-4 " + commit + "\n"

	srv := httptest.NewServer(fileHandler(
		map[string]string{
			"/recipes/j/jq.versions":                     versionsBody,
			"/" + commit + "/recipes/j/jq.toml":          recipeRev4,
			"/" + commit + "/recipes/j/jq.binaries.toml": staleBinaries1_8_1,
			// ref-tip binaries: up to date, matches rev 4.
			"/recipes/j/jq.binaries.toml": tipBinaries1_8_1_4,
		},
	))
	defer srv.Close()

	reg := testRegistry(srv.URL)
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2 (should fall back to ref-tip binaries, not source-build)",
			len(rec.Binary))
	}
}
