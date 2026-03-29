# CI/CD

Use gale in CI pipelines to get the same tool versions
your team uses locally.

## Install Gale

```sh
curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
```

This installs the gale binary to `~/.gale/current/bin`.

## Sync and Activate

```sh
gale sync
eval "$(gale env)"
```

`gale sync` reads `gale.toml` from the repository root
and installs every package at its pinned version.
`gale env` prints the PATH export for the current
directory.

## Lockfile

Commit `gale.lock` alongside `gale.toml`. The lockfile
pins exact versions and checksums. `gale sync` uses
the lockfile when present, so every CI run installs
identical binaries.

## Caching

Gale stores downloads in `~/.gale/cache/` and installed
packages in `~/.gale/pkg/`. Cache these directories in
CI to avoid redundant downloads:

```yaml
- uses: actions/cache@v4
  with:
    path: |
      ~/.gale/cache
      ~/.gale/pkg
    key: gale-${{ hashFiles('gale.lock') }}
```

## GitHub Actions Example

```yaml
name: CI
on: [push, pull_request]

jobs:
  build:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4

      - name: Cache gale packages
        uses: actions/cache@v4
        with:
          path: |
            ~/.gale/cache
            ~/.gale/pkg
          key: gale-${{ hashFiles('gale.lock') }}

      - name: Install gale
        run: |
          curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
          echo "$HOME/.gale/current/bin" >> $GITHUB_PATH

      - name: Install project tools
        run: gale sync

      - name: Activate environment
        run: echo "$(gale env)" >> $GITHUB_ENV

      - name: Build
        run: just build
```

## Linux CI

Gale supports macOS (arm64, amd64) and Linux (amd64).
The same `gale sync` works on both platforms. Recipes
define per-platform build steps and binary URLs.
