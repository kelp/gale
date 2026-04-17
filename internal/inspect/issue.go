package inspect

// Kind identifies a type of linkage issue.
type Kind string

const (
	// KindUnresolvableRef means a binary references
	// @rpath/libX and no rpath entry resolves to an
	// existing libX file.
	KindUnresolvableRef Kind = "unresolvable-ref"

	// KindStaleRpath means an LC_RPATH or RUNPATH entry
	// points to a path that does not exist.
	KindStaleRpath Kind = "stale-rpath"

	// KindUndeclaredDep means a binary references a gale
	// store dir for package X, but X is not listed in the
	// recipe's [dependencies] build or runtime.
	KindUndeclaredDep Kind = "undeclared-dep"

	// KindOverDeclaredDep means the recipe declares a dep
	// that no binary under the install references via rpath.
	KindOverDeclaredDep Kind = "over-declared-dep"

	// KindVersionSkew means two binaries under the same
	// install reference different versions of the same
	// gale-managed dep.
	KindVersionSkew Kind = "version-skew"
)

// Severity groups issue kinds by severity.
func (k Kind) Severity() string {
	switch k {
	case KindUnresolvableRef, KindStaleRpath:
		return "error"
	default:
		return "warn"
	}
}

// Issue describes one linkage problem found in an installed
// package.
type Issue struct {
	Kind    Kind   `json:"kind"`
	Package string `json:"package"`
	Version string `json:"version"`
	Binary  string `json:"binary,omitempty"`
	Details string `json:"details,omitempty"`
}
