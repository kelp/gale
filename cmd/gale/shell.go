package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/config"
	"github.com/spf13/cobra"
)

var shellProject string

var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a shell with the project environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		projectDir := shellProject
		if projectDir != "" {
			projectDir = resolveProjectRoot(projectDir)
		}

		syncIfNeeded(os.Stderr, projectDir)

		var galeDir, configPath string
		var err error
		if projectDir != "" {
			// Resolved project root — use its .gale/.
			galeDir = filepath.Join(projectDir, ".gale")
			configPath = filepath.Join(projectDir, "gale.toml")
		} else if galeDir, configPath, err = resolveScopedPaths(
			false, false,
		); err != nil {
			return err
		}
		// Fatal, not a warning. `gale shell` puts the project's
		// binaries on PATH and then hands the user an interactive
		// shell; warning and proceeding would leave them executing
		// artifacts that disagree with the lock for as long as the
		// shell lives.
		if err := gateActivation(galeDir, configPath); err != nil {
			return err
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
// config exists or if already up to date. When projectDir
// is non-empty it is used instead of os.Getwd() to locate
// gale.toml. Warnings are written to w.
func syncIfNeeded(w io.Writer, projectDir string) {
	out := newOutputForWriter(w)

	dir := projectDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			out.Warn(fmt.Sprintf(
				"sync: getting working dir: %v", err,
			))
			return
		}
	}
	configPath, err := config.FindGaleConfig(dir)
	if err != nil {
		return // no project config — nothing to sync
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		out.Warn(fmt.Sprintf(
			"sync: reading config: %v", err,
		))
		return
	}
	cfg, err := config.ParseGaleConfig(string(data))
	if err != nil {
		out.Warn(fmt.Sprintf(
			"sync: parsing config: %v", err,
		))
		return
	}
	cfg.ApplyHost(config.CurrentHost())
	lp, lpErr := lockfilePath(configPath)
	if lpErr != nil {
		out.Warn(fmt.Sprintf(
			"sync: lockfile path: %v", lpErr,
		))
		return
	}
	host := config.CurrentHost()
	stale, err := lockIsStale(lp, cfg.Packages, host)
	if err != nil {
		out.Warn(fmt.Sprintf(
			"sync: checking lockfile: %v", err,
		))
		return
	}
	// Pass the discovered project root (not the raw
	// input) so runSync targets the correct directory.
	projectRoot := filepath.Dir(configPath)
	// A fresh lock is not a synced project (gh#186). A partial install
	// failure leaves the lock describing the manifest exactly, so
	// lockIsStale reports fresh while a package is missing from PATH.
	// The completion stamp is what knows the difference.
	if !stale && !syncStateWantsWork(
		out.Warn, filepath.Join(projectRoot, ".gale"), configPath, host,
	) {
		return
	}
	if err := runSync("", false, false, false, projectRoot); err != nil {
		out.Warn(fmt.Sprintf("sync failed: %v", err))
	}
}

// prependPATH returns the current environment with binDir
// prepended to PATH. It replaces the existing PATH entry
// rather than appending a duplicate.
func prependPATH(binDir string) []string {
	environ := os.Environ()
	result := make([]string, 0, len(environ))
	for _, entry := range environ {
		if strings.HasPrefix(entry, "PATH=") {
			result = append(result,
				fmt.Sprintf("PATH=%s:%s",
					binDir, entry[len("PATH="):]))
		} else {
			result = append(result, entry)
		}
	}
	return result
}

// resolveProjectRoot walks up from dir to find the
// canonical project root. Checks for gale.toml first,
// then .tool-versions. Returns the directory containing
// whichever is found, or dir as-is if neither exists
// (the project may already have .gale/ from a prior sync).
func resolveProjectRoot(dir string) string {
	if cp, err := config.FindGaleConfig(dir); err == nil {
		return filepath.Dir(cp)
	}
	if tv := config.FindToolVersions(dir); tv != "" {
		return filepath.Dir(tv)
	}
	return dir
}
