# Design

## What Gale Is

Gale is a package manager for developer CLI tools.
It replaces three tools with one:

- **Homebrew** — global package installation
- **Nix / home-manager** — declarative package manifests
- **direnv + Nix flakes** — per-project environments

Gale is inspired by Nix and Homebrew but is not a clone
of either. It takes the best ideas from both — Nix's
declarative model, Homebrew's simplicity — and refines
them into something smaller and more opinionated.

## Principles

**Everything from source.** Every recipe defines how
to build from source. Prebuilt binaries (GHCR cache)
are an optimization, not a substitute. If someone
wants to verify what they're running, they can build
it themselves. This is the model Homebrew and Nix use
at distro scale.

**Prebuilt binaries only for compiler bootstraps.**
Building Go requires a Go compiler. Building Rust
requires a Rust compiler. These bootstrap binaries
are the one exception — a prebuilt binary used only
during the build, never shipped to users.

**Declarative over imperative.** The state of your
environment is a function of gale.toml, not a history
of commands you ran. `gale sync` always converges to
the correct state.

**One tool.** Gale replaces Homebrew (global packages),
Nix/home-manager (declarative manifests), and
direnv+flakes (per-project environments). Users should
not need multiple package managers.

## Directory Layout

```
~/.gale/
  gale.toml       Package manifest (source of truth)
  config.toml     Settings (registry URL, API keys)
  current → gen/2 Symlink to active generation
  gen/            Generation snapshots
    2/bin/        Symlinks into pkg/
  pkg/            Package store (immutable)
    jq/1.8.1/
    fd/10.4.2/
  README.md       Auto-generated, explains this layout
```

## Terminology

**Store** (`pkg/`): where package contents live. Each
version gets its own directory. Once installed, a store
entry is never modified — only deleted when the package
is removed. Inspired by the Nix store, but simpler:
no content-addressing, just `name/version/`.

**Generation** (`gen/`): a numbered snapshot of symlinks
pointing into the store. "Gen" is short for generation.
Each gen directory contains `bin/`, and eventually
`lib/` and `man/`. Generations are cheap to create and
disposable — only the one pointed to by `current`
matters.

**Current** (`current`): a symlink to the active gen.
This is what users put on PATH: `~/.gale/current/bin`.
Swapping `current` to a new gen atomically updates the
entire environment. Inspired by Nix generations, but
using human-friendly incrementing integers (1, 2, 3)
instead of content hashes.

## Why Generations

The old model: each `gale install` and `gale remove`
added or removed individual symlinks in `~/.gale/bin/`.
This was imperative — the bin directory was a history
of mutations. If something went wrong, you couldn't
tell what state it should be in.

The new model: the bin directory is a **function of
gale.toml**. Read the manifest, build a gen directory,
swap the symlink. Idempotent, predictable, recoverable.
Run `gale sync` and you always get the right state.

## Atomic Swap

Updating the environment is a single `os.Rename` call:

1. Build `gen/<N>/bin/` with symlinks to the store
2. Create temp symlink: `current-new → gen/<N>`
3. `os.Rename("current-new", "current")` — atomic
4. Delete `gen/<N-1>/`

Step 3 is one syscall. PATH never sees a broken or
partially-built state.

## Global vs Project

Global and project environments use the same model:

```
# Global
~/.gale/gale.toml    → ~/.gale/current/bin/
~/.gale/gen/2/bin/jq → ~/.gale/pkg/jq/1.8.1/bin/jq

# Project
./gale.toml          → ./.gale/current/bin/
./.gale/gen/1/bin/jq → ~/.gale/pkg/jq/1.7.1/bin/jq
```

Both read a gale.toml manifest and produce a gen
directory with symlinks. Project symlinks point into
the central store in `~/.gale/pkg/` — so moving a
project directory doesn't break anything (the `.gale/`
dir inside is gitignored and rebuilt on `gale sync`).

## Environment Activation

**Global**: add `~/.gale/current/bin` to PATH in your
shell config. Done.

**Project**: direnv. `gale init` creates a `.envrc`
with `use gale`. When you `cd` into the project,
direnv runs `gale sync` and adds `.gale/current/bin`
to PATH. When you leave, direnv restores PATH.

**CI / scripts**: `eval "$(gale env)"` prints the
right `export PATH=...` for the current directory.

We chose direnv over custom shell hooks because:
- direnv is battle-tested and handles PATH restoration
- One mechanism for all shells (no fish/zsh/bash hooks)
- Users already know direnv
- Less code for us to maintain

## Install Flow

`gale install jq`:

1. Fetch recipe from registry (GitHub raw URL)
2. Install to store: try prebuilt binary from GHCR
   first, fall back to building from source
3. Add `jq = "1.8.1"` to gale.toml
4. Rebuild generation from gale.toml

The installer only writes to the store. It knows
nothing about generations or symlinks. The command
layer handles the generation rebuild.

## Build Environment

Source builds run in a clean shell with minimal PATH
to avoid interference from nix coreutils or other
non-standard tools. Build tools (go, cargo, rustc)
are resolved from the host via `exec.LookPath` and
symlinked individually into a temp directory — so
only the specific binary is available, not everything
else in its parent directory.

This prevents nix vibeutils (`ls`, `mv`, etc.) from
leaking into autotools configure checks.

## Static Linking

Gale prefers static linking for all CLI tools.

**Why not shared libraries?** The traditional benefits
— smaller binaries, shared memory pages, patch-once
security updates — assume a mutable OS with long-lived
installs. That model is fading:

- **Containers killed shared memory savings.** Each
  container has its own filesystem namespace. Even
  identical shared libraries in different containers
  are different inodes — no page sharing across
  containers. Most containers run one process anyway.
- **Immutable deployments killed patch-once.** Whether
  you're rebuilding a container image or a static
  binary, it's a new artifact either way. The
  "update one .so, fix everything" benefit assumes
  mutable systems.
- **Disk is cheap.** A 20MB static binary vs 2MB
  dynamic + 18MB of shared libs is the same disk
  cost. The simplicity tradeoff is worth it.

**Where shared libs still make sense:** libc (kernel
interface), graphics frameworks (Cocoa, GTK, libGL),
and OS-level services (PAM, NSS). You can't
statically link the window system.

**Gale's policy:** static by default. The generation
model supports `lib/` and `include/` symlinks, and
`FixupBinaries` rewrites dylib paths with
`install_name_tool` (macOS) or `patchelf` (Linux)
for packages that need dynamic linking. But for
developer CLI tools, static is simpler and more
portable.

For autotools projects (like jq): `--disable-shared
--enable-all-static`. Rust and Go produce static
binaries by default.

## Two-Repo Architecture

- **gale** — the CLI tool. Go code, all packages.
- **gale-recipes** — recipe TOML files. CI builds
  each recipe on macOS arm64 and Linux amd64, pushes
  tar.zst to GHCR, updates binary sections.

Recipes are fetched on demand from GitHub raw URLs.
No git clone needed for installation.

## Bootstrap

Fresh machine setup:

```bash
# Install gale binary
# Add to .zshrc:
export PATH="$HOME/.gale/current/bin:$PATH"
eval "$(gale hook direnv)"
# Copy manifest from dotfiles:
cp ~/dotfiles/gale.toml ~/.gale/gale.toml
# Install everything:
gale sync
```

After sync, direnv is available (it's a gale package),
and project environments activate on `cd`.
