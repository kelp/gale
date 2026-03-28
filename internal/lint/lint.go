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
	}
	Source struct {
		URL        string
		SHA256     string
		Repo       string
		ReleasedAt string `toml:"released_at"`
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
