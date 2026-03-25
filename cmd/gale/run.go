package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/env"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run <command> [-- args...]",
	Short: "Run a command in the project environment",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working dir: %w", err)
		}

		galePath, err := config.FindGaleConfig(cwd)
		if err != nil {
			return fmt.Errorf("no gale.toml found: %w", err)
		}

		data, err := os.ReadFile(galePath)
		if err != nil {
			return fmt.Errorf("reading config: %w", err)
		}

		cfg, err := config.ParseGaleConfig(string(data))
		if err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}

		storeRoot := defaultStoreRoot()
		environ := env.BuildEnvironment(storeRoot, nil, cfg.Packages, cfg.Vars)

		c := exec.Command(args[0], args[1:]...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Env = buildShellEnv(environ)

		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
