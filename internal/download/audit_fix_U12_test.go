package download

import (
	"testing"

	"github.com/kelp/gale/internal/httpclient"
)

// gh#61: the package-private http.Client carried a hard 5-minute
// Timeout, aborting any GHCR blob or source tarball transfer that
// takes longer (large packages, slow links) mid-stream. All
// install-path HTTP traffic must use the shared no-timeout client
// from internal/httpclient; stall protection comes from the
// transport's dial and TLS handshake timeouts plus per-request
// contexts, not a whole-transfer cap.
func TestHTTPClientIsSharedNoTimeoutClient(t *testing.T) {
	if httpClient.Timeout != 0 {
		t.Fatalf(
			"httpClient.Timeout = %v, want 0: a whole-transfer cap "+
				"aborts large downloads mid-stream on slow links",
			httpClient.Timeout,
		)
	}
	if httpClient != httpclient.Default() {
		t.Error("httpClient should be the shared httpclient.Default() " +
			"so install-path traffic reuses one connection pool")
	}
}
