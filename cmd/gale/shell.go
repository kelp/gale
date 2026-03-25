package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/env"
	"github.com/spf13/cobra"
)

var shellProject string

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Launch a subshell with the project environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		projectDir := shellProject
		if projectDir == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working dir: %w", err)
			}
			projectDir = cwd
		}

		galePath, err := config.FindGaleConfig(projectDir)
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

		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}

		c := exec.Command(shell)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Env = buildShellEnv(environ)

		return c.Run()
	},
}

func init() {
	shellCmd.Flags().StringVar(&shellProject, "project", "",
		"Path to project directory")
	rootCmd.AddCommand(shellCmd)
}

func defaultStoreRoot() string {
	return filepath.Join("/", "gale", "packages")
}

func buildShellEnv(environ *env.Environment) []string {
	result := os.Environ()
	if environ.PATH != "" {
		currentPath := os.Getenv("PATH")
		result = append(result,
			fmt.Sprintf("PATH=%s:%s", environ.PATH, currentPath))
	}
	for k, v := range environ.Vars {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}
