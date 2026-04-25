package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/repo"
	"github.com/spf13/cobra"
)

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Manage recipe repositories",
}

var repoAddCmd = &cobra.Command{
	Use:   "add <name> <url>",
	Short: "Add a recipe repository",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, url := args[0], args[1]
		out := newCmdOutput(cmd)

		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}

		cacheRoot := filepath.Join(galeDir, "repos")
		mgr := repo.NewManager(cacheRoot)
		mgr.AddRepo(repo.RepoConfig{
			Name: name,
			URL:  url,
		})

		if err := mgr.Clone(name); err != nil {
			return fmt.Errorf("cloning repo: %w", err)
		}

		configPath := filepath.Join(galeDir, "config.toml")
		if err := addRepoToConfig(configPath, name, url); err != nil {
			return fmt.Errorf("persisting repo config: %w", err)
		}

		out.Success(fmt.Sprintf("Added repo %s from %s", name, url))
		return nil
	},
}

var repoRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a recipe repository",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		out := newCmdOutput(cmd)

		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}

		configPath := filepath.Join(galeDir, "config.toml")
		if err := removeRepoFromConfig(configPath, name); err != nil {
			return fmt.Errorf("updating config: %w", err)
		}

		cacheDir := filepath.Join(galeDir, "repos", name)
		if err := os.RemoveAll(cacheDir); err != nil {
			return fmt.Errorf("removing repo cache: %w", err)
		}

		out.Success(fmt.Sprintf("Removed repo %s", name))
		return nil
	},
}

var repoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recipe repositories",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		galeDir, err := galeConfigDir()
		if err != nil {
			return err
		}

		configPath := filepath.Join(galeDir, "config.toml")
		data, err := os.ReadFile(configPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Println("No repositories configured.")
				return nil
			}
			return fmt.Errorf("reading config: %w", err)
		}

		cfg, err := config.ParseAppConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}

		if len(cfg.Repos) == 0 {
			fmt.Println("No repositories configured.")
			return nil
		}

		// Configured repos are now consulted by the install
		// resolver in priority order (lowest number wins),
		// before the default registry. Show the order so the
		// user can see what install/sync will see first.
		sorted := make([]config.Repo, len(cfg.Repos))
		copy(sorted, cfg.Repos)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].Priority < sorted[j].Priority
		})
		for _, r := range sorted {
			fmt.Printf("%s (priority %d) %s\n", r.Name, r.Priority, r.URL)
		}
		return nil
	},
}

var repoInitCmd = &cobra.Command{
	Use:   "init <name>",
	Short: "Create a new recipe repository",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		out := newCmdOutput(cmd)

		if err := os.MkdirAll(name, 0o755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}

		recipesDir := filepath.Join(name, "recipes")
		if err := os.MkdirAll(recipesDir, 0o755); err != nil {
			return fmt.Errorf("creating recipes dir: %w", err)
		}

		out.Success(fmt.Sprintf("Initialized repo %s", name))
		return nil
	},
}

// addRepoToConfig persists a repo entry to config.toml.
func addRepoToConfig(configPath, name, url string) error {
	return config.AddRepo(configPath, config.Repo{
		Name: name,
		URL:  url,
	})
}

// removeRepoFromConfig removes a repo entry from config.toml.
func removeRepoFromConfig(configPath, name string) error {
	return config.RemoveRepo(configPath, name)
}

func init() {
	repoCmd.AddCommand(repoAddCmd)
	repoCmd.AddCommand(repoRemoveCmd)
	repoCmd.AddCommand(repoListCmd)
	repoCmd.AddCommand(repoInitCmd)
	rootCmd.AddCommand(repoCmd)
}
