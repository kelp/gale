# v0.1.0-dev

First development release. Not stable — APIs and
commands may change.

## Highlights

- **Declarative environments** — `gale.toml` declares
  packages, `gale sync` installs them, direnv activates
  them on `cd`. Atomic generation swap via symlinks.

- **Everything from source** — prebuilt binaries from
  GHCR are a cache, not a substitute. Every package
  can be built from source with `gale build`.

- **Local development** — `--source` builds from a
  local checkout, `--local` resolves recipes from a
  sibling gale-recipes directory. Gale self-installs
  with `just bootstrap`.

- **18 recipes** — jq, just, fd, ripgrep, bat,
  git-delta, starship, fzf, eza, direnv, go, rust,
  cmake, pkgconf, patchelf, lazygit, actionlint, gale.

## Commands

install, remove, add, sync, update, list, env, init,
build, lint, search, shell, run, hook, import.

## What's missing

- No semver tagging yet (version is git hash)
- No self-update from published releases
- No colored help output
- AI features are stubbed but not wired
