# CLAUDE.md

Guidance for Claude Code when working in this repository.
For design rationale, see `docs/design.md`.

## Overview

Gale is a macOS-first package manager for developer CLI
tools. Written in Go. Goal: replace Homebrew, Nix, and
home-manager with one tool that handles global packages
and per-project environments.

## Build & Test

```
just              # test + lint (default)
just build        # build binary
just test         # all tests
just test-pkg foo # single package tests
just check        # test + lint + format check
just fmt          # fix formatting with gofumpt
just lint         # golangci-lint + go vet
```

## Project Layout

```
cmd/gale/              CLI (cobra commands)
internal/generation/   gen dirs with symlinks, atomic swap
internal/installer/    install to store (binary or source)
internal/store/        package store (~/.gale/pkg/)
internal/build/        build-from-source orchestration
internal/download/     HTTP fetch, SHA256, tar extraction
internal/ghcr/         GHCR anonymous token exchange
internal/registry/     on-demand recipe fetch from GitHub
internal/recipe/       TOML recipe parsing
internal/config/       gale.toml and config.toml parsing
internal/env/          direnv hook, PATH building
internal/output/       colored terminal output
internal/lockfile/     gale.lock read/write
internal/repo/         recipe repository management
internal/trust/        ed25519 signing and verification
internal/ai/           Anthropic SDK integration
internal/homebrew/     Homebrew formula file parser
```

## Key Concepts

**Store** (`~/.gale/pkg/`): immutable package storage.
One directory per package per version. Append-only.

**Generation** (`~/.gale/gen/<N>/`): a snapshot of
symlinks into the store. `current` symlink points to
the active gen. Rebuilt declaratively from gale.toml
on every install/remove/sync. Atomic swap via
`os.Rename`. "gen" is short for generation.

**current** (`~/.gale/current`): symlink to the active
gen directory. User adds `~/.gale/current/bin` to PATH.
One symlink swap updates bin, lib, man — everything.

**Registry**: fetches recipes on demand from GitHub raw
URLs. Letter-bucketed: `recipes/j/jq.toml`. No git
clone needed.

## Two-Repo Architecture

- **gale** (this repo) — the CLI tool.
- **gale-recipes** (`../gale-recipes`) — recipe TOML
  files. CI builds recipes, pushes binaries to GHCR.

Install flow: `gale install jq` fetches the recipe
from the registry, pulls a prebuilt binary from GHCR
if available, falls back to building from source.

## Environment Activation

**Global**: `~/.gale/current/bin` on PATH.

**Project**: direnv integration. `gale init` creates
`.envrc` with `use gale`. direnv calls `gale sync`
and adds `.gale/current/bin` to PATH. Project and
global share the same generation model.

`gale env` prints `export PATH=...` for CI/scripts.

## Conventions

- Error handling: `fmt.Errorf("context: %w", err)`
- Testing: table-driven, temp dirs for filesystem ops
- TDD: `/tdd-orchestrate` pipeline for new modules
- No panics in library code
- One responsibility per package
- Format with gofumpt, lint with golangci-lint

## Gotchas

- Build PATH isolates individual tools via symlinks
  into a temp dir, preventing nix vibeutils (ls, mv)
  from leaking in and breaking autotools. See
  `buildPath()` in `internal/build/build.go`.
- Tar extraction handles PAX headers, hard links,
  symlinks, and validates paths against traversal.
  Shared `extractTar()` helper in `internal/download/`.
- Autotools builds need timestamp reset (`touchAll`)
  after extraction to avoid clock-skew errors.
- Recipe repo uses letter-bucketed layout
  (`recipes/j/jq.toml`).
- macOS `/var` is a symlink to `/private/var`. Tests
  that compare paths must `filepath.EvalSymlinks` both
  sides.
- Prefer static linking for CLI tools to avoid dylib
  path issues. Use `--disable-shared --enable-all-static`
  for autotools projects like jq.
- gale-recipes CI pushes binary sections to main
  after builds. Expect push rejections — use
  `git pull --rebase` before pushing.
- gosec G306 flags `os.WriteFile` with 0644. Use
  `//nolint:gosec` for world-readable files.
- Use `go:embed` to bake files into the binary
  (see `internal/generation/gale-readme.md`).
