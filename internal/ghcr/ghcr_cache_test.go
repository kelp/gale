package ghcr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetState restores all global state touched by cache tests.
// Must be called (via t.Cleanup) in every test in this file.
func resetState(t *testing.T, prevEndpoint string, prevNow func() time.Time) {
	t.Helper()
	t.Cleanup(func() {
		SetTokenEndpoint(prevEndpoint)
		now = prevNow
		ResetTokenCacheForTest()
	})
}

// --- Behavior 1: Cache hit returns the same token without re-issuing HTTP ---

func TestCacheHitReturnsSameTokenWithoutReissuingHTTP(t *testing.T) {
	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "cached-token-abc",
				"expires_in": 300,
			})
		}))
	defer srv.Close()

	prevEndpoint := SetTokenEndpoint(srv.URL)
	prevNow := now
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return baseTime }
	resetState(t, prevEndpoint, prevNow)

	tok1, err := Token("foo/bar")
	if err != nil {
		t.Fatalf("first Token() call failed: %v", err)
	}
	if tok1 == "" {
		t.Fatal("first Token() returned empty string")
	}

	tok2, err := Token("foo/bar")
	if err != nil {
		t.Fatalf("second Token() call failed: %v", err)
	}

	if tok1 != tok2 {
		t.Errorf("tokens differ: first=%q second=%q", tok1, tok2)
	}

	if got := hits.Load(); got != 1 {
		t.Errorf("HTTP hit count = %d, want 1 (cache should serve second call)", got)
	}
}

// --- Behavior 2: Different repositories cache independently ---

func TestDifferentRepositoriesCacheIndependently(t *testing.T) {
	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			// Embed the path so each repo gets a distinct token.
			tok := fmt.Sprintf("token-for-%s", r.URL.RawQuery)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"token":      tok,
				"expires_in": 300,
			})
		}))
	defer srv.Close()

	prevEndpoint := SetTokenEndpoint(srv.URL)
	prevNow := now
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return baseTime }
	resetState(t, prevEndpoint, prevNow)

	tokAB1, err := Token("a/b")
	if err != nil {
		t.Fatalf("Token(a/b) first call failed: %v", err)
	}
	if tokAB1 == "" {
		t.Fatal("Token(a/b) returned empty string")
	}

	tokCD, err := Token("c/d")
	if err != nil {
		t.Fatalf("Token(c/d) failed: %v", err)
	}
	if tokCD == "" {
		t.Fatal("Token(c/d) returned empty string")
	}

	tokAB2, err := Token("a/b")
	if err != nil {
		t.Fatalf("Token(a/b) second call failed: %v", err)
	}

	if tokAB1 != tokAB2 {
		t.Errorf("a/b tokens differ: first=%q second=%q", tokAB1, tokAB2)
	}

	if tokAB1 == tokCD {
		t.Errorf("a/b and c/d returned the same token %q; "+
			"repos should get distinct tokens", tokAB1)
	}

	if got := hits.Load(); got != 2 {
		t.Errorf("HTTP hit count = %d, want 2 "+
			"(one per unique repo; a/b second call must be cached)", got)
	}
}

// --- Behavior 3: expires_in in response is honoured for cache TTL ---

func TestExpiresInHonouredForCacheTTL(t *testing.T) {
	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"token":      fmt.Sprintf("ttl-token-%d", hits.Load()),
				"expires_in": 10, // 10-second TTL
			})
		}))
	defer srv.Close()

	prevEndpoint := SetTokenEndpoint(srv.URL)
	prevNow := now
	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	current := base
	nowMu := sync.Mutex{}
	now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return current
	}
	advance := func(d time.Duration) {
		nowMu.Lock()
		current = current.Add(d)
		nowMu.Unlock()
	}
	resetState(t, prevEndpoint, prevNow)

	// t=0: first call — must hit HTTP
	tok1, err := Token("expire/test")
	if err != nil {
		t.Fatalf("Token() at t=0 failed: %v", err)
	}
	if tok1 == "" {
		t.Fatal("Token() at t=0 returned empty string")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 HTTP hit after first call, got %d", hits.Load())
	}

	// t=9s: still within 10s TTL — must return cached token (no new HTTP)
	advance(9 * time.Second)
	tok2, err := Token("expire/test")
	if err != nil {
		t.Fatalf("Token() at t=9s failed: %v", err)
	}
	if tok2 == "" {
		t.Fatal("Token() at t=9s returned empty string")
	}
	if hits.Load() != 1 {
		t.Errorf("HTTP hit count = %d at t=9s, want 1 (cache should still be valid)", hits.Load())
	}

	// t=12s total: past the 10s TTL — must re-fetch
	advance(3 * time.Second) // now at 12s from base
	tok3, err := Token("expire/test")
	if err != nil {
		t.Fatalf("Token() at t=12s failed: %v", err)
	}
	if tok3 == "" {
		t.Fatal("Token() at t=12s returned empty string")
	}
	if hits.Load() != 2 {
		t.Errorf("HTTP hit count = %d at t=12s, want 2 (cache expired, must re-fetch)", hits.Load())
	}
}

// --- Behavior 4: Default TTL is 5 minutes when expires_in is missing or zero ---

func TestDefaultTTLFiveMinutesWhenExpiresInAbsent(t *testing.T) {
	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			// No expires_in field — default TTL must apply.
			json.NewEncoder(w).Encode(map[string]any{
				"token": fmt.Sprintf("default-ttl-token-%d", hits.Load()),
			})
		}))
	defer srv.Close()

	prevEndpoint := SetTokenEndpoint(srv.URL)
	prevNow := now
	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	current := base
	nowMu := sync.Mutex{}
	now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return current
	}
	advance := func(d time.Duration) {
		nowMu.Lock()
		current = current.Add(d)
		nowMu.Unlock()
	}
	resetState(t, prevEndpoint, prevNow)

	// t=0: first call
	tok1, err := Token("default/ttl")
	if err != nil {
		t.Fatalf("Token() at t=0 failed: %v", err)
	}
	if tok1 == "" {
		t.Fatal("Token() at t=0 returned empty string")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 HTTP hit after first call, got %d", hits.Load())
	}

	// t=4m59s: just inside 5-minute default TTL — cache hit
	advance(DefaultTokenTTL - time.Second)
	tok2, err := Token("default/ttl")
	if err != nil {
		t.Fatalf("Token() at t=4m59s failed: %v", err)
	}
	if tok2 == "" {
		t.Fatal("Token() at t=4m59s returned empty string")
	}
	if hits.Load() != 1 {
		t.Errorf("HTTP hit count = %d at t=4m59s, want 1 (within default TTL)", hits.Load())
	}

	// t=5m1s: past 5-minute default TTL — must re-fetch
	advance(2 * time.Second) // now at 5m1s from base
	tok3, err := Token("default/ttl")
	if err != nil {
		t.Fatalf("Token() at t=5m1s failed: %v", err)
	}
	if tok3 == "" {
		t.Fatal("Token() at t=5m1s returned empty string")
	}
	if hits.Load() != 2 {
		t.Errorf("HTTP hit count = %d at t=5m1s, want 2 (default TTL expired)", hits.Load())
	}
}

// --- Behavior 5: GALE_GITHUB_TOKEN env override bypasses cache and HTTP ---
//
// This test verifies:
//  1. While env var is set, Token() returns the env value without any HTTP.
//  2. The env-var path does NOT populate the cache. After warming the cache
//     legitimately (via HTTP), setting the env var and calling Token() returns
//     the env value — not the cached value — AND the HTTP hit count does not
//     increase. Then after clearing the env var, the cache is still valid and
//     serves the original cached token (hits still == 1, not a new fetch).
//     This proves the env path bypasses both the read AND the write paths of
//     the cache.
//
// The test will FAIL on the stub because the stub has no cache: without
// caching, the "clear env, call again" step re-fetches (hits == 2), but a
// correct implementation should still serve from cache (hits == 1).

func TestEnvTokenBypassesCacheAndHTTP(t *testing.T) {
	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "server-cached-token",
				"expires_in": 300, // 5-minute TTL
			})
		}))
	defer srv.Close()

	prevEndpoint := SetTokenEndpoint(srv.URL)
	prevNow := now
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return baseTime }
	resetState(t, prevEndpoint, prevNow)

	// Phase 1: warm the cache via a normal HTTP call.
	tok1, err := Token("env/bypass")
	if err != nil {
		t.Fatalf("warm-up Token() failed: %v", err)
	}
	if tok1 != "server-cached-token" {
		t.Errorf("warm-up: Token() = %q, want %q", tok1, "server-cached-token")
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 hit after warm-up, got %d", hits.Load())
	}

	// Phase 2: set env var — Token() must return env value, not cached value,
	// and must NOT make another HTTP call.
	t.Setenv("GALE_GITHUB_TOKEN", "my-override-token")

	const callCount = 3
	for i := range callCount {
		got, err := Token("env/bypass")
		if err != nil {
			t.Fatalf("Token() call %d (with env) failed: %v", i+1, err)
		}
		if got != "my-override-token" {
			t.Errorf("call %d (with env): Token() = %q, want %q",
				i+1, got, "my-override-token")
		}
	}
	if hits.Load() != 1 {
		t.Errorf("after env-var calls: HTTP hit count = %d, want 1 "+
			"(GALE_GITHUB_TOKEN must bypass HTTP entirely)", hits.Load())
	}

	// Phase 3: clear env var — the original cached token should still be
	// valid (clock has not advanced past TTL), so NO new HTTP request.
	// A stub without caching will re-fetch here, causing hits == 2.
	// Use os.Unsetenv (not t.Setenv) so the variable is truly absent,
	// not set to an empty string. The t.Cleanup registered by t.Setenv
	// in phase 2 still restores the original value on test teardown.
	os.Unsetenv("GALE_GITHUB_TOKEN") //nolint:errcheck

	tok2, err := Token("env/bypass")
	if err != nil {
		t.Fatalf("Token() after clearing env failed: %v", err)
	}
	if tok2 != "server-cached-token" {
		t.Errorf("after clearing env: Token() = %q, want %q "+
			"(cache should still be warm)", tok2, "server-cached-token")
	}
	if hits.Load() != 1 {
		t.Errorf("after clearing env: HTTP hit count = %d, want 1 "+
			"(cache still valid — no re-fetch expected)", hits.Load())
	}
}

// --- Behavior 6: Concurrent calls for same repo coalesce into one HTTP request ---

func TestConcurrentTokenCallsCoalesceIntoOneHTTPRequest(t *testing.T) {
	const goroutines = 20

	var hits atomic.Int64
	// inFlight counts how many goroutines have entered the server handler.
	// With singleflight, this stays at 1; without it, it climbs to goroutines.
	var inFlight atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			inFlight.Add(1)
			// Wait up to 500ms so any extra in-flight HTTP requests (i.e.
			// when singleflight is absent) have time to pile up and be
			// counted. With singleflight, inFlight stays at 1 and we simply
			// time out at 500ms — that latency is acceptable.
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				if inFlight.Load() >= goroutines {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"token":      "coalesced-token-xyz",
				"expires_in": 300,
			})
		}))
	defer srv.Close()

	prevEndpoint := SetTokenEndpoint(srv.URL)
	prevNow := now
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return baseTime }
	resetState(t, prevEndpoint, prevNow)

	tokens := make([]string, goroutines)
	errs := make([]error, goroutines)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func() {
			defer wg.Done()
			<-start // wait for all goroutines to be ready
			tokens[i], errs[i] = Token("foo/bar")
		}()
	}

	// Release all goroutines simultaneously so they race to call Token.
	close(start)

	wg.Wait()

	// All calls must succeed.
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Token() error: %v", i, err)
		}
	}

	// All calls must return a non-empty token.
	for i, tok := range tokens {
		if tok == "" {
			t.Errorf("goroutine %d: Token() returned empty string", i)
		}
	}

	// All tokens must be identical.
	first := tokens[0]
	for i, tok := range tokens[1:] {
		if tok != first {
			t.Errorf("goroutine %d: token=%q, want %q (all calls should return same token)",
				i+1, tok, first)
		}
	}

	// Singleflight: exactly one HTTP request despite 20 concurrent callers.
	if got := hits.Load(); got != 1 {
		t.Errorf("HTTP hit count = %d, want 1 "+
			"(concurrent calls for same repo must coalesce)", got)
	}
}

// --- Behavior 7: Concurrent calls for same repo propagate errors to all waiters ---

func TestConcurrentTokenCallsPropagateErrorToWaiters(t *testing.T) {
	const goroutines = 20

	var hits atomic.Int64
	var inFlight atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			inFlight.Add(1)
			// Hold the leader until waiters pile up (same pattern as the
			// coalesce test), then respond with 500 so all goroutines get
			// the leader's error via wg.Wait.
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				if inFlight.Load() >= goroutines {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}))
	defer srv.Close()

	prevEndpoint := SetTokenEndpoint(srv.URL)
	prevNow := now
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now = func() time.Time { return baseTime }
	resetState(t, prevEndpoint, prevNow)

	errs := make([]error, goroutines)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = Token("foo/error")
		}()
	}

	close(start)
	wg.Wait()

	// All calls must return an error.
	for i, err := range errs {
		if err == nil {
			t.Errorf("goroutine %d: Token() expected error, got nil", i)
		}
	}

	// Singleflight: exactly one HTTP request despite 20 concurrent callers.
	if got := hits.Load(); got != 1 {
		t.Errorf("HTTP hit count = %d, want 1 "+
			"(concurrent error calls must still coalesce)", got)
	}
}
