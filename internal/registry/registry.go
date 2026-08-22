package registry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/timing"
	"github.com/kelp/gale/internal/version"
)

// validCommitHash matches a lowercase hex string 7-40 chars long.
var validCommitHash = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// validRecipeName matches the gale-recipes naming convention:
// lowercase ASCII alphanumerics and hyphens, starting with an
// alphanumeric character. This is the charset observed across
// all recipes in ../gale-recipes/recipes/ (e.g. "jq", "ripgrep",
// "1password-cli", "arm-none-eabi-gcc"). A 64-char upper bound
// rules out absurd inputs without rejecting anything the
// registry actually serves.
//
// Anything outside this charset — slash, dot, percent, query,
// fragment, whitespace, uppercase, non-ASCII — is rejected
// before the name is interpolated into a registry URL, closing
// the arbitrary-URL-fetch surface flagged by
// audit/readonly/bad-input/0002.
var validRecipeName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidName reports whether name matches the recipe naming
// convention. Returns nil for valid names and a descriptive
// error otherwise. Callers should invoke this before
// interpolating user-supplied names into registry URLs.
func ValidName(name string) error {
	if name == "" {
		return fmt.Errorf("package name must not be empty")
	}
	if !validRecipeName.MatchString(name) {
		return fmt.Errorf(
			"invalid package name %q: must match [a-z0-9][a-z0-9-]*",
			name,
		)
	}
	return nil
}

const DefaultURL = "https://raw.githubusercontent.com/" +
	"kelp/gale-recipes/main"

// Registry fetches recipe TOML files from a remote HTTP
// registry using letter-bucketed paths.
//
// # Cache contract
//
// The on-disk cache at <CacheDir>/registry/ is a documented
// optimization, not silent state. It stores HTTP response
// bodies keyed by sha256(url) plus the matching ETag for
// conditional revalidation. Rules:
//
//   - DryRun=true suppresses cache writes. Bodies are still
//     returned to callers, but no files are persisted.
//   - Offline=true skips network entirely. A cached entry is
//     served verbatim; absence of a cached entry returns a
//     "no cached entry" error. Set by `gale --dry-run` (writes)
//     and by `GALE_OFFLINE=1` (network).
//   - Stale-on-error: when client.Do fails with a network
//     error (DNS, ECONNREFUSED, deadline, context cancel),
//     the cached body is served if present. The cache is
//     NOT rewritten in this path — staleness propagates via
//     a marker the caller may surface in user-facing output.
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

	// DryRun suppresses cache writes. Reads still consult
	// the cache (304 revalidation still serves the cached
	// body), but a 200 OK is never persisted. Set this when
	// the command-layer `--dry-run` flag is in effect.
	DryRun bool

	// Offline suppresses network traffic entirely. Cached
	// entries are served verbatim; a cache miss returns a
	// clear error. Set this when `GALE_OFFLINE=1` is in the
	// environment.
	Offline bool

	// warnf logs a warning. Defaults to fmt.Fprintf(os.Stderr, ...).
	// Override in tests to capture output.
	warnf func(format string, args ...any)
}

// New returns a Registry configured with DefaultURL and the
// default on-disk cache under ~/.gale/cache/. Offline is set
// when GALE_OFFLINE=1 is in the environment; callers that need
// to override (e.g. for tests) can mutate the returned value.
//
// It fails only when no location on the machine can hold a cache
// (see defaultCacheDir). A Registry with caching deliberately off
// is written as a literal with an empty CacheDir; it is not
// something this constructor produces from a failure.
func New() (*Registry, error) {
	cacheDir, err := defaultCacheDir()
	if err != nil {
		return nil, err
	}
	return &Registry{
		BaseURL:  DefaultURL,
		CacheDir: cacheDir,
		Offline:  os.Getenv("GALE_OFFLINE") == "1",
	}, nil
}

// NewWithURL returns a Registry with the given base URL and
// the default on-disk cache. If url is empty, DefaultURL is
// used. Honours GALE_OFFLINE=1 in the environment.
func NewWithURL(url string) (*Registry, error) {
	reg, err := New()
	if err != nil {
		return nil, err
	}
	if url != "" {
		reg.BaseURL = url
	}
	return reg, nil
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
// package. It prefers the latest entry in the package's
// .versions index and fetches the recipe from that immutable
// commit. When the package has no usable .versions index, it
// falls back to the configured ref. Uses an ETag-based HTTP
// cache under r.CacheDir when set.
func (r *Registry) FetchRecipe(ctx context.Context, name string) (*recipe.Recipe, error) {
	pinned, perr := r.fetchLatestPinned(ctx, name)
	if perr == nil {
		return pinned, nil
	}
	if errors.Is(perr, errNoVersionIndex) {
		return r.fetchRecipe(ctx, name)
	}
	return nil, perr
}

// errNoVersionIndex signals that a package has no usable
// .versions index (absent, unparseable, or empty), so the
// caller should fall back to the legacy ref-tip fetch.
var errNoVersionIndex = errors.New("no version index")

// fetchLatestPinned resolves the latest version from the
// package's .versions index and fetches the recipe at that
// commit. Returns errNoVersionIndex when the index is absent,
// unparseable, or empty so FetchRecipe can fall back.
func (r *Registry) fetchLatestPinned(ctx context.Context, name string) (*recipe.Recipe, error) {
	if err := ValidName(name); err != nil {
		return nil, fmt.Errorf("fetch recipe: %w", err)
	}

	defer timing.Phase("recipe-fetch " + name)()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	idx, err := r.fetchVersionIndex(ctx, name)
	if err != nil {
		// Missing index (404 for pre-.versions recipes), parse
		// failure, or any other fetch error → fall back to the
		// ref-tip path rather than hard-failing.
		return nil, errNoVersionIndex
	}
	resolved, ok := pickLatest(idx)
	if !ok {
		return nil, errNoVersionIndex
	}
	commit := idx[resolved]
	return r.fetchRecipeAtCommit(ctx, name, string(name[0]), commit)
}

// fetchRecipeAtCommit fetches and parses a recipe TOML pinned
// to a specific commit.
func (r *Registry) fetchRecipeAtCommit(
	ctx context.Context, name, bucket, commit string,
) (*recipe.Recipe, error) {
	recipeURL := fmt.Sprintf("%s/%s/recipes/%s/%s.toml",
		r.repoBase(), commit, bucket, name)
	cr, err := r.cachedGet(ctx, recipeURL)
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s@%s: %w", name, commit, err)
	}
	rec, err := recipe.Parse(string(cr.Body))
	if err != nil {
		return nil, fmt.Errorf("parse recipe %s@%s: %w", name, commit, err)
	}
	return rec, nil
}

// fetchVersionIndex fetches and parses the .versions index for
// name. Returns (nil, error) on fetch or parse failure. The ctx
// should already carry an appropriate timeout; no new timeout is
// added here.
func (r *Registry) fetchVersionIndex(
	ctx context.Context, name string,
) (map[string]string, error) {
	bucket := string(name[0])
	indexURL := fmt.Sprintf("%s/recipes/%s/%s.versions",
		r.BaseURL, bucket, name)
	cr, err := r.cachedGet(ctx, indexURL)
	if err != nil {
		return nil, err
	}
	return parseVersionIndex(string(cr.Body))
}

// pickLatest returns the newest version key in a version→commit
// index, using gale's total version order (see version.KeyNewer).
// Returns ("", false) for an empty index.
//
// Deliberately NOT version.IsNewer: its optimistic always-true
// answer for non-semver strings (socat's "1.8.1.1", autossh's
// "1.4g") is designed for update-candidate checks; in a
// max-selection loop it degenerates to last-map-key-wins, and Go
// randomizes map iteration — a bare `gale install socat`
// resolved to the OLDER revision ~10% of the time (gh#58).
func pickLatest(idx map[string]string) (string, bool) {
	return version.Latest(mapKeys(idx))
}

// mapKeys returns the keys of a version→commit index.
func mapKeys(idx map[string]string) []string {
	keys := make([]string, 0, len(idx))
	for k := range idx {
		keys = append(keys, k)
	}
	return keys
}

// FetchRecipeMetadata is FetchRecipe for read-only consumers
// (e.g. `gale info`) that only need package metadata.
func (r *Registry) FetchRecipeMetadata(ctx context.Context, name string) (*recipe.Recipe, error) {
	return r.fetchRecipe(ctx, name)
}

// fetchRecipe downloads and parses the recipe TOML at the
// configured ref tip.
func (r *Registry) fetchRecipe(ctx context.Context, name string) (*recipe.Recipe, error) {
	if err := ValidName(name); err != nil {
		return nil, fmt.Errorf("fetch recipe: %w", err)
	}

	defer timing.Phase("recipe-fetch " + name)()

	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.toml",
		r.BaseURL, bucket, name)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cr, err := r.cachedGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}

	rec, err := recipe.Parse(string(cr.Body))
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}
	return rec, nil
}

// FetchRecipeVersion fetches a recipe at a specific version
// by looking up the commit hash in the .versions index, then
// fetching the recipe at that commit.
func (r *Registry) FetchRecipeVersion(ctx context.Context, name, version string) (*recipe.Recipe, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	defer timing.Phase(fmt.Sprintf("recipe-fetch %s@%s", name, version))()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	idx, err := r.fetchVersionIndex(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("fetch version index for %s: %w", name, err)
	}
	resolved, ok := pickVersion(idx, version)
	if !ok {
		return nil, fmt.Errorf("%s@%s: version not found in registry", name, version)
	}
	commit := idx[resolved]
	return r.fetchRecipeAtCommit(ctx, name, string(name[0]), commit)
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
	return version.Pick(mapKeys(idx), requested)
}

// parseVersionIndex parses a .versions file into a
// version→commit map. Each line is "version commit-hash".
func parseVersionIndex(data string) (map[string]string, error) {
	idx := make(map[string]string)
	for line := range strings.SplitSeq(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf(
				"malformed version line: %q", line,
			)
		}
		if !validCommitHash.MatchString(parts[1]) {
			return nil, fmt.Errorf(
				"invalid commit hash: %q", parts[1],
			)
		}
		idx[parts[0]] = parts[1]
	}
	return idx, nil
}
