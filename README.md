# Gale

Gale fetches upstream CLI binaries, pins their hashes
in a lockfile, and puts them on PATH. It does not
compile packages. It does not ship bottles.

Declare tools in `gale.toml`. Install writes a v2 lock
and swaps `~/.gale/current`. Sync lands what that lock
already names. Not-in-index is an error.

## Why

One file names the tools. One lock names the exact
archives. One symlink swap updates bin and man. A
clone runs `gale sync` and gets the same trees.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
```

Or with Homebrew:

```sh
brew install kelp/tap/gale
```

Add gale to PATH:

```sh
export PATH="$HOME/.gale/current/bin:$PATH"
```

## Get Started

```sh
gale install jq
```

Gale resolves the [gale-recipes](https://github.com/kelp/gale-recipes)
index, downloads that platform's archive, checks
`sha256` and `tree_digest`, writes `gale.lock`, and
swaps `current`.

A project:

```sh
cd myproject
gale init
gale install go@1.26.1
gale install just
```

Commit `gale.toml` and `gale.lock`. Anyone who clones
the repo runs `gale sync` and gets the same tools.

## How It Works

Fetch trees live under
`~/.gale/pkg/fetch/<name>/<version>-<sha12>/` and are
never modified. A generation is a directory of
symlinks into that store. `~/.gale/current` points at
the active generation. Install, update, remove, and
sync build a new generation and rename `current` onto
it. PATH never sees a half-updated bin.

`gale.toml` declares names and versions. `gale.lock`
names URLs, hashes, and tree digests. Sync does not
rewrite the lock.

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

Enter the project and direnv syncs, adds
`.gale/current/bin` to PATH, and exports `[vars]`.
Leave and the global environment returns.

Global and project packages can coexist at different
versions. Go 1.24 globally, Go 1.26.1 in the project.

## Multiple Machines

One `gale.toml` is one machine. Leftover `[hosts.*]`
tables refuse. There is no `--host` flag. A second
machine uses a second file, then `gale sync` there.

See [docs/chezmoi.md](docs/chezmoi.md) and
[docs/configuration.md](docs/configuration.md).

## Commands

```
gale install <pkg>[@ver]  Fetch a package from the index
gale remove <pkg>         Remove a package
gale sync                 Activate the v2 lock (does not write it)
gale update [pkg...]      Fetch latest from the index
gale lock                 Rewrite the v2 lock from the index
gale fetch-adopt          Convert a v1 lock to v2
gale list                 List packages in the manifest
gale outdated             Show available updates
gale which <binary>       Find which package owns it
gale doctor               Check PATH, lock, generation, digests
gale verify [pkg]         Check store tree digests against the lock
gale gc                   Clean unused fetch trees and gens
gale generations          List generations or roll back one step
gale init                 Set up a project
gale env                  Print PATH and vars for shell
gale shell                Open shell with project env
gale run <cmd>            Run command in project env
gale lint <file>          Validate an index document
gale admit                Record an index artifact from an archive
gale completion <shell>   Generate shell completions
```

`gale migrate` is gone. It names `gale install` or
`gale fetch-adopt` and exits. `gale info` still reads
the leftover recipe cache and is not the index.

See `man gale` for the full reference.

## Index

The catalog lives in
[gale-recipes](https://github.com/kelp/gale-recipes)
under `index/`. Each document names versions, artifact
URLs, `sha256`, and `tree_digest`. Resolve verbs
(`install`, `update`, `lock`, `outdated`) talk to that
index. `--index <dir>` pins a local git checkout
(uncommitted edits are invisible).

A package that is not in the index is an error. A v1
lock migrates with `gale fetch-adopt`.

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

The catalog is macOS-first. Adding a package is
`gale admit` plus `gale lint` on an index document.
See [docs/writing-recipes.md](docs/writing-recipes.md).

## Optional Dependencies

None. Sigstore attestation verification and
`gale verify` run in-process. No `gh` CLI.

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
