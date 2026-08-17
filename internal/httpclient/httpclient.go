// Package httpclient exposes a single shared *http.Client for
// all install-path HTTP traffic (recipe fetches, GHCR token
// exchange, GHCR blob downloads, registry index fetches).
//
// Why share one client? Each *http.Client owns a *http.Transport
// whose idle-connection pool is the only thing that keeps TCP +
// TLS connections warm between requests. Constructing a fresh
// client per call — the previous pattern — gives every request
// its own empty pool, so a multi-package sync paid for a new
// TCP handshake and a new TLS negotiation on every recipe fetch
// and every token exchange.
//
// The shared client deliberately has no per-client Timeout. The
// install pipeline serves both short metadata fetches (seconds)
// and long binary downloads (minutes); callers enforce per-
// request budgets via context.WithTimeout instead.
package httpclient

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// shared is the package-level client. A bare *http.Client uses
// http.DefaultTransport at request time, which already includes
// HTTP/2 negotiation and reasonable idle-pool defaults
// (MaxIdleConns=100, IdleConnTimeout=90s). No custom Transport
// is needed; if we ever need to tune those, set the Transport
// field here.
var shared = &http.Client{} //nolint:gochecknoglobals

// fetchShared is the unauthenticated artifact/source client.
// Same idle pool as Default (nil Transport), plus hop-validated
// CheckRedirect. Default stays unrestricted so GHCR 303s and
// registry fetches are not fail-closed by the GitHub hop map.
var fetchShared = &http.Client{ //nolint:gochecknoglobals
	CheckRedirect: CheckRedirect,
}

// ErrRedirect reports a hop the compiled policy refuses.
var ErrRedirect = errors.New("redirect not allowed")

// redirectHops is the directed cross-host map. A from-host that
// is a key may hop only to the listed to-hosts (plus same
// origin). An unlisted from-host keeps stdlib host behavior so
// today's source catalog (ftpmirror, go.dev) still follows
// upstream redirects. Do not wildcard *.githubusercontent.com.
var redirectHops = map[string][]string{
	"github.com": {
		"objects.githubusercontent.com",
		"release-assets.githubusercontent.com",
		"codeload.github.com",
	},
	"objects.githubusercontent.com":        {},
	"release-assets.githubusercontent.com": {},
	"codeload.github.com":                  {},
}

const maxRedirectHops = 10

// Default returns the process-wide shared HTTP client. Callers
// pass a context with their own per-request timeout via
// http.NewRequestWithContext rather than relying on a client-
// level Timeout.
func Default() *http.Client {
	return shared
}

// FetchClient returns the unauthenticated fetch client. It
// shares Default's Transport and installs CheckRedirect.
func FetchClient() *http.Client {
	return fetchShared
}

// CheckRedirect is the unauthenticated hop policy. Same origin
// is always allowed. A mapped from-host may hop only to listed
// to-hosts. Unlisted from-hosts may hop anywhere. Userinfo on
// the next URL is refused.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if err := redirectCommon(req, via); err != nil {
		return err
	}
	prev := via[len(via)-1].URL
	if !allowedRedirect(prev, req.URL) {
		return fmt.Errorf("%w: %s -> %s", ErrRedirect, prev.Host, req.URL.Host)
	}
	return nil
}

// AuthCheckRedirect is the authenticated hop policy. Hosts are
// unrestricted so GHCR 303s keep working. Userinfo and
// https→http are refused. Authorization is stripped when the
// next origin differs from the first request's origin; Go may
// keep the header on subdomain or port changes.
func AuthCheckRedirect(req *http.Request, via []*http.Request) error {
	if err := redirectCommon(req, via); err != nil {
		return err
	}
	first := via[0].URL
	if first.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("%w: https to http", ErrRedirect)
	}
	if !sameOrigin(first, req.URL) {
		req.Header.Del("Authorization")
	}
	return nil
}

func redirectCommon(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirectHops {
		return fmt.Errorf("%w: too many hops", ErrRedirect)
	}
	if req.URL.User != nil {
		return fmt.Errorf("%w: credentials in redirect URL", ErrRedirect)
	}
	return nil
}

func allowedRedirect(from, to *url.URL) bool {
	if sameOrigin(from, to) {
		return true
	}
	fromHost := strings.ToLower(from.Hostname())
	toHost := strings.ToLower(to.Hostname())
	targets, mapped := redirectHops[fromHost]
	if !mapped {
		return true
	}
	for _, target := range targets {
		if target == toHost {
			return true
		}
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}
