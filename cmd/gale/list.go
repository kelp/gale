package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var listScope = "all"

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List packages declared in gale.toml",
	Long: "List packages declared in gale.toml.\n\n" +
		"Reads the active gale.toml (project if present, else " +
		"global). Entries not yet present in the store are " +
		"flagged with (not installed). Use `gale sbom` for a " +
		"store-rooted view of what is actually installed.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runList(cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// runList writes the package list to stdout. Empty-state and
// informational messages go to stderr so stdout stays clean
// for shell pipelines. Returns nil for empty configurations
// (exit 0): "nothing declared" is not an error.
func runList(stdout, stderr io.Writer) error {
	switch listScope {
	case "all", "shared", "host":
	default:
		return fmt.Errorf(
			"invalid --scope %q: want all|shared|host", listScope)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working dir: %w", err)
	}

	configPath, err := config.FindGaleConfig(cwd)
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return fmt.Errorf("finding home dir: %w", homeErr)
		}
		configPath = filepath.Join(home, ".gale", "gale.toml")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "No packages declared.")
			return nil
		}
		return fmt.Errorf("reading config: %w", err)
	}

	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	host := config.CurrentHost()
	hostPkgs := hostOverlayPackages(cfg, host)
	s := store.NewStore(defaultStoreRoot())

	showShared := listScope == "all" || listScope == "shared"
	showHost := listScope == "all" || listScope == "host"

	// Stable schema: always use the grouped Shared / Host
	// form. Previously the command switched to a flat
	// `name@version` schema when no overlays applied, which
	// broke pipelines the day a user added their first
	// overlay. See audit/readonly/output-format/findings/
	// 0003-list-format-changes-with-overlays.md.
	wrote := false
	if showShared && len(cfg.Packages) > 0 {
		fmt.Fprintln(stdout, "Shared:")
		for _, name := range sortedKeys(cfg.Packages) {
			ver := cfg.Packages[name]
			suffix := installedSuffix(s, name, ver)
			if _, shadowed := hostPkgs[name]; shadowed {
				fmt.Fprintf(stdout,
					"  %s@%s  (overridden by host)%s\n",
					name, ver, suffix)
			} else {
				fmt.Fprintf(stdout, "  %s@%s%s\n",
					name, ver, suffix)
			}
		}
		wrote = true
	}
	if showHost && len(hostPkgs) > 0 {
		if wrote {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "Host (%s):\n", host)
		for _, name := range sortedKeys(hostPkgs) {
			ver := hostPkgs[name]
			suffix := installedSuffix(s, name, ver)
			fmt.Fprintf(stdout, "  %s@%s%s\n",
				name, ver, suffix)
		}
		wrote = true
	}
	if !wrote {
		fmt.Fprintln(stderr, "No packages declared.")
	}
	return nil
}

// installedSuffix returns "  (not installed)" if the package
// is declared but absent from the store, else "". Gated on a
// cheap store.IsInstalled check — the same call doctor uses.
func installedSuffix(s *store.Store, name, ver string) string {
	if s == nil {
		return ""
	}
	if s.IsInstalled(name, ver) {
		return ""
	}
	return "  (not installed)"
}

// hostOverlayPackages returns the merged map of packages
// contributed by every [hosts.<key>] section that matches
// host. Unlike EffectivePackages, the shared [packages]
// section is NOT included — callers want only the overlay
// contributions for the Host section.
func hostOverlayPackages(cfg *config.GaleConfig, host string) map[string]string {
	if host == "" || len(cfg.Hosts) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, h := range cfg.Hosts {
		if !config.HostKeyMatches(key, host) {
			continue
		}
		maps.Copy(out, h.Packages)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func init() {
	listCmd.Flags().StringVar(&listScope, "scope", "all",
		"Filter by scope: all|shared|host")
	rootCmd.AddCommand(listCmd)
}
