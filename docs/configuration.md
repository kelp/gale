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

Maps package names to pinned versions. `gale install`
and `gale update` write the v2 lock from these pins.
`gale sync` activates that lock and does not rewrite
it.

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

Leftover. Lives at `~/.gale/config.toml`. Resolve
verbs talk to the compiled-in index URL, or
`--index <dir>`. `[build]`, `[anthropic]`,
`[registry]`, `[sync]`, and `[[repos]]` are ignored.
`GALE_JOBS` is ignored. `gale create-recipe` and
`gale repo` are gone. A config file cannot repoint
resolution or change install order.

`gale info` may still read leftover `[[repos]]` and
the legacy recipe cache. Resolve verbs do not.

## Lockfile (gale.lock)

Written by `gale install`, `gale update`, `gale remove`,
and `gale lock`. **`gale sync` never writes it**: sync
lands what the lock already names, so a sync that
rewrote the lock could not also enforce it. Records the
URL, `sha256`, and `tree_digest` of every locked
artifact. Do not edit manually.

Platform is a dimension inside the file, one artifact
entry per GOOS/GOARCH, so neither lockfile is
inherently machine-specific. Commit the project
lockfile (`./gale.lock`). For the global one
(`~/.gale/gale.lock`), see
[chezmoi.md](chezmoi.md#tracking-galelock).

Schema, enforcement model and remedies:
[lockfile.md](lockfile.md).

## Precedence

There is no build-debug stack. Fetch is the only
install path. `--debug` and `--release` are gone.
