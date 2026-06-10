package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/parallel"
	"github.com/kelp/gale/internal/registry"
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
			outdatedRecipes, outdatedGlobal, outdatedProject,
		)
		if err != nil {
			return err
		}

		// --no-refresh isn't just about taps: it also forces the
		// per-package recipe fetch onto the cache. Promote the
		// flag (and GALE_OFFLINE=1 for symmetry with the cache
		// contract) into registry.Offline so cachedGet serves
		// the cached body and never touches the network.
		applyOutdatedNoRefresh(ctx.Registry,
			tapsOfflineMode(outdatedNoRefresh))

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

// checkOutdated dispatches resolver calls in parallel under a
// bounded worker pool. On the first transport-level resolver
// error a shared atomic flag is raised; workers that have not
// yet started skip their resolver call entirely and surface a
// "skipped after earlier network error" entry. In-flight workers
// run to completion (no goroutine kill) but the per-request
// context timeout (in the registry layer) bounds the wait. The
// net effect on a dead registry is roughly one worker-pool-cycle
// of timeouts instead of N × 30s.
//
// Warnings and the result aggregation happen sequentially after
// the worker barrier so output order matches sorted package
// order, not goroutine completion order.
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

	type query struct {
		name, version string
	}
	type probe struct {
		latest  string
		err     error
		skipped bool
	}

	queries := make([]query, len(names))
	for i, name := range names {
		queries[i] = query{name: name, version: pkgs[name]}
	}

	var hardStop atomic.Bool
	// 8 workers: per-package work is HTTP-bound (registry fetch);
	// covers typical package list sizes without goroutine overhead.
	// Errors slice is always nil — probe captures errors in its fields.
	probes, _ := parallel.Map(context.Background(), queries, 8,
		func(_ context.Context, q query) (probe, error) {
			if hardStop.Load() {
				return probe{skipped: true}, nil
			}
			r, err := resolver(q.name)
			if err != nil {
				if isTransportError(err) {
					hardStop.Store(true)
				}
				return probe{err: err}, nil
			}
			return probe{latest: r.Package.Full()}, nil
		})

	var result outdatedResult
	for i, q := range queries {
		p := probes[i]
		switch {
		case p.skipped:
			result.Skipped++
			result.Errors = append(result.Errors,
				fmt.Errorf("%s: skipped after earlier network error",
					q.name))
		case p.err != nil:
			out.Warn(fmt.Sprintf("Skipping %s: %v", q.name, p.err))
			result.Skipped++
			result.Errors = append(result.Errors,
				fmt.Errorf("%s: %w", q.name, p.err))
		default:
			// Git-installed packages store a bare short hash as
			// their version. ver.IsNewer returns true
			// unconditionally for non-semver strings, so a hash
			// would always appear outdated. Skip such packages:
			// a read-only report must not flag a package as
			// outdated just because version format comparison
			// is undefined. Users can run `gale update <pkg>`
			// explicitly to rebuild from HEAD.
			if isGitHash(q.version) {
				continue
			}
			// Compare via Full() so a revision bump (recipe
			// revision 1 → 2 with unchanged upstream version)
			// still shows as outdated.
			if ver.IsNewer(p.latest, q.version) {
				result.Items = append(result.Items, outdatedItem{
					Name:    q.name,
					Current: q.version,
					Latest:  p.latest,
				})
			}
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

// applyOutdatedNoRefresh flips the registry into Offline mode
// when --no-refresh (or GALE_OFFLINE=1) is in effect. The
// outdated command lifts the flag into the cache contract so
// the per-package recipe fetch reuses the cached body
// instead of opening a fresh HTTP connection per package.
// Nil-safe for `--recipes`, which has no registry attached.
func applyOutdatedNoRefresh(reg *registry.Registry, noRefresh bool) {
	if !noRefresh || reg == nil {
		return
	}
	reg.Offline = true
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
