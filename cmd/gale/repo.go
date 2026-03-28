package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
	"github.com/kelp/gale/internal/repo"
	"github.com/kelp/gale/internal/trust"
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
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

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
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		galeDir, err := galeConfigDir()
		if err != nil {
			return err
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
			if os.IsNotExist(err) {
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

		for _, r := range cfg.Repos {
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
		out := output.New(os.Stderr, !cmd.Flags().Changed("no-color"))

		if err := os.MkdirAll(name, 0o755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}

		recipesDir := filepath.Join(name, "recipes")
		if err := os.MkdirAll(recipesDir, 0o755); err != nil {
			return fmt.Errorf("creating recipes dir: %w", err)
		}

		kp, err := trust.GenerateKeyPair()
		if err != nil {
			return fmt.Errorf("generating keypair: %w", err)
		}

		keyFile := filepath.Join(name, "keys.json")
		keyData, err := json.MarshalIndent(kp, "", "  ") //nolint:gosec // G117 — ed25519 signing key struct, not a hardcoded credential
		if err != nil {
			return fmt.Errorf("encoding keys: %w", err)
		}
		if err := os.WriteFile(keyFile, keyData, 0o600); err != nil {
			return fmt.Errorf("writing keys: %w", err)
		}

		out.Success(fmt.Sprintf("Initialized repo %s", name))
		out.Info(fmt.Sprintf("Public key: %s", kp.PublicKey))
		out.Warn("Keep keys.json private — do not commit it")
		return nil
	},
}

func init() {
	repoCmd.AddCommand(repoAddCmd)
	repoCmd.AddCommand(repoRemoveCmd)
	repoCmd.AddCommand(repoListCmd)
	repoCmd.AddCommand(repoInitCmd)
	rootCmd.AddCommand(repoCmd)
}

func galeConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home dir: %w", err)
	}
	return filepath.Join(home, ".gale"), nil
}
