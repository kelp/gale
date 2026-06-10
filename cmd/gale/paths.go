package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func galeConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home dir: %w", err)
	}
	return filepath.Join(home, ".gale"), nil
}

// galeDirForConfig returns the .gale directory that owns
// configPath. If configPath is inside the global dir
// (~/.gale/), returns ~/.gale/. Otherwise returns
// <project>/.gale/ next to the config file. This is the
// single source of truth for deriving the generation
// directory from a config path.
func galeDirForConfig(configPath string) (string, error) {
	globalDir, err := galeConfigDir()
	if err != nil {
		return "", err
	}
	if sameDir(filepath.Dir(configPath), globalDir) {
		return globalDir, nil
	}
	return filepath.Join(
		filepath.Dir(configPath), ".gale",
	), nil
}

// configInGaleDir reports whether configPath lives directly
// in galeDir. When cwd is anywhere under the global gale
// home, config.FindGaleConfig resolves to the GLOBAL
// gale.toml; callers that would treat such a path as a
// PROJECT config — deriving a <dir>/.gale next to it — must
// check this first or they invent the bogus <~/.gale>/.gale
// directory (gh#96). Doctor checks pass their resolved
// (injectable) gale home; other callers route through
// galeDirForConfig, which applies the same split.
func configInGaleDir(configPath, galeDir string) bool {
	return sameDir(filepath.Dir(configPath), galeDir)
}

// sameDir reports whether two paths name the same
// directory, resolving symlinks first so macOS /var vs
// /private/var spellings compare equal.
func sameDir(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return a == b
}

func defaultStoreRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("/", "gale", "pkg")
	}
	return filepath.Join(home, ".gale", "pkg")
}
