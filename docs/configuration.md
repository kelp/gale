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

[pinned]
  go = true

[vars]
  CGO_ENABLED = "0"
  GOFLAGS = "-mod=vendor"
```

### `[packages]`

Maps package names to pinned versions. `gale sync`
installs every listed package at the declared version.

### `[pinned]`

Packages listed here are skipped by `gale update`.
Use `gale pin <pkg>` and `gale unpin <pkg>` to
manage.

### `[vars]`

Environment variables exported when the environment
activates. Direnv exports these via `use_gale`.
`gale env` prints them. `gale env --vars-only`
prints only variables, not PATH.

### `[hosts.<key>.packages]` and `[hosts.<key>.pinned]`

Per-machine overlays. Top-level `[packages]` and
`[pinned]` apply on every machine. Host sections add
or override entries when the local hostname matches
`<key>`.

The key can be:

- A single hostname: `[hosts.my-mac.packages]`
- A comma-separated list: `[hosts."laptop,desktop".packages]`
- A glob pattern: `[hosts."work-*".packages]`,
  `[hosts."*".packages]`

Use the multi-host or glob forms to share one package
list across machines without duplicating it. Quote
the key when it contains commas or wildcards so TOML
treats it as a single string.

```toml
[packages]
  jq = "1.8.1"
  just = "1.48.0"

[hosts."laptop,desktop".packages]
  fzf = "0.50"

[hosts."work-*".packages]
  slack = "1.0"

[hosts.my-server.packages]
  htop = "3.0"
```

On `laptop`, `gale sync` installs jq, just, and fzf.
On `work-mac`, jq, just, and slack. On `my-server`,
jq, just, and htop. On any unmatched host, just jq
and just.

When the same package appears in multiple sections,
the most specific match wins: exact hostname overrides
a comma-list, which in turn overrides a glob. This is
useful for running a different version of one tool on
one machine while sharing everything else:

```toml
[hosts."laptop,desktop".packages]
  fzf = "0.50"

[hosts.laptop.packages]
  fzf = "0.60"          # laptop only — overrides the multi-host entry
```

`gale install`, `add`, `remove`, `pin`, and `unpin`
default to the shared `[packages]` section. Pass
`--host <name>` (or `--host current` for the local
machine) to write to a host section. The flag is
opt-in: users with a single machine can ignore it.

```sh
gale install fzf --host current      # install + record under [hosts.<this-host>.packages]
gale add fzf --host current
gale remove htop --host my-server    # edit another machine's section
```

Bare `gale install fzf` writes to shared
`[packages]`, except when the package already lives
in the current host's overlay — then it updates in
place. That way reinstalling a host-scoped tool
without remembering the flag does not silently move
it to shared.

When the same package appears in both shared
`[packages]` and a matching host overlay, **the host
overlay wins** — the shared value is dead config on
that machine. `gale list` flags shared entries with
`(overridden by host)` and `gale doctor` reports the
shadow so you can clean up if it was unintentional.

The active hostname comes from `hostname(1)`. Override
with the `GALE_HOST` environment variable for cases
where the system hostname is wrong or you want a
short identifier:

```sh
export GALE_HOST=my-mac
```

`gale list` groups output by scope when host
overlays are present. Use `--scope shared|host|all`
(default `all`) to filter:

```sh
gale list                # both sections
gale list --scope shared # shared [packages] only
gale list --scope host   # current host's overlay only
```

This composes naturally with [chezmoi](chezmoi.md):
one `gale.toml` tracked across machines, each
machine reads its own section.

## config.toml

Application settings. Lives at `~/.gale/config.toml`.

```toml
[build]
debug = false

[anthropic]
api_key = "sk-ant-..."
prompt_file = "~/.gale/recipe-prompt.md"

[registry]
url = "https://raw.githubusercontent.com/kelp/gale-recipes/main"
```

### `[build]`

| Field | Default | Description |
|-------|---------|-------------|
| `debug` | `false` | Build with debug flags (`-O0 -g`) instead of release flags (`-O2`) |

CLI `--debug` and `--release` flags override this.
Recipe `build.debug = true` overrides config but
not CLI flags.

### `[anthropic]`

| Field | Default | Description |
|-------|---------|-------------|
| `api_key` | (none) | Anthropic API key for `gale create-recipe` |
| `prompt_file` | (none) | Path to a file appended to the recipe creation system prompt |

The prompt file is read on every invocation. Changes
take effect without rebuilding gale. Use `~/` prefix
for home-relative paths.

### `[registry]`

| Field | Default | Description |
|-------|---------|-------------|
| `url` | GitHub raw URL | Override the recipe registry base URL |

### `[[repos]]`

Recipe repositories ("taps") consulted by the install
resolver before falling back to the default registry.
Managed via `gale repo add <name> <url>` /
`gale repo list` / `gale repo remove <name>`.

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
| `url` | (required) | Git URL cloned by `gale repo add` |
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

Auto-generated by `gale install`, `gale update`, and
`gale sync`. Records exact version and SHA256 hash
per package. Do not edit manually.

Global lockfile (`~/.gale/gale.lock`) is
machine-specific — do not commit or sync across
machines. Project lockfile (`./gale.lock`) should
be committed to version control.

## Precedence

For build debug mode:

1. CLI flag (`--debug` / `--release`)
2. Recipe setting (`build.debug = true`)
3. Config setting (`[build] debug = true`)
4. Default (release)
