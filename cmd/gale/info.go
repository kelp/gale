package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/store"
	"github.com/spf13/cobra"
)

var infoCmd = &cobra.Command{
	Use:   "info <package>",
	Short: "Show package information",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		// Check project config first.
		if projectPath, pErr := config.FindGaleConfig(cwd); pErr == nil {
			if found, err := printConfigInfo(
				name, projectPath, "project"); err != nil {
				return err
			} else if found {
				return nil
			}
		}

		// Check global config.
		globalDir, err := galeConfigDir()
		if err != nil {
			return err
		}
		globalPath := filepath.Join(globalDir, "gale.toml")
		if found, err := printConfigInfo(
			name, globalPath, "global"); err != nil {
			return err
		} else if found {
			return nil
		}

		// Not installed -- try registry.
		reg := newRegistry()
		r, err := reg.FetchRecipe(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		fmt.Printf("Name:    %s\n", r.Package.Name)
		fmt.Printf("Version: %s (latest)\n", r.Package.Version)
		if r.Package.Description != "" {
			fmt.Printf("About:   %s\n", r.Package.Description)
		}
		if r.Source.URL != "" {
			fmt.Printf("Source:  %s\n", r.Source.URL)
		}
		fmt.Println("(not installed)")

		return nil
	},
}

// printConfigInfo checks if name is in the config at
// configPath and prints its info. Returns true if the
// package was found.
func printConfigInfo(name, configPath, scope string) (bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
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

	version, ok := cfg.Packages[name]
	if !ok {
		return false, nil
	}

	storeRoot := defaultStoreRoot()
	s := store.NewStore(storeRoot)

	fmt.Printf("Name:    %s\n", name)
	fmt.Printf("Version: %s\n", version)
	if s.IsInstalled(name, version) {
		fmt.Printf("Store:   %s\n",
			filepath.Join(storeRoot, name, version))
	}
	fmt.Printf("Scope:   %s\n", scope)
	fmt.Printf("Config:  %s\n", configPath)

	pinned := "no"
	if cfg.Pinned[name] {
		pinned = "yes"
	}
	fmt.Printf("Pinned:  %s\n", pinned)

	return true, nil
}

func init() {
	rootCmd.AddCommand(infoCmd)
}
