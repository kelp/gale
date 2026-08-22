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
records the whole closure — declared packages and their
transitive dependencies — with the checksum of every
artifact, per platform.

It is enforced. `gale sync` installs what the lock names
and refuses anything else; it never rewrites the lock to
match what it found. A runner that resolves a different
artifact fails instead of proceeding. `gale sync
--no-frozen` opts out, installing from recipes without
integrity enforcement.

Full schema, enforcement model and remedies:
[lockfile.md](lockfile.md).

## Exit codes

Gale used only exit 1 before enforcement, so the
taxonomy below is additive. A pipeline can tell
"artifact tampered" from "build broke" without parsing
messages.

| Code | Class | Meaning |
| --- | --- | --- |
| 1 | ordinary failure | build error, network error, usage error |
| 3 | lock integrity violation | artifact SHA, manifest digest, provenance or `graph_digest` mismatch; store-dir provenance conflict; cross-project farm conflict |
| 4 | lock unusable | a lock that is present but cannot be parsed or fully modeled: stale lock; missing package, dependency or platform entry; legacy schema; unknown schema version; malformed downgrade guard; malformed TOML; unknown field |
| 5 | activation drift | the active generation does not match the lock, including carry-forward |

The split that matters is 3 against 4 and 5. **Code 3
means something disagreed with bytes the lock names, and
deserves a human** — never retry it automatically. Codes
4 and 5 mean the lock or the generation needs
regenerating, which a pipeline can handle itself:

```sh
gale sync
case $? in
  0) ;;
  3) echo "gale: lock integrity violation" >&2; exit 3 ;;
  4) gale lock && gale sync || exit $? ;;
  *) exit 1 ;;
esac
```

Code 5 comes from the activation commands — `gale env`,
`gale shell`, `gale run` — when the active generation no
longer matches the lock. Its remedy is `gale sync`.

The same codes come from every command that can reach
these states: `sync`, `install`, `update`, `lock`,
`migrate`, `shell`, `run`, `remove` and `env`.

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
