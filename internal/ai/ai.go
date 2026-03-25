package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrNotConfigured is returned when AI features are used
// without an API key.
var ErrNotConfigured = errors.New("ai: not configured (no API key)")

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultModel     = "claude-sonnet-4-20250514"
	anthropicVersion = "2023-06-01"
	defaultMaxTokens = 1024
)

// Client handles Anthropic API interactions.
type Client struct {
	apiKey  string
	baseURL string
}

// NewClient creates a Client. If apiKey is empty, calls
// will return ErrNotConfigured.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
	}
}

// SetBaseURL overrides the API base URL (for testing).
func (c *Client) SetBaseURL(url string) {
	c.baseURL = url
}

// request is the JSON body sent to the Anthropic messages API.
type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// response is the JSON body returned by the Anthropic
// messages API.
type response struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Complete sends a single-shot prompt and returns the
// response text.
func (c *Client) Complete(prompt string) (string, error) {
	if c.apiKey == "" {
		return "", ErrNotConfigured
	}

	reqBody := request{
		Model:     defaultModel,
		MaxTokens: defaultMaxTokens,
		Messages: []message{
			{Role: "user", Content: prompt},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("ai: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/messages"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ai: create request: %w", err)
	}

	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ai: send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ai: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ai: API returned status %d", resp.StatusCode)
	}

	var apiResp response
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("ai: parse response: %w", err)
	}

	var parts []string
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}

	return strings.Join(parts, ""), nil
}
