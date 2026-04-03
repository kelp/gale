package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/lockfile"
	"github.com/spf13/cobra"
)

var shellProject string

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a shell with the project environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		syncIfNeeded()

		var galeDir string
		var err error
		if shellProject != "" {
			// Explicit project dir — use its .gale/.
			galeDir = filepath.Join(shellProject, ".gale")
		} else {
			galeDir, err = resolveGaleDir()
			if err != nil {
				return err
			}
		}

		binDir := filepath.Join(galeDir, "current", "bin")

		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}

		c := exec.Command(shell)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Env = prependPATH(binDir)

		return c.Run()
	},
}

func init() {
	shellCmd.Flags().StringVar(&shellProject, "project", "",
		"Path to project directory")
	rootCmd.AddCommand(shellCmd)
}

// syncIfNeeded runs gale sync when the lockfile is stale
// relative to gale.toml. Returns silently if no project
// config exists or if already up to date.
func syncIfNeeded() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	configPath, err := config.FindGaleConfig(cwd)
	if err != nil {
		return
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		return
	}
	lp := lockfilePath(configPath)
	stale, err := lockfile.IsStale(configPath, lp, cfg.Packages)
	if err != nil || !stale {
		return
	}
	_ = runSync("", false, false)
}

// prependPATH returns the current environment with binDir
// prepended to PATH.
func prependPATH(binDir string) []string {
	result := os.Environ()
	currentPath := os.Getenv("PATH")
	result = append(result,
		fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
	return result
}
