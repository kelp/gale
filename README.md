# Gale

A macOS-first package manager for developer CLI tools.
Combines Homebrew's simplicity with Nix's isolation.
Written in Go.

## Features

- Install CLI tools and runtimes from prebuilt binaries
- Per-project environments that activate on `cd`
- Declarative `gale.toml` for reproducible setups
- Federated recipe repositories with ed25519 signing
- Optional AI-powered search and recipe generation

## Install

```
go install github.com/kelp/gale/cmd/gale@latest
```

## Quick Start

```sh
# Install a package
gale install jq
gale install python@3.11

# List installed packages
gale list

# Remove a package
gale remove jq

# Activate project environment
eval "$(gale hook zsh)"    # add to .zshrc
eval "$(gale hook fish)"   # add to config.fish
```

## Project Environments

Create a `gale.toml` in your project directory:

```toml
[packages]
python = "3.11"
nodejs = "20"
just = "latest"

[vars]
DATABASE_URL = "postgres://localhost/myapp"
```

Then run `gale sync` to install, or let the shell hook
activate the environment automatically when you `cd`
into the project.

## Configuration

Global packages live in `~/.gale/gale.toml`. App
settings (repos, AI) live in `~/.gale/config.toml`:

```toml
[[repos]]
name = "core"
url = "https://github.com/kelp/gale-recipes"
key = "gale-ed25519:abc123..."
priority = 1

[ai]
provider = "anthropic"
api_key = "sk-ant-..."
```

## Commands

```
gale install <pkg>[@version]   Install a package
gale remove <pkg>              Remove a package
gale list                      List packages
gale sync                      Install everything in gale.toml
gale update                    Re-resolve latest pins
gale shell                     Launch subshell with project env
gale run <cmd> [-- args]       Run command in project env
gale hook <shell>              Output shell activation hook
gale search <query>            Search for packages
gale import homebrew [pkg]     Import from Homebrew
gale create-recipe <url>       Generate recipe from GitHub repo
gale repo add <name> <url>     Add recipe repository
gale repo remove <name>        Remove recipe repository
gale repo list                 List repositories
gale repo init <name>          Scaffold new recipe repo
```

## Development

Requires Go 1.21+ and [gofumpt](https://github.com/mvdan/gofumpt).

```sh
just         # run tests and lint
just build   # build binary
just check   # tests + lint + format check
just test-pkg recipe  # test single package
```

## License

MIT
