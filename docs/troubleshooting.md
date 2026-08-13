# Troubleshooting

## Run Doctor First

```sh
gale doctor
```

Doctor checks PATH configuration, config files, the
package store, symlink integrity, and direnv setup.
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

Sync reads the manifest, installs missing packages,
and rebuilds the generation.

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

Nothing reclaims them on its own. `gale gc` skips
everything at or above `current`, and automatic
retention only reaches them once `current` climbs back
past its cutoff. `gale gc -n` reports how many are
being retained.

To discard a branch you abandoned on purpose, name it:

```sh
gale generations remove 6 7
```

The command refuses the current generation and removes
nothing at all when any number in the batch is not a
generation.

### Build failures

Source builds can fail for several reasons:

- **Missing build dependencies.** The recipe lists
  required tools in `[build] deps`. Install them first.
- **Stale source tarball.** Try building from the
  latest source with the `--git` flag:

  ```sh
  gale install <pkg> --git
  ```

- **Platform mismatch.** Some recipes only support
  specific platforms. Check the recipe for platform
  constraints.

#### A locked source build fails on another machine

A source build in `gale.lock` is enforced strictly, and
strict means the artifact must hash to what the lock
records. Source builds are not reproducible today — see
"Audit reports a mismatch" below for why — so a build
locked on one machine may legitimately fail on another.
The failure is real; the mismatch is not evidence of
tampering.

The remedy is to re-lock on the machine that failed:

```sh
gale lock --refresh <pkg>
```

Or pin a version with a prebuilt binary, so the closure
carries an artifact rather than a build.

This is why a committed lock is portable across machines
with certainty **only when its whole closure is binary.**
A source node's output hash feeds the `graph_digest` of
every package above it, so one unreproducible build makes
the digests above it unreproducible too. A closure
containing a source build is portable exactly as far as
that build reproduces. See [lockfile.md](lockfile.md).

### Audit reports a mismatch

`gale audit <pkg>` rebuilds a package from source and
compares the SHA256 against the installed binary. A
mismatch is normal for most packages.

A **match** confirms the build is reproducible — the
installed binary is exactly what the source produces.

A **mismatch** does not indicate tampering. These
sources of non-determinism are not fixable without
Nix-level build isolation:

- **Mach-O LC_UUID.** macOS clang embeds a unique
  UUID in every compiled binary.
- **Libtool .la files.** Contain absolute paths to
  the build temp directory.
- **pkg-config .pc files.** Contain absolute paths
  to the build prefix directory.
- **ar/ranlib timestamps.** Embedded in `.a` static
  archives. `ZERO_AR_DATE=1` helps but does not
  fully solve it on macOS.

These parts of the build output ARE deterministic:

- Archive packaging (zstd compression, tar metadata,
  symlink targets).
- Text files, man pages, shell scripts.
- File sizes and permissions.

`gale audit` currently reads from the project
lockfile. It does not yet support `-g` for auditing
globally installed packages.

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
one-time warning. `gale doctor` reports the cache
state.

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
