package index

import "strings"

// AllowedHost reports whether host is an initial origin the
// index may name. It takes url.Hostname() only. CDN hosts are
// redirect hops, not origins.
func AllowedHost(host string) bool {
	if host == "" || strings.ContainsAny(host, ":/") || strings.HasSuffix(host, ".") {
		return false
	}
	switch strings.ToLower(host) {
	case "github.com", "go.dev":
		return true
	default:
		return false
	}
}
