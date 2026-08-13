#!/usr/bin/env bash
# golangci-lint.sh — run golangci-lint, and say so plainly when the run is
# blocked by another golangci-lint rather than by your code.
#
# golangci-lint takes one exclusive lock per machine
# ($TMPDIR/golangci-lint.lock) for the whole run, so a second run exits 3 with
# `Error: parallel golangci-lint is running`. Two things make that common
# here: several worktrees running `just preflight` at once (the shape
# docs/dev/agent-environment.md already encourages), and `preflight` itself
# invoking golangci-lint twice — once for `lint`, once for `check-darwin` —
# so one agent can collide with its own still-draining run.
#
# Through `preflight` that surfaced as `FAILED at 'lint'`, which reads as
# "your change broke lint" when nothing was linted at all. This wrapper keeps
# the two apart (gh#237):
#
#   - your code has lint issues        -> golangci-lint's own output, exit 1
#   - another golangci-lint is running -> named as such, then queued behind it
#     with --allow-serial-runners (golangci-lint's own supported wait)
#   - still blocked past the deadline  -> exit 75 (EX_TEMPFAIL), which
#     `just preflight` renders as BLOCKED, not FAILED
#
# The uncontended path is a plain `golangci-lint run` with the same output and
# the same shared cache, so it costs nothing in the normal case. Per-invocation
# GOLANGCI_LINT_CACHE would dodge the lock instead, but a cold cache costs
# ~105s against ~1s warm on this repo — a bad trade for a rare collision.
#
# Usage: scripts/golangci-lint.sh [args passed to `golangci-lint run`]
#   GALE_LINT_LOCK_WAIT=<seconds>  how long to queue before giving up (600)

set -uo pipefail

lock_wait="${GALE_LINT_LOCK_WAIT:-600}"
lock_file="${TMPDIR:-/tmp}/golangci-lint.lock"
lock_msg='parallel golangci-lint is running'

log="$(mktemp "${TMPDIR:-/tmp}/gale-golangci-lint.XXXXXX")"
timed_out="$log.timeout"
cleanup() { rm -f "$log" "$timed_out"; }
trap cleanup EXIT INT TERM

# Other live golangci-lint processes, for the "who is holding it" line. The
# lock is an flock, so it dies with its holder — anything holding it is a
# running process. pgrep -l is portable across macOS and Linux; if it is
# missing or matches nothing, we simply do not name a holder.
holders() {
  pgrep -l golangci-lint 2>/dev/null | grep -v "^$$ " || true
}

# First attempt: exactly what the caller asked for, output streamed as usual.
golangci-lint run "$@" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}

if [ "$status" -eq 0 ] || ! grep -q "$lock_msg" "$log"; then
  exit "$status"
fi

held_by="$(holders)"
{
  echo
  echo "golangci-lint: BLOCKED by another golangci-lint run — this is NOT a lint failure."
  echo "  Nothing of yours was linted; no finding above belongs to your change."
  if [ -n "$held_by" ]; then
    echo "  Holding the lock ($lock_file):"
    echo "$held_by" | sed 's/^/    /'
  else
    echo "  Lock file: $lock_file (no golangci-lint process visible — the holder may have just exited)"
  fi
  echo "  Waiting for it to finish, then linting (up to ${lock_wait}s; GALE_LINT_LOCK_WAIT overrides)."
} >&2

# Second attempt: --allow-serial-runners waits for the lock instead of erroring
# out. Backgrounded so the wait has a deadline — an unbounded block is its own
# kind of unhelpful, especially for an agent running preflight unattended.
golangci-lint run --allow-serial-runners "$@" >"$log" 2>&1 &
pid=$!

# Watchdog: marks $timed_out before killing, so the outcome is unambiguous
# rather than inferred from a signal status.
(
  sleep "$lock_wait"
  : >"$timed_out"
  kill "$pid" 2>/dev/null
) &
watchdog=$!

wait "$pid"
status=$?
kill "$watchdog" 2>/dev/null
wait "$watchdog" 2>/dev/null

if [ -e "$timed_out" ]; then
  {
    echo
    echo "golangci-lint: still blocked after ${lock_wait}s by another golangci-lint run."
    echo "  Your code was never linted, so this says nothing about your change."
    held_by="$(holders)"
    if [ -n "$held_by" ]; then
      echo "  Still running:"
      echo "$held_by" | sed 's/^/    /'
    fi
    echo "  Re-run once it finishes, or clear a stray one with: pkill golangci-lint"
  } >&2
  exit 75
fi

cat "$log"
exit "$status"
