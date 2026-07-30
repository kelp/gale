package env

// gateCommand is the activation gate's invocation, named here so the
// hook and the tests that pin its placement cannot disagree about
// which line is the gate.
const gateCommand = "gale env --check"

// DirenvHook returns the direnv integration script.
// Add to ~/.config/direnv/direnvrc:
//
//	eval "$(gale hook direnv)"
func DirenvHook() string {
	return `# Gale integration for direnv.
# Add to ~/.config/direnv/direnvrc:
#   eval "$(gale hook direnv)"

use_gale() {
  local gale_dir="$(pwd)/.gale"
  local manifest="$(pwd)/gale.toml"
  local lockfile="$(pwd)/gale.lock"

  # Re-run this .envrc when either input changes. The lock is
  # watched as well as the manifest because the lock is what
  # activation is checked against: an edited or replaced lock must
  # re-trigger activation instead of waiting for something to
  # touch gale.toml.
  watch_file "$manifest"
  watch_file "$lockfile"

  # Sync only when the manifest is newer than the current
  # generation symlink (or no generation exists yet). The
  # symlink is swapped atomically at the end of every
  # successful gale sync, so its mtime is the source of truth.
  #
  # stderr is not discarded. A sync that fails an integrity check
  # has to say which artifact disagreed with the lock, and that
  # message is the only thing the user can act on. The ` + "`|| true`" + `
  # keeps an ordinary failure (offline, a broken build) from
  # aborting the shell; the gate below still decides whether
  # anything reaches PATH.
  if [ ! -L "$gale_dir/current" ] || [ "$manifest" -nt "$gale_dir/current" ]; then
    gale sync || true
  fi

  # Activation gate, run on every activation. The mtime guard
  # above cannot see a gale upgrade: upgrading the binary modifies
  # no file in the project, so a lock this build refuses to honor
  # would reach PATH unexamined. The gate reads the lock and store
  # provenance only — no hashing, no network — so it is cheap
  # enough for every cd.
  #
  # Refusal happens before PATH_add: the project's binaries stay
  # off PATH and the system PATH is untouched. Falling back to the
  # previous generation is deliberately not offered, because at an
  # upgrade boundary that generation was never verified either.
  ` + gateCommand + ` || return 1

  # Add the project's current/bin to PATH.
  if [ -d "$gale_dir/current/bin" ]; then
    PATH_add "$gale_dir/current/bin"
  fi

  # Export project variables from [vars] in gale.toml.
  # Errors (e.g. malformed [vars]) surface to the user and
  # fail direnv activation rather than silently exporting
  # nothing.
  eval "$(gale env --vars-only)"
}
`
}
