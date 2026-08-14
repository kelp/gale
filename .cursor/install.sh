#!/usr/bin/env bash
# Cursor Cloud Agent install step for the gale + gale-recipes workspace.
#
# Cursor cloud agents do not run the repos' Claude Code SessionStart hooks, so
# a fresh pod would otherwise land with only the base image (go, git) and none
# of the dev toolchain that docs/dev/agent-environment.md documents. This reuses
# the same idempotent bootstrap the Claude Code hook runs, so both agent hosts
# converge on the identical toolchain and pins. The install logic and version
# pins live in scripts/agent-bootstrap.sh (shared) — keep this wrapper thin.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# 1. gale's toolchain: gale, gofumpt, golangci-lint, govulncheck, actionlint,
#    just, patchelf, a warm module cache, unshallowed history, and .githooks.
#    This alone also covers what gale-recipes needs to lint (gale, just,
#    actionlint). The script is best-effort and never exits non-zero on a
#    single failed tool, so a transient download does not fail the whole build.
"$repo_root/scripts/agent-bootstrap.sh" --force

# 2. gale-recipes, best-effort: its bootstrap unshallows the recipes history
#    (check_ledger.py diffs origin/main) and records the python interpreter.
#    The sibling checkout location can vary between hosts, so probe the usual
#    spots and skip quietly when it is not colocated.
for recipes in \
  "$repo_root/../gale-recipes" \
  "$repo_root/../../gale-recipes" \
  "$repo_root/../gale-recipes/gale-recipes"; do
  if [ -x "$recipes/scripts/agent-bootstrap.sh" ]; then
    "$recipes/scripts/agent-bootstrap.sh" --force || true
    break
  fi
done

# 3. Persist ~/.local/bin on PATH for the agent's shells. agent-bootstrap.sh
#    exports PATH via CLAUDE_ENV_FILE, which only Claude Code sets; Cursor
#    shells pick up PATH from the rc/profile files instead. Idempotent.
bin_dir="$HOME/.local/bin"
mkdir -p "$bin_dir"
for rc in "$HOME/.bashrc" "$HOME/.profile" "$HOME/.zshrc"; do
  touch "$rc"
  if ! grep -qsF "# gale dev toolchain" "$rc"; then
    printf '\n# gale dev toolchain\nexport PATH="%s:$PATH"\n' "$bin_dir" >>"$rc"
  fi
done

echo "cursor cloud-agent install: toolchain ready in $bin_dir"
