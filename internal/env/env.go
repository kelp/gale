package env

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelp/gale/internal/config"
)

// ErrUnsupportedShell is returned when GenerateHook receives
// an unknown shell name.
var ErrUnsupportedShell = errors.New("unsupported shell")

// Environment represents a resolved environment.
type Environment struct {
	PATH string
	Vars map[string]string
}

// BuildPATH creates a PATH string from packages in the store.
func BuildPATH(storeRoot string, packages map[string]string) string {
	if len(packages) == 0 {
		return ""
	}

	// Sort names for deterministic output.
	names := make([]string, 0, len(packages))
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]string, 0, len(names))
	for _, name := range names {
		entries = append(entries,
			filepath.Join(storeRoot, name, packages[name], "bin"))
	}
	return strings.Join(entries, string(os.PathListSeparator))
}

// MergePackages merges global and project packages.
// Project entries take priority over global.
func MergePackages(global, project map[string]string) map[string]string {
	merged := make(map[string]string)
	for k, v := range global {
		merged[k] = v
	}
	for k, v := range project {
		merged[k] = v
	}
	return merged
}

// BuildEnvironment creates an Environment from packages and vars.
func BuildEnvironment(storeRoot string, global, project map[string]string, vars map[string]string) *Environment {
	merged := MergePackages(global, project)
	env := &Environment{
		PATH: BuildPATH(storeRoot, merged),
		Vars: make(map[string]string),
	}
	for k, v := range vars {
		env.Vars[k] = v
	}
	return env
}

// GenerateHook generates a hook script for the given
// integration. Currently only "direnv" is supported.
func GenerateHook(shell string) (string, error) {
	switch shell {
	case "direnv":
		return generateDirenvHook(), nil
	default:
		return "", ErrUnsupportedShell
	}
}

func generateDirenvHook() string {
	return `# Gale integration for direnv.
# Add to ~/.config/direnv/direnvrc:
#   eval "$(gale hook direnv)"

use_gale() {
  local gale_dir
  gale_dir="$(pwd)/.gale"

  # Sync project packages quietly.
  gale sync 2>/dev/null || true

  # Add the project's current/bin to PATH.
  if [ -d "$gale_dir/current/bin" ]; then
    PATH_add "$gale_dir/current/bin"
  fi
}
`
}

// DetectConfig checks if dir or any parent contains a gale.toml file.
func DetectConfig(dir string) (string, error) {
	return config.FindGaleConfig(dir)
}
