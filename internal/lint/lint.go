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

	// Required fields.
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

	// Build steps.
	steps := extractSteps(r.Build)
	if len(steps) == 0 {
		addErr("missing required field: build steps")
	}

	// SHA256 format.
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

	// File path checks.
	if filePath != "" && r.Package.Name != "" {
		checkFilePath(filePath, r.Package.Name, addErr)
	}

	// Warnings: optional fields.
	if r.Package.Description == "" {
		addWarn("missing description")
	}
	if r.Package.License == "" {
		addWarn("missing license")
	}
	if r.Package.Homepage == "" {
		addWarn("missing homepage")
	}
	if r.Source.Repo == "" {
		addWarn("missing source.repo (no auto-update)")
	} else if !isValidRepo(r.Source.Repo) {
		addWarn(fmt.Sprintf(
			"source.repo should be owner/repo or a full URL: %q",
			r.Source.Repo))
	}

	// Warning: released_at format.
	if r.Source.ReleasedAt != "" {
		if _, err := time.Parse("2006-01-02",
			r.Source.ReleasedAt); err != nil {
			addWarn(fmt.Sprintf(
				"released_at is not YYYY-MM-DD: %q",
				r.Source.ReleasedAt))
		}
	}

	// Warning: no ${PREFIX} in build steps.
	if len(steps) > 0 {
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
	}

	// Warning: missing build deps implied by build steps.
	if len(steps) > 0 {
		checkBuildDeps(steps, r.Dependencies.Build, addWarn)
	}

	// Warning: unrecognized platform strings.
	checkPlatforms(r.Package.Platforms, addWarn)

	return issues
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
