#!/usr/bin/env bash
# PreToolUse(Bash) guard: refuse `gale install|build|sync` in an agent sandbox.
#
# These cannot succeed here and they fail slowly. The egress proxy allows
# ghcr.io's token and manifest endpoints but blocks the blob host
# (pkg-containers.githubusercontent.com), so gale resolves a prebuilt binary,
# fails to download it, and falls back to a source build — whose source hosts
# (go.dev, ftp.gnu.org, codeload, ci-artifacts.rust-lang.org) are blocked too.
# A measured `gale install just` spent 3m11s compiling rustc before dying.
#
# `gale lint`, `info`, `which`, `list`, `sbom` and friends are untouched:
# they are offline-clean and genuinely useful here.
#
# Escape hatch: prefix the command with GALE_ALLOW_NETWORK_INSTALL=1 to run it
# anyway (e.g. against a fixture registry, or if the egress policy changes).

set -uo pipefail

command_line="$(jq -r '.command // empty' 2>/dev/null <<<"${CLAUDE_TOOL_INPUT:-}")"
[ -n "$command_line" ] || exit 0

# Explicit override wins.
grep -q 'GALE_ALLOW_NETWORK_INSTALL=1' <<<"$command_line" && exit 0

# Only match at a command position — start of line, or after a separator.
# Matching after any whitespace would also flag `echo gale install ...`, which
# is exactly the over-broad-guard bug this repo's sibling hook had.
# `just install` and `just bootstrap` call through to `gale install`.
cmd_start='(^|[;&|(][[:space:]]*)'
if grep -Eq "${cmd_start}(\./)?gale[[:space:]]+(install|build|sync)([[:space:]]|$)|${cmd_start}just[[:space:]]+(install|bootstrap)([[:space:]]|$)" <<<"$command_line"; then
  cat >&2 <<'MSG'
BLOCKED: gale install/build/sync cannot work in this sandbox.

The egress proxy blocks GHCR's blob host, so the binary path fails, and the
source-build fallback's upstream hosts are blocked too. The command will burn
minutes and then fail. Details: docs/dev/agent-environment.md.

What works instead:
  gale lint <recipe.toml>   fully offline, validates recipe TOML
  go build -o ~/.local/bin/gale ./cmd/gale/   rebuild gale from source
  just test / just lint / just integration    hermetic, no network
  recipe builds                                CI only, via verify.yml

Override with GALE_ALLOW_NETWORK_INSTALL=1 if you really mean it.
MSG
  exit 2
fi

exit 0
