package ai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- Behavior 1: Create client with API key ---

func TestNewClientReturnsNonNil(t *testing.T) {
	c := NewClient("sk-test-key")
	if c == nil {
		t.Fatal("expected non-nil Client")
	}
}

func TestNewClientEmptyKeyReturnsNonNil(t *testing.T) {
	c := NewClient("")
	if c == nil {
		t.Fatal("expected non-nil Client even with empty key")
	}
}

// --- Behavior 2: Single-shot completion ---

func TestCompleteReturnsResponseText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "Hello from Claude"},
				},
				"model": "claude-sonnet-4-20250514",
				"role":  "assistant",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	defer srv.Close()

	c := NewClient("sk-test-key")
	c.SetBaseURL(srv.URL)

	got, err := c.Complete("say hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Hello from Claude" {
		t.Errorf("Complete() = %q, want %q",
			got, "Hello from Claude")
	}
}

func TestCompleteSendsAPIKeyHeader(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotKey = r.Header.Get("x-api-key")
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
				"model": "claude-sonnet-4-20250514",
				"role":  "assistant",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	defer srv.Close()

	c := NewClient("sk-my-secret")
	c.SetBaseURL(srv.URL)

	if _, err := c.Complete("test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKey != "sk-my-secret" {
		t.Errorf("x-api-key header = %q, want %q",
			gotKey, "sk-my-secret")
	}
}

func TestCompleteSendsContentTypeHeader(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotCT = r.Header.Get("Content-Type")
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
				"model": "claude-sonnet-4-20250514",
				"role":  "assistant",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	defer srv.Close()

	c := NewClient("sk-test")
	c.SetBaseURL(srv.URL)

	if _, err := c.Complete("test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type header = %q, want %q",
			gotCT, "application/json")
	}
}

func TestCompleteSendsAnthropicVersionHeader(t *testing.T) {
	var gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotVersion = r.Header.Get("anthropic-version")
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
				"model": "claude-sonnet-4-20250514",
				"role":  "assistant",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	defer srv.Close()

	c := NewClient("sk-test")
	c.SetBaseURL(srv.URL)

	if _, err := c.Complete("test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotVersion == "" {
		t.Error("anthropic-version header is empty")
	}
}

// --- Behavior 3: Graceful degradation ---

func TestCompleteWithoutAPIKeyReturnsErrNotConfigured(t *testing.T) {
	c := NewClient("")

	_, err := c.Complete("hello")
	if err == nil {
		t.Fatal("expected error when API key is empty")
	}
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("error = %v, want ErrNotConfigured", err)
	}
}

func TestCompleteWithoutAPIKeyDoesNotMakeHTTPCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)

	_, _ = c.Complete("hello")
	if called {
		t.Error("HTTP call made despite empty API key")
	}
}

// --- Behavior 4: Parse API response ---

func TestParseMultipleContentBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "first "},
					{"type": "text", "text": "second"},
				},
				"model": "claude-sonnet-4-20250514",
				"role":  "assistant",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	defer srv.Close()

	c := NewClient("sk-test")
	c.SetBaseURL(srv.URL)

	got, err := c.Complete("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "first second" {
		t.Errorf("Complete() = %q, want %q",
			got, "first second")
	}
}

func TestCompleteReturnsErrorOnHTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error",
				http.StatusInternalServerError)
		}))
	defer srv.Close()

	c := NewClient("sk-test")
	c.SetBaseURL(srv.URL)

	_, err := c.Complete("test")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestCompleteReturnsErrorOnEmptyContentBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]any{
				"content": []map[string]any{},
				"model":   "claude-sonnet-4-20250514",
				"role":    "assistant",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	defer srv.Close()

	c := NewClient("sk-test")
	c.SetBaseURL(srv.URL)

	got, err := c.Complete("test")
	// Either an error or an empty string is acceptable.
	// But we should not get non-empty text from empty blocks.
	if err == nil && got != "" {
		t.Errorf("expected empty string or error for empty content blocks, got %q", got)
	}
}

// --- Behavior 5: Build request ---

func TestCompleteRequestBodyFormat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("failed to read request body: %v", err)
			}
			if err := json.Unmarshal(body, &gotBody); err != nil {
				t.Errorf("failed to parse request body: %v", err)
			}

			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
				"model": "claude-sonnet-4-20250514",
				"role":  "assistant",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	defer srv.Close()

	c := NewClient("sk-test")
	c.SetBaseURL(srv.URL)

	if _, err := c.Complete("my prompt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check model field.
	model, ok := gotBody["model"].(string)
	if !ok || model == "" {
		t.Error("request body missing model field")
	}

	// Check max_tokens field.
	maxTokens, ok := gotBody["max_tokens"].(float64)
	if !ok || maxTokens <= 0 {
		t.Errorf("max_tokens = %v, want positive number", gotBody["max_tokens"])
	}

	// Check messages array.
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatal("request body missing messages array")
	}

	msg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatal("first message is not an object")
	}
	if msg["role"] != "user" {
		t.Errorf("message role = %q, want %q",
			msg["role"], "user")
	}
	if msg["content"] != "my prompt" {
		t.Errorf("message content = %q, want %q",
			msg["content"], "my prompt")
	}
}

func TestCompleteRequestUsesPostMethod(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
				"model": "claude-sonnet-4-20250514",
				"role":  "assistant",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
	defer srv.Close()

	c := NewClient("sk-test")
	c.SetBaseURL(srv.URL)

	if _, err := c.Complete("test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want %q",
			gotMethod, http.MethodPost)
	}
}
