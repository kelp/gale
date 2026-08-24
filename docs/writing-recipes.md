# Adding Packages to the Index

Gale installs only what the
[gale-recipes](https://github.com/kelp/gale-recipes)
index names. There are no source recipes and no
gale-built bottles. A new package is an index
document plus an admitted artifact.

## Index document

Each package is `index/<letter>/<name>.toml`.

```toml
[package]
name = "just"
description = "Handy way to save and run project-specific commands"
license = "CC0-1.0"
homepage = "https://github.com/casey/just"
repo = "casey/just"
latest = "1.58.0"

[versions."1.58.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.58.0/just-1.58.0-aarch64-apple-darwin.tar.gz"
format = "tar.gz"
sha256 = "50ae3e996c974a0bf32ea7d10f495070df33f1b43e0616b2769e3d4821ed8f48"
tree_digest = "sha256:65e26ab18664bbd4354cc595fc14337d63aad08ebfecf29a587fb82f02e17226"
hash_source = "upstream-sha256sums"
strip = 0

[[versions."1.58.0".artifacts."darwin/arm64".files]]
src = "just"
dest = "bin/just"
mode = 0o755
```

`sha256` is the archive. `tree_digest` is the
extracted tree. `hash_source` records where the
archive hash came from (`upstream-sha256sums` or
`computed`). An entry without a per-platform
`sha256` is invalid.

## Admit and lint

```sh
gale admit <archive>
gale lint index/<letter>/<name>.toml
```

`gale admit` records the artifact. `gale lint`
accepts index documents only. A leftover source
recipe is "not an index document".

Point gale at a local checkout with `--index`:

```sh
gale install just --index ../gale-recipes
```

The checkout must be a git repo. `index.Open` reads
`git show` of HEAD, so uncommitted edits are
invisible.

## What is gone

`gale build`, `--source`, `--git`, `--recipe`, and
`gale create-recipe` are gone. Do not write a
`[build] steps` recipe. Fetch is the only installer.
