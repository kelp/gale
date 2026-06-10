package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kelp/gale/internal/httpclient"
)

// Cache layout on disk:
//
//   <CacheDir>/registry/<hash>/body       — last response body
//   <CacheDir>/registry/<hash>/etag       — last ETag value
//   <CacheDir>/registry/<hash>/not_found  — negative-cache marker
//
// <hash> is sha256 of the request URL, hex-encoded. This keeps
// a 1:1 mapping per fetch URL without filesystem-hostile
// characters. See defaultCacheDir() for the production root
// (~/.gale/cache/), which is shared with the source-tarball
// cache in internal/build.
//
// The not_found marker contains an RFC3339Nano timestamp. When
// present and younger than negativeCacheTTL, cachedGet short-
// circuits and returns an HTTP 404 error without touching the
// network. This avoids repeated wire trips for recipe names
// that are absent from the central registry — common when
// users mix locally-developed packages into their gale.toml
// and run read-only commands like outdated / sbom / doctor.

// negativeCacheTTL bounds how long a cached 404 stays
// authoritative. Long enough to dedupe back-to-back read-only
// command invocations in a busy session, short enough that a
// freshly-published recipe shows up without manual cache
// surgery. The positive cache is governed by ETag revalidation
// and has no equivalent timeout.
const negativeCacheTTL = 1 * time.Hour

// errHTTP404 is the error returned for a 404 response (live or
// negative-cache replay). Surfaced as text "HTTP 404" so the
// existing string-match in fetchBinaries continues to work.
var errHTTP404 = fmt.Errorf("HTTP 404")

// cacheResult is the return value of cachedGet. Body is the
// response (whether from network or cache); Stale is set true
// when the body came from the cache because the network was
// unreachable (stale-on-error). Callers may surface staleness
// in user-facing output.
type cacheResult struct {
	Body  []byte
	Stale bool
}

// cacheKey derives a filesystem-safe key for the given URL.
func cacheKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// defaultCacheDir returns ~/.gale/cache/, or the empty string
// if the home directory can't be determined. Matches the
// directory used by internal/build for source tarballs.
func defaultCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gale", "cache")
}

// cachedGet fetches url with an HTTP conditional GET, applying
// the cache contract documented on Registry:
//
//   - r.Offline=true: serve cached body or return a clear error;
//     never hit the network.
//   - r.DryRun=true: do not persist any fetched body.
//   - Stale-on-error: on transport-level failure, serve cached
//     body if present (cacheResult.Stale=true) without rewriting
//     the cache.
//
// Non-200/304 responses return an error wrapping the status
// code; the caller owns HTTP-specific handling like 404-is-not-
// fatal for the .binaries.toml path (see fetchBinaries).
func (r *Registry) cachedGet(ctx context.Context, url string) (cacheResult, error) {
	// No cache configured — plain fetch, unless offline.
	if r.CacheDir == "" {
		if r.Offline {
			return cacheResult{}, fmt.Errorf(
				"GALE_OFFLINE=1 and no cache directory configured for %s",
				url,
			)
		}
		body, err := plainGet(ctx, url)
		return cacheResult{Body: body}, err
	}

	entryDir := filepath.Join(r.CacheDir, "registry", cacheKey(url))
	bodyPath := filepath.Join(entryDir, "body")
	etagPath := filepath.Join(entryDir, "etag")
	markerPath := filepath.Join(entryDir, "not_found")

	// Lazy prune: if a not_found marker exists but is older than
	// the TTL, drop it before doing anything else. Best-effort —
	// a failed remove just means the next branch decides freshness
	// itself.
	markerFresh := negativeMarkerFresh(markerPath)

	// Offline mode: never touch the network. Order of precedence:
	//   1. positive cache (body) — strongest signal.
	//   2. fresh negative marker — package is known absent.
	//   3. otherwise → "no cached entry" error.
	if r.Offline {
		if body, err := os.ReadFile(bodyPath); err == nil {
			return cacheResult{Body: body, Stale: true}, nil
		}
		if markerFresh {
			return cacheResult{}, errHTTP404
		}
		return cacheResult{}, fmt.Errorf(
			"GALE_OFFLINE=1 and no cached entry for %s", url,
		)
	}

	// Fresh negative cache short-circuits before the wire.
	if markerFresh {
		return cacheResult{}, errHTTP404
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return cacheResult{}, err
	}
	// If we have a prior ETag, send it.
	if etag, err := os.ReadFile(etagPath); err == nil {
		req.Header.Set("If-None-Match", string(etag))
	}

	resp, err := httpclient.Default().Do(req)
	if err != nil {
		// Stale-on-error: transport-level failure. Serve the
		// cached body if it exists, then fall back to the
		// negative marker (any age — better than surfacing the
		// transport error). Do NOT rewrite the cache.
		if body, rerr := os.ReadFile(bodyPath); rerr == nil {
			return cacheResult{Body: body, Stale: true}, nil
		}
		if _, rerr := os.Stat(markerPath); rerr == nil {
			return cacheResult{}, errHTTP404
		}
		return cacheResult{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		// Cached body is authoritative — return it verbatim.
		body, rerr := os.ReadFile(bodyPath)
		if rerr != nil {
			// Cache disappeared between request and read. Fall
			// back to a fresh fetch without the conditional.
			body, perr := plainGet(ctx, url)
			return cacheResult{Body: body}, perr
		}
		return cacheResult{Body: body}, nil
	case http.StatusOK:
		body, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return cacheResult{}, rerr
		}
		// A 200 supersedes any stale negative marker.
		if !r.DryRun {
			_ = os.Remove(markerPath)
		}
		// Only persist if the server gave us a validator. ETag is
		// the only one we use; without it the cache entry could
		// never be refreshed and would serve stale content.
		// DryRun suppresses all writes.
		if !r.DryRun {
			if etag := resp.Header.Get("ETag"); etag != "" {
				writeCacheEntry(entryDir, body, etag)
			}
		}
		return cacheResult{Body: body}, nil
	case http.StatusNotFound:
		// Record the 404 so future calls within the TTL window
		// don't re-hit the network. DryRun suppresses the write.
		if !r.DryRun {
			writeNegativeMarker(entryDir, markerPath)
		}
		return cacheResult{}, errHTTP404
	default:
		return cacheResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// negativeMarkerFresh returns true if a not_found marker exists
// at path and its timestamp is within negativeCacheTTL. A stale
// marker is removed in-place (lazy prune) so the caller can
// proceed as if it never existed.
func negativeMarkerFresh(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		// Unparseable marker — drop it.
		_ = os.Remove(path)
		return false
	}
	if time.Since(ts) > negativeCacheTTL {
		_ = os.Remove(path)
		return false
	}
	return true
}

// writeNegativeMarker records a 404 by writing the current
// timestamp to <entryDir>/not_found. Best-effort: failures mean
// the next fetch will re-hit the wire, which is a correctness-
// preserving degradation.
func writeNegativeMarker(entryDir, markerPath string) {
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	_ = atomicWrite(markerPath, []byte(stamp))
}

// plainGet is an uncached GET. Returns the body on 200, an
// error (wrapping the status code) otherwise. The context
// carries the per-request timeout; the shared httpclient
// has no per-client timeout.
func plainGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpclient.Default().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// writeCacheEntry commits body + etag to disk atomically.
// Both files are written into a staging temp directory first, then
// the staging directory is renamed over entryDir in a single
// syscall. This ensures concurrent writers can never produce a
// mismatched body/etag pair: a reader always sees either the old
// complete pair or the new complete pair, never a mix.
// Best-effort — failures are swallowed. A failed write means the
// next fetch re-downloads, which is a correctness-preserving
// degradation.
func writeCacheEntry(entryDir string, body []byte, etag string) {
	parent := filepath.Dir(entryDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return
	}

	// Stage both files in a temp directory adjacent to entryDir so
	// the rename is on the same filesystem (required for atomicity
	// on Linux/macOS; os.Rename is not atomic across filesystems).
	tmp, err := os.MkdirTemp(parent, ".gale-cache-tmp-*")
	if err != nil {
		return
	}
	// Remove the staging dir on any error path.
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()

	if err := atomicWrite(filepath.Join(tmp, "body"), body); err != nil {
		return
	}
	if err := atomicWrite(filepath.Join(tmp, "etag"), []byte(etag)); err != nil {
		return
	}

	// Rename the staging directory over the entry directory.
	// On Linux this fails if entryDir already exists (ENOTDIR or
	// EEXIST when the old and new are both directories on some
	// kernels); remove the old entry first so the rename succeeds.
	// The window between Remove and Rename is tiny and any
	// concurrent reader that hits an absent entryDir will simply
	// re-fetch, which is the correct degraded behavior.
	_ = os.RemoveAll(entryDir)
	if err := os.Rename(tmp, entryDir); err != nil {
		return
	}
	ok = true
}

// atomicWrite writes data to path via a temp file in the same
// directory, then renames into place. Returns the first error
// encountered.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// If anything below fails we must remove the temp file.
	cleanup := func() {
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
