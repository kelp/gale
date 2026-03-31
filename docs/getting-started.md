# Getting Started

Set up gale from scratch. Five minutes to a working
environment.

## Install Gale

Pick one:

```sh
curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
```

```sh
brew install kelp/tap/gale
```

## Add to PATH

Add this line to your shell config (`~/.zshrc`,
`~/.bashrc`, or `~/.config/fish/config.fish`):

```sh
export PATH="$HOME/.gale/current/bin:$PATH"
```

Open a new terminal or source your config:

```sh
source ~/.zshrc
```

## Install a Package

```sh
gale install jq
```

Gale fetches the recipe, downloads a prebuilt binary
(or builds from source), and symlinks it into your
PATH. Verify:

```sh
jq --version
```

## Set Up Global Packages

Your global manifest lives at `~/.gale/gale.toml`.
Each `gale install` adds an entry. You can also edit
it directly:

```toml
[packages]
  jq = "1.8.1"
  fd = "10.4.2"
  ripgrep = "14.1.1"
```

After editing, run sync to install everything:

```sh
gale sync
```

Sync reads the manifest and installs any missing
packages at their pinned versions. It is idempotent.
Run it as many times as you like.

## The Store and Garbage Collection

Gale keeps every installed version in the store at
`~/.gale/pkg/`. When you update a package, the old
version stays until you clean up:

```sh
gale gc
```

Garbage collection removes any version not referenced
by a `gale.toml` (global or project). It is safe to
run at any time.

Build dependencies — packages installed temporarily to
compile another package from source — are not declared
in `gale.toml`. This means `gale gc` removes them. The
next build that needs them will reinstall them
automatically.

If a build dependency is something you use directly
(like Go for a Go project), add it to `gale.toml` so
gc keeps it:

```sh
gale install go
```

## Verify Setup

```sh
gale doctor
```

Doctor checks your PATH, config files, and store.
Fix anything it reports before continuing.

## Project Environments (Optional)

Gale integrates with direnv for per-project tool
isolation. A project gets its own `gale.toml` with
pinned versions that activate when you `cd` into
the directory.

### One-time direnv setup

Add the gale hook to your direnvrc:

```sh
# ~/.config/direnv/direnvrc
eval "$(gale hook direnv)"
```

If you do not have direnv installed yet:

```sh
gale install direnv
```

### Create a project environment

Inside a project directory:

```sh
gale init
```

This creates `gale.toml` and `.envrc`. Allow direnv:

```sh
direnv allow
```

Install project-specific tools:

```sh
gale install go
gale install just
```

These go into the project's `gale.toml`, not the
global one. Anyone who clones the repo runs
`gale sync` and gets the same versions.

## Next Steps

- `gale list` shows packages in the current manifest.
- `gale update` upgrades packages to the latest version.
- `gale search <query>` finds available packages.
- `man gale` has the full command reference.
