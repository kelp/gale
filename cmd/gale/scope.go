package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// validateScopeFlags returns an error if both --global and
// --project are set. Used by read-only commands that accept
// scope overrides. Mutation commands use the equivalent
// validateInstallFlags.
func validateScopeFlags(global, project bool) error {
	if global && project {
		return fmt.Errorf(
			"cannot use both --global and --project",
		)
	}
	return nil
}

// resolveReadOnlyConfigPath returns the gale.toml path for
// a read-only command. When --project is forced but no
// project gale.toml exists in the directory tree, returns
// an error. When --global is forced, returns the global
// path. When neither flag is set, prefers the project
// config if one exists, falling back to global.
//
// Unlike resolveConfigPath (used by mutation commands),
// the project path is only returned when the file actually
// exists — read-only commands have nothing meaningful to
// show for a non-existent project config.
func resolveReadOnlyConfigPath(global, project bool) (string, error) {
	if global && project {
		return "", fmt.Errorf(
			"cannot use both --global and --project",
		)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working dir: %w", err)
	}
	if global {
		return globalConfigPath()
	}
	if project {
		projectPath, err := projectConfigPath(cwd)
		if err != nil {
			return "", fmt.Errorf(
				"no project found — run 'gale init' first",
			)
		}
		return projectPath, nil
	}
	// Auto-detect: project first, then global.
	if projectPath, err := projectConfigPath(cwd); err == nil {
		return projectPath, nil
	}
	return globalConfigPath()
}

// globalConfigPath returns ~/.gale/gale.toml.
func globalConfigPath() (string, error) {
	globalDir, err := galeConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(globalDir, "gale.toml"), nil
}
