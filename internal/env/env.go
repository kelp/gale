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
