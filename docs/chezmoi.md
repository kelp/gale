# Using Gale with [Chezmoi](https://www.chezmoi.io/)

Gale's global config lives in `~/.gale/gale.toml`.
This file declares every tool you want on every
machine. Chezmoi can track it alongside your other
dotfiles, giving you a single `chezmoi apply` to
restore your toolset on a new machine.

## Add gale.toml to chezmoi

```sh
chezmoi add ~/.gale/gale.toml
```

This copies `~/.gale/gale.toml` into your chezmoi
source directory. Future edits go through chezmoi.

## Optionally track config.toml

`~/.gale/config.toml` holds settings like the
registry URL. If you customize it, track it too:

```sh
chezmoi add ~/.gale/config.toml
```

## Do not track gale.lock

`~/.gale/gale.lock` records SHA256 hashes for
installed packages. These hashes differ between
platforms (macOS arm64 vs Linux amd64), so the
lockfile is machine-specific. Do not add it to
chezmoi. Gale regenerates it on `gale sync`.

## New machine setup

1. Install gale.
2. Run `chezmoi apply` to place `gale.toml` (and
   `config.toml` if tracked) into `~/.gale/`.
3. Run `gale sync`.

```sh
chezmoi apply
gale sync
```

Gale reads `~/.gale/gale.toml`, installs every
listed package, and builds a generation with
symlinks in `~/.gale/current/bin/`.

## Editing workflow

Edit the source file through chezmoi, apply, then
sync:

```sh
chezmoi edit ~/.gale/gale.toml
chezmoi apply
gale sync
```

Or edit `~/.gale/gale.toml` directly. Use
`gale install` or `gale remove` to modify it, then
push changes back to chezmoi:

```sh
gale install fd
chezmoi add ~/.gale/gale.toml
```

## Example gale.toml

```toml
[packages]
  jq = "1.8.1"
  fd = "10.4.2"
  ripgrep = "14.1.1"
  direnv = "2.36.0"
  just = "1.48.0"
```

This file is the source of truth for your global
tools. Chezmoi ensures it reaches every machine.
`gale sync` ensures every machine matches it.

## Different packages per machine

When some tools belong on your laptop but not your
server (or vice versa), use `[hosts.<name>]`
sections. Gale auto-selects the section matching the
current hostname.

```toml
[packages]
  jq = "1.8.1"
  ripgrep = "14.1.1"

[hosts.my-mac.packages]
  fzf = "0.50"
  mas = "1.8.6"

[hosts.my-server.packages]
  htop = "3.0"
  tmux = "3.5"
```

Same chezmoi-tracked file on every machine. `gale
sync` on `my-mac` installs jq, ripgrep, fzf, mas.
On `my-server`, jq, ripgrep, htop, tmux.

If your system hostname doesn't match what you want
to call the machine, set `GALE_HOST` in your shell:

```sh
export GALE_HOST=my-mac
```

Add packages to a host section with `--host`:

```sh
gale add fzf --host current
```

See [configuration](configuration.md) for the full
reference.

## Replacing `gale remote`

Earlier versions of gale shipped a `gale remote`
command that scp'd your config to a remote host and
ran `gale sync` over SSH. With chezmoi + per-host
sections, this is unnecessary. Your config reaches
the remote machine through normal dotfile
synchronization, and the remote runs its own
`gale sync`:

```sh
ssh server gale sync
```

Compose with whatever else you need — no special
flags or remote-aware tool required.
