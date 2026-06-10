package registry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
	"github.com/kelp/gale/internal/timing"
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

const defaultGHCRBase = "kelp/gale-recipes"

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
func New() *Registry {
	return &Registry{
		BaseURL:  DefaultURL,
		CacheDir: defaultCacheDir(),
		Offline:  os.Getenv("GALE_OFFLINE") == "1",
	}
}

// NewWithURL returns a Registry with the given base URL and
// the default on-disk cache. If url is empty, DefaultURL is
// used. Honours GALE_OFFLINE=1 in the environment.
func NewWithURL(url string) *Registry {
	if url == "" {
		return New()
	}
	return &Registry{
		BaseURL:  url,
		CacheDir: defaultCacheDir(),
		Offline:  os.Getenv("GALE_OFFLINE") == "1",
	}
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
// package, resolving it atomically. It reads the latest entry
// from the package's .versions index and fetches BOTH the
// recipe and its .binaries.toml from that single immutable
// commit, so a caller never observes a new recipe paired with a
// stale binary index (the mutable-main-ref race). When the
// package has no usable .versions index, it falls back to the
// legacy two-file fetch against the configured ref. Uses an
// ETag-based HTTP cache under r.CacheDir when set.
func (r *Registry) FetchRecipe(name string) (*recipe.Recipe, error) {
	rec, err := r.fetchLatestPinned(name)
	if err == nil {
		return rec, nil
	}
	if errors.Is(err, errNoVersionIndex) {
		return r.fetchRecipe(name, true)
	}
	return nil, err
}

// errNoVersionIndex signals that a package has no usable
// .versions index (absent, unparseable, or empty), so the
// caller should fall back to the legacy ref-tip fetch.
var errNoVersionIndex = errors.New("no version index")

// fetchLatestPinned resolves the latest version from the
// package's .versions index and fetches the recipe and its
// .binaries.toml at that single commit — the atomic-resolution
// path. Returns errNoVersionIndex when the index is absent,
// unparseable, or empty so FetchRecipe can fall back.
func (r *Registry) fetchLatestPinned(name string) (*recipe.Recipe, error) {
	if err := ValidName(name); err != nil {
		return nil, fmt.Errorf("fetch recipe: %w", err)
	}

	defer timing.Phase("recipe-fetch " + name)()

	bucket := string(name[0])
	indexURL := fmt.Sprintf("%s/recipes/%s/%s.versions",
		r.BaseURL, bucket, name)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cr, err := r.cachedGet(ctx, indexURL)
	if err != nil {
		// Missing index (404 for pre-.versions recipes) or any
		// other fetch error → fall back to the legacy ref-tip
		// path rather than hard-failing.
		return nil, errNoVersionIndex
	}

	idx, err := parseVersionIndex(string(cr.Body))
	if err != nil {
		return nil, errNoVersionIndex
	}
	resolved, ok := pickLatest(idx)
	if !ok {
		return nil, errNoVersionIndex
	}
	commit := idx[resolved]

	// Both fetches share the 30s budget and, crucially, the
	// same immutable commit — the recipe and its binary index
	// come from one frozen snapshot.
	rec, err := r.fetchRecipeAtCommit(ctx, name, bucket, commit)
	if err != nil {
		return nil, err
	}
	if len(rec.Binary) == 0 {
		if err := r.mergeBinariesAtCommit(ctx, rec, name, bucket, commit); err != nil {
			return nil, err
		}
	}

	// Fallback: if the pinned commit had no matching binary index
	// (404, or a version/revision mismatch because .versions
	// points at the recipe-bump commit whose .binaries.toml is
	// still the prior version — the historical gale-recipes shape
	// before CI pinned .versions at the binaries commit), fall
	// back to the ref-tip binaries rather than regress to a source
	// build. This degrades to the pre-commit-pin behavior (no
	// atomicity guarantee, but no regression); once CI rewrites
	// .versions to point at the binaries commit, the pinned fetch
	// matches and this fallback never fires.
	// This is the pre-commit-pin behavior (ref-tip binaries), so it
	// adds no atomicity guarantee but also no regression. When it
	// actually rescues a binary the pinned commit lacked, we record
	// the package silently: .versions likely pins a commit whose
	// .binaries.toml doesn't carry the matching binary (a mispin).
	// The rescue is collected for a once-per-command summary
	// (drained via TakeMispinned) rather than warned per-package.
	// Once CI rewrites .versions to point at the binaries commit,
	// the pinned fetch above matches and this never fires.
	if len(rec.Binary) == 0 {
		idx, ferr := r.fetchBinaries(name)
		if ferr == nil && idx != nil {
			recipe.MergeBinaries(rec, idx, ghcrBaseFromURL(r.BaseURL))
			if len(rec.Binary) > 0 {
				recordMispin(name)
			}
		}
	}

	// Version-skew fallback: the resolved-latest version has no binary
	// at its pinned commit or at ref-tip. .versions lists a version
	// ahead of what main currently ships (a reverted release, or a
	// binary never built). Rather than source-build a phantom-latest
	// (a regression vs the pre-commit-pin client), fall back to the
	// main-tip recipe+binary — the version main actually ships — and
	// record the skew for a once-per-command summary (drained via
	// TakeSkewed).
	if len(rec.Binary) == 0 {
		if tipRec, ferr := r.fetchRecipe(name, true); ferr == nil && len(tipRec.Binary) > 0 {
			recordSkew(name)
			return tipRec, nil
		}
	}
	return rec, nil
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

// mergeBinariesAtCommit fetches the .binaries.toml pinned to
// the same commit as the recipe and merges it. A missing index
// is non-fatal (the caller source-builds).
func (r *Registry) mergeBinariesAtCommit(
	ctx context.Context, rec *recipe.Recipe, name, bucket, commit string,
) error {
	binURL := fmt.Sprintf("%s/%s/recipes/%s/%s.binaries.toml",
		r.repoBase(), commit, bucket, name)
	idx, err := r.fetchBinariesURL(ctx, name, binURL)
	if err != nil {
		return err
	}
	if idx != nil {
		recipe.MergeBinaries(rec, idx, ghcrBaseFromURL(r.BaseURL))
	}
	return nil
}

// pickLatest returns the newest version key in a version→commit
// index, using gale's version ordering (base version plus a
// numeric "-<N>" revision suffix). Returns ("", false) for an
// empty index.
//
// Deliberately NOT version.IsNewer: its optimistic always-true
// answer for non-semver strings (socat's "1.8.1.1", autossh's
// "1.4g") is designed for update-candidate checks; in a
// max-selection loop it degenerates to last-map-key-wins, and Go
// randomizes map iteration — a bare `gale install socat`
// resolved to the OLDER revision ~10% of the time (gh#58).
// indexKeyNewer is a deterministic total order instead.
func pickLatest(idx map[string]string) (string, bool) {
	best := ""
	for k := range idx {
		if best == "" || indexKeyNewer(k, best) {
			best = k
		}
	}
	return best, best != ""
}

// indexKeyNewer reports whether index key a orders after key b.
// Bases compare by semver when both parse as semver, by natural
// (digit-run-aware) ordering otherwise; equal bases break the
// tie on the numeric "-<N>" revision suffix (absent means 1),
// then on the raw string so the order is total.
func indexKeyNewer(a, b string) bool {
	aBase, aRev := store.SplitRevision(a)
	bBase, bRev := store.SplitRevision(b)
	if aBase != bBase {
		av, bv := "v"+aBase, "v"+bBase
		if semver.IsValid(av) && semver.IsValid(bv) {
			if c := semver.Compare(av, bv); c != 0 {
				return c > 0
			}
		} else if c := naturalCompare(aBase, bBase); c != 0 {
			return c > 0
		}
	}
	if aRev != bRev {
		return aRev > bRev
	}
	return a > b
}

// naturalCompare compares two strings chunk-wise: maximal runs
// of ASCII digits compare numerically, everything else compares
// byte-wise. Gives "1.8.1.10" > "1.8.1.2" and "15.2.rel2" >
// "15.2.rel1" without pretending such strings are semver.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		if isASCIIDigit(a[0]) && isASCIIDigit(b[0]) {
			aNum, aRest := splitLeadingDigits(a)
			bNum, bRest := splitLeadingDigits(b)
			at := strings.TrimLeft(aNum, "0")
			bt := strings.TrimLeft(bNum, "0")
			// Longer trimmed digit run is the bigger number;
			// equal lengths compare lexically (same as
			// numerically for equal-length digit strings).
			if len(at) != len(bt) {
				if len(at) < len(bt) {
					return -1
				}
				return 1
			}
			if at != bt {
				if at < bt {
					return -1
				}
				return 1
			}
			a, b = aRest, bRest
			continue
		}
		if a[0] != b[0] {
			if a[0] < b[0] {
				return -1
			}
			return 1
		}
		a, b = a[1:], b[1:]
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	default:
		return 1
	}
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// splitLeadingDigits splits s into its maximal leading digit run
// and the remainder.
func splitLeadingDigits(s string) (digits, rest string) {
	i := 0
	for i < len(s) && isASCIIDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// FetchRecipeMetadata is FetchRecipe without the secondary
// .binaries.toml roundtrip. Suitable for read-only consumers
// (e.g. `gale info`) that only need package metadata, not the
// binary distribution map. Saves one HTTP request per
// invocation — significant for cache-cold runs against the
// real registry. See audit/readonly/network-perf/0005.
func (r *Registry) FetchRecipeMetadata(name string) (*recipe.Recipe, error) {
	return r.fetchRecipe(name, false)
}

// fetchRecipe is the shared implementation. When mergeBinaries
// is true the legacy behavior is preserved (extra
// .binaries.toml fetch when no inline binaries are declared).
func (r *Registry) fetchRecipe(name string, mergeBinaries bool) (*recipe.Recipe, error) {
	if err := ValidName(name); err != nil {
		return nil, fmt.Errorf("fetch recipe: %w", err)
	}

	defer timing.Phase("recipe-fetch " + name)()

	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.toml",
		r.BaseURL, bucket, name)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cr, err := r.cachedGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}

	rec, err := recipe.Parse(string(cr.Body))
	if err != nil {
		return nil, fmt.Errorf("fetch recipe %s: %w", name, err)
	}

	// If the recipe has no inline binary entries, try to
	// fetch a separate .binaries.toml file.
	if mergeBinaries && len(rec.Binary) == 0 {
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
	if err := ValidName(name); err != nil {
		return nil, err
	}

	defer timing.Phase(fmt.Sprintf("recipe-fetch %s@%s", name, version))()

	// Fetch the versions index.
	bucket := string(name[0])
	indexURL := fmt.Sprintf("%s/recipes/%s/%s.versions",
		r.BaseURL, bucket, name)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cr, err := r.cachedGet(ctx, indexURL)
	if err != nil {
		return nil, fmt.Errorf(
			"fetch version index for %s: %w", name, err,
		)
	}

	idx, err := parseVersionIndex(string(cr.Body))
	if err != nil {
		return nil, fmt.Errorf(
			"parse version index for %s: %w", name, err,
		)
	}

	resolved, ok := pickVersion(idx, version)
	if !ok {
		return nil, fmt.Errorf(
			"%s@%s: version not found in registry", name, version,
		)
	}
	commit := idx[resolved]

	// Fetch recipe at the specific commit. BaseURL already
	// includes the ref (e.g. "/main") for the .versions index
	// above; for a per-commit fetch we substitute the commit
	// for that ref segment.
	recipeURL := fmt.Sprintf("%s/%s/recipes/%s/%s.toml",
		r.repoBase(), commit, bucket, name)

	// Reuse the same context — both fetches share the 30s budget,
	// which is intentional: a slow versions-index fetch should not
	// extend the per-call deadline for the follow-up recipe fetch.
	rcr, err := r.cachedGet(ctx, recipeURL)
	if err != nil {
		return nil, fmt.Errorf(
			"fetch %s@%s recipe: %w", name, version, err,
		)
	}

	rec, err := recipe.Parse(string(rcr.Body))
	if err != nil {
		return nil, fmt.Errorf(
			"parse %s@%s recipe: %w", name, version, err,
		)
	}

	// Fetch the binary index pinned to the same commit so a
	// pinned-version install resolves a consistent recipe+binary
	// pair instead of always source-building.
	if len(rec.Binary) == 0 {
		if err := r.mergeBinariesAtCommit(ctx, rec, name, bucket, commit); err != nil {
			return nil, err
		}
	}

	return rec, nil
}

// fetchBinaries fetches the .binaries.toml file for a package
// at the configured ref tip. Returns nil (not error) if the
// file is not found or the network is unreachable — the caller
// falls back to source build. Uses the ETag cache when enabled.
func (r *Registry) fetchBinaries(name string) (*recipe.BinaryIndex, error) {
	bucket := string(name[0])
	url := fmt.Sprintf("%s/recipes/%s/%s.binaries.toml",
		r.BaseURL, bucket, name)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return r.fetchBinariesURL(ctx, name, url)
}

// fetchBinariesURL fetches and parses a .binaries.toml from an
// explicit URL (ref tip or per-commit). A 404 or network error
// is non-fatal: it returns (nil, nil) so the caller source-builds.
func (r *Registry) fetchBinariesURL(
	ctx context.Context, name, url string,
) (*recipe.BinaryIndex, error) {
	cr, err := r.cachedGet(ctx, url)
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

	return recipe.ParseBinaryIndex(string(cr.Body))
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
	if bare, ok := strings.CutSuffix(requested, "-1"); ok {
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
