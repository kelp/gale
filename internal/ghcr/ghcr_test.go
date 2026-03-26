package ghcr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// --- Behavior 1: Fetches anonymous token ---

func TestTokenReturnsTokenFromEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"token": "test-bearer-token-123",
			})
		}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	got, err := Token("kelp/gale-recipes/jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "test-bearer-token-123" {
		t.Errorf("Token() = %q, want %q",
			got, "test-bearer-token-123")
	}
}

// --- Behavior 2: Parses token from JSON response ---

func TestTokenParsesTokenField(t *testing.T) {
	want := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.sub.sig"
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type",
				"application/json")
			json.NewEncoder(w).Encode(
				map[string]string{"token": want})
		}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	got, err := Token("kelp/gale-recipes/jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("Token() = %q, want %q", got, want)
	}
}

// --- Behavior 3: Errors on non-200 from token endpoint ---

func TestTokenErrorsOnNon200(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"server error", http.StatusInternalServerError},
		{"unauthorized", http.StatusUnauthorized},
		{"forbidden", http.StatusForbidden},
		{"not found", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "error", tt.status)
				}))
			defer srv.Close()

			old := tokenEndpoint
			tokenEndpoint = srv.URL
			defer func() { tokenEndpoint = old }()

			_, err := Token("kelp/gale-recipes/jq")
			if err == nil {
				t.Fatalf("expected error for status %d",
					tt.status)
			}
		})
	}
}

// --- Behavior 4: Errors on malformed JSON ---

func TestTokenErrorsOnMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("this is not json"))
		}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	_, err := Token("kelp/gale-recipes/jq")
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestTokenErrorsOnMissingTokenField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"not_token": "value",
			})
		}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	_, err := Token("kelp/gale-recipes/jq")
	if err == nil {
		t.Fatal("expected error when token field missing")
	}
}

// --- Behavior 5: Uses GALE_GITHUB_TOKEN env var ---

func TestTokenUsesEnvVarWhenSet(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			called.Store(true)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"token": "should-not-use-this",
			})
		}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	t.Setenv("GALE_GITHUB_TOKEN", "my-personal-token")

	got, err := Token("kelp/gale-recipes/jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "my-personal-token" {
		t.Errorf("Token() = %q, want %q",
			got, "my-personal-token")
	}
	if called.Load() {
		t.Error("HTTP request made despite " +
			"GALE_GITHUB_TOKEN being set")
	}
}

func TestTokenIgnoresEmptyEnvVar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"token": "fetched-token",
			})
		}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	t.Setenv("GALE_GITHUB_TOKEN", "")

	got, err := Token("kelp/gale-recipes/jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "fetched-token" {
		t.Errorf("Token() = %q, want %q",
			got, "fetched-token")
	}
}

// --- Behavior 6: Constructs correct token URL ---

func TestTokenSendsCorrectRequest(t *testing.T) {
	type reqInfo struct {
		method  string
		service string
		scope   string
	}
	ch := make(chan reqInfo, 1)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			ch <- reqInfo{
				method:  r.Method,
				service: r.URL.Query().Get("service"),
				scope:   r.URL.Query().Get("scope"),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"token": "tok",
			})
		}))
	defer srv.Close()

	old := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = old }()

	_, err := Token("kelp/gale-recipes/jq")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case got := <-ch:
		if got.method != http.MethodGet {
			t.Errorf("HTTP method = %q, want %q",
				got.method, http.MethodGet)
		}
		if got.service != "ghcr.io" {
			t.Errorf("service param = %q, want %q",
				got.service, "ghcr.io")
		}
		wantScope := "repository:kelp/gale-recipes/jq:pull"
		if got.scope != wantScope {
			t.Errorf("scope param = %q, want %q",
				got.scope, wantScope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called; Token() did not " +
			"make HTTP request")
	}
}
