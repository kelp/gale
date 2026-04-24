package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Cache layout on disk:
//
//   <CacheDir>/registry/<hash>/body   — last response body
//   <CacheDir>/registry/<hash>/etag   — last ETag value
//
// <hash> is sha256 of the request URL, hex-encoded. This keeps
// a 1:1 mapping per fetch URL without filesystem-hostile
// characters. See defaultCacheDir() for the production root
// (~/.gale/cache/), which is shared with the source-tarball
// cache in internal/build.

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

// cachedGet fetches url with an HTTP conditional GET. If the
// response is a 304 Not Modified, cachedGet returns the cached
// body. If the response is a 200 OK with an ETag, the body and
// ETag are written to the cache before returning. If the cache
// is disabled (cacheDir == "") or unreachable, cachedGet falls
// back to an ordinary GET.
//
// Non-200/304 responses return an error wrapping the status
// code; the caller owns HTTP-specific handling like 404-is-not-
// fatal for the .binaries.toml path (see fetchBinaries).
func cachedGet(client *http.Client, url, cacheDir string) ([]byte, error) {
	// No cache configured — plain fetch.
	if cacheDir == "" {
		return plainGet(client, url)
	}

	entryDir := filepath.Join(cacheDir, "registry", cacheKey(url))
	bodyPath := filepath.Join(entryDir, "body")
	etagPath := filepath.Join(entryDir, "etag")

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// If we have a prior ETag, send it.
	if etag, err := os.ReadFile(etagPath); err == nil {
		req.Header.Set("If-None-Match", string(etag))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		// Cached body is authoritative — return it verbatim.
		body, rerr := os.ReadFile(bodyPath)
		if rerr != nil {
			// Cache disappeared between request and read. Fall
			// back to a fresh fetch without the conditional.
			return plainGet(client, url)
		}
		return body, nil
	case http.StatusOK:
		body, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return nil, rerr
		}
		// Only persist if the server gave us a validator. ETag is
		// the only one we use; without it the cache entry could
		// never be refreshed and would serve stale content.
		if etag := resp.Header.Get("ETag"); etag != "" {
			writeCacheEntry(entryDir, bodyPath, etagPath, body, etag)
		}
		return body, nil
	default:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// plainGet is an uncached GET. Returns the body on 200, an
// error (wrapping the status code) otherwise.
func plainGet(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// writeCacheEntry commits body + etag to disk. Best-effort —
// failures are swallowed. A failed write means the next fetch
// re-downloads, which is a correctness-preserving degradation.
func writeCacheEntry(entryDir, bodyPath, etagPath string, body []byte, etag string) {
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		return
	}
	// Write body + etag via temp-file + rename so a crash can't
	// leave body and etag out of sync (partial body with a fresh
	// etag would revalidate to 304 and return garbage).
	if err := atomicWrite(bodyPath, body); err != nil {
		return
	}
	_ = atomicWrite(etagPath, []byte(etag))
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
