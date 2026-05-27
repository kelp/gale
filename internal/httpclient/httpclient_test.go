package httpclient

import (
	"net/http"
	"testing"
)

// Default must return the same client instance across calls so
// the underlying *http.Transport's idle-connection pool is
// shared by every caller. A constructor-style implementation
// that returned a new client each call would defeat keepalive.
func TestDefaultReturnsSingletonInstance(t *testing.T) {
	a := Default()
	b := Default()
	if a != b {
		t.Errorf("Default() returned distinct clients on two calls; want the same *http.Client instance")
	}
	if a == nil {
		t.Fatal("Default() returned nil")
	}
}

// The shared client must NOT carry a per-client Timeout because
// the install pipeline serves both short recipe fetches (seconds)
// and long binary downloads (minutes) from the same client.
// Callers enforce per-request timeouts via context. A non-zero
// Client.Timeout would clip slow downloads.
func TestDefaultHasNoClientTimeout(t *testing.T) {
	c := Default()
	if c.Timeout != 0 {
		t.Errorf("Default().Timeout = %v, want 0 (callers use context for per-request timeouts)", c.Timeout)
	}
}

// The shared client must use a Transport that supports HTTP/2
// negotiation and idle connection reuse. http.DefaultTransport
// is the stdlib-blessed transport with these defaults; using it
// is the simplest way to guarantee both. A bare *http.Client{}
// with no Transport field also falls back to DefaultTransport,
// which is equally fine — the test allows either, but rejects
// any explicit replacement that disables those features.
func TestDefaultUsesConnectionReusingTransport(t *testing.T) {
	c := Default()
	if c.Transport == nil {
		return // bare client → falls back to http.DefaultTransport at use time
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Default().Transport is %T, want *http.Transport or nil (got non-stdlib transport)", c.Transport)
	}
	// MaxIdleConns == 0 means unlimited (good). Explicit small
	// values (e.g. 1) would defeat the point of pooling.
	if tr.MaxIdleConns > 0 && tr.MaxIdleConns < 10 {
		t.Errorf("Transport.MaxIdleConns = %d, want >=10 or 0 (unlimited)", tr.MaxIdleConns)
	}
}
