// Package inspect walks installed gale packages and reports
// linkage issues: unresolvable @rpath references, stale
// rpath entries, and mismatches between a binary's dep
// references and its recipe's declared deps.
//
// Pure read-only. No state changes.
package inspect

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/recipe"
)

// storeRe matches the trailing .gale/pkg/<name>/<version>
// in a path. Works for any home prefix.
var storeRe = regexp.MustCompile(
	`\.gale[/\\]pkg[/\\]([^/\\]+)[/\\]([^/\\]+)`)

// binaryRefs holds what we extracted from one binary file.
type binaryRefs struct {
	// rel is the path relative to the install prefix,
	// used for reporting.
	rel string
	// rpaths are absolute (or @-relative) LC_RPATH /
	// RUNPATH entries in the order they appear.
	rpaths []string
	// deps are LC_LOAD_DYLIB / ELF NEEDED references.
	deps []string
}

// readBinary returns binaryRefs for a single file, or nil
// if the file isn't an inspectable binary on this platform.
// Implementations live in binary_{darwin,linux}.go.
// readBinary returns (nil, nil) for files the scanner
// should skip silently.

// ScanInstalled scans one installed package. r may be nil,
// in which case checks that require the recipe
// (undeclared-dep, over-declared-dep) are skipped.
//
// prefix is the install directory
// (e.g. ~/.gale/pkg/curl/8.19.0). name and version are used
// only for populating Issue fields.
func ScanInstalled(
	prefix, name, version string, r *recipe.Recipe,
) ([]Issue, error) {
	var issues []Issue

	// referencedPkgs tracks pkgs the binaries use, with one
	// of the referenced versions remembered per pkg. Used
	// for the over-declared-dep check.
	referencedPkgs := map[string]string{}

	// versionsByPkg tracks every version of each pkg seen
	// across all binaries, for version-skew detection.
	versionsByPkg := map[string]map[string]struct{}{}

	err := filepath.Walk(prefix, func(
		path string, info os.FileInfo, err error,
	) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		refs, err := readBinary(path)
		if err != nil || refs == nil {
			return nil //nolint:nilerr // not an inspectable binary
		}
		rel, rErr := filepath.Rel(prefix, path)
		if rErr != nil {
			rel = path
		}
		refs.rel = rel

		// Expand @loader_path / @executable_path to
		// concrete dirs so rpath walks find the libs.
		loaderDir := filepath.Dir(path)
		expandedRpaths := expandRpaths(refs.rpaths, loaderDir)

		// stale-rpath: each rpath that points to a
		// non-existent absolute path after expansion.
		for i, rp := range refs.rpaths {
			ex := expandedRpaths[i]
			if strings.HasPrefix(ex, "@") || strings.HasPrefix(ex, "$") {
				continue
			}
			if _, err := os.Stat(ex); err != nil {
				issues = append(issues, Issue{
					Kind:    KindStaleRpath,
					Package: name,
					Version: version,
					Binary:  rel,
					Details: rp,
				})
			}
		}

		// Resolve each dep ref.
		for _, dep := range refs.deps {
			if skipDep(dep) {
				continue
			}
			resolvedPath, ok := resolveRef(dep, expandedRpaths)
			if !ok {
				issues = append(issues, Issue{
					Kind:    KindUnresolvableRef,
					Package: name,
					Version: version,
					Binary:  rel,
					Details: dep,
				})
				continue
			}
			if pkg, ver, ok := storeNameVersion(resolvedPath); ok {
				// Skip self-references: curl's binaries
				// naturally load curl's own dylibs.
				if pkg == name {
					continue
				}
				referencedPkgs[pkg] = ver
				if _, seen := versionsByPkg[pkg]; !seen {
					versionsByPkg[pkg] = map[string]struct{}{}
				}
				versionsByPkg[pkg][ver] = struct{}{}
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", prefix, err)
	}

	// version-skew: any pkg with more than one version.
	for pkg, vers := range versionsByPkg {
		if len(vers) <= 1 {
			continue
		}
		var vs []string
		for v := range vers {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		issues = append(issues, Issue{
			Kind:    KindVersionSkew,
			Package: name,
			Version: version,
			Details: fmt.Sprintf("%s: %s",
				pkg, strings.Join(vs, ", ")),
		})
	}

	// Recipe-dependent checks.
	if r != nil {
		declared := declaredDepSet(r)

		// undeclared-dep: binary references pkg X not in
		// the recipe's deps.
		for pkg := range referencedPkgs {
			if _, ok := declared[pkg]; ok {
				continue
			}
			issues = append(issues, Issue{
				Kind:    KindUndeclaredDep,
				Package: name,
				Version: version,
				Details: pkg,
			})
		}

		// over-declared-dep: recipe declares a dep that no
		// binary references. Checked against runtime deps
		// only; build deps aren't expected to appear in
		// rpaths and flagging them would be noise.
		for _, pkg := range r.Dependencies.Runtime {
			if _, ok := referencedPkgs[pkg]; ok {
				continue
			}
			issues = append(issues, Issue{
				Kind:    KindOverDeclaredDep,
				Package: name,
				Version: version,
				Details: pkg,
			})
		}
	}

	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Binary != b.Binary {
			return a.Binary < b.Binary
		}
		return a.Details < b.Details
	})
	return issues, nil
}

// skipDep reports whether a dep reference should be
// ignored (system paths, loader-relative).
func skipDep(dep string) bool {
	if strings.HasPrefix(dep, "/System/") ||
		strings.HasPrefix(dep, "/usr/lib/") ||
		strings.HasPrefix(dep, "/lib/") ||
		strings.HasPrefix(dep, "/lib64/") {
		return true
	}
	if strings.HasPrefix(dep, "@loader_path") ||
		strings.HasPrefix(dep, "@executable_path") {
		return true
	}
	return false
}

// expandRpaths substitutes @loader_path and
// @executable_path with loaderDir. We treat both as the
// binary's own directory. This is exact for @loader_path
// and for @executable_path when the binary is itself the
// executable (most common case); for a dylib referenced
// transitively, @executable_path would depend on the
// loader which we don't know — the approximation is good
// enough for lint purposes.
func expandRpaths(rpaths []string, loaderDir string) []string {
	out := make([]string, len(rpaths))
	for i, rp := range rpaths {
		switch {
		case strings.HasPrefix(rp, "@loader_path"):
			out[i] = filepath.Clean(filepath.Join(
				loaderDir, strings.TrimPrefix(rp, "@loader_path")))
		case strings.HasPrefix(rp, "@executable_path"):
			out[i] = filepath.Clean(filepath.Join(
				loaderDir, strings.TrimPrefix(rp, "@executable_path")))
		default:
			out[i] = rp
		}
	}
	return out
}

// resolveRef resolves a dep reference against a binary's
// (already @-expanded) rpath list. Returns the absolute
// path of the first rpath that contains the referenced
// library, along with true. Returns ("", false) if
// unresolvable.
func resolveRef(dep string, rpaths []string) (string, bool) {
	if strings.HasPrefix(dep, "@rpath/") {
		lib := strings.TrimPrefix(dep, "@rpath/")
		for _, rp := range rpaths {
			if strings.HasPrefix(rp, "@") || strings.HasPrefix(rp, "$") {
				continue
			}
			p := filepath.Join(rp, lib)
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
		return "", false
	}
	// ELF NEEDED entries are usually bare SONAMEs like
	// libcurl.so.4. Search rpaths for them.
	if !strings.ContainsRune(dep, filepath.Separator) {
		for _, rp := range rpaths {
			if strings.HasPrefix(rp, "@") || strings.HasPrefix(rp, "$") {
				continue
			}
			p := filepath.Join(rp, dep)
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
		return "", false
	}
	// Absolute path.
	if _, err := os.Stat(dep); err == nil {
		return dep, true
	}
	return "", false
}

// storeNameVersion extracts (name, version) from a path
// containing .gale/pkg/<name>/<version>. Returns
// ("", "", false) if the path isn't under a gale store.
func storeNameVersion(p string) (string, string, bool) {
	m := storeRe.FindStringSubmatch(p)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// declaredDepSet builds a set containing every name in
// the recipe's build and runtime deps.
func declaredDepSet(r *recipe.Recipe) map[string]struct{} {
	s := map[string]struct{}{}
	for _, d := range r.Dependencies.Build {
		s[d] = struct{}{}
	}
	for _, d := range r.Dependencies.Runtime {
		s[d] = struct{}{}
	}
	return s
}
