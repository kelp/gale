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

## Preview Changes

Before running sync on a modified manifest, preview
what would change:

```sh
gale diff
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
