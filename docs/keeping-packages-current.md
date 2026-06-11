# Keeping Packages Current

Gale pins exact versions in `gale.toml`. Updates are
explicit, never automatic.

## Check for Updates

```sh
gale outdated
```

Lists every package in your manifest that has a newer
version available in the registry.

## Update a Specific Package

```sh
gale update jq
```

Downloads and installs the latest version, updates
`gale.toml` and `gale.lock`, and rebuilds the current
generation.

## Update All Packages

```sh
gale update
```

Updates every package in the manifest to its latest
version.

## Roll Back to a Specific Version

Bad release? Switch back to a known-good version:

```sh
gale switch gh 2.89.0
```

`switch` works for downgrades and upgrades alike. It updates
`gale.toml` and `gale.lock`, installs the requested version
(cache-hit if it's already in the store from a prior
install), and rebuilds the generation. The package must
already be in `gale.toml`; `switch` will not add new
packages — use `gale install` for that.

Unlike `gale update`, `switch` ignores `[pinned]`: an
explicit switch is the user's choice and overrides the pin
guard.

The `gale switch gh@2.89.0` form is accepted too, for
consistency with `gale install`.

## Bump Pins Without Installing

To split the workflow — review the new versions before
touching the store — use `--no-install`:

```sh
gale update --no-install      # rewrite gale.toml pins
git diff gale.toml            # review the bumps
gale sync                     # install what gale.toml says
```

`--no-install` writes new versions to `gale.toml` but
does not build, install, update `gale.lock`, or rebuild
the generation. The follow-up `gale sync` does all of
those based on the new pins. This is useful in shared
projects where a PR that bumps pins is reviewed
separately from the install.

## Preview Changes

Before running sync on a modified manifest, preview
what would change:

```sh
gale sync --dry-run
```

Shows packages that would be added, removed, or
changed in version. No files are modified.

## Clean Up Old Versions

After updates, previous versions remain in the store.
Remove versions not referenced by any `gale.toml`:

```sh
gale gc
```

Preview what would be removed without deleting:

```sh
gale gc --dry-run
```

Gale keeps a machine-local registry of projects at
`~/.gale/projects`, filled in automatically whenever a
project environment is used (direnv activation,
`gale sync`, project-scoped installs). `gale gc`
retains every registered project's pins and active
generation, so a gc run from your home directory or
one project cannot sweep store versions another
project still links. The dry run lists which projects
contributed retention; registry entries whose project
directory has vanished are pruned on each real gc run.

## Workflow

A typical update session:

```sh
gale outdated          # see what's available
gale update            # update everything
gale gc --dry-run      # preview cleanup
gale gc                # remove old versions
```

For project environments, commit the updated
`gale.toml` and `gale.lock` so the team gets the
same versions on their next `gale sync`.
