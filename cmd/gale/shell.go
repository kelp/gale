package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

var shellProject string

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a shell with the project environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
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

// prependPATH returns the current environment with binDir
// prepended to PATH.
func prependPATH(binDir string) []string {
	result := os.Environ()
	currentPath := os.Getenv("PATH")
	result = append(result,
		fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
	return result
}
