package main

import (
	"testing"
)

func TestFormatOutdated(t *testing.T) {
	tests := []struct {
		name       string
		items      []outdatedItem
		wantLines  int
		wantEmpty  bool
	}{
		{
			"no outdated packages",
			nil,
			0,
			true,
		},
		{
			"one outdated package",
			[]outdatedItem{
				{"jq", "1.7.1", "1.8.1"},
			},
			1,
			false,
		},
		{
			"multiple outdated packages",
			[]outdatedItem{
				{"jq", "1.7.1", "1.8.1"},
				{"go", "1.25.0", "1.26.1"},
			},
			2,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := formatOutdated(tt.items)
			if tt.wantEmpty && len(lines) != 0 {
				t.Errorf("expected empty, got %d lines",
					len(lines))
			}
			if !tt.wantEmpty && len(lines) != tt.wantLines {
				t.Errorf("got %d lines, want %d",
					len(lines), tt.wantLines)
			}
			// Each line should contain name, current, and
			// latest version.
			for i, line := range lines {
				item := tt.items[i]
				if !contains(line, item.Name) ||
					!contains(line, item.Current) ||
					!contains(line, item.Latest) {
					t.Errorf("line %q missing info for %s",
						line, item.Name)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		findSubstring(s, substr)
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
