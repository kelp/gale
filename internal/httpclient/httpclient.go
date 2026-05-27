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

import "net/http"

// shared is the package-level client. A bare *http.Client uses
// http.DefaultTransport at request time, which already includes
// HTTP/2 negotiation and reasonable idle-pool defaults
// (MaxIdleConns=100, IdleConnTimeout=90s). No custom Transport
// is needed; if we ever need to tune those, set the Transport
// field here.
var shared = &http.Client{} //nolint:gochecknoglobals

// Default returns the process-wide shared HTTP client. Callers
// pass a context with their own per-request timeout via
// http.NewRequestWithContext rather than relying on a client-
// level Timeout.
func Default() *http.Client {
	return shared
}
