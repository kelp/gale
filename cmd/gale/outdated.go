package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	ver "github.com/kelp/gale/internal/version"
	"github.com/spf13/cobra"
)

var (
	outdatedRecipes   string
	outdatedNoRefresh bool
	outdatedGlobal    bool
	outdatedProject   bool
)

// outdatedItem represents a package with a newer version.
type outdatedItem struct {
	Name    string
	Current string
	Latest  string
}

// outdatedResult is the aggregate outcome of one outdated run.
// Items lists packages with newer versions; Skipped counts
// packages whose resolver call failed. Errors carries the
// per-package failures in iteration order so the command layer
// can surface them.
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
		out := newCmdOutput(cmd)

		// Auto-refresh configured taps so listings reflect the
		// actual upstream state. Skip with --recipes,
		// --no-refresh, or GALE_OFFLINE=1.
		if outdatedRecipes == "" && !tapsOfflineMode(outdatedNoRefresh) {
			if err := refreshConfiguredTapsDefault(out); err != nil {
				out.Warn(fmt.Sprintf("tap refresh: %v", err))
			}
		}

		if err := validateScopeFlags(outdatedGlobal, outdatedProject); err != nil {
			return err
		}

		ctx, err := newCmdContext(
			outdatedRecipes, outdatedGlobal, outdatedProject)
		if err != nil {
			return err
		}

		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}

		if len(cfg.Packages) == 0 {
			out.Info("No packages installed.")
			return nil
		}

		result := checkOutdated(cfg.Packages, ctx.Resolver, out)

		// Print outdated rows in sorted order.
		for _, line := range formatOutdated(result.Items) {
			fmt.Println(line)
		}

		return summarizeOutdated(result, out)
	},
}

// checkOutdated iterates the sorted package list and queries
// the resolver once per entry. On the first transport-level
// resolver error we stop probing further packages — every
// subsequent call would fail with the same 30s timeout, and
// the user wants a useful answer in <10s, not 30s × N. The
// short-circuit case is recorded as one error per skipped
// package so callers can report both the cause and the count.
func checkOutdated(
	pkgs map[string]string,
	resolver installer.RecipeResolver,
	out *output.Output,
) outdatedResult {
	names := make([]string, 0, len(pkgs))
	for name := range pkgs {
		names = append(names, name)
	}
	sort.Strings(names)

	var result outdatedResult
	var hardStop bool
	for _, name := range names {
		version := pkgs[name]
		if hardStop {
			result.Skipped++
			result.Errors = append(result.Errors,
				fmt.Errorf("%s: skipped after earlier network error",
					name))
			continue
		}
		r, err := resolver(name)
		if err != nil {
			out.Warn(fmt.Sprintf("Skipping %s: %v", name, err))
			result.Skipped++
			result.Errors = append(result.Errors,
				fmt.Errorf("%s: %w", name, err))
			if isTransportError(err) {
				// First transport-level error wins — assume the
				// registry is unreachable and skip the rest.
				hardStop = true
			}
			continue
		}

		// Compare via Full() so a revision bump (e.g.
		// recipe revision 1 → 2 with unchanged upstream
		// version) still shows as outdated. Raw Package.Version
		// only carries the upstream triple, which drops the
		// revision entirely.
		latest := r.Package.Full()
		if ver.IsNewer(latest, version) {
			result.Items = append(result.Items, outdatedItem{
				Name:    name,
				Current: version,
				Latest:  latest,
			})
		}
	}
	return result
}

// isTransportError reports whether err looks like a network
// failure (DNS, refused, timeout, context cancel). On the
// first such error in an outdated run, we stop probing
// further packages — they will all fail identically. HTTP
// status errors (404 for a renamed recipe) don't trip this
// since they're per-package, not registry-wide.
func isTransportError(err error) bool {
	s := err.Error()
	switch {
	case strings.Contains(s, "no such host"),
		strings.Contains(s, "connection refused"),
		strings.Contains(s, "i/o timeout"),
		strings.Contains(s, "Client.Timeout"),
		strings.Contains(s, "context deadline"),
		strings.Contains(s, "context canceled"),
		strings.Contains(s, "GALE_OFFLINE=1 and no cached entry"):
		return true
	}
	return false
}

// summarizeOutdated emits the closing line and returns the
// command's exit error. Skipped > 0 with no items is the
// "could not check anything" case — exit non-zero so CI gates
// like `gale outdated && release` don't false-pass on a
// registry outage.
func summarizeOutdated(
	result outdatedResult, out *output.Output,
) error {
	switch {
	case len(result.Items) == 0 && result.Skipped == 0:
		out.Success("Everything is up to date.")
		return nil
	case len(result.Items) == 0 && result.Skipped > 0:
		return fmt.Errorf(
			"could not check %d package(s); registry "+
				"unreachable (see warnings above)",
			result.Skipped)
	case result.Skipped > 0:
		// Some checked, some couldn't — surface a non-zero
		// exit so scripts don't treat the partial result as
		// a clean signal.
		return fmt.Errorf(
			"checked partial result: %d outdated, %d "+
				"unchecked (see warnings above)",
			len(result.Items), result.Skipped)
	}
	return nil
}

// formatOutdated formats outdated items as lines of text.
func formatOutdated(items []outdatedItem) []string {
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("%s %s → %s",
			item.Name, item.Current, item.Latest)
	}
	return lines
}

func init() {
	outdatedCmd.Flags().StringVar(&outdatedRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry "+
			"(bare --recipes uses ../gale-recipes/)")
	outdatedCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	outdatedCmd.Flags().BoolVar(&outdatedNoRefresh, "no-refresh", false,
		"Skip refreshing configured recipe taps before resolving")
	outdatedCmd.Flags().BoolVarP(&outdatedGlobal, "global", "g", false,
		"Check outdated packages in the global gale.toml")
	outdatedCmd.Flags().BoolVarP(&outdatedProject, "project", "p", false,
		"Check outdated packages in the project gale.toml")
	rootCmd.AddCommand(outdatedCmd)
}
