package recipe

import (
	"fmt"
	"strconv"
	"strings"
)

// Constraint represents a version constraint expression
// on a dep. Empty constraint matches any version.
type Constraint struct {
	// Op is one of ">=", ">", "<=", "<", "=", or "" (any).
	Op string
	// Major, Minor, Patch, Revision are the four-tuple
	// components of the version bound. Revision defaults
	// to 1 when not specified in Raw.
	Major, Minor, Patch, Revision int
	// Any is true when the constraint is empty (no
	// restriction). Op/numbers are zero in this case.
	Any bool
}

// versionTuple is a four-part version for comparison.
// Satisfies uses the caller's revision, not the one
// parseVersion extracted from the version string.
type versionTuple struct {
	major, minor, patch, revision int
}

// parseVersion parses a version string of the form
// [v]major.minor.patch[-revision] and returns its components.
// revision defaults to 1 if absent or non-numeric.
// Returns ok=false if the string does not have at least three
// dot-separated numeric parts.
func parseVersion(s string) (versionTuple, bool) {
	// Strip leading 'v'.
	s = strings.TrimPrefix(s, "v")

	// Split off optional revision suffix.
	revision := 1
	if idx := strings.LastIndex(s, "-"); idx >= 0 {
		rev, err := strconv.Atoi(s[idx+1:])
		if err == nil {
			revision = rev
			s = s[:idx]
		}
	}

	parts := strings.Split(s, ".")
	if len(parts) < 3 {
		return versionTuple{}, false
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return versionTuple{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return versionTuple{}, false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return versionTuple{}, false
	}

	return versionTuple{major, minor, patch, revision}, true
}

// ParseConstraint parses an expression into a Constraint.
// Examples:
//
//	""          → Constraint{Any: true}
//	">=1.2.3-2" → {Op: ">=", Major:1, Minor:2, Patch:3, Revision:2}
//	"1.2.3"     → {Op: "=", Major:1, Minor:2, Patch:3, Revision:1}
//	"=1.2.3-5"  → {Op: "=", Major:1, Minor:2, Patch:3, Revision:5}
func ParseConstraint(expr string) (Constraint, error) {
	if expr == "" {
		return Constraint{Any: true}, nil
	}

	// Strip leading operator (try multi-char ops first).
	op := ""
	rest := expr
	for _, candidate := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(expr, candidate) {
			op = candidate
			rest = expr[len(candidate):]
			break
		}
	}
	if op == "" {
		op = "="
	}

	v, ok := parseVersion(rest)
	if !ok {
		return Constraint{}, fmt.Errorf("invalid version %q in constraint %q", rest, expr)
	}

	return Constraint{
		Op:       op,
		Major:    v.major,
		Minor:    v.minor,
		Patch:    v.patch,
		Revision: v.revision,
	}, nil
}

// compareTuples returns -1, 0, or 1 for a<b, a==b, a>b
// where tuples are (major, minor, patch, revision).
func compareTuples(a, b versionTuple) int {
	for _, pair := range [][2]int{
		{a.major, b.major},
		{a.minor, b.minor},
		{a.patch, b.patch},
		{a.revision, b.revision},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

// Satisfies reports whether an installed (version, revision)
// satisfies the constraint.
func (c Constraint) Satisfies(version string, revision int) bool {
	if c.Any {
		return true
	}

	// Parse the installed version; caller provides revision
	// authoritatively so parseVersion's revision is overwritten.
	installed, ok := parseVersion(version)
	if !ok {
		return false
	}
	installed.revision = revision

	cmp := compareTuples(installed, versionTuple{
		c.Major, c.Minor, c.Patch, c.Revision,
	})

	switch c.Op {
	case "=":
		return cmp == 0
	case ">=":
		return cmp >= 0
	case ">":
		return cmp > 0
	case "<=":
		return cmp <= 0
	case "<":
		return cmp < 0
	default:
		return false
	}
}
