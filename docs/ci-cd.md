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
records the version and checksum of each package
listed in `gale.toml`. Transitive dependencies are not
recorded.

Today it is a record, not a control: `gale sync`
installs from the current recipe and rewrites the
lockfile to match, so a changed upstream artifact is
accepted rather than rejected. Do not rely on
`gale.lock` for supply-chain integrity yet. What does
hold is version selection: pin exact versions in
`gale.toml` and every runner installs those versions.

`gale audit` and `gale verify` do not close this gap.
Both read their expected value from the same lockfile,
and neither inspects the installed store directory:
`audit` rebuilds from source and compares against the
lockfile's SHA256, and `verify` checks the Sigstore
attestation for the manifest digest the lockfile
names.

Enforcement is being added in
[issue #182](https://github.com/kelp/gale/issues/182).

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
