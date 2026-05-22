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
	"github.com/spf13/cobra"
)

var listScope = "all"

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed packages",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runList(cmd.OutOrStdout())
	},
}

// runList writes the package list to w. Split out so tests
// can inject a buffer without reaching into the cobra command.
func runList(w io.Writer) error {
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
			fmt.Fprintln(w, "No packages installed.")
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

	// Backward-compatible flat output when no host overlays
	// apply to this machine. Users who never touched hosts
	// see the same output they always have.
	if len(hostPkgs) == 0 && listScope == "all" {
		if len(cfg.Packages) == 0 {
			fmt.Fprintln(w, "No packages installed.")
			return nil
		}
		for _, name := range sortedKeys(cfg.Packages) {
			fmt.Fprintf(w, "%s@%s\n", name, cfg.Packages[name])
		}
		return nil
	}

	showShared := listScope == "all" || listScope == "shared"
	showHost := listScope == "all" || listScope == "host"

	wrote := false
	if showShared && len(cfg.Packages) > 0 {
		fmt.Fprintln(w, "Shared:")
		for _, name := range sortedKeys(cfg.Packages) {
			ver := cfg.Packages[name]
			if _, shadowed := hostPkgs[name]; shadowed {
				fmt.Fprintf(w,
					"  %s@%s  (overridden by host)\n", name, ver)
			} else {
				fmt.Fprintf(w, "  %s@%s\n", name, ver)
			}
		}
		wrote = true
	}
	if showHost && len(hostPkgs) > 0 {
		if wrote {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "Host (%s):\n", host)
		for _, name := range sortedKeys(hostPkgs) {
			fmt.Fprintf(w, "  %s@%s\n", name, hostPkgs[name])
		}
		wrote = true
	}
	if !wrote {
		fmt.Fprintln(w, "No packages installed.")
	}
	return nil
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
