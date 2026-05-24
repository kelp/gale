package main

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/installer"
	"github.com/spf13/cobra"
)

var (
	switchRecipes string
	switchGlobal  bool
	switchProject bool
	switchBuild   bool
)

var switchCmd = &cobra.Command{
	Use:   "switch <pkg> <version>",
	Short: "Switch a managed package to a specific version",
	Long: `Switch a package that is already listed in gale.toml to a
specific version. Useful for moving back to a known-good
version after a bad release.

Accepts both the two-arg form (gale switch gh 2.89.0) and
the @version form (gale switch gh@2.89.0).

Unlike 'gale update', switch:
  - bypasses [pinned] (an explicit switch is the user's choice)
  - works for downgrades as well as upgrades
  - refuses to add packages that are not already in gale.toml
    (use 'gale install' for that)`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, version, err := parseSwitchArgs(args)
		if err != nil {
			return err
		}

		out := newCmdOutput(cmd)

		if err := validateInstallFlags(switchGlobal, switchProject); err != nil {
			return err
		}

		ctx, err := newCmdContext(switchRecipes, switchGlobal, switchProject)
		if err != nil {
			return err
		}

		cfg, err := ctx.LoadConfig()
		if err != nil {
			return err
		}

		current, ok := cfg.Packages[name]
		if !ok {
			return fmt.Errorf(
				"%s is not in gale.toml — use "+
					"'gale install %s@%s' to add it",
				name, name, version)
		}

		if current == version {
			out.Info(fmt.Sprintf(
				"%s is already at %s", name, version))
			return nil
		}

		r, err := ctx.ResolveVersionedRecipe(name, version)
		if err != nil {
			return fmt.Errorf("resolving %s@%s: %w",
				name, version, err)
		}

		if dryRun {
			out.Info(fmt.Sprintf("switch %s %s → %s",
				name, current, r.Package.Full()))
			return nil
		}

		out.Info(fmt.Sprintf("Switching %s %s → %s...",
			name, current, r.Package.Full()))

		if switchBuild {
			ctx.Installer.SourceOnly = true
		}

		result, err := ctx.Installer.InstallWithFinalize(r, false,
			func(res *installer.InstallResult) error {
				return ctx.FinalizeRecipeInstall(r, res.SHA256)
			})
		if err != nil {
			if errors.Is(err, build.ErrUnsupportedPlatform) {
				out.Warn(fmt.Sprintf(
					"%s does not support %s/%s",
					name, runtime.GOOS, runtime.GOARCH))
			}
			return fmt.Errorf("install failed: %w", err)
		}

		reportResult(out, result, "Switched", "built from source")
		return nil
	},
}

// parseSwitchArgs accepts either "<pkg> <version>" (two args)
// or "<pkg>@<version>" (one arg). Returns an error if no
// version is supplied.
func parseSwitchArgs(args []string) (name, version string, err error) {
	switch len(args) {
	case 2:
		return args[0], args[1], nil
	case 1:
		n, v, err := parsePackageArg(args[0])
		if err != nil {
			return "", "", err
		}
		if v == "" {
			return "", "", fmt.Errorf(
				"missing version: use 'gale switch %s <version>' "+
					"or 'gale switch %s@<version>'", n, n)
		}
		return n, v, nil
	default:
		return "", "", fmt.Errorf("expected 1 or 2 args, got %d", len(args))
	}
}

func init() {
	switchCmd.Flags().BoolVarP(&switchGlobal, "global", "g",
		false, "Switch in global config")
	switchCmd.Flags().BoolVarP(&switchProject, "project", "p",
		false, "Switch in project config")
	switchCmd.Flags().StringVar(&switchRecipes, "recipes", "",
		"Resolve recipes from a local directory instead of the registry "+
			"(bare --recipes uses ../gale-recipes/)")
	switchCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
	switchCmd.Flags().BoolVar(&switchBuild, "build", false,
		"Build from source (skip prebuilt binary)")
	rootCmd.AddCommand(switchCmd)
}
