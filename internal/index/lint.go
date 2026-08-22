package index

import (
	"cmp"
	"slices"
	"strings"

	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/store"
)

// Issue is one lint finding. Every issue is an error.
type Issue struct {
	Path    string
	Message string
}

// Lint reports every problem in f. It does not compute or fill
// digests. A nil file yields one issue.
func Lint(f *File) []Issue {
	if f == nil {
		return []Issue{{Message: "index is required"}}
	}
	var issues []Issue
	lintPackage(&issues, f)
	for _, ver := range sortedKeys(f.Versions) {
		lintVersion(&issues, ver, f.Versions[ver])
	}
	sortIssues(issues)
	return issues
}

// LintDiff reports mutations that break append-only version
// blocks. Both sides must be non-nil.
func LintDiff(old, next *File) []Issue {
	if old == nil || next == nil {
		return []Issue{{Message: "both files are required"}}
	}
	var issues []Issue
	if old.Package.Name != next.Package.Name {
		add(&issues, "package.name", "package name is immutable")
	}
	for _, ver := range sortedKeys(old.Versions) {
		nv, ok := next.Versions[ver]
		if !ok {
			add(&issues, "versions."+ver, "version was removed")
			continue
		}
		lintDiffArtifacts(&issues, "versions."+ver, old.Versions[ver], nv)
	}
	sortIssues(issues)
	return issues
}

func lintPackage(issues *[]Issue, f *File) {
	lintName(issues, f.Package.Name)
	if f.Package.Latest == "" {
		add(issues, "package.latest", "latest is required")
		return
	}
	if _, ok := f.Versions[f.Package.Latest]; !ok {
		add(issues, "package.latest", "latest does not name a version")
	}
}

func lintName(issues *[]Issue, name string) {
	if name == "" {
		add(issues, "package.name", "name is required")
		return
	}
	if !startsAlnum(name) {
		add(issues, "package.name", "name must start with a letter or digit")
	}
	if !isIdent(name) {
		add(issues, "package.name", "name has invalid characters")
	}
	if !store.SafeComponent(name) {
		add(issues, "package.name", "name is not a single path component")
	}
	if name == store.FetchNamespace {
		add(issues, "package.name", "name is reserved")
	}
}

func lintVersion(issues *[]Issue, ver string, v Version) {
	p := "versions." + ver
	if !isIdent(ver) {
		add(issues, p, "version has invalid characters")
	}
	if !store.SafeComponent(ver) {
		add(issues, p, "version is not a single path component")
	}
	if len(v.Artifacts) == 0 {
		add(issues, p, "version has no artifacts")
		return
	}
	for _, plat := range sortedKeys(v.Artifacts) {
		lintPlatform(issues, p+".artifacts."+plat, plat, v.Artifacts[plat])
	}
}

func lintPlatform(issues *[]Issue, p, plat string, a Artifact) {
	if !validPlatform(plat) {
		add(issues, p, "platform must be os/arch")
	}
	lintArtifact(issues, p, a)
}

func lintArtifact(issues *[]Issue, p string, a Artifact) {
	lintURL(issues, p+".url", a.URL)
	lintEnum(issues, p+".format", a.Format, allowedFormat)
	lintSHA256(issues, p+".sha256", a.SHA256)
	lintTreeDigest(issues, p+".tree_digest", a.TreeDigest)
	lintEnum(issues, p+".hash_source", a.HashSource, allowedHashSource)
	if a.Strip < 0 {
		add(issues, p+".strip", "strip must not be negative")
	}
	if a.Attestation != nil && !*a.Attestation {
		add(issues, p+".attestation", "attestation must be true")
	}
	lintFiles(issues, p, a.Files)
}

func lintSHA256(issues *[]Issue, p, v string) {
	if v == "" {
		add(issues, p, "sha256 is required")
		return
	}
	if !lockgraph.IsHexSHA256(v) {
		add(issues, p, "sha256 must be 64 lowercase hex digits")
	}
}

func lintTreeDigest(issues *[]Issue, p, v string) {
	if v == "" {
		add(issues, p, "tree_digest is required")
		return
	}
	if !lockgraph.IsDigest(v) {
		add(issues, p, "tree_digest must be a sha256 digest")
	}
}

func lintEnum(issues *[]Issue, p, v string, allowed map[string]bool) {
	field := p[strings.LastIndex(p, ".")+1:]
	if v == "" {
		add(issues, p, field+" is required")
		return
	}
	if !allowed[v] {
		add(issues, p, field+" is not allowed")
	}
}

func lintDiffArtifacts(issues *[]Issue, p string, old, next Version) {
	pref := p + ".artifacts."
	for _, plat := range sortedKeys(old.Artifacts) {
		na, ok := next.Artifacts[plat]
		if !ok {
			add(issues, pref+plat, "artifact was removed")
			continue
		}
		lintDiffArtifact(issues, pref+plat, old.Artifacts[plat], na)
	}
	for _, plat := range sortedKeys(next.Artifacts) {
		if _, ok := old.Artifacts[plat]; !ok {
			add(issues, pref+plat, "artifact was added to an existing version")
		}
	}
}

func lintDiffArtifact(issues *[]Issue, p string, old, next Artifact) {
	if old.URL != next.URL {
		add(issues, p+".url", "field is immutable")
	}
	if old.Format != next.Format {
		add(issues, p+".format", "field is immutable")
	}
	if old.SHA256 != next.SHA256 {
		add(issues, p+".sha256", "field is immutable")
	}
	if old.TreeDigest != next.TreeDigest {
		add(issues, p+".tree_digest", "field is immutable")
	}
	if old.HashSource != next.HashSource {
		add(issues, p+".hash_source", "field is immutable")
	}
	if old.Strip != next.Strip {
		add(issues, p+".strip", "field is immutable")
	}
	if !filesEqual(old.Files, next.Files) {
		add(issues, p+".files", "field is immutable")
	}
	lintDiffAttestation(issues, p, old.Attestation, next.Attestation)
}

func lintDiffAttestation(issues *[]Issue, p string, old, next *bool) {
	if old != nil && *old && next == nil {
		add(issues, p+".attestation", "attestation cannot be removed")
		return
	}
	if !boolPtrEqual(old, next) {
		add(issues, p+".attestation", "attestation is immutable")
	}
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func filesEqual(a, b []FileEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func add(issues *[]Issue, path, msg string) {
	*issues = append(*issues, Issue{Path: path, Message: msg})
}

func sortIssues(issues []Issue) {
	slices.SortFunc(issues, func(a, b Issue) int {
		if c := cmp.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return cmp.Compare(a.Message, b.Message)
	})
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func startsAlnum(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isIdentRune(r) {
			return false
		}
	}
	return true
}

func isIdentRune(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == '+' || r == '-':
		return true
	default:
		return false
	}
}

func validPlatform(s string) bool {
	osName, arch, ok := strings.Cut(s, "/")
	if !ok || osName == "" || arch == "" || strings.Contains(arch, "/") {
		return false
	}
	switch osName {
	case "darwin", "linux":
	default:
		return false
	}
	switch arch {
	case "arm64", "amd64":
		return true
	default:
		return false
	}
}

var allowedFormat = map[string]bool{
	"tar.gz": true,
	"tar.xz": true,
	"zip":    true,
	"binary": true,
}

var allowedHashSource = map[string]bool{
	"upstream-sha256sums": true,
	"computed":            true,
}
