package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/output"
	ver "github.com/kelp/gale/internal/version"
	"github.com/spf13/cobra"
)

var (
	outdatedIndex   string
	outdatedGlobal  bool
	outdatedProject bool
)

// outdatedItem represents a package with a newer version.
type outdatedItem struct {
	Name    string
	Current string
	Latest  string
}

// outdatedResult is the aggregate outcome of one outdated run.
// Items lists packages with newer versions; Skipped counts
// packages whose resolve failed. Errors carries the per-package
// failures in iteration order so the command layer can surface
// them.
type outdatedResult struct {
	Items   []outdatedItem
	Skipped int
	Errors  []error
}

var outdatedCmd = &cobra.Command{
	Use:   "outdated",
	Short: "Show packages with newer versions available",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(outdatedGlobal, outdatedProject); err != nil {
			return err
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runOutdated(ctx, indexSource(outdatedIndex),
			newCmdOutput(cmd))
	},
}

// runOutdated resolves every declared package against one index
// session. Index fetch errors are errors (§15.16): there is no
// stale-serving path here, so an unreachable index fails the run.
func runOutdated(
	ctx context.Context, src index.Source, out *output.Output,
) error {
	c, err := newCmdContext("", outdatedGlobal, outdatedProject)
	if err != nil {
		return err
	}
	cfg, err := c.LoadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Packages) == 0 {
		out.Info("No packages installed.")
		return nil
	}

	sess, err := index.Open(ctx, src)
	if err != nil {
		return fmt.Errorf("opening index: %w", err)
	}
	latest := func(name string) (string, error) {
		got, _, err := sess.Resolve(ctx, name, "")
		return got, err
	}

	result := checkOutdated(cfg.Packages, latest, out)

	// Print outdated rows in sorted order.
	for _, line := range formatOutdated(result.Items) {
		fmt.Println(line)
	}

	return summarizeOutdated(result, out)
}

// checkOutdated probes packages in sorted name order against the
// session-pinned index. A per-package failure (entry not found,
// bad document) is recorded and reported; it does not stop the
// remaining probes, and none of them is ever answered from a cache.
func checkOutdated(
	pkgs map[string]string,
	latest func(name string) (string, error),
	out *output.Output,
) outdatedResult {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)

	var result outdatedResult
	for _, name := range names {
		current := pkgs[name]
		got, err := latest(name)
		if err != nil {
			out.Warn(fmt.Sprintf("Skipping %s: %v", name, err))
			result.Skipped++
			result.Errors = append(result.Errors,
				fmt.Errorf("%s: %w", name, err))
			continue
		}
		// Git-installed packages store a bare short hash as
		// their version. ver.IsNewer returns true
		// unconditionally for non-semver strings, so a hash
		// would always appear outdated. Skip such packages:
		// a read-only report must not flag a package as
		// outdated just because version format comparison
		// is undefined. Users can run `gale update <pkg>`
		// explicitly to move to the locked tip.
		if isGitHash(current) {
			continue
		}
		if ver.IsNewer(got, current) {
			result.Items = append(result.Items, outdatedItem{
				Name:    name,
				Current: current,
				Latest:  got,
			})
		}
	}
	return result
}

// summarizeOutdated emits the closing line and returns the
// command's exit error. Skipped > 0 with no items is the
// "could not check anything" case — exit non-zero so CI gates
// like `gale outdated && release` don't false-pass on an index
// outage.
func summarizeOutdated(
	result outdatedResult, out *output.Output,
) error {
	switch {
	case len(result.Items) == 0 && result.Skipped == 0:
		out.Success("Everything is up to date.")
		return nil
	case len(result.Items) == 0 && result.Skipped > 0:
		return fmt.Errorf(
			"could not check %d package(s); index "+
				"unreachable (see warnings above)",
			result.Skipped,
		)
	case result.Skipped > 0:
		// Some checked, some couldn't — surface a non-zero
		// exit so scripts don't treat the partial result as
		// a clean signal.
		return fmt.Errorf(
			"checked partial result: %d outdated, %d "+
				"unchecked (see warnings above)",
			len(result.Items), result.Skipped,
		)
	}
	return nil
}

// formatOutdated formats outdated items as lines of text.
// The separator between current and latest is the Unicode
// arrow `→` when the locale advertises UTF-8, otherwise `->`
// — falling back to ASCII keeps the output legible under
// LANG=C / LC_ALL=C terminals that would render the arrow
// as `?` or mojibake.
func formatOutdated(items []outdatedItem) []string {
	sep := "→"
	if !supportsUnicode() {
		sep = "->"
	}
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("%s %s %s %s",
			item.Name, item.Current, sep, item.Latest)
	}
	return lines
}

// supportsUnicode reports whether the active locale can
// render multi-byte UTF-8 glyphs. We inspect LC_ALL first
// (POSIX precedence) and fall back to LANG. A locale string
// counts as UTF-8 when it carries a `.UTF-8` or `.utf8`
// charset suffix (case-insensitive); the bare names "C" /
// "POSIX" and an unset environment imply ASCII-only.
func supportsUnicode() bool {
	val := os.Getenv("LC_ALL")
	if val == "" {
		val = os.Getenv("LANG")
	}
	if val == "" {
		return false
	}
	lower := strings.ToLower(val)
	return strings.HasSuffix(lower, ".utf-8") ||
		strings.HasSuffix(lower, ".utf8")
}

func init() {
	outdatedCmd.Flags().StringVar(&outdatedIndex, "index", "",
		"Resolve against a local index checkout")
	outdatedCmd.Flags().BoolVarP(&outdatedGlobal, "global", "g", false,
		"Check outdated packages in the global gale.toml")
	outdatedCmd.Flags().BoolVarP(&outdatedProject, "project", "p", false,
		"Check outdated packages in the project gale.toml")
	rootCmd.AddCommand(outdatedCmd)
}
