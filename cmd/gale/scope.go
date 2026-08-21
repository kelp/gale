package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errNoProject is the canonical message returned when
// --project is forced but no project gale.toml exists
// in the directory tree. One definition site so all
// commands agree on the wording.
const errNoProject = "no project found — run 'gale init' first"

// validateScopeFlags returns an error if both --global and
// --project are set. Used by all commands that accept scope
// overrides.
func validateScopeFlags(global, project bool) error {
	if global && project {
		return fmt.Errorf(
			"cannot use both --global and --project",
		)
	}
	return nil
}

// resolveScopedPaths returns the active .gale dir and the
// gale.toml path for the given scope flags. The gale dir is
// returned even when no gale.toml exists on disk — commands
// like which and generations operate on generation symlinks,
// not the config file itself.
//
// Uses galeDirForConfig for every gale-dir derivation so the
// gh#96 guard (cwd inside ~/.gale/) is always applied. Uses
// projectConfigPath so the gh#96 guard is always applied.
func resolveScopedPaths(
	global, project bool,
) (galeDir, configPath string, err error) {
	if err = validateScopeFlags(global, project); err != nil {
		return "", "", err
	}

	if global {
		galeDir, err = galeConfigDir()
		if err != nil {
			return "", "", err
		}
		configPath, err = globalConfigPath()
		return galeDir, configPath, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("getting working dir: %w", err)
	}

	if project {
		projPath, pErr := projectConfigPath(cwd)
		if pErr != nil {
			return "", "", errors.New(errNoProject)
		}
		galeDir, err = galeDirForConfig(projPath)
		if err != nil {
			return "", "", err
		}
		return galeDir, projPath, nil
	}

	// Auto: project preferred when it exists.
	if projPath, pErr := projectConfigPath(cwd); pErr == nil {
		galeDir, err = galeDirForConfig(projPath)
		if err != nil {
			return "", "", err
		}
		return galeDir, projPath, nil
	}

	// Fall back to global.
	galeDir, err = galeConfigDir()
	if err != nil {
		return "", "", err
	}
	configPath, err = globalConfigPath()
	return galeDir, configPath, err
}

// resolveReadOnlyConfigPath returns the gale.toml path for
// a read-only command. Routes through resolveScopedPaths so
// the gh#96 guard applies uniformly.
func resolveReadOnlyConfigPath(global, project bool) (string, error) {
	_, configPath, err := resolveScopedPaths(global, project)
	return configPath, err
}

// globalConfigPath returns ~/.gale/gale.toml.
func globalConfigPath() (string, error) {
	globalDir, err := galeConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(globalDir, "gale.toml"), nil
}
