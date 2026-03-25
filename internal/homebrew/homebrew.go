package homebrew

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Formula holds metadata parsed from a Homebrew formula file.
type Formula struct {
	Name        string
	Version     string
	Description string
	License     string
	Homepage    string
	SourceURL   string
	SHA256      string
	RuntimeDeps []string
	BuildDeps   []string
	BuildSteps  []string
}

// defaultBaseURL is the raw GitHub URL for homebrew-core formulas.
const defaultBaseURL = "https://raw.githubusercontent.com/Homebrew/homebrew-core/master/Formula"

// FetchFormula fetches and parses a formula from GitHub.
// baseURL overrides the default raw GitHub URL (for testing).
func FetchFormula(name, baseURL string) (*Formula, error) {
	letter := string(name[0])
	url := fmt.Sprintf("%s/%s/%s.rb", baseURL, letter, name)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch formula %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch formula %s: HTTP %d", name, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read formula %s: %w", name, err)
	}

	return ParseFormula(name, string(body))
}

// ParseFormula parses a Homebrew Ruby formula string into a Formula.
func ParseFormula(name, ruby string) (*Formula, error) {
	f := &Formula{Name: name}

	f.Description = extractQuoted(ruby, `desc\s+"`)
	f.Homepage = extractQuoted(ruby, `homepage\s+"`)
	f.License = extractLicense(ruby)

	// Extract URL and SHA256 (outside head block).
	f.SourceURL = extractTopLevelURL(ruby)
	f.SHA256 = extractTopLevelSHA256(ruby)

	// Extract version from URL if possible.
	f.Version = extractVersion(f.SourceURL, name)

	// Extract dependencies.
	f.BuildDeps, f.RuntimeDeps = extractDeps(ruby)

	// Extract build steps from install method.
	f.BuildSteps = extractBuildSteps(ruby)

	return f, nil
}

// extractQuoted pulls the first quoted string after a pattern.
var reQuoted = regexp.MustCompile(`"([^"]*)"`)

func extractQuoted(ruby, pattern string) string {
	re := regexp.MustCompile(pattern + `([^"]*)"`)
	m := re.FindStringSubmatch(ruby)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func extractLicense(ruby string) string {
	// Simple case: license "MIT"
	re := regexp.MustCompile(`license\s+"([^"]+)"`)
	m := re.FindStringSubmatch(ruby)
	if len(m) > 1 {
		return m[1]
	}
	// any_of case: license any_of: ["Apache-2.0", "MIT"]
	re2 := regexp.MustCompile(`license\s+any_of:\s*\[([^\]]+)\]`)
	m2 := re2.FindStringSubmatch(ruby)
	if len(m2) > 1 {
		// Return first license.
		inner := reQuoted.FindStringSubmatch(m2[1])
		if len(inner) > 1 {
			return inner[1]
		}
	}
	return ""
}

// extractTopLevelURL gets the url outside of head/bottle blocks.
func extractTopLevelURL(ruby string) string {
	// Remove head block to avoid matching head URLs.
	cleaned := removeBlock(ruby, "head do")
	re := regexp.MustCompile(`(?m)^\s*url\s+"([^"]+)"`)
	m := re.FindStringSubmatch(cleaned)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// extractTopLevelSHA256 gets sha256 outside of bottle/head blocks.
func extractTopLevelSHA256(ruby string) string {
	cleaned := removeBlock(ruby, "bottle do")
	cleaned = removeBlock(cleaned, "head do")
	re := regexp.MustCompile(`(?m)^\s*sha256\s+"([0-9a-f]+)"`)
	m := re.FindStringSubmatch(cleaned)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// extractVersion tries to pull a version from the source URL.
func extractVersion(url, name string) string {
	// Match common patterns: /v1.2.3/, /name-1.2.3/, /jq-1.2.3/
	patterns := []string{
		`/v?(\d+\.\d+(?:\.\d+)*)\.tar`,
		`/v?(\d+\.\d+(?:\.\d+)*)\.zip`,
		`/` + regexp.QuoteMeta(name) + `[._-]v?(\d+\.\d+(?:\.\d+)*)`,
		`/tags/v?(\d+\.\d+(?:\.\d+)*)`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		m := re.FindStringSubmatch(url)
		if len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// extractDeps parses depends_on lines into build and runtime deps.
func extractDeps(ruby string) (build, runtime []string) {
	// Remove head block deps.
	cleaned := removeBlock(ruby, "head do")

	re := regexp.MustCompile(`depends_on\s+"([^"]+)"(?:\s*=>\s*:build)?`)
	for _, m := range re.FindAllStringSubmatch(cleaned, -1) {
		name := m[1]
		full := m[0]
		if strings.Contains(full, "=> :build") {
			build = append(build, name)
		} else {
			runtime = append(runtime, name)
		}
	}
	return build, runtime
}

// extractBuildSteps parses the install method into build commands.
func extractBuildSteps(ruby string) []string {
	// Find the install method body.
	re := regexp.MustCompile(`(?ms)def install\s*\n(.*?)\n\s*end`)
	m := re.FindStringSubmatch(ruby)
	if len(m) < 2 {
		return nil
	}

	body := m[1]
	var steps []string

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Skip conditional and non-build lines.
		if strings.HasSuffix(line, "if build.head?") ||
			strings.HasPrefix(line, "generate_completions") ||
			strings.HasPrefix(line, "zsh_completion") ||
			strings.HasPrefix(line, "bash_completion") ||
			strings.HasPrefix(line, "fish_completion") ||
			strings.HasPrefix(line, "(man") ||
			strings.HasPrefix(line, "man1") ||
			strings.HasPrefix(line, "ENV[") ||
			strings.HasPrefix(line, "if ") ||
			strings.HasPrefix(line, "end") ||
			strings.HasPrefix(line, "mkdir") {
			continue
		}

		step := parseSystemCall(line)
		if step != "" {
			steps = append(steps, step)
		}
	}

	return steps
}

// parseSystemCall converts a Ruby system call to a shell command.
func parseSystemCall(line string) string {
	// system "cmd", "arg1", "arg2" → "cmd arg1 arg2"
	if !strings.HasPrefix(line, "system ") {
		return ""
	}

	inner := strings.TrimPrefix(line, "system ")

	// Handle multi-line system calls (joined with commas).
	var args []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		// Strip Ruby string quotes.
		part = strings.Trim(part, `"`)
		// Translate Homebrew splat helpers.
		if part == "*std_configure_args" {
			args = append(args, "--prefix=${PREFIX}")
			continue
		}
		if strings.HasPrefix(part, "*std_cargo_args") {
			args = append(args, "--root", "${PREFIX}")
			continue
		}
		// Skip Ruby interpolation and method calls.
		if strings.HasPrefix(part, "*") ||
			strings.Contains(part, "#{Formula") ||
			strings.Contains(part, "#{etc}") ||
			strings.Contains(part, "#{share}") ||
			strings.Contains(part, "(") {
			continue
		}
		if part != "" {
			args = append(args, part)
		}
	}

	if len(args) == 0 {
		return ""
	}

	cmd := strings.Join(args, " ")

	// Translate Ruby interpolation for common Homebrew paths.
	cmd = strings.ReplaceAll(cmd, "#{prefix}", "${PREFIX}")
	cmd = strings.ReplaceAll(cmd, "#{buildpath}", ".")

	// Remove args that reference other Homebrew formulas
	// (e.g., #{Formula["openssl@3"].opt_prefix}) — these
	// need manual translation.
	re := regexp.MustCompile(`\s*--[a-z-]+=#{Formula\[[^\]]+\][^"]*}`)
	cmd = re.ReplaceAllString(cmd, "")

	return strings.TrimSpace(cmd)
}

// removeBlock removes a Ruby block (e.g., "head do...end").
func removeBlock(ruby, start string) string {
	idx := strings.Index(ruby, start)
	if idx < 0 {
		return ruby
	}
	// Scan line by line for proper do/end matching.
	lines := strings.Split(ruby[idx:], "\n")
	depth := 0
	endIdx := idx
	for _, line := range lines {
		endIdx += len(line) + 1
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, " do") || strings.HasSuffix(trimmed, "\tdo") || strings.Contains(trimmed, start) {
			depth++
		}
		if trimmed == "end" {
			depth--
			if depth == 0 {
				return ruby[:idx] + ruby[endIdx:]
			}
		}
	}
	return ruby
}

// ToRecipeTOML generates a gale recipe TOML string.
func (f *Formula) ToRecipeTOML() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Generated from Homebrew formula (BSD-2-Clause)\n\n")
	fmt.Fprintf(&b, "[package]\n")
	fmt.Fprintf(&b, "name = %q\n", f.Name)
	fmt.Fprintf(&b, "version = %q\n", f.Version)
	fmt.Fprintf(&b, "description = %q\n", f.Description)
	fmt.Fprintf(&b, "license = %q\n", f.License)
	fmt.Fprintf(&b, "homepage = %q\n", f.Homepage)

	fmt.Fprintf(&b, "\n[source]\n")
	fmt.Fprintf(&b, "url = %q\n", f.SourceURL)
	fmt.Fprintf(&b, "sha256 = %q\n", f.SHA256)

	if len(f.BuildSteps) > 0 {
		fmt.Fprintf(&b, "\n[build]\n")
		fmt.Fprintf(&b, "steps = [\n")
		for _, step := range f.BuildSteps {
			// Replace Homebrew prefix with ${PREFIX}.
			fmt.Fprintf(&b, "  %q,\n", step)
		}
		fmt.Fprintf(&b, "]\n")
	}

	if len(f.BuildDeps) > 0 || len(f.RuntimeDeps) > 0 {
		fmt.Fprintf(&b, "\n[dependencies]\n")
		if len(f.BuildDeps) > 0 {
			fmt.Fprintf(&b, "build = %s\n", toTOMLArray(f.BuildDeps))
		}
		if len(f.RuntimeDeps) > 0 {
			fmt.Fprintf(&b, "runtime = %s\n", toTOMLArray(f.RuntimeDeps))
		}
	}

	return b.String()
}

func toTOMLArray(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
