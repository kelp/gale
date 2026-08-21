# Design

## What Gale Is

Gale is a package manager for developer CLI tools.
It fetches verified artifacts from a signed index,
pins them in a lockfile, and activates them through
atomic generation snapshots. Global tools and
per-project environments share that model. direnv
loads a project generation onto PATH.

Gale is not a Homebrew replacement and not an
everything-from-source build farm. A package that
is not in the index is an error.

## Principles

**Fetch from the index.** `install`, `sync`,
`update`, and `remove` resolve against the catalog
and stage `pkg/fetch/<name>/<version>-<sha12>/`.
`--index <dir>` is the only local override. A mixed
source/fetch lock is refused. `gale fetch-adopt`
migrates a v1 lock.

**Declarative over imperative.** The state of your
environment is a function of the v2 lock, not a
history of commands you ran. `gale.toml` declares
what to lock; a locked rebuild selects the lock.
`gale sync` activates the lock and does not rewrite
it.

**Rollback is temporary.** `gale generations rollback`
moves `current` only. The next sync returns PATH to
the lock. Durable undo is reverting the lock in git.

## Directory Layout

```
~/.gale/
  gale.toml       Package manifest (source of truth)
  config.toml     Settings (registry URL, API keys)
  sync-state.toml Last sync's verdict (see Environment Activation)
  current → gen/2 Symlink to active generation
  gen/            Generation snapshots
    2/bin/        Symlinks into pkg/
  pkg/            Package store (immutable)
    jq/1.8.1-2/   <name>/<version>-<revision>/
    fd/10.4.2-1/
  README.md       Auto-generated, explains this layout
```

A project's `.gale/` has the same shape, so `sync-state.toml` is
per-scope by construction. It is derived state: deleting it costs
one extra sync, never correctness.

## Terminology

**Store** (`pkg/`): where package contents live. Each
version gets its own directory. Once installed, a store
entry is never modified — only deleted when the package
is removed. Inspired by the Nix store, but simpler:
no content-addressing, just `name/version-revision/`
(e.g. `jq/1.8.1-2/`).

A committed store directory is byte-stable for as long
as any generation links it, and `gale install --path`
enforces that rather than assuming it: a local build's
version carries a digest of the uncommitted working
tree, so a changed tree asks for a different directory,
and a replace that would land on a referenced one is
refused. The identity is content-**keyed**, not
content-**addressed** — the digest distinguishes one
tree from another on one machine; it does not address
the artifact globally the way a Nix hash does.

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
4. Old generations accumulate; `gale gc` or
   `PruneOldGenerations` removes them (keep 2:
   current + one previous).
   This is required for `gale generations rollback`.

Step 3 is one syscall. PATH never sees a broken or
partially-built state.

## Global vs Project

Global and project environments use the same model:

```
# Global
~/.gale/gale.toml    → ~/.gale/current/bin/
~/.gale/gen/2/bin/jq → ~/.gale/pkg/jq/1.8.1-2/bin/jq

# Project
./gale.toml          → ./.gale/current/bin/
./.gale/gen/1/bin/jq → ~/.gale/pkg/jq/1.7.1-1/bin/jq
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

### Deciding whether to sync

The hook runs `gale sync --if-needed`, and gale decides.
It used to be a shell mtime comparison against
`.gale/current`, which could not work: a partial sync
rebuilds the generation on purpose so the packages that
did install stay usable (issue #20), and the swap gives
`current` a fresh mtime. From the next activation on the
comparison was false forever, so the failed packages were
never retried and nothing was printed (gh#186).

Sync therefore records its own verdict in
`sync-state.toml`: `complete` or `incomplete`, the
packages that failed, and a fingerprint of the inputs —
the manifest's bytes, the lock's bytes or its absence,
the host, and the platform. Content, not mtimes, so a
`git checkout` does not force a resync.

`--if-needed` reads it back:

| State | Result |
|---|---|
| No stamp, or an unreadable one | sync |
| Fingerprint differs | sync |
| `incomplete`, within the retry interval | one warning naming the failed packages, exit 0 |
| No active generation | sync |
| `complete`, fingerprint matches | silent no-op |
| `incomplete`, interval elapsed | sync |

The stamp is consulted **before** the generation, which
matters for the case that hurts most: a locked sync that
fails leaves the generation untouched (§8), so a project
whose first sync failed has no `current` at all. Asking
for the generation first would run a full failing sync on
every `cd` — a minutes-long stall for a source build, and
worse than the stale environment the stamp exists to
prevent. Sync's recorder is deferred from the command,
not from the rebuild, so the stamp is written whether or
not a generation was ever built.

The interval therefore bounds the work a broken package
can cause without exception: one attempt per interval per
fingerprint, and one file read plus one warning in
between. Editing gale.toml or gale.lock moves the
fingerprint and reaches the packages immediately, and a
user-typed `gale sync` ignores the stamp entirely — which
the warning says, because it is the only place it is
ever said.

`--if-needed` itself is also bounded: a compiled 15s
deadline is the parent of every index HTTP, artifact
HTTP, hash, and extract on that path. Timeout stamps
`incomplete`, cancels the work, and leaves `current`
unchanged. A typed `gale sync` has no overall deadline.

`gale generations rollback` is temporary: it moves
`current` only. The lock still names the intended
roots. Rollback deletes this scope's stamp so the
next `--if-needed` (direnv) syncs back to the lock
instead of treating the rolled-back generation as
complete. Durable undo is reverting the lock in git.

`gale shell` and `gale run` consult the same stamp. Their
own gate asks whether the lock still describes the
manifest, which a partial install failure leaves true.

We chose direnv over custom shell hooks because:
- direnv is battle-tested and handles PATH restoration
- One mechanism for all shells (no fish/zsh/bash hooks)
- Users already know direnv
- Less code for us to maintain

## Install Flow

`gale install jq`:

1. Resolve `jq` from the index (or `--index <dir>`)
2. Stage the artifact under `pkg/fetch/`
3. Write `jq = "<version>"` to gale.toml
4. Write a v2 lock
5. Rebuild the generation from the lock and swap
   `current` last

`gale sync` lands any missing fetch trees and
rebuilds the generation. It does not write the
lock. `--host` overlays refuse. A v1 lock names
`gale fetch-adopt`.

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

When static linking is not practical, recipes should
use the smallest dynamic surface possible and rely on
Gale's fixups to make packaged binaries relocatable.
Modern C++ CLI tools may opt into an explicit LLVM
build toolchain (`build.toolchain = "llvm"`) so they
use the packaged compiler, linker, headers, and C++
runtime instead of whatever happens to be on the host.

## Two-Repo Architecture

- **gale** — the CLI tool. Go code, all packages.
- **gale-recipes** — recipe TOML files. CI builds
  each recipe on macOS arm64 and Linux amd64, pushes
  tar.zst to GHCR, updates binary sections.

Recipes are fetched on demand from GitHub raw URLs.
No git clone needed for installation.

## Registry Cache

Recipe TOML and `index.tsv` responses are cached under
`~/.gale/cache/registry/<sha256(url)>/{body,etag,not_found}`.
The cache is a documented optimization, not silent state.
A fetch-archive cache at
`~/.gale/cache/artifacts/<sha256>` is parked
(Milestone 6, off by default, or never).
Rules:

- **First fetch** writes body + ETag. Subsequent fetches send
  `If-None-Match` and accept 304 to skip the body transfer.
- **`--dry-run`** suppresses cache writes (positive and
  negative). The body is still returned to the caller, but no
  files are persisted.
- **`GALE_OFFLINE=1`** suppresses network entirely. Precedence:
  positive cache (body), then a fresh negative marker
  (replays as `HTTP 404`), then `GALE_OFFLINE=1 and no cached
  entry for <url>`.
- **Stale-on-error**: when the network errors out (DNS,
  ECONNREFUSED, deadline, context cancel), the cached body is
  served if present, then any negative marker is replayed as
  `HTTP 404`. The cache is NOT rewritten in this path.
- **Negative cache (404)**: a 404 response writes a `not_found`
  marker holding an RFC3339Nano timestamp. While the marker is
  younger than `negativeCacheTTL` (1 hour), repeat fetches
  short-circuit to `HTTP 404` without a wire trip. The TTL is
  long enough to dedupe back-to-back read-only command runs
  (`outdated`, `sbom`) and short enough that a freshly
  published recipe shows up without manual cache surgery. The
  marker is pruned lazily on read once it expires; only 404s
  are negatively cached (other non-200 responses surface as
  real errors). A subsequent 200 OK supersedes the marker.

Implementation lives in `internal/registry/cache.go`. All HTTP
fetches (`FetchRecipe`, `FetchRecipeVersion`, `fetchBinaries`,
`Search`) route through `(*Registry).cachedGet`.

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
