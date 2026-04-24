package registry

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kelp/gale/internal/recipe"
)

// validCommitHash matches a lowercase hex string 7-40 chars long.
var validCommitHash = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

const DefaultURL = "https://raw.githubusercontent.com/" +
	"kelp/gale-recipes/main"

const defaultGHCRBase = "kelp/gale-recipes"

// Registry fetches recipe TOML files from a remote HTTP
// registry using letter-bucketed paths.
type Registry struct {
	BaseURL string

	// CacheDir is the root for HTTP response caching. When
	// non-empty, FetchRecipe and related calls write fetched
	// bodies + ETags under <CacheDir>/registry/<hash>/ and
	// revalidate with If-None-Match on subsequent calls. When
	// empty, no caching is performed. Defaults to
	// ~/.gale/cache/ via New() / NewWithURL(); tests set it to
	// a temp dir.
	CacheDir string

	// warnf logs a warning. Defaults to fmt.Fprintf(os.Stderr, ...).
	// Override in tests to capture output.
	warnf func(format string, args ...any)
}

// New returns a Registry configured with DefaultURL and the
// default on-disk cache under ~/.gale/cache/.
func New() *Registry {
	return &Registry{BaseURL: DefaultURL, CacheDir: defaultCacheDir()}
}

// NewWithURL returns a Registry with the given base URL and
// the default on-disk cache. If url is empty, DefaultURL is
// used.
func NewWithURL(url string) *Registry {
	if url == "" {
		return New()
	}
	return &Registry{BaseURL: url, CacheDir: defaultCacheDir()}
}

// repoBase returns BaseURL with the trailing path segment
// (the ref, typically "main") stripped, so a commit can be
// substituted for it. raw.githubusercontent.com URLs have
// the form ".../<owner>/<repo>/<ref>"; FetchRecipeVersion
// needs the ".../<owner>/<repo>" prefix to splice a commit
// in. When BaseURL has no path component (test setups
// pointing at httptest.Server.URL), returns it unchanged.
func (r *Registry) repoBase() string {
	u, err := url.Parse(r.BaseURL)
	if err != nil {
		return r.BaseURL
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		return r.BaseURL
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		u.Path = path[:i]
	} else {
		u.Path = ""
	}
	return u.String()
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
// package from the registry. Uses an ETag-based HTTP cache
// under r.CacheDir when set.
func (r *Registry) FetchRecipe(name string) (*recipe.Recipe, error) {
	if name == "" {
		return nil, fmt.Errorf("fetch recipe: name must not be empty")
	}

	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.toml",
		r.BaseURL, bucket, name)

	client := &http.Client{Timeout: 30 * time.Second}
	body, err := cachedGet(client, url, r.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
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
	body, err := cachedGet(client, indexURL, r.CacheDir)
	if err != nil {
		return nil, fmt.Errorf(
			"fetch version index for %s: %w", name, err)
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

	// Fetch recipe at the specific commit. BaseURL already
	// includes the ref (e.g. "/main") for the .versions index
	// above; for a per-commit fetch we substitute the commit
	// for that ref segment.
	recipeURL := fmt.Sprintf("%s/%s/recipes/%s/%s.toml",
		r.repoBase(), commit, bucket, name)

	recipeBody, err := cachedGet(client, recipeURL, r.CacheDir)
	if err != nil {
		return nil, fmt.Errorf(
			"fetch %s@%s recipe: %w", name, version, err)
	}

	rec, err := recipe.Parse(string(recipeBody))
	if err != nil {
		return nil, fmt.Errorf(
			"parse %s@%s recipe: %w", name, version, err)
	}

	return rec, nil
}

// fetchBinaries fetches the .binaries.toml file for a
// package. Returns nil (not error) if the file is not found
// or the network is unreachable — the caller falls back to
// source build. Uses the ETag cache when enabled.
func (r *Registry) fetchBinaries(name string) (*recipe.BinaryIndex, error) {
	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.binaries.toml",
		r.BaseURL, bucket, name)

	client := &http.Client{Timeout: 30 * time.Second}
	body, err := cachedGet(client, url, r.CacheDir)
	if err != nil {
		// 404 → graceful nil, everything else → warn + nil.
		// cachedGet wraps the status in the error text so we
		// can detect 404 via string match; this keeps the
		// helper simple at the cost of a fragile pattern.
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		r.warn("fetch binaries %s: %v", name, err)
		return nil, nil //nolint:nilerr // network error is not fatal
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
	// 2. If requested has a "-1" suffix and the bare version
	//    exists in the index, return the bare entry. Legacy
	//    pre-revision .versions entries record the bare
	//    version; revision 1 is the implicit default, so a
	//    "-1" lookup should still find them.
	if strings.HasSuffix(requested, "-1") {
		bare := strings.TrimSuffix(requested, "-1")
		if _, ok := idx[bare]; ok {
			return bare, true
		}
	}
	// 3. Other -<digits> suffixes get no fallback — we only
	//    bump to latest revision for bare base versions.
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
