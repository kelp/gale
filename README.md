# Gale

Fast, isolated package management for developers.
Versioned installs, per-project environments that
activate automatically.

## Why

Gale pins CLI tools in `gale.toml`, locks their
artifacts, and activates them through an atomic
generation swap. Install fetches a verified tree
from the index. Sync rebuilds PATH from that lock
and does not rewrite it.

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

Gale resolves the index, fetches the artifact, verifies
its tree digest, and swaps it onto PATH.

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

## Multiple Machines

One `gale.toml` can describe more than one machine.
Top-level `[packages]` applies everywhere; per-host
sections add or override entries on a specific
machine:

```toml
[packages]
  jq = "1.8.1"
  ripgrep = "14.1.1"

[hosts.my-mac.packages]
  fzf = "0.50"

[hosts.my-server.packages]
  htop = "3.0"
```

Gale picks the section that matches `hostname` (or
`GALE_HOST` if set). Sync the same file across
machines with chezmoi or git — each machine runs
`gale sync` and gets its own toolset.

```sh
gale add fzf --host current   # write to this host's section
ssh server gale sync          # remote install — no special command needed
```

See [docs/chezmoi.md](docs/chezmoi.md) and
[docs/configuration.md](docs/configuration.md) for
details.

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
gale generations          List generations or roll back one step
gale init                 Set up a project
gale env                  Print PATH and vars for shell
gale shell                Open shell with project env
gale run <cmd>            Run command in project env
gale build <recipe>       Build from source
gale lint <file>          Validate a recipe or index file
gale admit                Record an index artifact from an archive
gale create-recipe <repo> Generate recipe with AI
gale audit <pkg>          Rebuild and compare hashes
gale verify [pkg]         Check store tree digests against the lock
gale sbom [pkg]           Software bill of materials
gale completion <shell>   Generate shell completions
```

See `man gale` for the full reference.

## Index

The catalog lives in
[gale-recipes](https://github.com/kelp/gale-recipes)
under `index/`. Each document names versions, artifact
URLs, `sha256`, and `tree_digest`. `gale install` and
`gale update` resolve against that index.
`--index <dir>` points at a local checkout (a git
repo; uncommitted edits are invisible).

A package that is not in the index is an error. v1
locks migrate with `gale fetch-adopt`.

```toml
[package]
name = "just"
latest = "1.58.0"

[versions."1.58.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.58.0/just-1.58.0-aarch64-apple-darwin.tar.gz"
format = "tar.gz"
sha256 = "..."
tree_digest = "sha256:..."
hash_source = "upstream-sha256sums"
```

## Optional Dependencies

None. Sigstore attestation verification (binary
installs, `gale verify`, `gale audit`) runs
in-process — no `gh` CLI or other external tool
required.

**[Anthropic API key](https://console.anthropic.com/)** —
used by `gale create-recipe` for AI-powered recipe
generation. Configure in `~/.gale/config.toml` under
`[anthropic]`. Not needed for any other functionality.

## Development

Requires Go 1.26+ for bootstrapping.

```sh
git clone https://github.com/kelp/gale
git clone https://github.com/kelp/gale-recipes
cd gale
just bootstrap
gale lock --index ../gale-recipes
gale sync
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
