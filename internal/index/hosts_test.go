package index

import "testing"

func TestAllowedHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"github.com", true},
		{"go.dev", true},
		{"GitHub.com", true},
		{"GO.DEV", true},
		{"objects.githubusercontent.com", false},
		{"release-assets.githubusercontent.com", false},
		{"codeload.github.com", false},
		{"www.github.com", false},
		{"ghcr.io", false},
		{"", false},
		{"github.com.", false},
		{"github.com:443", false},
		{"github.com/foo", false},
	}
	for _, tt := range tests {
		name := tt.host
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			if got := AllowedHost(tt.host); got != tt.want {
				t.Fatalf("AllowedHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}
