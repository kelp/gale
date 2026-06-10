package main

import (
	"fmt"
	"os"

	"github.com/kelp/gale/internal/config"
	"github.com/spf13/cobra"
)

var (
	pinHost      string
	pinGlobal    bool
	pinProject   bool
	unpinHost    string
	unpinGlobal  bool
	unpinProject bool
)

var pinCmd = &cobra.Command{
	Use:   "pin <package>",
	Short: "Pin a package to skip during updates",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)
		name := args[0]
		host := resolveHostFlag(pinHost)

		configPath, err := resolvePinConfigPath(
			pinGlobal, pinProject,
		)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("reading config: %w", err)
		}
		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return err
		}
		// Verify membership in the targeted section so we
		// produce a clear error before locking the file.
		var pkgVer string
		if host == "" {
			v, ok := cfg.Packages[name]
			if !ok {
				return fmt.Errorf(
					"%s is not in gale.toml", name,
				)
			}
			pkgVer = v
		} else {
			v, ok := cfg.Hosts[host].Packages[name]
			if !ok {
				return fmt.Errorf(
					"%s is not in [hosts.%s.packages]",
					name, host,
				)
			}
			pkgVer = v
		}

		if dryRun {
			out.Info(fmt.Sprintf("pin %s@%s", name, pkgVer))
			return nil
		}

		if err := config.PinPackage(configPath, host, name); err != nil {
			return fmt.Errorf("pinning %s: %w", name, err)
		}

		out.Success(fmt.Sprintf("Pinned %s@%s",
			name, pkgVer))
		return nil
	},
}

var unpinCmd = &cobra.Command{
	Use:   "unpin <package>",
	Short: "Unpin a package to allow updates",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		out := newCmdOutput(cmd)
		name := args[0]
		host := resolveHostFlag(unpinHost)

		configPath, err := resolvePinConfigPath(
			unpinGlobal, unpinProject,
		)
		if err != nil {
			return err
		}

		if dryRun {
			out.Info(fmt.Sprintf("unpin %s", name))
			return nil
		}

		if err := config.UnpinPackage(
			configPath, host, name,
		); err != nil {
			return fmt.Errorf("unpinning %s: %w", name, err)
		}

		out.Success(fmt.Sprintf("Unpinned %s", name))
		return nil
	},
}

// resolvePinConfigPath resolves the gale.toml that pin and
// unpin mutate, honoring -g/-p like the other mutating
// commands (install, add, remove). Pin used to hardcode
// resolveConfigPath(false), so the global config could never
// be targeted and a non-project cwd failed on a gale.toml
// that never existed (gh#73).
func resolvePinConfigPath(global, project bool) (string, error) {
	if err := validateScopeFlags(global, project); err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}
	return resolveConfigPath(resolveScope(global, project, cwd))
}

func init() {
	pinCmd.Flags().StringVar(&pinHost, "host", "",
		"Pin in [hosts.<host>.pinned] "+
			"(use 'current' for this machine)")
	pinCmd.Flags().BoolVarP(&pinGlobal, "global", "g",
		false, "Pin in the global config")
	pinCmd.Flags().BoolVarP(&pinProject, "project", "p",
		false, "Pin in the project config")
	unpinCmd.Flags().StringVar(&unpinHost, "host", "",
		"Unpin from [hosts.<host>.pinned] "+
			"(use 'current' for this machine)")
	unpinCmd.Flags().BoolVarP(&unpinGlobal, "global", "g",
		false, "Unpin in the global config")
	unpinCmd.Flags().BoolVarP(&unpinProject, "project", "p",
		false, "Unpin in the project config")
	rootCmd.AddCommand(pinCmd)
	rootCmd.AddCommand(unpinCmd)
}
