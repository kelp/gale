# Gale Design

A macOS-first package manager for developer CLI tools and
per-project environments. Combines Homebrew's simplicity
with Nix's isolation. Written in Zig.

## Goals

- Install CLI tools and language runtimes fast, from
  prebuilt binaries
- Per-project environments that activate on `cd`
- Declarative config files for reproducible setups
- AI-maintained recipe repository — always current
- macOS first, Linux second

## Non-goals

- System management (no nix-darwin equivalent)
- Dotfile management (chezmoi does that)
- Language-specific package management (not npm/pip/cargo)
- Building from source by default

## Architecture

### Store

Immutable, versioned package store:

```
/gale/packages/<name>/<version>/
/gale/packages/jq/1.7.1/bin/jq
/gale/packages/python/3.11.11/bin/python3.11
```

Multiple versions coexist. Packages are never mutated
in place. No content-addressing — version is sufficient
because builds come from a centralized build farm. A hash
suffix may be added later for custom local builds.

### Profiles

`~/.gale/bin/` contains symlinks into the store. Added to
PATH once in the user's shell config.

### Recipes

TOML files that define how to build a package. Stored in a
public recipes repository, maintained by an AI agent.

```toml
[package]
name = "jq"
version = "1.7.1"
description = "Command-line JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
url = "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
sha256 = "478c9ca129fd2e3443fe27314b455e211e0d8c60bc8ff7df703f25571c92f12e"

[build]
system = "autotools"
steps = [
  "./configure --prefix=${PREFIX} --disable-docs",
  "make -j${JOBS}",
  "make install",
]

[dependencies]
build = ["autoconf", "automake", "libtool"]
runtime = ["oniguruma"]
```

TOML was chosen over shell scripts for programmatic
validation and linting. The parser will be a minimal
subset implementation in Zig (strings, string arrays,
tables, comments) to avoid external dependencies.

### Binary distribution

Recipes are built on GitHub Actions using free macOS
runners. Binaries are distributed via GitHub Releases or
S3. Binary download is the default; source build is the
fallback.

## CLI

### Package management

```
gale install jq                 # latest, from binary cache
gale install python@3.11        # pinned version
gale install --global ripgrep   # force global from a project dir
gale remove jq
gale update                     # re-resolve "latest" pins
gale list
gale sync                       # install everything in gale.toml
```

### Environment activation

```
gale shell                      # subshell with project env
gale shell --project ./other    # activate another project's env
gale run python -- -c "print('hello')"   # run one command in env
gale run node@20 -- server.js            # run a specific version
```

### Shell hook

```
eval "$(gale hook fish)"        # in shell config
eval "$(gale hook zsh)"
```

Activates environments on `cd`. Detects `gale.toml` in
the current or parent directory, prepends package paths
to PATH, sets environment variables. Restores previous
state on leave.

## File format

### Global environment (`~/.gale/gale.toml`)

```toml
[packages]
jq = "1.7.1"
ripgrep = "latest"
bat = "latest"
python = "3.12"
```

`gale install` outside a project writes here.

### Project environment (`./gale.toml`)

```toml
[packages]
python = "3.11"
nodejs = "20"
just = "latest"

[vars]
DATABASE_URL = "postgres://localhost/myapp"
FLASK_ENV = "development"
```

`gale install` inside a project writes here. Project
scope shadows global — project `python@3.11` wins over
global `python@3.12`.

### Lock file (`gale.lock`)

Auto-generated. Pins every `latest` to an exact version.
Committed to version control so collaborators get
deterministic environments. Updated by `gale update`.

## Bootstrap

```
curl -fsSL https://gale.dev/install | sh
chezmoi init kelp/dotfiles      # brings ~/.gale/gale.toml
gale sync                       # installs everything
```

Gale reads files. How they reach a new machine is the
user's concern (chezmoi, git, scp, whatever).

## AI update agent

A Claude agent runs on a schedule in GitHub Actions
against the recipes repository:

1. Watch upstream releases for each package
2. Bump version and hash in the recipe TOML
3. Trigger a build, verify it passes
4. Push to `unstable` branch automatically
5. Stable branch = recipes where the build passed CI

The TOML recipe format was chosen partly because
structured data is safer for AI manipulation than shell
scripts.

## Terminal output

Borrow vibeutils' colored help and output patterns:

- Syntax-highlighted flags and subcommands in `--help`
- Colored status output (install progress, environment
  activation, errors)
- Smart terminal detection (NO_COLOR, 256-color, truecolor)
- Graceful degradation to plain text in pipes and dumb
  terminals
- Share vibeutils' terminal/color library if possible, or
  port the approach

The CLI should feel polished out of the box.

## Platform support

- macOS aarch64 (primary)
- Linux aarch64, x86_64 (secondary)
- Darwin framework linking handled per-recipe

## Open questions

- Exact binary cache format and hosting (GitHub Releases
  vs S3 vs custom)
- Lock file format (TOML? separate format?)
- Dependency resolution strategy for runtime deps
- Sandboxing for source builds
- Whether to add content-addressed hash suffix for local
  custom builds
