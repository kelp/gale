# Writing Recipes

A recipe is a TOML file that tells gale how to build a
package from source. Every recipe lives in the
gale-recipes repository, letter-bucketed by name:
`recipes/j/jq.toml`.

## Recipe Structure

A complete recipe for an autotools project:

```toml
[package]
name = "jq"
version = "1.8.1"
description = "Lightweight and flexible command-line JSON processor"
license = "MIT"
homepage = "https://jqlang.github.io/jq"

[source]
repo = "jqlang/jq"
url = "https://github.com/jqlang/jq/releases/download/jq-1.8.1/jq-1.8.1.tar.gz"
sha256 = "2be64e7129cecb11d5906290eba10af694fb9e3e7f9fc208a311dc33ca837eb0"
released_at = "2025-07-01"

[build]
steps = [
  "./configure --prefix=${PREFIX} --with-oniguruma=builtin --disable-docs --disable-maintainer-mode",
  "make -j${JOBS}",
  "make install",
]

[dependencies]
build = []
```

## Required Fields

Every recipe must have these fields:

- `package.name` -- package name, lowercase
- `package.version` -- semver version string
- `source.url` -- URL to the source tarball
- `source.sha256` -- SHA-256 hash of the tarball
- `build.steps` -- list of shell commands to build

Get the SHA-256 hash of a source tarball:

```sh
curl -sL <url> | shasum -a 256
```

## Optional Fields

**Package metadata:**

- `package.description` -- one-line summary
- `package.license` -- SPDX license identifier
- `package.homepage` -- project URL

**Source metadata:**

- `source.repo` -- GitHub `owner/repo`. Enables
  `gale update` to check for new releases.
- `source.released_at` -- release date (`YYYY-MM-DD`).
  Used for update cooldown.

## Build Steps

Each step runs as a separate `sh -c` command in the
source directory. Steps run sequentially. If any step
exits nonzero, the build fails.

These variables are available in build steps:

| Variable     | Value                              |
|--------------|------------------------------------|
| `${PREFIX}`  | Install destination directory      |
| `${VERSION}` | Package version                    |
| `${JOBS}`    | CPU count for parallel builds      |
| `${OS}`      | Operating system (e.g., `darwin`)  |
| `${ARCH}`    | Architecture (e.g., `arm64`)       |
| `${PLATFORM}`| `${OS}-${ARCH}` (e.g., `darwin-arm64`) |

Install all output to `${PREFIX}`. Binaries go in
`${PREFIX}/bin`. Gale creates symlinks from the
generation directory into this location.

## Build Patterns

**Autotools** (jq, gnumake):

```toml
[build]
steps = [
  "./configure --prefix=${PREFIX}",
  "make -j${JOBS}",
  "make install",
]
```

**Go** (direnv, gofumpt):

```toml
[build]
steps = [
  "mkdir -p ${PREFIX}/bin",
  "go build -o ${PREFIX}/bin/direnv",
]

[dependencies]
build = ["go"]
```

**Cargo** (fd, bat, ripgrep):

```toml
[build]
steps = [
  "cargo install --path . --root ${PREFIX}",
]

[dependencies]
build = ["rust"]
```

Always use `--path .` with `cargo install`. Without
it, cargo fetches from crates.io instead of building
the local source.

**CMake** (cmake self-bootstrap):

```toml
[build]
steps = [
  "./bootstrap --prefix=${PREFIX} --parallel=${JOBS}",
  "make -j${JOBS}",
  "make install",
]
```

## Build Dependencies

List packages required at build time under
`[dependencies.build]`. Gale installs these before
the build and adds their `bin/` directories to the
build PATH.

```toml
[dependencies]
build = ["rust"]
```

Multiple dependencies:

```toml
[dependencies]
build = ["cmake", "pkgconf", "rust"]
```

Runtime dependencies go in `runtime`:

```toml
[dependencies]
build = ["cmake", "pkgconf", "rust"]
runtime = ["dbus", "zlib-ng-compat"]
```

## Per-Platform Build Steps

Some packages need different build steps on different
platforms. Use `[build.<platform>]` sections. The
platform key is `<os>-<arch>` (e.g., `darwin-arm64`,
`linux-amd64`).

Example from the Go recipe, which downloads a
different bootstrap binary per platform:

```toml
[build.darwin-arm64]
steps = [
  "curl -sL -o /tmp/go-bootstrap.tar.gz https://go.dev/dl/go1.25.8.darwin-arm64.tar.gz",
  "mkdir -p /tmp/go-bootstrap && tar -xzf /tmp/go-bootstrap.tar.gz -C /tmp/go-bootstrap --strip-components=1",
  "cd src && GOROOT_BOOTSTRAP=/tmp/go-bootstrap GOROOT_FINAL=${PREFIX} CGO_ENABLED=0 bash make.bash",
  "cp -R bin pkg src api lib misc doc go.env VERSION ${PREFIX}/",
]

[build.linux-amd64]
steps = [
  "curl -sL -o /tmp/go-bootstrap.tar.gz https://go.dev/dl/go1.25.8.linux-amd64.tar.gz",
  "mkdir -p /tmp/go-bootstrap && tar -xzf /tmp/go-bootstrap.tar.gz -C /tmp/go-bootstrap --strip-components=1",
  "cd src && GOROOT_BOOTSTRAP=/tmp/go-bootstrap GOROOT_FINAL=${PREFIX} CGO_ENABLED=0 bash make.bash",
  "cp -R bin pkg src api lib misc doc go.env VERSION ${PREFIX}/",
]
```

When a platform-specific section exists, gale uses
it instead of the default `[build]` section.

## Binary Metadata

Binary metadata lives in a separate `.binaries.toml`
file alongside the recipe. CI populates this file
after building on each platform. You do not write
these by hand.

For example, `jq.binaries.toml`:

```toml
version = "1.8.1"

[darwin-arm64]
sha256 = "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"

[linux-amd64]
sha256 = "a903b0ca428c174e611ad78ee6508fefeab7a8b2eb60e55b554280679b2c07c6"
```

When a user runs `gale install jq`, gale checks for
a matching binary first. If one exists, it downloads
the prebuilt archive from GHCR. If not, it falls back
to building from source using the recipe.

## Validating a Recipe

Lint a recipe file for errors:

```sh
gale lint myrecipe.toml
```

Lint checks required fields, TOML syntax, and
structural correctness. Fix any errors before
building.

## Testing a Recipe

Build the recipe to verify the build steps work:

```sh
gale build myrecipe.toml
```

This produces a `tar.zst` archive in the current
directory. Inspect the contents:

```sh
tmpdir=$(mktemp -d)
tar -xf myrecipe-1.0.0.tar.zst -C "$tmpdir"
ls "$tmpdir/bin/"
"$tmpdir/bin/myrecipe" --version
rm -rf "$tmpdir"
```

Install from your local recipe file to test the full
flow:

```sh
gale install mypkg --recipe myrecipe.toml
```

## Contributing to gale-recipes

1. Create the recipe file in the correct letter
   bucket: `recipes/<first-letter>/<name>.toml`.

2. Get the source SHA-256:

   ```sh
   curl -sL <source-url> | shasum -a 256
   ```

3. Write the recipe with all required fields.

4. Lint:

   ```sh
   gale lint recipes/<letter>/<name>.toml
   ```

5. Build:

   ```sh
   gale build recipes/<letter>/<name>.toml
   ```

6. Test the installed binary.

7. Submit a pull request to the gale-recipes
   repository. CI builds the recipe on macOS ARM64
   and Linux AMD64, generates the `.binaries.toml`
   file, and pushes prebuilt archives to GHCR.
