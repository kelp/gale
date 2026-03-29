# Project Environments

Gale gives each project its own set of tools at
pinned versions. When you `cd` into a project,
direnv activates the project's environment. When
you leave, it restores your global PATH.

## One-time setup

### Install direnv

```sh
gale install direnv
```

### Add the gale hook to direnvrc

```sh
echo 'eval "$(gale hook direnv)"' \
  >> ~/.config/direnv/direnvrc
```

This defines a `use_gale` shell function that
direnv calls when it encounters `use gale` in a
project's `.envrc`.

## Project setup

Initialize a project:

```sh
cd myproject
gale init
```

`gale init` creates three things:

- `gale.toml` with an empty `[packages]` section
- `.envrc` containing `use gale`
- `.gale/` entry in `.gitignore`

Allow direnv to run the new `.envrc`:

```sh
direnv allow
```

## Add packages

```sh
gale install go@1.26.1
```

Gale asks which scope to use:

```
Install go to [g]lobal or [p]roject? p
```

Choose `p` for project. Gale writes `go = "1.26.1"`
to the project's `gale.toml`, installs Go to the
store, and rebuilds the project generation.

## How it works

When you `cd` into a project with a `.envrc`, direnv
runs the `use_gale` function. That function:

1. Runs `gale sync` to install any missing packages.
2. Adds `.gale/current/bin` to PATH.
3. Exports variables from `[vars]` in gale.toml.

The `.gale/` directory inside the project contains
generations and a `current` symlink, just like the
global `~/.gale/`. Symlinks in `.gale/current/bin/`
point into the central store at `~/.gale/pkg/`. The
store is shared -- installing Go 1.26.1 for one
project makes it available to any other project that
pins the same version.

When you `cd` out, direnv restores your PATH to its
previous state.

## Version scoping

You can run different versions of the same tool in
different contexts.

Global config (`~/.gale/gale.toml`):

```toml
[packages]
  go = "1.24.0"
  jq = "1.8.1"
```

Project config (`~/code/myproject/gale.toml`):

```toml
[packages]
  go = "1.26.1"
  golangci-lint = "2.11.4"
```

In your home directory, Go 1.24.0 is active:

```
~ $ which go
/Users/you/.gale/current/bin/go
~ $ go version
go version go1.24.0 darwin/arm64
```

Enter the project directory and direnv activates the
project's environment:

```
~ $ cd ~/code/myproject
direnv: loading .envrc
direnv: using gale

~/code/myproject $ which go
/Users/you/code/myproject/.gale/current/bin/go
~/code/myproject $ go version
go version go1.26.1 darwin/arm64
```

Leave the project and your global tools return:

```
~/code/myproject $ cd ~
direnv: unloading

~ $ go version
go version go1.24.0 darwin/arm64
```

The project's `golangci-lint` is only available
inside the project directory. Global `jq` is
available everywhere.

## Environment variables

The `[vars]` section in gale.toml defines environment
variables that are exported when the project
environment activates:

```toml
[packages]
  go = "1.26.1"

[vars]
  GOFLAGS = "-mod=vendor"
  CGO_ENABLED = "0"
```

direnv exports these alongside PATH changes. You
can also print them with `gale env`:

```
$ gale env
export PATH="/Users/you/code/myproject/.gale/current/bin:$PATH"
export CGO_ENABLED="0"
export GOFLAGS="-mod=vendor"
```

Or just the variables:

```
$ gale env --vars-only
export CGO_ENABLED="0"
export GOFLAGS="-mod=vendor"
```

## Auto-sync

`gale run` and `gale shell` detect when `gale.toml`
has changed since the last sync and run `gale sync`
automatically before executing. No need to remember
to sync manually after editing the manifest.

direnv's `use_gale` function also syncs on every
directory change.

## Sharing with teammates

Commit `gale.toml` and `gale.lock` to version
control:

```sh
git add gale.toml gale.lock
git commit -m "Pin project tool versions"
```

`gale.lock` records the exact version and SHA256
hash for each package. Unlike the global lockfile,
the project lockfile belongs in git -- it ensures
every developer and CI runner installs identical
artifacts.

A teammate clones the repo, installs gale, and runs:

```sh
gale sync
```

They get the same tool versions. No "works on my
machine" problems.

## CI integration

CI environments typically lack direnv. Use
`gale env` instead:

```sh
eval "$(gale env)"
```

This prints an `export PATH=...` statement that
prepends the project's `.gale/current/bin` to PATH.
It does not run `gale sync` -- install packages
first:

```sh
gale sync
eval "$(gale env)"
go build ./...
```

### GitHub Actions example

```yaml
steps:
  - uses: actions/checkout@v4
  - name: Install gale
    run: |
      curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
      echo "$HOME/.gale/bin" >> "$GITHUB_PATH"
  - name: Set up project tools
    run: |
      gale sync
      eval "$(gale env)"
      echo "$PATH" | tr ':' '\n' | head -1 >> "$GITHUB_PATH"
  - name: Build
    run: go build ./...
```
