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
binary. Requires the `gh` CLI.

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
