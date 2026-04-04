# Gale

macOS-first package manager for developer CLI tools. Written in Go.
Replaces Homebrew, Nix, and home-manager.

## Key Facts
- Just cut over from nix-darwin/home-manager — still stabilizing
- Strict TDD mandatory
- Uses `just` as task runner

## Build & Test
```bash
just              # test + lint (default)
just build        # build binary
just test         # all tests
just test-pkg foo # single package tests
just check        # test + lint + format check
just lint         # golangci-lint + go vet
just fmt          # gofumpt
```

## Architecture
- Store (`~/.gale/pkg/`): immutable package storage, append-only
- Generation (`~/.gale/gen/<N>/`): symlink snapshots into store
- Recipes: TOML package definitions fetched from GitHub
- Config: `~/.gale/gale.toml` (global), `gale.toml` (per-project)
- Direnv integration: `.envrc` with `use gale` activates project deps

## Key Packages
```
cmd/gale/           CLI (cobra commands)
internal/store/     package store
internal/installer/ install to store
internal/build/     build-from-source
internal/config/    gale.toml parsing
internal/env/       direnv hook, PATH building
internal/registry/  recipe fetch from GitHub
```
