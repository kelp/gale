# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code)
when working with code in this repository.

## Overview

Gale is a macOS-first package manager for developer CLI
tools. Written in Go.

## Build & Test

```
just              # test + lint (default)
just build        # build binary
just test         # all tests
just test-pkg recipe  # single package tests
just check        # test + lint + format check
just fmt          # fix formatting with gofumpt
```

Or directly:

```
go build ./cmd/gale/
go test ./...
go test ./internal/recipe/...
go vet ./...
gofumpt -l .
```

## Project Layout

```
cmd/gale/           CLI entry point (cobra commands)
internal/recipe/    TOML recipe parsing
internal/config/    gale.toml and config.toml parsing
internal/store/     package store directory management
internal/output/    colored terminal output
internal/download/  HTTP fetch, SHA256, tar.gz/zip/tar.zst
internal/profile/   symlink management (~/.gale/bin/)
internal/lockfile/  gale.lock read/write
internal/env/       PATH building, shell hooks
internal/repo/      recipe repository management
internal/trust/     ed25519 signing and verification
internal/ai/        Anthropic SDK integration
internal/homebrew/  Homebrew formula file parser
internal/build/     build-from-source orchestration
internal/installer/ install flow (binary or source)
internal/ghcr/      GHCR anonymous token exchange
```

## Conventions

- Error handling: return errors, wrap with
  `fmt.Errorf("context: %w", err)`
- Testing: table-driven tests, temp directories for
  filesystem operations
- No panics in library code
- Keep packages focused — one responsibility each
- Format all Go code with gofumpt before committing

## Two-Repo Architecture

Gale is split across two repositories:

- **gale** — the tool (this repo). Go code, CLI, all
  internal packages. CI runs tests on macOS + Linux,
  builds the binary, publishes releases.
- **gale-recipes** — the content (`../gale-recipes`).
  Recipe TOML files. CI builds every recipe on each
  platform, pushes tar.zst binaries to GHCR via ORAS,
  and updates `[binary.<platform>]` sections in the
  recipe TOML.

**Dependency chain**: gale-recipes CI needs a `gale`
binary to build recipes. gale needs OCI pull support
(`internal/oci/`) so users can install prebuilt binaries
from GHCR instead of building from source.

**Install flow**: `gale install jq` checks for a
`[binary.<platform>]` match first (OCI pull from GHCR),
falls back to building from source via `[build]` steps.

**Development methodology**: strict red-green TDD via the
`/tdd-orchestrate` pipeline for every new module.

## Gotchas

- The build module uses a clean PATH to avoid nix
  coreutils interfering with autotools. Build tools
  (go, cargo, rustc) are resolved from the host PATH
  via `exec.LookPath` and their directories added
  individually. See `buildPath()` in
  `internal/build/build.go`.
- Tar extraction must handle PAX headers
  (`TypeXGlobalHeader`, `TypeXHeader`) — GitHub tarballs
  include them. Skip silently.
- `CreateTarZstd` must use `os.Lstat` to detect symlinks
  instead of following them. `filepath.Walk` follows
  symlinks by default, which causes `write too long`
  errors for symlinked shared libraries.
- Autotools builds need timestamp reset (`touchAll`)
  after extraction to avoid clock-skew errors. Skip
  symlinks and use best-effort `Chtimes`.
- Recipe repo uses letter-bucketed layout
  (`recipes/j/jq.toml`). The `repo.Manager` recurses
  into single-letter subdirectories under `recipes/`.
