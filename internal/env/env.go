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

  # Freshness is gale's decision, not the shell's. This used to
  # compare the manifest's mtime against the current generation
  # symlink, but a partial sync rebuilds that generation on purpose
  # (issue #20) and the swap gives it a now-mtime, so from the next
  # activation on the comparison was false forever and the failed
  # packages were never retried (gh#186). Only gale can tell a sync
  # that finished from one that gave up, so --if-needed reads the
  # completion stamp it wrote and rate-limits the retry itself.
  #
  # stderr is not discarded. A sync that fails an integrity check
  # has to say which artifact disagreed with the lock, and that
  # message is the only thing the user can act on. The ` + "`|| true`" + `
  # keeps an ordinary failure (offline, a broken build) from
  # aborting the shell; the gate below still decides whether
  # anything reaches PATH.
  gale sync --if-needed || true

  # Activation gate, run on every activation. The freshness check
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
