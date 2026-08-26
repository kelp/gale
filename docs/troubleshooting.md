# Troubleshooting

## Run Doctor First

```sh
gale doctor
```

Doctor checks PATH, that the lock is readable, that
the generation matches lock roots, and that tree
digests match.
Fix everything it reports before investigating further.

## Common Issues

### Command not found after install

Your PATH does not include the gale bin directory. Add
this to your shell config (`~/.zshrc`, `~/.bashrc`, or
`~/.config/fish/config.fish`):

```sh
export PATH="$HOME/.gale/current/bin:$PATH"
```

Open a new terminal or source your config file.

### Missing packages after clone

A project has a `gale.toml` but the packages are not
installed. Run sync:

```sh
gale sync
```

Sync lands the trees the v2 lock names and rebuilds
the generation. It does not write the lock. If there
is no v2 lock, run `gale lock` or `gale migrate`.

### Broken symlinks

If binaries stop working or point to missing files,
rebuild the generation:

```sh
gale sync
```

Sync recreates the generation directory with fresh
symlinks into the store. This fixes stale or broken
links.

One historical cause was `gale gc` run from outside a
project: it could not see other projects' generations
and swept store versions they still linked. Gale now
records every project in `~/.gale/projects` as a side
effect of normal use, and gc retains all registered
projects' active generations. If a project predates
this (its environment was never activated since
upgrading), one `gale sync` inside the project both
relinks it and registers it.

### Generations left behind by a rollback

`gale generations rollback 5` moves `current` back to
gen 5 and leaves gens 6 and up on disk. The next sync
builds gen 11, not gen 6. That is deliberate: a
generation number permanently identifies one snapshot,
so rolling forward still works and no history is
overwritten.

`gale generations` marks those generations with `+`
and the active one with `*`:

```
  4   12 packages
* 5   12 packages
+ 6   13 packages
+ 7   13 packages
```

Nothing reclaims them on its own until the next rebuild
allocates above the highest number. Automatic retention
then prunes history below the keep-2 cutoff.
`gale gc` keeps current and at most one previous
generation.
Abandoned generations above current are swept.
`gale gc -n` reports what would be removed.

### Package not in the index

```
Error: no such package
```

Gale only installs names the gale-recipes index
documents. There is no source fallback. Admit the
artifact or use another tool.

### Verify reports a digest mismatch

`gale verify` recomputes the tree digest of each
`pkg/fetch/` directory the v2 lock names. A mismatch
means the store is not the locked artifact. Re-fetch
or restore the store; do not treat this as a
source-rebuild check. `gale audit` is gone.

### Direnv not activating

Verify the gale hook is in your direnvrc:

```sh
# ~/.config/direnv/direnvrc
eval "$(gale hook direnv)"
```

Then allow the project:

```sh
direnv allow
```

## Diagnostic Commands

### Find which package provides a binary

```sh
gale which jq
```

Prints the full path and the package that owns it.

### Verify binary attestation

```sh
gale verify jq
```

Checks the Sigstore attestation for the installed
binary. Verification runs in-process; no external
tool is required.

Gale resolves the Sigstore trusted root from the
Sigstore TUF CDN, caching it for a day under
`~/.gale/cache/sigstore-tuf/`. If the network is
unreachable, gale falls back to a trusted-root
snapshot embedded in the binary and prints a
one-time warning.

For air-gapped verification, set
`GALE_SIGSTORE_TRUSTED_ROOT` to a local
`trusted_root.json` to bypass the TUF fetch
entirely.

### Preview sync changes

```sh
gale sync --dry-run
```

Shows what `gale sync` would add, remove, or change
without modifying any files.

### Check installed versions

```sh
gale list
```

Lists every package in the current manifest with its
pinned version.
