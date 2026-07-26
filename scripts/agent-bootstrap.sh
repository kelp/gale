#!/usr/bin/env bash
# agent-bootstrap.sh — install the gale dev toolchain in an agent sandbox.
#
# Claude Code (and other agent) containers ship a bare Linux image: go and
# git are present, but just, gofumpt, golangci-lint, govulncheck, actionlint
# and gale itself are not. Nothing here can install them the normal way —
# `gale install` cannot work in the sandbox (the egress proxy blocks GHCR's
# blob host and every upstream source host), so this script fetches each tool
# from an allowed origin instead. See docs/dev/agent-environment.md.
#
# Properties this script guarantees, because agents depend on them:
#
#   Idempotent   Every step is skipped when already satisfied. Running it
#                twice costs a few stat calls.
#   Serialized   An flock means a second invocation BLOCKS until the first
#                finishes. That is the wait primitive: to wait for the
#                background session-start bootstrap, just run this script.
#   Best-effort  One failed tool never aborts the rest. Failures are recorded
#                in the status file so a blocked download can't leave the
#                session with no gale binary.
#
# Usage:
#   scripts/agent-bootstrap.sh          # no-op unless CLAUDE_CODE_REMOTE is set
#   scripts/agent-bootstrap.sh --force  # run anywhere (also: just agent-bootstrap)
#
# The no-op guard exists so this never fights a real dev machine, where the
# same toolchain comes from direnv + gale via gale.toml.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin_dir="$HOME/.local/bin"
state_dir="$HOME/.cache/gale-agent-bootstrap"
status_file="$state_dir/status"
lock_file="$state_dir/lock"

force=0
[ "${1:-}" = "--force" ] && force=1

if [ "$force" -eq 0 ] && [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  echo "agent-bootstrap: not a remote agent container; skipping (use --force to override)"
  exit 0
fi

mkdir -p "$bin_dir" "$state_dir"

# Serialize. Re-exec under flock so concurrent runs queue instead of racing.
if [ "${GALE_BOOTSTRAP_LOCKED:-}" != "1" ]; then
  export GALE_BOOTSTRAP_LOCKED=1
  exec flock "$lock_file" "${BASH_SOURCE[0]}" "$@"
fi

record() { printf '%-16s %s\n' "$1" "$2" >>"$status_file"; }

note() { echo "agent-bootstrap: $*"; }

: >"$status_file"
record "started" "$(date -u +%Y-%m-%dT%H:%M:%SZ) repo=$repo_root"

# Pins come from the repo, never from this script, so the sandbox toolchain
# tracks gale.toml the way a real dev machine does.
pin() {
  awk -F'"' -v key="$1" '
    $0 ~ "^[[:space:]]*" key "[[:space:]]*=" { print $2; exit }
  ' "$repo_root/gale.toml"
}

# The go directive in go.mod, e.g. 1.26.4. Load-bearing for golangci-lint.
gomod_go_version() {
  awk '/^go [0-9]/ { print $2; exit }' "$repo_root/go.mod"
}

platform_slug() {
  case "$(uname -m)" in
    x86_64 | amd64) echo "x86_64" ;;
    aarch64 | arm64) echo "aarch64" ;;
    *) echo "" ;;
  esac
}

# go install into ~/.local/bin, which is already ahead of everything on PATH.
# toolchain: pass a go version (e.g. go1.26.4) to force GOTOOLCHAIN, or "" for
# the default. See install_golangci_lint for why that override exists.
go_install() {
  local name="$1" pkg="$2" toolchain="$3"
  if command -v "$name" >/dev/null 2>&1 && [ -x "$bin_dir/$name" ]; then
    record "$name" "ok (already present)"
    return 0
  fi
  note "installing $name from $pkg"
  if GOBIN="$bin_dir" GOTOOLCHAIN="${toolchain:-auto}" go install "$pkg" 2>&1 | tail -5; then
    record "$name" "ok ($pkg)"
  else
    record "$name" "FAILED ($pkg)"
  fi
}

# Extract a single binary out of a GitHub release tarball. Release assets on
# github.com are reachable from the sandbox; codeload and most upstream CDNs
# are not.
install_release_tarball() {
  local name="$1" url="$2" member="$3"
  if [ -x "$bin_dir/$name" ]; then
    record "$name" "ok (already present)"
    return 0
  fi
  note "downloading $name from $url"
  local tmp
  tmp="$(mktemp -d)"
  # Retry: the egress proxy returns an occasional transient 502 on release
  # assets, and a bootstrap that gives up on one is worse than a slow one.
  if curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors "$url" -o "$tmp/asset.tar.gz" &&
    tar -xzf "$tmp/asset.tar.gz" -C "$tmp" &&
    find "$tmp" -type f -name "$member" -perm -u+x -exec install -m 0755 {} "$bin_dir/$name" \; &&
    [ -x "$bin_dir/$name" ]; then
    record "$name" "ok ($url)"
  else
    record "$name" "FAILED ($url)"
  fi
  rm -rf "$tmp"
}

go_version="$(gomod_go_version)"
arch="$(platform_slug)"

# 1. Warm the module cache. Cold this is the long pole (minutes); warm it is
#    a no-op. Everything below reuses it.
note "warming the go module cache"
if (cd "$repo_root" && go mod download); then
  record "go-mod-cache" "ok (go $go_version via GOTOOLCHAIN)"
else
  record "go-mod-cache" "FAILED"
fi

# 2. gale itself. Highest value and cheapest — the recipes repo's lint hook
#    and `gale lint` both need it. Always rebuilt so it tracks the worktree.
note "building gale from source"
if (cd "$repo_root" && go build -o "$bin_dir/gale" ./cmd/gale/); then
  record "gale" "ok (built from $repo_root)"
else
  record "gale" "FAILED (go build ./cmd/gale/)"
fi

# 3. gofumpt — `just fmt`, `just fmt-check`, and the .githooks pre-commit gate.
go_install gofumpt "mvdan.cc/gofumpt@v$(pin gofumpt)" ""

# 4. golangci-lint. GOTOOLCHAIN is load-bearing, not incidental: a Go analysis
#    tool loads this module's packages with the toolchain it was BUILT with,
#    and refuses when that is older than the go directive here. Both
#    golangci-lint and govulncheck pin an older toolchain in their own go.mod,
#    so a plain `go install ...@latest` yields a binary that errors out with
#    "the Go language version (go1.25) used to build golangci-lint is lower
#    than the targeted Go version" (govulncheck says "package requires newer
#    Go version"). Forcing go$(go.mod version) is the only thing that works.
go_install golangci-lint \
  "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(pin golangci-lint)" \
  "go$go_version"

# 5. govulncheck — CI runs it; no justfile target, so agents need it by hand.
#    Same toolchain constraint as golangci-lint above.
go_install govulncheck "golang.org/x/vuln/cmd/govulncheck@latest" "go$go_version"

# 6. actionlint — lints .github/workflows here and in gale-recipes.
go_install actionlint "github.com/rhysd/actionlint/cmd/actionlint@latest" ""

# 7. just — every documented command in CLAUDE.md goes through it.
just_version="$(pin just)"
if [ -n "$arch" ]; then
  install_release_tarball just \
    "https://github.com/casey/just/releases/download/${just_version}/just-${just_version}-${arch}-unknown-linux-musl.tar.gz" \
    just
else
  record "just" "SKIPPED (unsupported arch $(uname -m))"
fi

# 8. Full history for origin/main. .golangci.yml sets
#    `issues: new-from-merge-base: origin/main`, so a shallow clone silently
#    changes which findings are reported relative to CI.
if [ "$(git -C "$repo_root" rev-parse --is-shallow-repository 2>/dev/null)" = "true" ]; then
  git -C "$repo_root" fetch --unshallow origin main >/dev/null 2>&1 &&
    record "git-history" "ok (unshallowed)" ||
    record "git-history" "FAILED (fetch --unshallow)"
else
  record "git-history" "ok (full history)"
fi

# 9. The pre-commit gofumpt gate, same as `just hooks`.
if git -C "$repo_root" config core.hooksPath .githooks; then
  record "git-hooks" "ok (core.hooksPath=.githooks)"
else
  record "git-hooks" "FAILED"
fi

# Persist for the rest of the session. GOBIN keeps future `go install` calls
# landing on PATH instead of the unreferenced /root/go/bin.
if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
  {
    echo "export GOBIN=$bin_dir"
    echo "export PATH=$bin_dir:\$PATH"
  } >>"$CLAUDE_ENV_FILE"
fi

if grep -q FAILED "$status_file"; then
  record "finished" "with failures — see lines above"
else
  record "finished" "all tools ready"
fi

cat "$status_file"
