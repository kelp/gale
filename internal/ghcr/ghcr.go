package ghcr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/kelp/gale/internal/httpclient"
	"github.com/kelp/gale/internal/timing"
)

// DefaultTokenTTL is the cache TTL used when the token
// response does not include an expires_in field.
const DefaultTokenTTL = 5 * time.Minute

// now is the clock used by the cache. Tests override this
// via t.Cleanup to restore the original after each test.
var now = time.Now //nolint:gochecknoglobals

// tokenEntry holds a cached token and its expiry time.
type tokenEntry struct {
	token     string
	expiresAt time.Time
}

// inflightCall represents a single in-flight or just-completed
// token fetch. Callers that arrive while a fetch is in flight
// wait on wg rather than issuing a duplicate HTTP request.
//
// Publish/read invariant: the leader goroutine writes token and err
// while holding mu (after the mu.Lock() call in Token), then calls
// wg.Done() after releasing mu. Waiters call wg.Wait() (line 107)
// and read token/err afterward (line 108). The leader's writes are
// ordered before mu.Unlock() by program order in the same goroutine;
// mu.Unlock() is ordered before wg.Done() by program order; and the
// Go memory model guarantees that wg.Done() happens-before the
// wg.Wait() that observes the count reaching zero. By transitivity,
// all writes to token and err happen-before any waiter's reads,
// making the post-Wait reads safe without an additional lock.
type inflightCall struct {
	wg    sync.WaitGroup
	token string
	err   error
}

// tokenCache is the in-process cache for GHCR tokens.
type tokenCache struct {
	mu       sync.Mutex
	entries  map[string]tokenEntry
	inflight map[string]*inflightCall
}

// globalCache holds all cached tokens. ResetTokenCacheForTest
// flushes it between tests.
var globalCache = &tokenCache{ //nolint:gochecknoglobals
	entries:  make(map[string]tokenEntry),
	inflight: make(map[string]*inflightCall),
}

// ResetTokenCacheForTest flushes all cached token entries and
// any in-flight state. Call this in t.Cleanup to prevent
// cross-test pollution.
func ResetTokenCacheForTest() {
	globalCache.mu.Lock()
	defer globalCache.mu.Unlock()
	globalCache.entries = make(map[string]tokenEntry)
	globalCache.inflight = make(map[string]*inflightCall)
}

// BlobURL returns the full GHCR blob URL for a given base
// repository, package name, and SHA256 hash.
func BlobURL(base, name, sha256 string) string {
	return fmt.Sprintf(
		"https://ghcr.io/v2/%s/%s/blobs/sha256:%s",
		base, name, sha256)
}

// tokenEndpoint is the base URL for the GHCR token service.
// Tests override this to point at httptest servers.
var tokenEndpoint = "https://ghcr.io/token" //nolint:gosec // G101 — URL, not a credential

// SetTokenEndpoint overrides the token endpoint URL.
// Returns the previous value for restoring in tests.
func SetTokenEndpoint(u string) string {
	old := tokenEndpoint
	tokenEndpoint = u
	return old
}

// Token fetches an anonymous bearer token for pulling
// from the given GHCR repository. If the GALE_GITHUB_TOKEN
// environment variable is set, its value is returned
// directly without making any HTTP request or touching
// the cache.
func Token(repository string) (string, error) {
	if tok := os.Getenv("GALE_GITHUB_TOKEN"); tok != "" {
		return tok, nil
	}

	// Check cache first (under lock).
	globalCache.mu.Lock()
	if entry, ok := globalCache.entries[repository]; ok {
		if !now().After(entry.expiresAt) {
			globalCache.mu.Unlock()
			return entry.token, nil
		}
		// Entry expired — delete it.
		delete(globalCache.entries, repository)
	}

	// Cache miss. Check for an in-flight call for this repo.
	if call, ok := globalCache.inflight[repository]; ok {
		// Another goroutine is already fetching — wait for it.
		globalCache.mu.Unlock()
		call.wg.Wait()
		return call.token, call.err
	}

	// We are the leader — register the in-flight call.
	call := &inflightCall{}
	call.wg.Add(1)
	globalCache.inflight[repository] = call
	globalCache.mu.Unlock()

	// Only the leader goroutine performs the HTTP fetch.
	defer timing.Phase("ghcr-token " + repository)()

	tok, expiresAt, fetchErr := fetchToken(repository)

	// Store result in the in-flight struct, then signal waiters.
	globalCache.mu.Lock()
	if fetchErr == nil {
		globalCache.entries[repository] = tokenEntry{
			token:     tok,
			expiresAt: expiresAt,
		}
	}
	call.token = tok
	call.err = fetchErr
	// Delete before Done so any goroutine arriving after this point becomes a new leader rather than a stale waiter.
	delete(globalCache.inflight, repository)
	globalCache.mu.Unlock()

	call.wg.Done()
	return tok, fetchErr
}

// fetchToken performs the HTTP call to the GHCR token endpoint
// and returns the token string, its computed expiry, and any
// error.
func fetchToken(repository string) (string, time.Time, error) {
	scope := fmt.Sprintf("repository:%s:pull", repository)
	reqURL := fmt.Sprintf("%s?service=%s&scope=%s",
		tokenEndpoint,
		url.QueryEscape("ghcr.io"),
		url.QueryEscape(scope))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build ghcr token request: %w", err)
	}
	resp, err := httpclient.Default().Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("fetch ghcr token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf(
			"fetch ghcr token: HTTP %d", resp.StatusCode)
	}

	var body struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", time.Time{}, fmt.Errorf("parse ghcr token response: %w", err)
	}

	if body.Token == "" {
		return "", time.Time{}, fmt.Errorf(
			"ghcr token response: missing token field")
	}

	var expiresAt time.Time
	if body.ExpiresIn > 0 {
		expiresAt = now().Add(time.Duration(body.ExpiresIn) * time.Second)
	} else {
		expiresAt = now().Add(DefaultTokenTTL)
	}

	return body.Token, expiresAt, nil
}
