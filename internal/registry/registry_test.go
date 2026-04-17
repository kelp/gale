package registry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kelp/gale/internal/trust"
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

// testKeyPair is a shared keypair for tests that need
// signed recipes but aren't testing verification itself.
var testKeyPair *trust.KeyPair

func init() {
	kp, err := trust.GenerateKeyPair()
	if err != nil {
		panic("generate test keypair: " + err.Error())
	}
	testKeyPair = kp
}

// signedHandler returns an http.HandlerFunc that serves
// recipes and their signatures for the given files map.
// Keys are URL paths (e.g., "/recipes/t/testpkg.toml"),
// values are file contents. Signature endpoints (*.sig)
// are auto-generated using the test keypair.
func signedHandler(files map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Check for .sig request.
		if strings.HasSuffix(path, ".sig") {
			base := strings.TrimSuffix(path, ".sig")
			content, ok := files[base]
			if !ok {
				http.NotFound(w, r)
				return
			}
			sig, err := trust.Sign(
				[]byte(content), testKeyPair.PrivateKey)
			if err != nil {
				http.Error(w, err.Error(),
					http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, sig)
			return
		}

		content, ok := files[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, content)
	}
}

// testRegistry returns a Registry configured with the test
// keypair's public key and the given base URL.
func testRegistry(baseURL string) *Registry {
	return &Registry{
		BaseURL:   baseURL,
		publicKey: testKeyPair.PublicKey,
	}
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

			reg := testRegistry(srv.URL)
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
	srv := httptest.NewServer(signedHandler(
		map[string]string{
			"/recipes/t/testpkg.toml": validTOML,
		}))
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
		}))
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
	srv := httptest.NewServer(signedHandler(
		map[string]string{
			"/recipes/b/badpkg.toml": malformed,
		}))
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

	inner := signedHandler(map[string]string{
		"/recipes/t/testpkg.toml": validTOML,
	})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			called = true
			mu.Unlock()
			inner.ServeHTTP(w, r)
		}))
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

	srv := httptest.NewServer(signedHandler(
		map[string]string{
			"/recipes/j/jq.versions":            versionsBody,
			"/" + commit + "/recipes/j/jq.toml": validTOML,
		}))
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

func TestFetchRecipeVersionNotFound(t *testing.T) {
	versionsBody := "1.8.1 abc1234\n"

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, versionsBody)
		}))
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
		func(w http.ResponseWriter, r *http.Request) {}))
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
	srv := httptest.NewServer(signedHandler(
		map[string]string{
			"/recipes/j/jq.toml":          recipeNoBinaries,
			"/recipes/j/jq.binaries.toml": binariesToml,
		}))
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
	srv := httptest.NewServer(signedHandler(
		map[string]string{
			"/recipes/j/jq.toml": recipeNoBinaries,
		}))
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
		func(w http.ResponseWriter, r *http.Request) {}))
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
	inner := signedHandler(map[string]string{
		"/recipes/j/jq.toml": inlineRecipe,
	})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/recipes/j/jq.binaries.toml" {
				binariesFetched = true
			}
			inner.ServeHTTP(w, r)
		}))
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
		}))
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
		}))
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
		}))
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
		func(w http.ResponseWriter, r *http.Request) {}))
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
		}))
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
		}))
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
		}))
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

	srv := httptest.NewServer(signedHandler(
		map[string]string{
			"/recipes/j/jq.toml":          recipeNoBinaries,
			"/recipes/j/jq.binaries.toml": staleBinaries,
		}))
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
		}))
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
		func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	reg := &Registry{BaseURL: addr}
	_, err := reg.FetchRecipeVersion("jq", "1.0.0")
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
}

// --- Behavior 27: FetchRecipe verifies signature ---

func TestFetchRecipeVerifiesSignature(t *testing.T) {
	kp, err := trust.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	recipeBody := []byte(validTOML)
	sig, err := trust.Sign(recipeBody, kp.PrivateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/t/testpkg.toml":
				w.Write(recipeBody)
			case "/recipes/t/testpkg.toml.sig":
				fmt.Fprint(w, sig)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:   srv.URL,
		publicKey: kp.PublicKey,
	}
	rec, err := reg.FetchRecipe("testpkg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Name != "testpkg" {
		t.Errorf("Name = %q, want %q",
			rec.Package.Name, "testpkg")
	}
}

// --- Behavior 28: FetchRecipe rejects bad signature ---

func TestFetchRecipeRejectsBadSignature(t *testing.T) {
	kp1, _ := trust.GenerateKeyPair()
	kp2, _ := trust.GenerateKeyPair()

	recipeBody := []byte(validTOML)
	sig, _ := trust.Sign(recipeBody, kp1.PrivateKey)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/t/testpkg.toml":
				w.Write(recipeBody)
			case "/recipes/t/testpkg.toml.sig":
				fmt.Fprint(w, sig)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:   srv.URL,
		publicKey: kp2.PublicKey,
	}
	_, err := reg.FetchRecipe("testpkg")
	if err == nil {
		t.Fatal("expected error for bad signature")
	}
}

// --- Behavior 29: FetchRecipe rejects missing signature ---

func TestFetchRecipeRejectsMissingSignature(t *testing.T) {
	kp, _ := trust.GenerateKeyPair()

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/t/testpkg.toml":
				fmt.Fprint(w, validTOML)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:   srv.URL,
		publicKey: kp.PublicKey,
	}
	_, err := reg.FetchRecipe("testpkg")
	if err == nil {
		t.Fatal("expected error for missing signature")
	}
}

// --- Behavior 30: verifyRecipe errors when publicKey is empty ---

func TestVerifyRecipeErrorsWhenPublicKeyEmpty(t *testing.T) {
	reg := &Registry{BaseURL: "https://example.com"}
	err := reg.verifyRecipe([]byte("data"), "https://example.com/r.toml")
	if err == nil {
		t.Fatal("expected error when publicKey is empty")
	}
}

// --- Behavior 36: New always sets publicKey ---

func TestNewSetsPublicKey(t *testing.T) {
	reg := New()
	if reg.publicKey == "" {
		t.Fatal("New() should set publicKey")
	}
}

func TestNewWithURLSetsPublicKey(t *testing.T) {
	reg := NewWithURL("https://example.com")
	if reg.publicKey == "" {
		t.Fatal("NewWithURL() should set publicKey")
	}
}

func TestNewWithKeySetsPublicKey(t *testing.T) {
	kp, _ := trust.GenerateKeyPair()
	reg := NewWithKey("https://example.com", kp.PublicKey)
	if reg.publicKey != kp.PublicKey {
		t.Errorf("publicKey = %q, want %q",
			reg.publicKey, kp.PublicKey)
	}
}

// --- Behavior 31: FetchRecipe verifies binaries signature ---

func TestFetchRecipeVerifiesBinariesSignature(t *testing.T) {
	kp, _ := trust.GenerateKeyPair()

	recipeBody := []byte(recipeNoBinaries)
	recipeSig, _ := trust.Sign(recipeBody, kp.PrivateKey)

	binBody := []byte(binariesToml)
	binSig, _ := trust.Sign(binBody, kp.PrivateKey)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.toml":
				w.Write(recipeBody)
			case "/recipes/j/jq.toml.sig":
				fmt.Fprint(w, recipeSig)
			case "/recipes/j/jq.binaries.toml":
				w.Write(binBody)
			case "/recipes/j/jq.binaries.toml.sig":
				fmt.Fprint(w, binSig)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:   srv.URL,
		publicKey: kp.PublicKey,
	}
	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Binary) != 2 {
		t.Fatalf("Binary count = %d, want 2", len(rec.Binary))
	}
}

// --- Behavior 32: FetchRecipe rejects bad binaries signature ---

func TestFetchRecipeRejectsBadBinariesSignature(t *testing.T) {
	kp1, _ := trust.GenerateKeyPair()
	kp2, _ := trust.GenerateKeyPair()

	recipeBody := []byte(recipeNoBinaries)
	recipeSig, _ := trust.Sign(recipeBody, kp1.PrivateKey)

	binBody := []byte(binariesToml)
	binSig, _ := trust.Sign(binBody, kp2.PrivateKey)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.toml":
				w.Write(recipeBody)
			case "/recipes/j/jq.toml.sig":
				fmt.Fprint(w, recipeSig)
			case "/recipes/j/jq.binaries.toml":
				w.Write(binBody)
			case "/recipes/j/jq.binaries.toml.sig":
				fmt.Fprint(w, binSig)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:   srv.URL,
		publicKey: kp1.PublicKey,
	}
	_, err := reg.FetchRecipe("jq")
	if err == nil {
		t.Fatal("expected error for bad binaries signature")
	}
}

// --- Behavior 33: FetchRecipeVersion verifies signature ---

func TestFetchRecipeVersionVerifiesSignature(t *testing.T) {
	kp, _ := trust.GenerateKeyPair()

	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.7.1 " + commit + "\n"

	recipeBody := []byte(validTOML)
	sig, _ := trust.Sign(recipeBody, kp.PrivateKey)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.versions":
				fmt.Fprint(w, versionsBody)
			case "/" + commit + "/recipes/j/jq.toml":
				w.Write(recipeBody)
			case "/" + commit + "/recipes/j/jq.toml.sig":
				fmt.Fprint(w, sig)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:   srv.URL,
		publicKey: kp.PublicKey,
	}
	rec, err := reg.FetchRecipeVersion("jq", "1.7.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Package.Name != "testpkg" {
		t.Errorf("Name = %q, want %q",
			rec.Package.Name, "testpkg")
	}
}

// --- Behavior 34: FetchRecipeVersion rejects bad signature ---

func TestFetchRecipeVersionRejectsBadSignature(t *testing.T) {
	kp1, _ := trust.GenerateKeyPair()
	kp2, _ := trust.GenerateKeyPair()

	const commit = "abc1234def5678901234567890abcdef12345678"
	versionsBody := "1.7.1 " + commit + "\n"

	recipeBody := []byte(validTOML)
	sig, _ := trust.Sign(recipeBody, kp1.PrivateKey)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.versions":
				fmt.Fprint(w, versionsBody)
			case "/" + commit + "/recipes/j/jq.toml":
				w.Write(recipeBody)
			case "/" + commit + "/recipes/j/jq.toml.sig":
				fmt.Fprint(w, sig)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:   srv.URL,
		publicKey: kp2.PublicKey,
	}
	_, err := reg.FetchRecipeVersion("jq", "1.7.1")
	if err == nil {
		t.Fatal("expected error for bad signature")
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

// --- Behavior 35: end-to-end signature verification ---

func TestFetchRecipeEndToEndSignatureFlow(t *testing.T) {
	kp, err := trust.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	recipeBody := []byte(recipeNoBinaries)
	recipeSig, _ := trust.Sign(recipeBody, kp.PrivateKey)

	binBody := []byte(binariesToml)
	binSig, _ := trust.Sign(binBody, kp.PrivateKey)

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.toml":
				w.Write(recipeBody)
			case "/recipes/j/jq.toml.sig":
				fmt.Fprint(w, recipeSig)
			case "/recipes/j/jq.binaries.toml":
				w.Write(binBody)
			case "/recipes/j/jq.binaries.toml.sig":
				fmt.Fprint(w, binSig)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv.Close()

	reg := &Registry{
		BaseURL:   srv.URL,
		publicKey: kp.PublicKey,
	}

	rec, err := reg.FetchRecipe("jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Package.Name != "jq" {
		t.Errorf("Name = %q, want %q",
			rec.Package.Name, "jq")
	}
	if rec.Package.Version != "1.8.1" {
		t.Errorf("Version = %q, want %q",
			rec.Package.Version, "1.8.1")
	}
	if len(rec.Binary) != 2 {
		t.Errorf("Binary count = %d, want 2",
			len(rec.Binary))
	}

	// Tamper with recipe body — signature should reject.
	tamperedBody := []byte(
		strings.ReplaceAll(
			string(recipeBody), "1.8.1", "1.8.2-evil"))

	srv2 := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/recipes/j/jq.toml":
				w.Write(tamperedBody)
			case "/recipes/j/jq.toml.sig":
				fmt.Fprint(w, recipeSig)
			default:
				http.NotFound(w, r)
			}
		}))
	defer srv2.Close()

	reg2 := &Registry{
		BaseURL:   srv2.URL,
		publicKey: kp.PublicKey,
	}
	_, err = reg2.FetchRecipe("jq")
	if err == nil {
		t.Fatal("expected error for tampered recipe")
	}
}
