package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/index"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var (
	infoIndex   string
	infoGlobal  bool
	infoProject bool
)

var infoCmd = &cobra.Command{
	Use:   "info <package>[@version]",
	Short: "Show package information",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateScopeFlags(infoGlobal, infoProject); err != nil {
			return err
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runInfo(ctx, cmd.OutOrStdout(), indexSource(infoIndex), args[0])
	},
}

// runInfo prints package metadata for arg (which may be
// "<name>" or "<name>@<version>") to w. The version form
// resolves through the index. The bare form checks
// project/global config first, then the index.
func runInfo(
	ctx context.Context, w io.Writer, src index.Source, arg string,
) error {
	name, version, err := parsePackageArg(arg)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working dir: %w", err)
	}

	if version == "" && (infoGlobal || infoProject) {
		configPath, err := resolveReadOnlyConfigPath(
			infoGlobal, infoProject,
		)
		if err != nil {
			return err
		}
		scope := "project"
		if infoGlobal {
			scope = "global"
		}
		found, err := printConfigInfo(w, name, configPath, scope)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%s not found in %s gale.toml",
				name, scope)
		}
		return nil
	}

	if version == "" {
		found, err := findInstalledInfo(w, name, cwd)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
	}

	return fetchAndPrintIndexInfo(ctx, w, src, name, version)
}

// findInstalledInfo searches the project config (if present) then
// the global config for name, printing details to w on the first
// match. Returns true when the package was found and printed.
func findInstalledInfo(w io.Writer, name, cwd string) (bool, error) {
	if projectPath, pErr := config.FindGaleConfig(cwd); pErr == nil {
		if found, err := printConfigInfo(w, name, projectPath, "project"); err != nil {
			return false, err
		} else if found {
			return true, nil
		}
	}

	globalDir, err := galeConfigDir()
	if err != nil {
		return false, err
	}
	globalPath := filepath.Join(globalDir, "gale.toml")
	return printConfigInfo(w, name, globalPath, "global")
}

// fetchAndPrintIndexInfo loads one index document and prints it.
func fetchAndPrintIndexInfo(
	ctx context.Context, w io.Writer, src index.Source, name, version string,
) error {
	sess, err := index.Open(ctx, src)
	if err != nil {
		return fmt.Errorf("opening index: %w", err)
	}
	f, err := sess.Get(ctx, name)
	if err != nil {
		if version != "" {
			return fmt.Errorf("%s@%s: %w", name, version, err)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	if version == "" {
		version = f.Package.Latest
	}
	ver, ok := f.Versions[version]
	if !ok {
		return fmt.Errorf("index %s: version %s not found", name, version)
	}

	fmt.Fprintf(w, "Name:    %s\n", f.Package.Name)
	fmt.Fprintf(w, "Version: %s\n", version)
	if version == f.Package.Latest {
		fmt.Fprintf(w, "Latest:  %s\n", f.Package.Latest)
	}
	if f.Package.Description != "" {
		fmt.Fprintf(w, "About:   %s\n", f.Package.Description)
	}
	if f.Package.Homepage != "" {
		fmt.Fprintf(w, "Home:    %s\n", f.Package.Homepage)
	}
	if f.Package.Repo != "" {
		fmt.Fprintf(w, "Repo:    %s\n", f.Package.Repo)
	}
	if art, ok := ver.Artifacts[currentPlatform()]; ok && art.URL != "" {
		fmt.Fprintf(w, "URL:     %s\n", art.URL)
	}
	fmt.Fprintln(w, "(not installed)")
	return nil
}

// printConfigInfo checks if name is in the config at
// configPath and prints its info to w. Returns true if the
// package was found.
func printConfigInfo(w io.Writer, name, configPath, scope string) (bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w",
			configPath, err)
	}

	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return false, fmt.Errorf("parsing %s: %w",
			configPath, err)
	}
	host, err := config.CurrentHost()
	if err != nil {
		return false, err
	}
	cfg.ApplyHost(host)

	version, ok := cfg.Packages[name]
	if !ok {
		return false, nil
	}

	storeRoot := defaultStoreRoot()
	s := store.NewStore(storeRoot)

	fmt.Fprintf(w, "Name:    %s\n", name)
	fmt.Fprintf(w, "Version: %s\n", version)
	if s.IsInstalled(name, version) {
		fmt.Fprintf(w, "Store:   %s\n",
			filepath.Join(storeRoot, name, version))
	}
	fmt.Fprintf(w, "Scope:   %s\n", scope)
	fmt.Fprintf(w, "Config:  %s\n", configPath)

	return true, nil
}

func init() {
	infoCmd.Flags().StringVar(&infoIndex, "index", "",
		"Resolve against a local index checkout")
	infoCmd.Flags().BoolVarP(&infoGlobal, "global", "g", false,
		"Look up the package in the global gale.toml")
	infoCmd.Flags().BoolVarP(&infoProject, "project", "p", false,
		"Look up the package in the project gale.toml")
	rootCmd.AddCommand(infoCmd)
}
