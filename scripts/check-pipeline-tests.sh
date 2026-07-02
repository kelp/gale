#!/usr/bin/env bash
# check-pipeline-tests.sh — CI guard for change-discipline test layers.
#
# When pipeline-sensitive production code changes AND this diff adds or
# modifies tests, require at least one cmd/gale *_test.go or integration/
# change. An internal/*_test.go alone is insufficient for tier-3 pipelines.
#
# Refactors that touch sensitive paths without changing tests are allowed
# (existing coverage must still pass).
#
# Usage:
#   scripts/check-pipeline-tests.sh              # diff vs origin/main
#   scripts/check-pipeline-tests.sh origin/main  # explicit base ref

set -euo pipefail

base="${1:-origin/main}"

if ! git rev-parse --verify "$base" >/dev/null 2>&1; then
  echo "check-pipeline-tests: base ref $base not found, skipping"
  exit 0
fi

merge_base="$(git merge-base HEAD "$base")"

diff_files() {
  git diff --name-only "$merge_base" -- "$@"
}

# Pipeline-sensitive production paths (tier 3). Match non-test .go files.
sensitive_prod="$(
  diff_files \
    cmd/gale/context.go \
    cmd/gale/sync.go \
    cmd/gale/gc.go \
    cmd/gale/generations.go \
    internal/generation \
    internal/farm \
  | grep -E '\.go$' | grep -v '_test\.go$' || true
)"

if [ -z "$sensitive_prod" ]; then
  exit 0
fi

# Any test file added or modified in this diff.
test_files="$(
  diff_files cmd/gale internal integration \
  | grep -E '(^cmd/gale/.*_test\.go$|^internal/.*_test\.go$|^integration/)' || true
)"

# No test changes: rely on existing cmd/integration coverage.
if [ -z "$test_files" ]; then
  exit 0
fi

has_cmd_test=false
has_integration=false
while IFS= read -r f; do
  [ -z "$f" ] && continue
  case "$f" in
    cmd/gale/*_test.go) has_cmd_test=true ;;
    integration/*) has_integration=true ;;
  esac
done <<<"$test_files"

if $has_cmd_test || $has_integration; then
  exit 0
fi

# Tests changed, but only under internal/.
echo "::error::Pipeline-sensitive production code changed with only internal/ test updates." >&2
echo "Changed production files:" >&2
echo "$sensitive_prod" | sed 's/^/  /' >&2
echo "Changed test files (internal/ only):" >&2
echo "$test_files" | sed 's/^/  /' >&2
echo >&2
echo "Add or extend a repro in cmd/gale/*_test.go or integration/." >&2
echo "See docs/dev/change-discipline.md (Test layer choice)." >&2
exit 1
