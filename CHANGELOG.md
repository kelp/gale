# Changelog

## Unreleased

### Added

- Declarative environment model with atomic generation
  swap. `~/.gale/current` symlink points to a numbered
  generation directory containing bin/, lib/, man/,
  include/ symlinks into the package store.
- `gale env` command prints `export PATH=...` for CI
  and scripts.
- `gale init` bootstraps a project with gale.toml,
  .envrc, and .gitignore entry.
- `gale add` command adds packages to gale.toml
  without installing.
- `gale hook direnv` outputs `use_gale` function for
  direnv integration. Replaces fish/zsh/bash shell
  hooks.
- Interactive scope prompt (`[g/p]`) when a project
  gale.toml exists and no `-g`/`-p` flag is set.
- Registry URL override in `~/.gale/config.toml`.
- Post-build dylib fixup rewrites dynamic library
  paths to `@rpath` (macOS) and `$ORIGIN/../lib`
  (Linux) for portable binaries.
- Per-platform build overrides in recipe format:
  `[build.darwin-arm64]` and `[build.linux-amd64]`.
- Embedded README.md written into `.gale/` on every
  generation rebuild.
- Streaming build output for long-running builds.
- 15 recipes: jq, just, fd, ripgrep, bat, git-delta,
  starship, fzf, eza, direnv, go, rust, cmake,
  pkgconf, lazygit, patchelf.
- golangci-lint v2 with strict configuration.
- CI: golangci-lint, race detector, govulncheck on
  macOS arm64 and Linux amd64.

### Changed

- `gale install` fetches recipes from the public
  registry, installs binary from GHCR (preferred) or
  builds from source, adds to gale.toml, and rebuilds
  the generation.
- `gale remove` cleans up store, removes from config,
  and rebuilds the generation.
- `gale sync` falls back to `~/.gale/gale.toml` when
  no project config exists.
- `gale shell` and `gale run` use the generation model
  (current/bin on PATH) instead of concatenating store
  paths.
- Installer decoupled from symlinks — only manages
  the store. Commands rebuild the generation.
- Build PATH isolates individual tools via symlinks
  to prevent nix vibeutils contamination.
- Shortened directory names: `packages/` to `pkg/`,
  `generations/` to `gen/`.

### Removed

- `internal/profile/` package replaced by
  `internal/generation/`.
- Fish, zsh, and bash shell hooks replaced by direnv.
- Dead code in `internal/env/`: BuildPATH,
  BuildEnvironment, MergePackages, DetectConfig.
