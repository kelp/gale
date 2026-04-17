package main

import (
	"testing"
)

func TestFormatOutdated(t *testing.T) {
	tests := []struct {
		name      string
		items     []outdatedItem
		wantLines int
		wantEmpty bool
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

func TestVersionNewer(t *testing.T) {
	tests := []struct {
		name     string
		registry string
		current  string
		want     bool
	}{
		{"newer patch", "1.8.2", "1.8.1", true},
		{"newer minor", "1.9.0", "1.8.1", true},
		{"newer major", "2.0.0", "1.8.1", true},
		{"same version", "1.8.1", "1.8.1", false},
		{"older patch", "1.8.0", "1.8.1", false},
		{"older minor", "1.7.0", "1.8.1", false},
		{"older major", "0.9.0", "1.8.1", false},
		{"non-semver registry", "abc", "1.0.0", false},
		{"non-semver current", "1.0.0", "abc", false},
		{"both non-semver differ", "xyz", "abc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionNewer(tt.registry, tt.current)
			if got != tt.want {
				t.Errorf(
					"versionNewer(%q, %q) = %v, want %v",
					tt.registry, tt.current, got, tt.want)
			}
		})
	}
}

func TestParseSemver_BareVersionDefaultsRevision1(t *testing.T) {
	got, ok := parseSemver("1.2.3")
	if !ok {
		t.Fatal("parseSemver(\"1.2.3\") returned ok=false, want true")
	}
	want := [4]int{1, 2, 3, 1}
	if got != want {
		t.Errorf("parseSemver(\"1.2.3\") = %v, want %v", got, want)
	}
}

func TestParseSemver_ExplicitRevision(t *testing.T) {
	got, ok := parseSemver("1.2.3-2")
	if !ok {
		t.Fatal("parseSemver(\"1.2.3-2\") returned ok=false, want true")
	}
	want := [4]int{1, 2, 3, 2}
	if got != want {
		t.Errorf("parseSemver(\"1.2.3-2\") = %v, want %v", got, want)
	}
}

func TestParseSemver_LeadingVTrimmed(t *testing.T) {
	got, ok := parseSemver("v1.2.3-5")
	if !ok {
		t.Fatal("parseSemver(\"v1.2.3-5\") returned ok=false, want true")
	}
	want := [4]int{1, 2, 3, 5}
	if got != want {
		t.Errorf("parseSemver(\"v1.2.3-5\") = %v, want %v", got, want)
	}
}

func TestParseSemver_NonNumericSuffixDefaultsRevision1(t *testing.T) {
	got, ok := parseSemver("1.0.0-rc1")
	if !ok {
		t.Fatal("parseSemver(\"1.0.0-rc1\") returned ok=false, want true")
	}
	want := [4]int{1, 0, 0, 1}
	if got != want {
		t.Errorf("parseSemver(\"1.0.0-rc1\") = %v, want %v", got, want)
	}
}

func TestParseSemver_TwoPartsInvalid(t *testing.T) {
	_, ok := parseSemver("1.2")
	if ok {
		t.Error("parseSemver(\"1.2\") returned ok=true, want false")
	}
}

func TestParseSemver_NonNumericInvalid(t *testing.T) {
	_, ok := parseSemver("abc")
	if ok {
		t.Error("parseSemver(\"abc\") returned ok=true, want false")
	}
}

func TestParseSemver_MultiDigitRevision(t *testing.T) {
	got, ok := parseSemver("1.2.3-10")
	if !ok {
		t.Fatal("parseSemver(\"1.2.3-10\") returned ok=false, want true")
	}
	want := [4]int{1, 2, 3, 10}
	if got != want {
		t.Errorf("parseSemver(\"1.2.3-10\") = %v, want %v", got, want)
	}
}

func TestVersionNewer_RevisionBumpCountsAsOutdated(t *testing.T) {
	got := versionNewer("1.0.0-2", "1.0.0-1")
	if !got {
		t.Error(
			"versionNewer(\"1.0.0-2\", \"1.0.0-1\") = false, want true")
	}
}

// Bare version implies revision 1, so rev-2 must be newer than bare.
// Stub sets revision=0 for both, so "1.0.0-2" and "1.0.0" compare as
// equal (both revision=0) and return false — test fails on stub.
func TestVersionNewer_Revision2NewerThanBare(t *testing.T) {
	got := versionNewer("1.0.0-2", "1.0.0")
	if !got {
		t.Error(
			"versionNewer(\"1.0.0-2\", \"1.0.0\") = false, want true")
	}
}

// Bare version implies revision 1, so rev-3 is newer than bare (rev 1 < 3).
// Stub sets revision=0 for both so returns false — test fails on stub.
func TestVersionNewer_Revision3NewerThanBare(t *testing.T) {
	got := versionNewer("1.0.0-3", "1.0.0")
	if !got {
		t.Error(
			"versionNewer(\"1.0.0-3\", \"1.0.0\") = false, want true")
	}
}

// With stub revision=0, "1.0.0-100" and "1.0.0-99" are both (1,0,0,0)
// and versionNewer returns false. Real impl parses 100 > 99 → true.
func TestVersionNewer_HigherRevisionIsNewer(t *testing.T) {
	got := versionNewer("1.0.0-100", "1.0.0-99")
	if !got {
		t.Error(
			"versionNewer(\"1.0.0-100\", \"1.0.0-99\") = false, want true")
	}
}

// Stub sets revision=0 for "1.0.0-6", so both sides compare as (1,0,0,0)
// and returns false. Real impl: 6 > 5 → true. Test fails on stub.
func TestVersionNewer_HigherRevisionIncrementIsNewer(t *testing.T) {
	got := versionNewer("1.0.0-6", "1.0.0-5")
	if !got {
		t.Error(
			"versionNewer(\"1.0.0-6\", \"1.0.0-5\") = false, want true")
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
