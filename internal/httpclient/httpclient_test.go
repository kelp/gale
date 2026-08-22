package httpclient

import (
	"errors"
	"net/http"
	"net/url"
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

func TestFetchClientSharesDefaultTransport(t *testing.T) {
	c := FetchClient()
	if c == nil {
		t.Fatal("FetchClient() returned nil")
	}
	if c == Default() {
		t.Fatal("FetchClient() must not be Default(); hop policy must not ride registry/GHCR")
	}
	if c.Transport != Default().Transport {
		t.Errorf("FetchClient().Transport = %v, want Default().Transport (shared pool)", c.Transport)
	}
	if c.CheckRedirect == nil {
		t.Fatal("FetchClient().CheckRedirect is nil, want hop policy")
	}
	if Default().CheckRedirect != nil {
		t.Fatal("Default().CheckRedirect is set; registry and GHCR must stay unrestricted")
	}
	if c.Timeout != 0 {
		t.Errorf("FetchClient().Timeout = %v, want 0", c.Timeout)
	}
}

func TestAllowedRedirect(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{
			name: "github to objects",
			from: "https://github.com/kelp/jq/releases/download/1/jq.tar.gz",
			to:   "https://objects.githubusercontent.com/github-production-release-asset/jq",
			want: true,
		},
		{
			name: "github to evil",
			from: "https://github.com/kelp/jq/releases/download/1/jq.tar.gz",
			to:   "https://evil.example/jq.tar.gz",
			want: false,
		},
		{
			name: "objects to github",
			from: "https://objects.githubusercontent.com/x",
			to:   "https://github.com/x",
			want: false,
		},
		{
			name: "objects to evil",
			from: "https://objects.githubusercontent.com/x",
			to:   "https://evil.example/x",
			want: false,
		},
		{
			name: "unlisted from to other host",
			from: "https://ftpmirror.gnu.org/gnu/make/make-4.4.tar.gz",
			to:   "https://mirrors.kernel.org/gnu/make/make-4.4.tar.gz",
			want: true,
		},
		{
			name: "same host default vs explicit 443",
			from: "https://github.com/x",
			to:   "https://github.com:443/y",
			want: true,
		},
		{
			name: "same host different explicit ports",
			from: "http://127.0.0.1:1234/a",
			to:   "http://127.0.0.1:5678/b",
			want: true, // 127.0.0.1 is unlisted; port is not a hop key
		},
		{
			name: "mapped host different explicit ports",
			from: "https://github.com:8443/x",
			to:   "https://github.com:9443/y",
			want: false,
		},
		{
			name: "case fold github",
			from: "https://GitHub.com/x",
			to:   "https://objects.githubusercontent.com/x",
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, err := url.Parse(tc.from)
			if err != nil {
				t.Fatalf("parse from: %v", err)
			}
			to, err := url.Parse(tc.to)
			if err != nil {
				t.Fatalf("parse to: %v", err)
			}
			got := allowedRedirect(from, to)
			if got != tc.want {
				t.Errorf("allowedRedirect(%q, %q) = %v, want %v",
					tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestCheckRedirectRejectsUserinfo(t *testing.T) {
	via := []*http.Request{mustRequest(t, "https://github.com/x")}
	req := mustRequest(t, "https://user:pass@objects.githubusercontent.com/x")
	err := CheckRedirect(req, via)
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("CheckRedirect userinfo: %v, want ErrRedirect", err)
	}
}

func TestCheckRedirectStopsAtTenHops(t *testing.T) {
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = mustRequest(t, "https://github.com/x")
	}
	req := mustRequest(t, "https://github.com/y")
	err := CheckRedirect(req, via)
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("CheckRedirect 10 hops: %v, want ErrRedirect", err)
	}
}

func TestCheckRedirectUsesImmediateFrom(t *testing.T) {
	// via[0] is github (mapped). Immediate predecessor is
	// objects (mapped, no cross-host targets). A hop to
	// release-assets must deny even though via[0] would allow it.
	via := []*http.Request{
		mustRequest(t, "https://github.com/x"),
		mustRequest(t, "https://objects.githubusercontent.com/x"),
	}
	req := mustRequest(t, "https://release-assets.githubusercontent.com/x")
	err := CheckRedirect(req, via)
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("3-hop via objects: %v, want ErrRedirect", err)
	}
}

func TestAuthCheckRedirectRefusesHTTPSToHTTP(t *testing.T) {
	via := []*http.Request{mustRequest(t, "https://ghcr.io/v2/x/blobs/sha256:abc")}
	req := mustRequest(t, "http://ghcr.io/v2/x/blobs/sha256:abc")
	err := AuthCheckRedirect(req, via)
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("auth https→http: %v, want ErrRedirect", err)
	}
}

func TestAuthCheckRedirectStripsAuthorizationOnOriginChange(t *testing.T) {
	via := []*http.Request{mustRequest(t, "https://ghcr.io/v2/x/blobs/sha256:abc")}
	via[0].Header.Set("Authorization", "Bearer secret")
	req := mustRequest(t, "https://pkg-containers.githubusercontent.com/v2/x")
	req.Header.Set("Authorization", "Bearer secret")
	if err := AuthCheckRedirect(req, via); err != nil {
		t.Fatalf("auth cross-host: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty after origin change", got)
	}
}

func TestAuthCheckRedirectKeepsAuthorizationOnSameOrigin(t *testing.T) {
	via := []*http.Request{mustRequest(t, "https://ghcr.io/v2/x/blobs/sha256:abc")}
	req := mustRequest(t, "https://ghcr.io/v2/x/blobs/sha256:def")
	req.Header.Set("Authorization", "Bearer secret")
	if err := AuthCheckRedirect(req, via); err != nil {
		t.Fatalf("auth same-origin: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("Authorization = %q, want kept on same origin", got)
	}
}

func TestAuthCheckRedirectAllowsUnlistedHost(t *testing.T) {
	via := []*http.Request{mustRequest(t, "https://ghcr.io/v2/x")}
	req := mustRequest(t, "https://pkg-containers.githubusercontent.com/v2/x")
	if err := AuthCheckRedirect(req, via); err != nil {
		t.Fatalf("auth unlisted hop: %v", err)
	}
}

func mustRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatalf("NewRequest(%s): %v", raw, err)
	}
	return req
}
