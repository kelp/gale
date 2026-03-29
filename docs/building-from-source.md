# Building from Source

Gale can build any package from source. Every recipe
defines build steps, so you always have this option
even when prebuilt binaries exist.

## When to Build from Source

Three common reasons:

- **Unreleased version.** You need a fix that landed
  after the latest release.
- **Custom patch.** You modified the source and want
  to install your version.
- **No prebuilt binary.** The recipe has no binary
  for your platform.

## From a Local Directory

Build and install a package from source on disk:

```sh
gale install myproject --source .
```

The `--source` flag accepts any directory path. Gale
reads the recipe (from the registry or a `--recipe`
file), builds in `~/.gale/tmp/`, and installs the
result to the store.

Combine with `--recipe` to use a local recipe file:

```sh
gale install myproject --source ./src --recipe myproject.toml
```

### Version Numbering

Local source builds derive their version from
`git describe` in the source directory. The format
is semver-compliant:

| Git state                | Version               |
|--------------------------|-----------------------|
| Exactly on tag `v0.2.0`  | `0.2.0`               |
| 7 commits ahead of tag   | `0.2.0-dev.7+5395b8f` |
| No tags in repo           | `0.0.0-dev+5395b8f`   |

This means each build gets a unique version. Gale
stores them side by side in the package store without
overwriting released versions.

## From Git HEAD

Clone a repository and build from the latest commit:

```sh
gale install myproject --git
```

Gale clones the repo specified in `source.repo` of the
recipe, builds from HEAD, and installs the result. The
version is the short git hash of HEAD.

Use `--recipe` to point at a local recipe file:

```sh
gale install myproject --git --recipe myproject.toml
```

## Building Without Installing

The `gale build` command builds a recipe and produces
a `tar.zst` archive in the current directory. It does
not install anything.

```sh
gale build recipes/j/jq.toml
```

Output:

```
archive: jq-1.8.1.tar.zst
sha256:  839a6fb89610eba4e06ba6...
```

### Build from Git

Clone and build instead of downloading the source
tarball:

```sh
gale build --git recipes/j/jq.toml
```

### Resolve Dependencies Locally

When working in the gale-recipes repository, use
`--local` to resolve build dependencies from the
sibling directory instead of the registry:

```sh
gale build --local recipes/f/fd.toml
```

If the recipe path is inside a `recipes/` directory,
gale detects this automatically and resolves
dependencies locally without the flag.

## Build Dependencies

Gale handles build dependencies automatically. If a
recipe declares `[dependencies.build]`, gale installs
those packages first and adds their `bin/` directories
to the build PATH.

For example, fd requires Rust:

```toml
[dependencies]
build = ["rust"]
```

When you run `gale build recipes/f/fd.toml`, gale
installs Rust (if not already in the store), then
runs the build steps with `cargo` available on PATH.

## Build Environment

Build steps run in a clean shell. Gale sets these
environment variables:

| Variable     | Value                              |
|--------------|------------------------------------|
| `${PREFIX}`  | Install destination directory      |
| `${VERSION}` | Package version                    |
| `${JOBS}`    | CPU count for parallel builds      |
| `${OS}`      | Operating system (e.g., `darwin`)  |
| `${ARCH}`    | Architecture (e.g., `arm64`)       |
| `${PLATFORM}`| `${OS}-${ARCH}` (e.g., `darwin-arm64`) |

The PATH is minimal: only the system essentials and
any build dependency `bin/` directories. This prevents
tools from the host environment (like nix coreutils)
from interfering with configure scripts.

Builds run in `~/.gale/tmp/`. Source tarballs are
cached in `~/.gale/cache/`.
