package env

import "errors"

// ErrUnsupportedShell is returned when GenerateHook receives
// an unknown integration name.
var ErrUnsupportedShell = errors.New("unsupported shell")

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
  local gale_dir="$(pwd)/.gale"
  local manifest="$(pwd)/gale.toml"

  # Re-run this .envrc when the manifest changes.
  watch_file "$manifest"

  # Sync only when the manifest is newer than the current
  # generation symlink (or no generation exists yet). The
  # symlink is swapped atomically at the end of every
  # successful gale sync, so its mtime is the source of truth.
  if [ ! -L "$gale_dir/current" ] || [ "$manifest" -nt "$gale_dir/current" ]; then
    gale sync 2>/dev/null || true
  fi

  # Add the project's current/bin to PATH.
  if [ -d "$gale_dir/current/bin" ]; then
    PATH_add "$gale_dir/current/bin"
  fi

  # Export project variables from [vars] in gale.toml.
  eval "$(gale env --vars-only 2>/dev/null)" || true
}
`
}
