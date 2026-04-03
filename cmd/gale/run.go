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

		c := exec.Command(args[0], args[1:]...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Env = prependPATH(binDir)

		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
