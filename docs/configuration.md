# Configuration

Gale uses two config files:

- `gale.toml` — package manifest (global or project)
- `config.toml` — application settings

## gale.toml

Declares packages, pinned versions, and environment
variables. Lives at `~/.gale/gale.toml` (global) or
`./gale.toml` (project).

```toml
[packages]
  go = "1.26.1"
  jq = "1.8.1"
  just = "1.48.0"

[vars]
  CGO_ENABLED = "0"
  GOFLAGS = "-mod=vendor"
```

### `git = "system"`

A top-level note, not a package pin. Write it
before the first table header (`[packages]`).
Gale ignores the key. It means assume `git` is
on PATH; do not fetch it. No store entry, no
lock hash.

Do not put `git = "system"` under `[packages]`
or `[hosts.<key>.packages]`. That is a pin named
`git` at version `system`, and not-in-index
refuses. Use the OS copy or `brew install git`.
Gale does not run brew.

### `[packages]`

Maps package names to pinned versions. `gale sync`
installs every listed package at the declared version.

### `[vars]`

Environment variables exported when the environment
activates. Direnv exports these via `use_gale`.
`gale env` prints them. `gale env --vars-only`
prints only variables, not PATH.

### `[bin]`

Leftover. Ignored. Two packages shipping the same
basename refuse the generation. Remove one package.
There is no table that names a winner.

`[hosts.<key>.bin]` is leftover the same way.

`bin/` is the only namespace gale arbitrates. Man
pages and root-level files are **not arbitrated**:
the rebuild links the first package in sorted
order, as it always has.
There is deliberately no `[man]` table. Two packages
shipping `man/man1/foo.1` is an ordinary setup — a
library and its CLI, a compat shim — and refusing it
would reject installations that have always been
correct. A shadowed man page shows the wrong docs; a
shadowed executable runs the wrong program. Remove one
provider to change which copy wins.

### `[hosts.<key>.packages]`

Leftover. Refused. Move pins into `[packages]`
and delete the `[hosts.*]` tables. There is no
`--host` flag. Multi-machine setups use a second
file (chezmoi, git).

## config.toml

Application settings. Lives at `~/.gale/config.toml`.

```toml
[build]
debug = false

[anthropic]
api_key = "sk-ant-..."
prompt_file = "~/.gale/recipe-prompt.md"
```

### `[build]`

| Field | Default | Description |
|-------|---------|-------------|
| `debug` | `false` | Build with debug flags (`-O0 -g`) instead of release flags (`-O2`) |

CLI `--debug` and `--release` flags override this.
Recipe `build.debug = true` overrides config but
not CLI flags.

### `[anthropic]`

Leftover. `gale create-recipe` is gone. The keys
are ignored.

The recipe registry URL is compiled in. Leftover
`[registry] url` and `[sync] parallelism` keys are
ignored. `GALE_JOBS` is ignored. A config file cannot
repoint resolution or change install order.

### `[[repos]]`

Leftover tap list. `gale repo *` is gone. Remaining
commands that still resolve recipes (`outdated`,
`gc`, `migrate`) may still read these entries.

```toml
[[repos]]
name = "mytap"
url = "https://github.com/me/gale-tap.git"
priority = 1

[[repos]]
name = "experiments"
url = "https://github.com/me/gale-experiments.git"
priority = 5
```

| Field | Default | Description |
|-------|---------|-------------|
| `name` | (required) | Local cache directory name under `~/.gale/repos/` |
| `url` | (required) | Git URL of a leftover tap |
| `priority` | `0` | Lower number wins. Ties resolve by config order |

`gale install <pkg>` walks repos in priority order
(lowest number first) and returns the first hit. If
no configured repo has the recipe, the default
registry is consulted last. Repos whose cache
directory is missing (e.g. clone failed, or removed
manually) are silently skipped — the resolver does
not block the install. Versioned fetches
(`gale install pkg@1.2.3`) still go through the
registry; taps don't yet expose a per-version API.

For binary-first installs from a tap, recipes must
declare an inline `[binary.<platform>]` section —
auto-deriving a per-tap GHCR base from the repo URL
is not yet wired up. Tap recipes without inline
binaries fall back to source build.

## Lockfile (gale.lock)

Written by `gale install`, `gale update`, `gale remove`,
and `gale lock`. **`gale sync` never writes it**: sync
installs what the lock already names, so a sync that
rewrote the lock could not also enforce it. Records the
exact version, hash, and dependency edges of every
package in the closure. Do not edit manually.

Platform is a dimension inside the file, one artifact
entry per GOOS/GOARCH, so neither lockfile is
inherently machine-specific. Commit the project
lockfile (`./gale.lock`). For the global one
(`~/.gale/gale.lock`), see
[chezmoi.md](chezmoi.md#tracking-galelock).

Schema, enforcement model and remedies:
[lockfile.md](lockfile.md).

## Precedence

For build debug mode:

1. CLI flag (`--debug` / `--release`)
2. Recipe setting (`build.debug = true`)
3. Config setting (`[build] debug = true`)
4. Default (release)
