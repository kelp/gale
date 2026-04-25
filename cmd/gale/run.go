package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run <command> [-- args...]",
	Short: "Run a command in the project environment",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		syncIfNeeded(os.Stderr, "")

		galeDir, err := resolveGaleDir()
		if err != nil {
			return err
		}

		binDir := filepath.Join(galeDir, "current", "bin")

		// Resolve args[0] against the project's binDir
		// first, then fall back to the ambient PATH. Go's
		// exec.Command resolves the name before applying
		// c.Env, so without this the project env only
		// works when the caller already has binDir on PATH
		// (typically via the direnv hook).
		resolved := args[0]
		if cand := filepath.Join(binDir, args[0]); fileExecutable(cand) {
			resolved = cand
		} else if p, err := exec.LookPath(args[0]); err == nil {
			resolved = p
		}

		c := exec.Command(resolved, args[1:]...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Env = prependPATH(binDir)

		return c.Run()
	},
}

func fileExecutable(path string) bool {
	// G703 false positive — `gale run <name>` is contractually
	// user-controlled. We just want to know whether
	// <binDir>/<name> is an executable regular file; a
	// traversal name like "../etc/passwd" yields a Stat miss
	// or non-executable mode and we fall through to LookPath.
	info, err := os.Stat(path) //nolint:gosec
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func init() {
	rootCmd.AddCommand(runCmd)
}
