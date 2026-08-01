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
	return resolveDeepest(a) == resolveDeepest(b)
}

// resolveDeepest resolves the longest existing prefix of path and
// re-joins whatever remains.
//
// EvalSymlinks fails outright on a path that does not exist, and a
// plain fallback to the raw string is not good enough: on macOS the
// two spellings of a not-yet-created directory then compare unequal
// even though they name the same place. That matters for the paths
// this is asked about — a scope's .gale directory does not exist until
// its first sync, and design §13's migration veto exempts the
// initiating scope by comparing exactly that path, so a false negative
// makes the scope veto itself.
func resolveDeepest(path string) string {
	var tail []string
	cur := path
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the root without finding anything that exists;
			// the raw spelling is the best answer available.
			return path
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}

func defaultStoreRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("/", "gale", "pkg")
	}
	return filepath.Join(home, ".gale", "pkg")
}

// genLockPath is the store-gen lock every generation rebuild, store
// commit and guarded removal contends on. One spelling, because two
// callers naming different files would each think they held it.
// filepath.Dir(storeRoot) is the global gale dir at either scope,
// since the store is shared.
func genLockPath(storeRoot string) string {
	return filepath.Join(filepath.Dir(storeRoot), "generation.lock")
}
