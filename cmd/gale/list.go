package main

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	listScope   = "all"
	listGlobal  bool
	listProject bool
	listAll     bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List packages declared in gale.toml",
	Long: "List packages declared in gale.toml.\n\n" +
		"Reads the active gale.toml (project if present, else " +
		"global). Entries not yet present in the store are " +
		"flagged with (not installed).",
	// ExactArgs(0) over NoArgs: cobra.NoArgs emits the
	// confusing "unknown command" message when called with a
	// positional like `gale list pkgname` — list has no
	// subcommands.  ExactArgs(0) emits the straightforward
	// "accepts 0 arg(s), received 1".
	Args: cobra.ExactArgs(0),
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
	case "all", "shared":
	default:
		return fmt.Errorf(
			"invalid --scope %q: want all|shared", listScope,
		)
	}
	if err := validateScopeFlags(listGlobal, listProject); err != nil {
		return err
	}

	// --all prints both project + global configs (when they
	// exist) with section headers.
	if listAll {
		return runListAll(stdout, stderr)
	}

	configPath, err := resolveReadOnlyConfigPath(listGlobal, listProject)
	if err != nil {
		return err
	}
	return printConfigList(stdout, stderr, configPath, "")
}

// runListAll prints both project and global package listings
// with section headers. Missing configs are skipped silently.
// When neither exists, "No packages declared." goes to stderr.
func runListAll(stdout, stderr io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working dir: %w", err)
	}
	projectPath, projErr := projectConfigPath(cwd)
	globalPath, gErr := globalConfigPath()
	if gErr != nil {
		return gErr
	}

	wrote := false
	if projErr == nil {
		ok, existsErr := galeConfigExists(projectPath)
		if existsErr != nil {
			return existsErr
		}
		if ok {
			fmt.Fprintln(stdout, "Project:")
			if err := printConfigList(
				stdout, stderr, projectPath, "  ",
			); err != nil {
				return err
			}
			wrote = true
		}
	}
	if _, statErr := os.Stat(globalPath); statErr == nil {
		if wrote {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintln(stdout, "Global:")
		if err := printConfigList(
			stdout, stderr, globalPath, "  ",
		); err != nil {
			return err
		}
		wrote = true
	}
	if !wrote {
		fmt.Fprintln(stderr, "No packages declared.")
	}
	return nil
}

// printConfigList prints the package list for a single
// gale.toml. Headers and entries go to stdout indented with
// prefix; the empty-state notice goes to stderr.
func printConfigList(stdout, stderr io.Writer, configPath, prefix string) error {
	// A missing gale.toml returns an empty config, which
	// flows to the empty-state notice below.
	cfg, err := readGaleConfig(configPath)
	if err != nil {
		return err
	}

	s := store.NewStore(defaultStoreRoot())

	// Shared [packages] only. Leftover [hosts.*] overlays
	// refuse on live verbs; list does not present them.
	wrote := false
	if len(cfg.Packages) > 0 {
		fmt.Fprintf(stdout, "%sShared:\n", prefix)
		for _, name := range sortedKeys(cfg.Packages) {
			ver := cfg.Packages[name]
			suffix := installedSuffix(s, name, ver)
			fmt.Fprintf(stdout, "%s  %s@%s%s\n",
				prefix, name, ver, suffix)
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
		"Filter by scope: all|shared")
	listCmd.Flags().BoolVarP(&listGlobal, "global", "g", false,
		"List packages from the global gale.toml")
	listCmd.Flags().BoolVarP(&listProject, "project", "p", false,
		"List packages from the project gale.toml")
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false,
		"List packages from both project and global gale.toml")
	rootCmd.AddCommand(listCmd)
}
