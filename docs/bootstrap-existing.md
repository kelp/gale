# Bootstrap a New Machine

Reproduce your gale environment on a new machine using
an existing `gale.toml`. One install, one sync.

## Install Gale

```sh
curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh
```

Or:

```sh
brew install kelp/tap/gale
```

## Add to PATH

Add to your shell config (`~/.zshrc`, `~/.bashrc`,
or `~/.config/fish/config.fish`):

```sh
export PATH="$HOME/.gale/current/bin:$PATH"
```

Source it or open a new terminal.

## Restore Your Manifest

Copy your existing `gale.toml` into gale's config
directory:

```sh
mkdir -p ~/.gale
cp /path/to/your/gale.toml ~/.gale/gale.toml
```

If your manifest lives in a dotfiles repo:

```sh
cp ~/dotfiles/gale.toml ~/.gale/gale.toml
```

Or symlink it:

```sh
ln -s ~/dotfiles/gale.toml ~/.gale/gale.toml
```

## Install Everything

```sh
gale sync
```

Sync reads `~/.gale/gale.toml`, installs every
package at its pinned version, and rebuilds the
generation. After sync completes, all your tools
are on PATH.

Verify:

```sh
gale doctor
```

## Project Repositories

For projects that have their own `gale.toml`, clone
and sync:

```sh
git clone https://github.com/you/project
cd project
gale sync
```

If you use direnv, add the gale hook once:

```sh
# ~/.config/direnv/direnvrc
eval "$(gale hook direnv)"
```

Then direnv handles sync automatically. When you
`cd` into a project with a `.envrc` containing
`use gale`, direnv runs `gale sync` and activates
the project environment. No manual sync needed.

## Managing gale.toml Across Machines

Any dotfile manager works. The file is plain TOML
with no machine-specific values.

If you use [chezmoi](https://www.chezmoi.io/), add
the manifest to your source directory:

```sh
chezmoi add ~/.gale/gale.toml
```

Chezmoi then deploys it on every machine where you
run `chezmoi apply`. Other dotfile managers (stow,
yadm, bare git repos) work the same way -- copy or
link the file into `~/.gale/`.

## Summary

The full bootstrap sequence:

```sh
# 1. Install gale
curl -fsSL https://raw.githubusercontent.com/kelp/gale/main/scripts/install.sh | sh

# 2. Add to PATH (add to shell config permanently)
export PATH="$HOME/.gale/current/bin:$PATH"

# 3. Place your manifest
cp ~/dotfiles/gale.toml ~/.gale/gale.toml

# 4. Install everything
gale sync

# 5. Verify
gale doctor
```
