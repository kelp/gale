# Gale

**One tool for all your dev packages.** Install CLI
tools, languages, and runtimes into isolated
directories. Pin versions per project. Activate
on `cd`.

No more juggling Homebrew, nix, asdf, and mise.
Declare what you need in `gale.toml`. Gale installs
it — prebuilt binary when available, source build
when not.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
```

Or with Homebrew:

```sh
brew install kelp/tap/gale
```

## Get Started

Add gale to your PATH:

```sh
export PATH="$HOME/.gale/current/bin:$PATH"
```

Install a package:

```sh
gale install jq
```

Set up a project:

```sh
gale init
gale install go just golangci-lint
gale sync
```

This creates `gale.toml` with pinned versions.
Anyone who clones the repo runs `gale sync` and
gets the same tools.

## Project Environments

```toml
[packages]
  go = "1.26.1"
  just = "1.48.0"
  golangci-lint = "2.11.4"
```

With direnv, environments activate on `cd`:

```sh
# ~/.config/direnv/direnvrc
eval "$(gale hook direnv)"
```

Teams using asdf or mise can keep their
`.tool-versions` file — gale reads it as a
fallback when no `gale.toml` exists.

## Commands

```
gale install <pkg>[@ver]  Install a package
gale remove <pkg>         Remove a package
gale sync                 Install at pinned versions
gale update [pkg...]      Update to latest
gale list                 List installed packages
gale outdated             Show available updates
gale which <binary>       Find which package owns it
gale diff                 Preview what sync would do
gale search <query>       Search by name or description
gale doctor               Check for problems
gale gc                   Clean unused versions
gale init                 Set up a project
gale build <recipe>       Build from source
gale lint <recipe>        Validate a recipe
```

See `man gale` for the full reference.

## How It Works

Packages live in `~/.gale/pkg/<name>/<version>/`.
Each version is self-contained. A generation
(`~/.gale/current/`) symlinks into the store.
One atomic symlink swap updates everything.

`gale.toml` pins exact versions. `gale.lock`
records SHA256 hashes. `gale sync` installs what's
pinned and verifies integrity.

Recipes are TOML files in a separate repository
([gale-recipes](https://github.com/kelp/gale-recipes)).
Prebuilt binaries are cached in GHCR. Source builds
are the fallback — every recipe can build from
source.

## Development

Requires Go 1.21+ for bootstrapping.

```sh
git clone https://github.com/kelp/gale
git clone https://github.com/kelp/gale-recipes
cd gale
just bootstrap
gale sync --local
direnv allow
```

After bootstrap, `just install` rebuilds gale from
source using gale itself.

```sh
just            # test + lint
just build      # build binary
just check      # test + lint + format
```

### Adding a Recipe

Recipes use letter-bucketed paths:
`recipes/<letter>/<name>.toml`.

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
  "go build -ldflags \"-X main.version=${VERSION}\" -o ${PREFIX}/bin/mytool .",
]
```

Build and test:

```sh
gale build recipes/m/mytool.toml
gale lint recipes/m/mytool.toml
```

## License

MIT
