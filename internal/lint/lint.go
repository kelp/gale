package lint

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Issue represents a lint finding.
type Issue struct {
	Level   string // "error" or "warning"
	Message string
}

// recipe mirrors the TOML structure for linting.
// Uses pointers and raw maps to detect missing fields.
type recipe struct {
	Package struct {
		Name        string
		Version     string
		Description string
		License     string
		Homepage    string
		Platforms   []string
	}
	Source struct {
		URL        string
		SHA256     string
		Repo       string
		ReleasedAt string `toml:"released_at"`
	}
	Dependencies struct {
		Build   []string `toml:"build"`
		Runtime []string `toml:"runtime"`
	}
	Build  map[string]interface{}
	Binary map[string]struct {
		URL    string
		SHA256 string
	}
}

var hexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// lintRule validates one aspect of a parsed recipe.
type lintRule func(
	r *recipe, filePath string,
	addErr func(string), addWarn func(string),
)

// rules is the ordered list of validators that Lint runs.
var rules = []lintRule{
	lintRequiredFields,
	lintSHA256Format,
	lintFilePath,
	lintOptionalFields,
	lintSourceRepo,
	lintReleasedAt,
	lintBuildSteps,
	lintPlatforms,
}

// Lint validates a recipe TOML string and returns issues.
// filePath is used for path-based checks; pass "" to skip.
func Lint(data, filePath string) []Issue {
	var r recipe
	if err := toml.Unmarshal([]byte(data), &r); err != nil {
		return []Issue{{
			Level:   "error",
			Message: fmt.Sprintf("invalid TOML: %v", err),
		}}
	}

	var issues []Issue
	addErr := func(msg string) {
		issues = append(issues, Issue{"error", msg})
	}
	addWarn := func(msg string) {
		issues = append(issues, Issue{"warning", msg})
	}

	for _, rule := range rules {
		rule(&r, filePath, addErr, addWarn)
	}

	return issues
}

// lintRequiredFields checks name, version, url, sha256,
// and build steps.
func lintRequiredFields(
	r *recipe, _ string,
	addErr func(string), _ func(string),
) {
	if r.Package.Name == "" {
		addErr("missing required field: package.name")
	}
	if r.Package.Version == "" {
		addErr("missing required field: package.version")
	}
	if r.Source.URL == "" {
		addErr("missing required field: source.url")
	}
	if r.Source.SHA256 == "" {
		addErr("missing required field: source.sha256")
	}
	if len(extractSteps(r.Build)) == 0 {
		addErr("missing required field: build steps")
	}
}

// lintSHA256Format validates hex format for source and
// binary SHA256 values.
func lintSHA256Format(
	r *recipe, _ string,
	addErr func(string), _ func(string),
) {
	if r.Source.SHA256 != "" && !hexRe.MatchString(r.Source.SHA256) {
		addErr(fmt.Sprintf(
			"source.sha256 is not valid 64-char hex: %q",
			r.Source.SHA256))
	}
	for platform, bin := range r.Binary {
		if bin.SHA256 != "" && !hexRe.MatchString(bin.SHA256) {
			addErr(fmt.Sprintf(
				"binary.%s.sha256 is not valid 64-char hex: %q",
				platform, bin.SHA256))
		}
	}
}

// lintFilePath checks that the file name and letter bucket
// match the package name.
func lintFilePath(
	r *recipe, filePath string,
	addErr func(string), _ func(string),
) {
	if filePath != "" && r.Package.Name != "" {
		checkFilePath(filePath, r.Package.Name, addErr)
	}
}

// lintOptionalFields warns about missing description,
// license, and homepage.
func lintOptionalFields(
	r *recipe, _ string,
	_ func(string), addWarn func(string),
) {
	if r.Package.Description == "" {
		addWarn("missing description")
	}
	if r.Package.License == "" {
		addWarn("missing license")
	}
	if r.Package.Homepage == "" {
		addWarn("missing homepage")
	}
}

// lintSourceRepo checks repo presence, format, and URL
// mismatch.
func lintSourceRepo(
	r *recipe, _ string,
	_ func(string), addWarn func(string),
) {
	if r.Source.Repo == "" {
		addWarn("missing source.repo (no auto-update)")
	} else if !isValidRepo(r.Source.Repo) {
		addWarn(fmt.Sprintf(
			"source.repo should be owner/repo or a full URL: %q",
			r.Source.Repo))
	}

	if r.Source.Repo != "" && r.Source.URL != "" {
		checkRepoURL(r.Source.Repo, r.Source.URL, addWarn)
	}
}

// lintReleasedAt validates the released_at date format.
func lintReleasedAt(
	r *recipe, _ string,
	_ func(string), addWarn func(string),
) {
	if r.Source.ReleasedAt != "" {
		if _, err := time.Parse("2006-01-02",
			r.Source.ReleasedAt); err != nil {
			addWarn(fmt.Sprintf(
				"released_at is not YYYY-MM-DD: %q",
				r.Source.ReleasedAt))
		}
	}
}

// lintBuildSteps checks PREFIX usage, autoreconf, and
// missing build deps.
func lintBuildSteps(
	r *recipe, _ string,
	_ func(string), addWarn func(string),
) {
	steps := extractSteps(r.Build)
	if len(steps) == 0 {
		return
	}

	// No ${PREFIX} usage.
	hasPrefix := false
	for _, s := range steps {
		if strings.Contains(s, "${PREFIX}") ||
			strings.Contains(s, "$PREFIX") {
			hasPrefix = true
			break
		}
	}
	if !hasPrefix {
		addWarn("no build step references ${PREFIX}")
	}

	// autoreconf warning.
	for _, s := range steps {
		if containsCommand(s, "autoreconf") {
			addWarn(
				"autoreconf requires autoconf, automake, " +
					"libtool, and m4; prefer a release tarball " +
					"with pre-generated configure")
			break
		}
	}

	// Missing build deps.
	checkBuildDeps(steps, r.Dependencies.Build, addWarn)
}

// lintPlatforms warns about unrecognized platform strings.
func lintPlatforms(
	r *recipe, _ string,
	_ func(string), addWarn func(string),
) {
	checkPlatforms(r.Package.Platforms, addWarn)
}

// extractSteps pulls the "steps" array from the raw
// build map. Handles both top-level [build] and
// per-platform [build.<platform>] sections.
func extractSteps(build map[string]interface{}) []string {
	if build == nil {
		return nil
	}
	if raw, ok := build["steps"]; ok {
		return toStringSlice(raw)
	}
	// Check platform overrides.
	for _, v := range build {
		if m, ok := v.(map[string]interface{}); ok {
			if raw, ok := m["steps"]; ok {
				return toStringSlice(raw)
			}
		}
	}
	return nil
}

func toStringSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// isValidRepo returns true if repo is either an owner/repo
// shorthand (e.g., "jqlang/jq") or a full URL.
func isValidRepo(repo string) bool {
	if strings.HasPrefix(repo, "https://") {
		return true
	}
	// owner/repo: exactly one slash, no other special chars.
	parts := strings.Split(repo, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

// validPlatforms lists the recognized <os>-<arch> values.
var validPlatforms = map[string]bool{
	"darwin-arm64": true,
	"darwin-amd64": true,
	"linux-amd64":  true,
	"linux-arm64":  true,
}

// depPatterns maps a tool pattern to the expected build dep.
// Each entry has a list of substrings to match in step text,
// and an optional skip condition.
type depPattern struct {
	substrs []string // any match triggers the check
	dep     string   // expected dep name
}

var depPatterns = []depPattern{
	{substrs: []string{"go build", "go install"}, dep: "go"},
	{substrs: []string{"cargo build", "cargo install"}, dep: "rust"},
	{substrs: []string{"cmake"}, dep: "cmake"},
	{substrs: []string{"zig build"}, dep: "zig"},
	{substrs: []string{"pip install", "python setup.py"}, dep: "python"},
	{substrs: []string{"meson setup", "meson compile"}, dep: "meson"},
	{substrs: []string{"pkg-config", "pkgconf"}, dep: "pkgconf"},
	// gnumake is handled separately due to ./configure exception
}

func checkBuildDeps(
	steps, buildDeps []string, addWarn func(string),
) {
	depSet := make(map[string]bool, len(buildDeps))
	for _, d := range buildDeps {
		depSet[d] = true
	}

	// Check standard patterns.
	for _, pat := range depPatterns {
		if depSet[pat.dep] {
			continue
		}
		for _, step := range steps {
			for _, sub := range pat.substrs {
				if containsCommand(step, sub) {
					addWarn(fmt.Sprintf(
						"build step uses %q but %q "+
							"is not in build deps",
						sub, pat.dep))
					goto nextPattern
				}
			}
		}
	nextPattern:
	}

	// Check make → gnumake, but skip if ./configure present
	// (autotools provides its own make).
	if depSet["gnumake"] {
		return
	}
	hasConfigure := false
	hasMake := false
	for _, step := range steps {
		if strings.Contains(step, "./configure") {
			hasConfigure = true
		}
		if step == "make" ||
			strings.HasPrefix(step, "make ") {
			hasMake = true
		}
	}
	if hasMake && !hasConfigure {
		addWarn(
			"build step uses \"make\" but \"gnumake\" " +
				"is not in build deps")
	}
}

// containsCommand checks if sub appears in s as a command
// (at the start or after a non-alphanumeric character), not
// as a substring of another word. This prevents "go install"
// from matching inside "cargo install".
func containsCommand(s, sub string) bool {
	idx := strings.Index(s, sub)
	if idx < 0 {
		return false
	}
	if idx == 0 {
		return true
	}
	prev := s[idx-1]
	return prev == ' ' || prev == '\t' || prev == ';' ||
		prev == '&' || prev == '|' || prev == '('
}

func checkPlatforms(
	platforms []string, addWarn func(string),
) {
	for _, p := range platforms {
		if !validPlatforms[p] {
			addWarn(fmt.Sprintf(
				"unrecognized platform %q", p))
		}
	}
}

// checkRepoURL warns if source.url is on github.com but
// points at a different repo than source.repo declares.
// Only checks GitHub shorthand repos (owner/repo) against
// GitHub URLs. Non-GitHub URLs are fine — many projects
// distribute tarballs from their own CDN.
func checkRepoURL(repo, sourceURL string, addWarn func(string)) {
	// Only check GitHub shorthand repos.
	if strings.HasPrefix(repo, "https://") {
		return
	}

	// Only check when URL is on github.com.
	if !strings.Contains(sourceURL, "github.com/") {
		return
	}

	expected := "github.com/" + repo + "/"
	if !strings.Contains(sourceURL, expected) {
		addWarn(fmt.Sprintf(
			"source.url does not match source.repo %q",
			repo))
	}
}

func checkFilePath(filePath, name string, addErr func(string)) {
	base := filepath.Base(filePath)
	expectedBase := name + ".toml"
	if base != expectedBase {
		addErr(fmt.Sprintf(
			"file path %q does not match package name %q",
			filePath, name))
		return
	}

	dir := filepath.Dir(filePath)
	letter := filepath.Base(dir)
	if letter != string(name[0]) {
		addErr(fmt.Sprintf(
			"file path letter bucket %q does not match "+
				"package name %q (expected %q/)",
			letter, name, string(name[0])))
	}
}
