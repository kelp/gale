# Gale

Fast, isolated package management for developers.
Versioned installs, per-project environments that
activate automatically.

## Features

- Install CLI tools and runtimes into isolated directories
- Per-project environments that activate on `cd`
- Declarative `gale.toml` for reproducible setups
- Federated recipe repositories with ed25519 signing
- Optional AI-powered search and recipe generation

## Install

Gale is not yet published. Build from source:

```sh
git clone https://github.com/kelp/gale
cd gale
go build -o gale ./cmd/gale/
# Copy to somewhere on your PATH
```

## Setup

Add `~/.gale/current/bin` to your PATH:

```sh
# .zshrc or .bashrc
export PATH="$HOME/.gale/current/bin:$PATH"
```

For per-project environments with direnv, add the gale
hook to your direnvrc:

```sh
# ~/.config/direnv/direnvrc
eval "$(gale hook direnv)"
```

## Quick Start

```sh
# Install a package globally
gale install jq

# List installed packages
gale list

# Remove a package
gale remove jq

# Initialize a project environment
gale init
# Creates gale.toml and .envrc, then:
direnv allow
```

## Project Environments

`gale.toml` declares project dependencies:

```toml
[packages]
  go = "1.26.1"
  just = "1.48.0"
```

Run `gale sync` to install, or let direnv activate
the environment automatically when you `cd` into
the project.

## Commands

```
gale install <pkg>[@ver]  Install a package
gale remove <pkg>         Remove a package
gale add <pkg> [pkg...]   Add to gale.toml without installing
gale sync                 Install all packages in gale.toml
gale list                 List packages in gale.toml
gale env                  Print export PATH for current scope
gale init                 Bootstrap project (gale.toml, .envrc)
gale hook direnv          Print use_gale function for direnvrc
gale build <recipe.toml>  Build recipe from source
gale search <query>       Search for packages
gale shell                Open shell with project environment
gale run <cmd>            Run command in project environment
gale import homebrew <n>  Import Homebrew formula as recipe
```

## Development

### Prerequisites

Go 1.21+ is required for bootstrapping. After that,
gale manages its own dev dependencies.

### First-Time Setup

```sh
# Clone both repos side-by-side
git clone https://github.com/kelp/gale
git clone https://github.com/kelp/gale-recipes

# Bootstrap: build gale with go, then self-install
cd gale
just bootstrap

# Sync project dev tools (go, just, golangci-lint, gofumpt)
gale sync --local

# Activate the project environment
direnv allow
```

After bootstrapping, `just install` rebuilds gale from
the current source using gale itself. The version is
set from `git rev-parse --short HEAD`.

### Common Tasks

```sh
just         # run tests and lint
just build   # build binary
just check   # tests + lint + format check
just test-pkg recipe  # test single package
```

### Two-Repo Architecture

Gale uses two repositories side-by-side:

- **gale** — the CLI tool (this repo)
- **gale-recipes** — TOML recipe files and CI that
  builds binaries and pushes them to GHCR

When you run `gale install jq`, it fetches the recipe
from the registry, pulls a prebuilt binary from GHCR
if available, and falls back to building from source.

### Local Development Flags

When working on gale or recipes locally:

- `--source <dir>` — build from a local source
  directory instead of downloading. Auto-finds the
  recipe in a sibling `gale-recipes/` directory.
  Version is detected from `git rev-parse --short HEAD`.
- `--recipe <file>` — use a local recipe TOML file
  instead of fetching from the registry.
- `--local` (on `gale sync`) — resolve all recipes
  from the sibling `gale-recipes/` directory instead
  of the remote registry.

### Adding a Recipe

Recipes live in `gale-recipes/` with letter-bucketed
paths: `recipes/<first-letter>/<name>.toml`.

```toml
[package]
name = "mytool"
version = "1.0.0"
description = "My tool"
license = "MIT"
homepage = "https://github.com/owner/mytool"

[source]
repo = "owner/mytool"
url = "https://github.com/owner/mytool/archive/refs/tags/v1.0.0.tar.gz"
sha256 = "..."

[dependencies]
build = ["go"]

[build]
steps = [
  "mkdir -p ${PREFIX}/bin",
  "go build -o ${PREFIX}/bin/mytool .",
]
```

Test locally with:

```sh
gale build ../gale-recipes/recipes/m/mytool.toml
```

## License

MIT
