package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/spf13/cobra"
)

var (
	envVarsOnly bool
	envGlobal   bool
	envProject  bool
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print shell commands to activate the environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(envGlobal, envProject); err != nil {
			return err
		}
		out := cmd.OutOrStdout()

		galeDir, configPath, err := resolveEnvScope(envGlobal, envProject)
		if err != nil {
			return err
		}

		// `gale env` is the direnv activation path (`use gale`
		// runs it), so this is where most projects first enter
		// the gc retention registry (gh#115). No-op for the
		// global scope.
		registerProject(configPath)

		if !envVarsOnly {
			binDir := filepath.Join(galeDir, "current", "bin")
			fmt.Fprintf(out, "export PATH=\"%s:$PATH\"\n", binDir)
		}

		// Export [vars] from the resolved gale.toml. When no
		// config exists at the resolved path (e.g., auto-resolved
		// to global but ~/.gale/gale.toml is absent), no-op.
		if configPath == "" {
			return nil
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("reading %s: %w", configPath, err)
		}
		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing %s: %w", configPath, err)
		}

		// Sort keys for deterministic output.
		keys := make([]string, 0, len(cfg.Vars))
		for k := range cfg.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "export %s='%s'\n",
				k, shellEscape(cfg.Vars[k]))
		}

		return nil
	},
}

// resolveEnvScope returns the .gale directory and config path
// to use for env output. Scope flags select which gale.toml's
// vars to export and which generation's bin dir lands on PATH.
//
// Default (no flag): project if a gale.toml is in the
// directory tree, otherwise global.
// --global: ~/.gale + ~/.gale/gale.toml.
// --project: project .gale + project gale.toml (errors if no
// project found).
func resolveEnvScope(global, project bool) (galeDir, configPath string, err error) {
	if global {
		galeDir, err = galeConfigDir()
		if err != nil {
			return "", "", err
		}
		configPath, err = globalConfigPath()
		return galeDir, configPath, err
	}

	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return "", "", fmt.Errorf("getting working dir: %w", cwdErr)
	}

	if project {
		projectCfg, err := projectConfigPath(cwd)
		if err != nil {
			return "", "", fmt.Errorf(
				"no project found — run 'gale init' first",
			)
		}
		galeDir, err = galeDirForConfig(projectCfg)
		if err != nil {
			return "", "", err
		}
		return galeDir, projectCfg, nil
	}

	// Auto: project preferred when it exists.
	// galeDirForConfig (not Dir(cfg)/.gale): under ~/.gale
	// the found config is the GLOBAL one and the derived
	// dir would be the bogus ~/.gale/.gale (gh#96).
	if projectCfg, err := projectConfigPath(cwd); err == nil {
		galeDir, err = galeDirForConfig(projectCfg)
		if err != nil {
			return "", "", err
		}
		return galeDir, projectCfg, nil
	}
	galeDir, err = galeConfigDir()
	if err != nil {
		return "", "", err
	}
	configPath, err = globalConfigPath()
	return galeDir, configPath, err
}

func init() {
	envCmd.Flags().BoolVar(&envVarsOnly, "vars-only", false,
		"Only print variable exports, not PATH")
	envCmd.Flags().BoolVarP(&envGlobal, "global", "g", false,
		"Print env for the global gale.toml")
	envCmd.Flags().BoolVarP(&envProject, "project", "p", false,
		"Print env for the project gale.toml")
	rootCmd.AddCommand(envCmd)
}

// resolveGaleDir returns the .gale directory for the
// current scope. If a project gale.toml exists, returns
// the project's .gale/ dir. Otherwise returns ~/.gale/.
func resolveGaleDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}

	// Check for project gale.toml. galeDirForConfig handles
	// the cwd-under-~/.gale case, where FindGaleConfig
	// resolves to the GLOBAL config and a naive
	// Dir(cfg)/.gale would be bogus (gh#96).
	projectConfig, err := config.FindGaleConfig(cwd)
	if err == nil {
		return galeDirForConfig(projectConfig)
	}

	// Fall back to global.
	return galeConfigDir()
}

// shellEscape escapes a string for use inside single
// quotes in a POSIX shell. Embedded single quotes are
// replaced with the '\” idiom (end quote, escaped
// literal quote, re-open quote).
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
