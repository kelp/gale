package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/spf13/cobra"
)

var (
	addGlobal  bool
	addProject bool
	addRecipes string
	addHost    string
)

var addCmd = &cobra.Command{
	Use:   "add <package>[@version] [package...]",
	Short: "Add packages to gale.toml without installing",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(addGlobal, addProject); err != nil {
			return err
		}

		out := newCmdOutput(cmd)

		// Set up resolver for version lookup.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		if addProject {
			if _, err := projectConfigPath(cwd); err != nil {
				return errors.New(errNoProject)
			}
		}

		recipeRes, _, resolveErr := resolveRecipeResolver(addRecipes)
		if resolveErr != nil {
			return resolveErr
		}
		resolver := func(name string) (string, error) {
			r, err := recipeRes(name)
			if err != nil {
				return "", err
			}
			return r.Package.Version, nil
		}

		// Resolve scope and config path once — they cannot change
		// between iterations.
		useGlobal := resolveScope(addGlobal, addProject, cwd)
		configPath, err := resolveConfigPath(useGlobal)
		if err != nil {
			return err
		}

		host, err := resolveHostFlag(addHost)
		if err != nil {
			return err
		}
		if host != "" {
			noticeNewHostSection(out, configPath, host)
		}

		for _, arg := range args {
			name, version, err := parsePackageArg(arg)
			if err != nil {
				return err
			}

			// If @version specified, trust the user.
			if version == "" {
				resolved, err := resolver(name)
				if err != nil {
					return fmt.Errorf("resolving %s: %w",
						name, err)
				}
				version = resolved
			}

			if dryRun {
				location := configPath
				if host != "" {
					location = fmt.Sprintf("%s [hosts.%s]",
						configPath, host)
				}
				out.Info(fmt.Sprintf("add %s@%s to %s",
					name, version, location))
				continue
			}

			writtenPath, err := addToConfig(
				name, version, host, configPath,
			)
			if err != nil {
				return err
			}

			location := writtenPath
			if host != "" {
				location = fmt.Sprintf("%s [hosts.%s]",
					writtenPath, host)
			}
			out.Success(fmt.Sprintf("Added %s@%s to %s",
				name, version, location))
		}

		return nil
	},
}

// resolveHostFlag turns a --host CLI value into a host name.
// Empty string is returned unchanged; callers keep today's
// location-preserving upsert, which is not "always write
// shared". "current" expands to the local hostname, and fails
// when that cannot be read: an unresolved "current" would
// flatten to "", which every caller reads as shared [packages]
// — writing host-scoped packages into the set every machine
// shares (gh#254).
//
// Frozen: no new aliases. Any other string is a selector
// written verbatim. See the Milestone 2 host-section freeze in
// docs/dev/proposals/fetch-dont-build.md.
func resolveHostFlag(v string) (string, error) {
	if v == "current" {
		return config.CurrentHost()
	}
	return v, nil
}

// noticeNewHostSection warns when a --host value names a host
// that is neither this machine nor covered by any existing
// [hosts.<key>] section in configPath, so a typo'd hostname
// that would silently create a brand-new section is visible
// (gh#108). A notice, not an error: declaring packages for a
// host that isn't in the config yet is a supported workflow.
// No-op for empty host (shared [packages]) and under --dry-run
// (nothing is created).
func noticeNewHostSection(out *output.Output, configPath, host string) {
	if host == "" || dryRun {
		return
	}
	current, err := config.CurrentHost()
	if err != nil {
		// Whether host names this machine is exactly what cannot be
		// decided now, and the notice is advisory — so say so rather
		// than answer it with a guess.
		out.Warn(fmt.Sprintf(
			"cannot tell whether '%s' names this machine: %v",
			host, err,
		))
		return
	}
	if config.HostKeyMatches(host, current) {
		return
	}
	if config.HostSectionExists(configPath, host) {
		return
	}
	out.Warn(fmt.Sprintf(
		"creating new host section '%s' in %s", host, configPath,
	))
}

func init() {
	addCmd.Flags().BoolVarP(&addGlobal, "global", "g",
		false, "Add to global config")
	addCmd.Flags().BoolVarP(&addProject, "project", "p",
		false, "Add to project config")
	addCmd.Flags().StringVar(&addRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry")
	addCmd.Flags().StringVar(&addHost, "host", "",
		"Write under [hosts.<host>.packages] "+
			"(use 'current' for this machine)")
	rootCmd.AddCommand(addCmd)
}
