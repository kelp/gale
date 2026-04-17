package registry

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/trust"
)

// validCommitHash matches a lowercase hex string 7-40 chars long.
var validCommitHash = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

const DefaultURL = "https://raw.githubusercontent.com/" +
	"kelp/gale-recipes/main"

const defaultGHCRBase = "kelp/gale-recipes"

// Registry fetches recipe TOML files from a remote HTTP
// registry using letter-bucketed paths.
type Registry struct {
	BaseURL   string
	publicKey string // ed25519 public key (base64)

	// warnf logs a warning. Defaults to fmt.Fprintf(os.Stderr, ...).
	// Override in tests to capture output.
	warnf func(format string, args ...any)
}

// New returns a Registry configured with DefaultURL and
// the embedded recipe signing public key.
func New() *Registry {
	return &Registry{
		BaseURL:   DefaultURL,
		publicKey: trust.RecipePublicKey(),
	}
}

// NewWithURL returns a Registry with the given base URL
// and the embedded recipe signing public key. If url is
// empty, DefaultURL is used.
func NewWithURL(url string) *Registry {
	if url == "" {
		return New()
	}
	return &Registry{
		BaseURL:   url,
		publicKey: trust.RecipePublicKey(),
	}
}

// NewWithKey returns a Registry with the given base URL
// and public key. If url is empty, DefaultURL is used.
func NewWithKey(url, publicKey string) *Registry {
	if url == "" {
		url = DefaultURL
	}
	return &Registry{
		BaseURL:   url,
		publicKey: publicKey,
	}
}

// warn logs a warning via the configured warnf function,
// defaulting to stderr.
func (r *Registry) warn(format string, args ...any) {
	f := r.warnf
	if f == nil {
		f = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
		}
	}
	f(format, args...)
}

// FetchRecipe downloads and parses the recipe for the named
// package from the registry.
func (r *Registry) FetchRecipe(name string) (*recipe.Recipe, error) {
	if name == "" {
		return nil, fmt.Errorf("fetch recipe: name must not be empty")
	}

	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.toml",
		r.BaseURL, bucket, name)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch recipe %s: HTTP %d",
			name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}

	if err := r.verifyRecipe(body, url); err != nil {
		return nil, fmt.Errorf("recipe %s: %w", name, err)
	}

	rec, err := recipe.Parse(string(body))
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}

	// If the recipe has no inline binary entries, try to
	// fetch a separate .binaries.toml file.
	if len(rec.Binary) == 0 {
		idx, err := r.fetchBinaries(name)
		if err != nil {
			return nil, err
		}
		if idx != nil {
			base := ghcrBaseFromURL(r.BaseURL)
			recipe.MergeBinaries(rec, idx, base)
		}
	}

	return rec, nil
}

// FetchRecipeVersion fetches a recipe at a specific version
// by looking up the commit hash in the .versions index, then
// fetching the recipe at that commit.
func (r *Registry) FetchRecipeVersion(name, version string) (*recipe.Recipe, error) {
	if name == "" {
		return nil, fmt.Errorf("name must not be empty")
	}

	// Fetch the versions index.
	bucket := string(name[0])
	indexURL := fmt.Sprintf("%s/recipes/%s/%s.versions",
		r.BaseURL, bucket, name)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(indexURL)
	if err != nil {
		return nil, fmt.Errorf(
			"fetch version index for %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"version index for %s: HTTP %d", name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"read version index for %s: %w", name, err)
	}

	idx, err := parseVersionIndex(string(body))
	if err != nil {
		return nil, fmt.Errorf(
			"parse version index for %s: %w", name, err)
	}

	resolved, ok := pickVersion(idx, version)
	if !ok {
		return nil, fmt.Errorf(
			"%s@%s: version not found in registry", name, version)
	}
	commit := idx[resolved]

	// Fetch recipe at the specific commit.
	recipeURL := fmt.Sprintf("%s/%s/recipes/%s/%s.toml",
		r.BaseURL, commit, bucket, name)

	resp2, err := client.Get(recipeURL)
	if err != nil {
		return nil, fmt.Errorf(
			"fetch %s@%s recipe: %w", name, version, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"fetch %s@%s recipe: HTTP %d",
			name, version, resp2.StatusCode)
	}

	recipeBody, err := io.ReadAll(resp2.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"read %s@%s recipe: %w", name, version, err)
	}

	if err := r.verifyRecipe(recipeBody, recipeURL); err != nil {
		return nil, fmt.Errorf("%s@%s: %w", name, version, err)
	}

	rec, err := recipe.Parse(string(recipeBody))
	if err != nil {
		return nil, fmt.Errorf(
			"parse %s@%s recipe: %w", name, version, err)
	}

	return rec, nil
}

// fetchBinaries fetches the .binaries.toml file for a
// package. Returns nil (not error) if the file is not found.
func (r *Registry) fetchBinaries(name string) (*recipe.BinaryIndex, error) {
	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.binaries.toml",
		r.BaseURL, bucket, name)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		r.warn("fetch binaries %s: %v", name, err)
		return nil, nil //nolint:nilerr // network error is not fatal
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"fetch binaries %s: HTTP %d", name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"read binaries %s: %w", name, err)
	}

	if err := r.verifyRecipe(body, url); err != nil {
		return nil, fmt.Errorf("binaries %s: %w", name, err)
	}

	return recipe.ParseBinaryIndex(string(body))
}

// ghcrBaseFromURL extracts the "owner/repo" from a
// raw.githubusercontent.com URL. Falls back to the default
// GHCR base if the URL doesn't match the expected pattern.
func ghcrBaseFromURL(rawURL string) string {
	const prefix = "https://raw.githubusercontent.com/"
	if !strings.HasPrefix(rawURL, prefix) {
		return defaultGHCRBase
	}
	path := strings.TrimPrefix(rawURL, prefix)
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		return defaultGHCRBase
	}
	return parts[0] + "/" + parts[1]
}

// fetchSignature fetches the .sig file for the given recipe URL.
func (r *Registry) fetchSignature(url string) (string, error) {
	sigURL := url + ".sig"
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(sigURL)
	if err != nil {
		return "", fmt.Errorf("fetch signature: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch signature: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read signature: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

// verifyRecipe checks the recipe signature using the
// configured public key. Returns an error if no public key
// is configured.
func (r *Registry) verifyRecipe(data []byte, recipeURL string) error {
	if r.publicKey == "" {
		return fmt.Errorf("signature verification: no public key configured")
	}
	sig, err := r.fetchSignature(recipeURL)
	if err != nil {
		return fmt.Errorf("signature verification: %w", err)
	}
	ok, err := trust.Verify(data, sig, r.publicKey)
	if err != nil {
		return fmt.Errorf("signature verification: %w", err)
	}
	if !ok {
		return fmt.Errorf("signature verification failed: invalid signature")
	}
	return nil
}

// pickVersion resolves a user-supplied version string against
// a version→commit index. If requested is already in idx,
// returns it as-is. Otherwise, if requested has no
// "-<digits>" revision suffix, scans idx for entries of the
// form "<requested>-<N>" and returns the one with the
// highest N. Bare versions in the index are treated as
// revision 1 for comparison. Returns ("", false) if
// no match is found.
func pickVersion(idx map[string]string, requested string) (string, bool) {
	// 1. Exact match wins immediately.
	if _, ok := idx[requested]; ok {
		return requested, true
	}
	// 2. If requested already has a -<digits> suffix, no
	//    fallback — we only bump to latest revision for
	//    bare base versions.
	if hasRevisionSuffix(requested) {
		return "", false
	}
	// 3. Scan idx for entries of the form "<requested>-<N>"
	//    where N is all digits. Pick the highest N.
	prefix := requested + "-"
	bestRev := -1
	bestKey := ""
	for k := range idx {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		suf := k[len(prefix):]
		n, err := strconv.Atoi(suf)
		if err != nil || n < 0 {
			continue
		}
		if n > bestRev {
			bestRev = n
			bestKey = k
		}
	}
	if bestKey != "" {
		return bestKey, true
	}
	return "", false
}

// hasRevisionSuffix reports whether v ends with "-<digits>".
func hasRevisionSuffix(v string) bool {
	i := strings.LastIndex(v, "-")
	if i < 0 || i == len(v)-1 {
		return false
	}
	for _, c := range v[i+1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseVersionIndex parses a .versions file into a
// version→commit map. Each line is "version commit-hash".
func parseVersionIndex(data string) (map[string]string, error) {
	idx := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf(
				"malformed version line: %q", line)
		}
		if !validCommitHash.MatchString(parts[1]) {
			return nil, fmt.Errorf(
				"invalid commit hash: %q", parts[1])
		}
		idx[parts[0]] = parts[1]
	}
	return idx, nil
}
