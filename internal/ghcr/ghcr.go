package ghcr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// tokenEndpoint is the base URL for the GHCR token service.
// Tests override this to point at httptest servers.
var tokenEndpoint = "https://ghcr.io/token" //nolint:gosec // G101 — URL, not a credential

// SetTokenEndpoint overrides the token endpoint URL.
// Returns the previous value for restoring in tests.
func SetTokenEndpoint(url string) string {
	old := tokenEndpoint
	tokenEndpoint = url
	return old
}

// Token fetches an anonymous bearer token for pulling
// from the given GHCR repository. If the GALE_GITHUB_TOKEN
// environment variable is set, its value is returned
// directly without making any HTTP request.
func Token(repository string) (string, error) {
	if tok := os.Getenv("GALE_GITHUB_TOKEN"); tok != "" {
		return tok, nil
	}

	scope := fmt.Sprintf("repository:%s:pull", repository)
	reqURL := fmt.Sprintf("%s?service=%s&scope=%s",
		tokenEndpoint,
		url.QueryEscape("ghcr.io"),
		url.QueryEscape(scope))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(reqURL)
	if err != nil {
		return "", fmt.Errorf("fetch ghcr token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"fetch ghcr token: HTTP %d", resp.StatusCode)
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("parse ghcr token response: %w", err)
	}

	if body.Token == "" {
		return "", fmt.Errorf(
			"ghcr token response: missing token field")
	}

	return body.Token, nil
}
