# Gale

Fast, isolated package management for developers.
Versioned installs, per-project environments that
activate automatically.

## Why

Homebrew is easy to start with and hard to maintain.
Dependencies pile up invisibly, there is no manifest
to version-control, and a clean reinstall means
starting over. Nix solves all of this — declarative
config, rollback, per-project isolation — but demands
you learn a language and debug cryptic build failures.
Gale gives you the declarative model without the
complexity. Gale is as easy to use as Homebrew and as predictable
as Nix, without the baggage of either.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
```

Or with Homebrew:

```sh
brew install kelp/tap/gale
```

Add gale to your PATH:

```sh
export PATH="$HOME/.gale/current/bin:$PATH"
```

## Get Started

Install a tool:

```sh
gale install jq
```

Gale fetches a prebuilt binary, verifies its SHA256,
and symlinks it into your PATH.

Set up a project manifest:

```sh
cd myproject
gale init
gale install go@1.26.1
gale install just
```

This creates `gale.toml` with pinned versions and a
lockfile with SHA256 hashes. Commit both. Anyone who
clones the repo runs `gale sync` and gets identical
tools.

## How It Works

Packages live in `~/.gale/pkg/`, one directory per
version, never modified after install. A generation
directory holds symlinks into the store, and
`~/.gale/current` points to the active generation.
Installing or removing a package builds a new
generation and swaps the `current` symlink in one
atomic operation. No partial states, no broken PATH.

## Project Environments

A project's `gale.toml` pins the tools it needs:

```toml
[packages]
  go = "1.26.1"
  just = "1.48.0"
  golangci-lint = "2.11.4"

[vars]
  CGO_ENABLED = "0"
```

With direnv, environments activate on `cd`:

```sh
# One-time setup in ~/.config/direnv/direnvrc
eval "$(gale hook direnv)"
```

Enter the project directory and direnv syncs packages,
adds `.gale/current/bin` to PATH, and exports
variables from `[vars]`. Leave the directory and your
global environment returns.

Global and project packages can coexist at different
versions. Go 1.24 globally, Go 1.26.1 in the project
— direnv handles the switch.

Teams migrating from asdf or mise can keep their
`.tool-versions` file. Gale reads it as a fallback
when no `gale.toml` exists.

## Commands

```
gale install <pkg>[@ver]  Install a package
gale remove <pkg>         Remove a package
gale sync                 Install at pinned versions
gale update [pkg...]      Update to latest
gale list                 List packages in manifest
gale info <pkg>           Show package metadata
gale outdated             Show available updates
gale search <query>       Search by name or description
gale which <binary>       Find which package owns it
gale doctor               Diagnose setup issues
gale gc                   Clean unused versions + gens
gale generations          List and manage generations
gale init                 Set up a project
gale env                  Print PATH and vars for shell
gale shell                Open shell with project env
gale run <cmd>            Run command in project env
gale pin <pkg>            Pin version, skip on update
gale unpin <pkg>          Unpin a package
gale build <recipe>       Build from source
gale lint <recipe>        Validate a recipe
gale create-recipe <repo> Generate recipe with AI
gale audit <pkg>          Rebuild and compare hashes
gale verify <pkg>         Check binary attestation
gale sbom [pkg]           Software bill of materials
gale remote sync <host>   Sync packages to remote host
gale remote diff <host>   Compare local vs remote
gale completion <shell>   Generate shell completions
```

See `man gale` for the full reference.

## Recipes

Recipes are TOML files in
[gale-recipes](https://github.com/kelp/gale-recipes).
The repository has over 120 recipes today, covering
tools like jq, ripgrep, git, terraform, kubectl, and
Go. Each recipe defines how to build a package from
source.
Prebuilt binaries cached in GHCR are an optimization
— every recipe can build from source if needed.

`gale create-recipe owner/repo` generates a recipe
from a GitHub repository using the Anthropic API.
It detects the build system, computes the SHA256,
and produces a valid recipe. Requires an API key
in `~/.gale/config.toml`.

```toml
[package]
name = "mytool"
version = "1.0.0"
description = "Does the thing"
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

## Optional Dependencies

**[gh](https://cli.github.com/)** — GitHub CLI.
Used for Sigstore attestation verification during
binary installs, `gale verify`, and `gale audit`.
Without it, gale skips attestation checks and
installs proceed normally. `gale doctor` reports
its availability.

**ssh / scp** — used by `gale remote` commands to
sync packages to remote machines. Standard on macOS
and Linux. Respects `~/.ssh/config` for host aliases
and key configuration.

**[Anthropic API key](https://console.anthropic.com/)** —
used by `gale create-recipe` for AI-powered recipe
generation. Configure in `~/.gale/config.toml` under
`[anthropic]`. Not needed for any other functionality.

## Development

Requires Go 1.21+ for bootstrapping.

```sh
git clone https://github.com/kelp/gale
git clone https://github.com/kelp/gale-recipes
cd gale
just bootstrap
gale sync --recipes
direnv allow
```

After bootstrap, `just install` rebuilds gale from
source using gale itself.

```sh
just            # test + lint
just build      # build binary
just check      # test + lint + format
```

## License

MIT
