#!/usr/bin/env bash
# check-pipeline-tests-selftest.sh — exercise check-pipeline-tests.sh against
# throwaway git repos.
#
# The guard decides whether a change is allowed to land, so its own logic wants
# a regression test: gh#237 widened it to see untracked files, and the risk of
# that widening is laundering the rule it exists to enforce. Cases 1 and 3
# below are the ones that must keep failing.
#
# Usage: scripts/check-pipeline-tests-selftest.sh   (no args, no network)

set -uo pipefail

guard="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-pipeline-tests.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/gale-pipeline-selftest.XXXXXX")"
trap 'rm -rf "$work"' EXIT INT TERM

failures=0

# fixture <name> — a repo on branch `work`, one commit past `main`, holding a
# realistic slice of the tree the guard inspects.
fixture() {
  local dir="$work/$1"
  mkdir -p "$dir"
  (
    cd "$dir" || exit 1
    git init -q .
    git config user.email selftest@example.com
    git config user.name selftest
    git config commit.gpgsign false
    mkdir -p cmd/gale internal/generation integration
    echo "package main" >cmd/gale/context.go
    echo "package main" >cmd/gale/existing_test.go
    echo "package generation" >internal/generation/gen_test.go
    git add -A
    git commit -qm baseline
    git branch -M main
    git checkout -qb work
  ) || exit 1
  echo "$dir"
}

# expect <exit> <name> <description>
expect() {
  local want="$1" dir="$work/$2" desc="$3" out got
  out="$(cd "$dir" && "$guard" main 2>&1)"
  got=$?
  if [ "$got" -eq "$want" ]; then
    printf 'ok   %-44s (exit %d)\n' "$desc" "$got"
  else
    printf 'FAIL %-44s (exit %d, want %d)\n' "$desc" "$got" "$want"
    echo "$out" | sed 's/^/       /'
    failures=$((failures + 1))
  fi
}

# 1. The rule itself: sensitive production change whose only tests are under
#    internal/. Must fail — this is what the guard exists for.
d="$(fixture only-internal)"
(
  cd "$d" || exit 1
  echo "// changed" >>cmd/gale/context.go
  echo "// changed" >>internal/generation/gen_test.go
  git commit -qam change
)
expect 1 only-internal "internal/-only tests still fail"

# 2. gh#237: the new cmd/gale test exists but is untracked, which is exactly
#    what writing the failing test first produces. Must pass.
d="$(fixture untracked-cmd-test)"
(
  cd "$d" || exit 1
  echo "// changed" >>cmd/gale/context.go
  echo "// changed" >>internal/generation/gen_test.go
  git commit -qam change
  echo "package main" >cmd/gale/new_test.go
)
expect 0 untracked-cmd-test "untracked cmd/gale test counts"

# 3. Untracked files must not launder the rule: an untracked internal/ test is
#    still an internal/-only test. Must fail.
d="$(fixture untracked-internal-test)"
(
  cd "$d" || exit 1
  echo "// changed" >>cmd/gale/context.go
  git commit -qam change
  echo "package generation" >internal/generation/new_test.go
)
expect 1 untracked-internal-test "untracked internal/ test still fails"

# 4. An untracked integration/ file satisfies the guard like a committed one.
d="$(fixture untracked-integration)"
(
  cd "$d" || exit 1
  echo "// changed" >>cmd/gale/context.go
  echo "// changed" >>internal/generation/gen_test.go
  git commit -qam change
  echo "package integration" >integration/new_test.go
)
expect 0 untracked-integration "untracked integration/ test counts"

# 5. Refactor allowance: sensitive code changed, no test touched anywhere.
d="$(fixture no-test-changes)"
(
  cd "$d" || exit 1
  echo "// changed" >>cmd/gale/context.go
  git commit -qam change
)
expect 0 no-test-changes "no test changes stays allowed"

# 6. Nothing sensitive changed.
d="$(fixture no-sensitive-change)"
(
  cd "$d" || exit 1
  echo "// changed" >>internal/generation/gen_test.go
  git commit -qam change
)
expect 0 no-sensitive-change "non-sensitive change stays allowed"

if [ "$failures" -ne 0 ]; then
  echo "check-pipeline-tests-selftest: $failures case(s) failed" >&2
  exit 1
fi
echo "check-pipeline-tests-selftest: all cases passed"
