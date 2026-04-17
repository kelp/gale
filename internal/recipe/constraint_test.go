package recipe

import (
	"testing"
)

// behavior 1: Empty string parses as Any.
func TestParseConstraint_EmptyStringIsAny(t *testing.T) {
	c, err := ParseConstraint("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.Any {
		t.Error("expected Any=true for empty constraint")
	}
	if c.Op != "" {
		t.Errorf("expected Op=\"\", got %q", c.Op)
	}
}

// behavior 2: Bare version parses as exact match.
func TestParseConstraint_BareVersionExactMatch(t *testing.T) {
	c, err := ParseConstraint("1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Op != "=" {
		t.Errorf("expected Op=\"=\", got %q", c.Op)
	}
	if c.Major != 1 {
		t.Errorf("expected Major=1, got %d", c.Major)
	}
	if c.Minor != 2 {
		t.Errorf("expected Minor=2, got %d", c.Minor)
	}
	if c.Patch != 3 {
		t.Errorf("expected Patch=3, got %d", c.Patch)
	}
	if c.Revision != 1 {
		t.Errorf("expected Revision=1 (default), got %d", c.Revision)
	}
	if c.Raw != "1.2.3" {
		t.Errorf("expected Raw=\"1.2.3\", got %q", c.Raw)
	}
	if c.Any {
		t.Error("expected Any=false")
	}
}

// behavior 3: Bare version with explicit revision.
func TestParseConstraint_BareVersionWithRevision(t *testing.T) {
	c, err := ParseConstraint("1.2.3-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Op != "=" {
		t.Errorf("expected Op=\"=\", got %q", c.Op)
	}
	if c.Major != 1 || c.Minor != 2 || c.Patch != 3 {
		t.Errorf("expected 1.2.3, got %d.%d.%d", c.Major, c.Minor, c.Patch)
	}
	if c.Revision != 2 {
		t.Errorf("expected Revision=2, got %d", c.Revision)
	}
}

// behavior 4: >= operator parses correctly.
func TestParseConstraint_GreaterOrEqualOperator(t *testing.T) {
	c, err := ParseConstraint(">=1.2.3-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Op != ">=" {
		t.Errorf("expected Op=\">=\", got %q", c.Op)
	}
	if c.Major != 1 || c.Minor != 2 || c.Patch != 3 {
		t.Errorf("expected 1.2.3, got %d.%d.%d", c.Major, c.Minor, c.Patch)
	}
	if c.Revision != 2 {
		t.Errorf("expected Revision=2, got %d", c.Revision)
	}
	if c.Any {
		t.Error("expected Any=false")
	}
}

// behavior 5: > operator parses correctly.
func TestParseConstraint_StrictGreaterOperator(t *testing.T) {
	c, err := ParseConstraint(">1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Op != ">" {
		t.Errorf("expected Op=\">\", got %q", c.Op)
	}
	if c.Major != 1 || c.Minor != 2 || c.Patch != 3 {
		t.Errorf("expected 1.2.3, got %d.%d.%d", c.Major, c.Minor, c.Patch)
	}
}

// behavior 6a: <= operator parses correctly.
func TestParseConstraint_LessOrEqualOperator(t *testing.T) {
	c, err := ParseConstraint("<=2.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Op != "<=" {
		t.Errorf("expected Op=\"<=\", got %q", c.Op)
	}
	if c.Major != 2 || c.Minor != 0 || c.Patch != 0 {
		t.Errorf("expected 2.0.0, got %d.%d.%d", c.Major, c.Minor, c.Patch)
	}
}

// behavior 6b: < operator parses correctly.
func TestParseConstraint_StrictLessOperator(t *testing.T) {
	c, err := ParseConstraint("<2.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Op != "<" {
		t.Errorf("expected Op=\"<\", got %q", c.Op)
	}
	if c.Major != 2 || c.Minor != 0 || c.Patch != 0 {
		t.Errorf("expected 2.0.0, got %d.%d.%d", c.Major, c.Minor, c.Patch)
	}
}

// behavior 7: Explicit = prefix parses same as bare version.
func TestParseConstraint_ExplicitEqualPrefix(t *testing.T) {
	c, err := ParseConstraint("=1.2.3-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Op != "=" {
		t.Errorf("expected Op=\"=\", got %q", c.Op)
	}
	if c.Major != 1 || c.Minor != 2 || c.Patch != 3 {
		t.Errorf("expected 1.2.3, got %d.%d.%d", c.Major, c.Minor, c.Patch)
	}
	if c.Revision != 5 {
		t.Errorf("expected Revision=5, got %d", c.Revision)
	}
}

// behavior 8: Invalid version string returns error.
func TestParseConstraint_InvalidVersionReturnsError(t *testing.T) {
	_, err := ParseConstraint(">=not.a.version")
	if err == nil {
		t.Error("expected error for invalid version, got nil")
	}
}

// behavior 9: Two-part version returns error.
func TestParseConstraint_TwoPartVersionReturnsError(t *testing.T) {
	_, err := ParseConstraint(">=1.2")
	if err == nil {
		t.Error("expected error for two-part version, got nil")
	}
}

// behavior 10: Any constraint satisfies every version.
func TestConstraint_AnySatisfiesEveryVersion(t *testing.T) {
	c := Constraint{Any: true}

	if !c.Satisfies("0.0.1", 1) {
		t.Error("Any constraint should satisfy 0.0.1-1")
	}
	if !c.Satisfies("99.99.99", 99) {
		t.Error("Any constraint should satisfy 99.99.99-99")
	}
}

// behavior 11: >= satisfies equal and greater; rejects lesser.
func TestConstraint_GreaterOrEqualSatisfies(t *testing.T) {
	c, err := ParseConstraint(">=1.2.3-2")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	cases := []struct {
		version  string
		revision int
		want     bool
	}{
		{"1.2.3", 2, true},  // equal
		{"1.2.3", 3, true},  // higher revision
		{"1.2.4", 1, true},  // higher patch
		{"2.0.0", 1, true},  // higher major
		{"1.2.3", 1, false}, // lesser revision — must NOT satisfy
	}

	for _, tc := range cases {
		got := c.Satisfies(tc.version, tc.revision)
		if got != tc.want {
			t.Errorf(">=1.2.3-2 Satisfies(%q, %d): got %v, want %v",
				tc.version, tc.revision, got, tc.want)
		}
	}
}

// behavior 12: = only matches exact four-tuple.
func TestConstraint_EqualOnlyMatchesExact(t *testing.T) {
	c, err := ParseConstraint("=1.2.3-2")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if !c.Satisfies("1.2.3", 2) {
		t.Error("=1.2.3-2 should satisfy 1.2.3-2")
	}
	if c.Satisfies("1.2.3", 3) {
		t.Error("=1.2.3-2 should NOT satisfy 1.2.3-3")
	}
	if c.Satisfies("1.2.4", 1) {
		t.Error("=1.2.3-2 should NOT satisfy 1.2.4-1")
	}
	if c.Satisfies("1.2.2", 2) {
		t.Error("=1.2.3-2 should NOT satisfy 1.2.2-2")
	}
}

// behavior 13: Bare "1.2.3" (Revision 1) satisfies "1.2.3-1".
func TestConstraint_BareVersionSatisfiesRevisionOne(t *testing.T) {
	c, err := ParseConstraint("1.2.3")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	// Bare version parses as Revision:1; must match 1.2.3-1.
	if !c.Satisfies("1.2.3", 1) {
		t.Error("bare 1.2.3 should satisfy installed 1.2.3-1")
	}
	// Must NOT satisfy 1.2.3-2 (different four-tuple).
	if c.Satisfies("1.2.3", 2) {
		t.Error("bare 1.2.3 should NOT satisfy installed 1.2.3-2")
	}
}

// behavior 14: > excludes equal; satisfies strictly greater.
func TestConstraint_StrictGreaterExcludesEqual(t *testing.T) {
	c, err := ParseConstraint(">1.2.3-2")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if c.Satisfies("1.2.3", 2) {
		t.Error(">1.2.3-2 should NOT satisfy equal 1.2.3-2")
	}
	if !c.Satisfies("1.2.3", 3) {
		t.Error(">1.2.3-2 should satisfy 1.2.3-3")
	}
	if !c.Satisfies("2.0.0", 1) {
		t.Error(">1.2.3-2 should satisfy 2.0.0-1")
	}
}

// behavior 15: Malformed installed version in Satisfies returns false.
func TestConstraint_MalformedInstalledVersionReturnsFalse(t *testing.T) {
	c, err := ParseConstraint(">=1.0.0")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	// First verify the constraint parses into what we expect — this
	// assertion fails against the stub (Op stays "").
	if c.Op != ">=" {
		t.Errorf("expected Op=\">=\", got %q", c.Op)
	}

	// A well-formed constraint with a broken installed version string
	// must return false (not panic).
	if c.Satisfies("not-a-version", 1) {
		t.Error("malformed installed version should not satisfy any constraint")
	}
}
