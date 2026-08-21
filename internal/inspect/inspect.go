// Package inspect walks installed gale packages and reports
// linkage issues: unresolvable @rpath references, stale
// rpath entries, and mismatches between a binary's dep
// references and its recipe's declared deps.
//
// Pure read-only. No state changes.
package inspect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/recipe"
)

// ErrNotBinary reports a file that is not an inspectable
// binary on this platform. readBinary returns it so
// ScanInstalled can skip the file.
var ErrNotBinary = errors.New("not an inspectable binary")

// storeRe matches the trailing .gale/pkg/<name>/<version>
// in a path. Works for any home prefix.
var storeRe = regexp.MustCompile(
	`\.gale[/\\]pkg[/\\]([^/\\]+)[/\\]([^/\\]+)`,
)

// binaryRefs holds what we extracted from one binary file.
type binaryRefs struct {
	// rpaths are absolute (or @-relative) LC_RPATH /
	// RUNPATH entries in the order they appear.
	rpaths []string
	// deps are LC_LOAD_DYLIB / ELF NEEDED references.
	deps []string
}

// readBinary returns binaryRefs for a single file, or
// (nil, ErrNotBinary) if the file isn't an inspectable
// binary on this platform. Implementations live in
// binary_{darwin,linux}.go. ScanInstalled skips that
// sentinel the same way it skips any readBinary error.

// hasBinaryMagic reports whether path begins with one of the 4-byte
// words this platform's binary parser accepts (binaryMagics, defined
// per platform beside readBinary).
//
// It exists for cost, not correctness. ScanInstalled opens every
// regular file under an install — a 30-package closure is tens of
// thousands of them, nearly all documentation, headers and share/
// data — and on darwin each non-binary otherwise costs two full
// parser opens, macho.Open followed by macho.OpenFat. Every file
// either parser accepts starts with one of these words, so the filter
// changes what the scan COSTS and never what it ANSWERS.
//
// An unopenable or too-short file is not a binary, which is the same
// answer the parsers gave for it.
func hasBinaryMagic(path string) bool {
	f, err := os.Open(path) //nolint:gosec // walks an install prefix
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return slices.Contains(binaryMagics, magic)
}

// ScanInstalled scans one installed package. r may be nil,
// in which case checks that require the recipe
// (undeclared-dep, over-declared-dep) are skipped.
//
// prefix is the install directory
// (e.g. ~/.gale/pkg/curl/8.19.0). name and version are used
// only for populating Issue fields.
type scanPkg struct {
	prefix, name, version string
}

type scanRefs struct {
	// referencedPkgs tracks pkgs the binaries use, with one
	// of the referenced versions remembered per pkg. Used
	// for the over-declared-dep check.
	referencedPkgs map[string]string
	// versionsByPkg tracks every version of each pkg seen
	// across all binaries, for version-skew detection.
	versionsByPkg map[string]map[string]struct{}
}

func ScanInstalled(
	prefix, name, version string, r *recipe.Recipe,
) ([]Issue, error) {
	var issues []Issue
	pkg := scanPkg{prefix: prefix, name: name, version: version}
	refs := scanRefs{
		referencedPkgs: map[string]string{},
		versionsByPkg:  map[string]map[string]struct{}{},
	}

	err := filepath.Walk(prefix, func(
		path string, info os.FileInfo, err error,
	) error {
		issues = append(issues, inspectWalkFile(pkg, path, info, err, &refs)...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", prefix, err)
	}

	issues = append(issues, versionSkewIssues(pkg, refs.versionsByPkg)...)
	issues = append(issues, recipeDepIssues(pkg, refs.referencedPkgs, r)...)

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

func inspectWalkFile(
	pkg scanPkg, path string, info os.FileInfo, err error, refs *scanRefs,
) []Issue {
	if err != nil {
		return nil //nolint:nilerr // skip unreadable
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return nil
	}
	bin, err := readBinary(path)
	if err != nil || bin == nil {
		return nil //nolint:nilerr // not an inspectable binary
	}
	return inspectBinary(pkg, path, bin, refs)
}

func inspectBinary(pkg scanPkg, path string, bin *binaryRefs, refs *scanRefs) []Issue {
	rel, rErr := filepath.Rel(pkg.prefix, path)
	if rErr != nil {
		rel = path
	}
	expanded := expandRpaths(bin.rpaths, filepath.Dir(path))
	var issues []Issue
	issues = append(issues, staleRpathIssues(pkg, rel, bin.rpaths, expanded)...)
	issues = append(issues, resolveDepIssues(pkg, rel, bin.deps, expanded, refs)...)
	return issues
}

func staleRpathIssues(pkg scanPkg, rel string, rpaths, expanded []string) []Issue {
	var issues []Issue
	for i, rp := range rpaths {
		ex := expanded[i]
		if strings.HasPrefix(ex, "@") || strings.HasPrefix(ex, "$") {
			continue
		}
		if _, err := os.Stat(ex); err != nil {
			issues = append(issues, Issue{
				Kind:    KindStaleRpath,
				Package: pkg.name,
				Version: pkg.version,
				Binary:  rel,
				Details: rp,
			})
		}
	}
	return issues
}

func resolveDepIssues(
	pkg scanPkg, rel string, deps, expanded []string, refs *scanRefs,
) []Issue {
	var issues []Issue
	for _, dep := range deps {
		if skipDep(dep) {
			continue
		}
		resolvedPath, ok := resolveRef(dep, expanded)
		if !ok {
			issues = append(issues, Issue{
				Kind:    KindUnresolvableRef,
				Package: pkg.name,
				Version: pkg.version,
				Binary:  rel,
				Details: dep,
			})
			continue
		}
		name, ver, ok := storeNameVersion(resolvedPath)
		if !ok || name == pkg.name {
			// Skip self-references: curl's binaries
			// naturally load curl's own dylibs.
			continue
		}
		refs.referencedPkgs[name] = ver
		if _, seen := refs.versionsByPkg[name]; !seen {
			refs.versionsByPkg[name] = map[string]struct{}{}
		}
		refs.versionsByPkg[name][ver] = struct{}{}
	}
	return issues
}

func versionSkewIssues(pkg scanPkg, versionsByPkg map[string]map[string]struct{}) []Issue {
	var issues []Issue
	for name, vers := range versionsByPkg {
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
			Package: pkg.name,
			Version: pkg.version,
			Details: fmt.Sprintf("%s: %s",
				name, strings.Join(vs, ", ")),
		})
	}
	return issues
}

func recipeDepIssues(pkg scanPkg, referencedPkgs map[string]string, r *recipe.Recipe) []Issue {
	if r == nil {
		return nil
	}
	declared := declaredDepSet(r)
	var issues []Issue
	for name := range referencedPkgs {
		if _, ok := declared[name]; ok {
			continue
		}
		issues = append(issues, Issue{
			Kind:    KindUndeclaredDep,
			Package: pkg.name,
			Version: pkg.version,
			Details: name,
		})
	}
	for _, name := range r.Dependencies.Runtime {
		if _, ok := referencedPkgs[name]; ok {
			continue
		}
		issues = append(issues, Issue{
			Kind:    KindOverDeclaredDep,
			Package: pkg.name,
			Version: pkg.version,
			Details: name,
		})
	}
	return issues
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
				loaderDir, strings.TrimPrefix(rp, "@loader_path"),
			))
		case strings.HasPrefix(rp, "@executable_path"):
			out[i] = filepath.Clean(filepath.Join(
				loaderDir, strings.TrimPrefix(rp, "@executable_path"),
			))
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
