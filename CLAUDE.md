# CLAUDE.md

Guidance for Claude Code when working in this repository.
For design rationale, see `docs/dev/design.md`.

## Overview

Gale is a macOS-first package manager for developer CLI
tools. Written in Go. Goal: replace Homebrew, Nix, and
home-manager with one tool that handles global packages
and per-project environments.

## Build & Test

```
just              # test + lint (default)
just build        # build binary (ldflags version)
just test         # all tests
just test-pkg foo # single package tests
just check        # test + lint + format check
just cover        # test coverage per package
just fmt          # fix formatting with gofumpt
just lint         # golangci-lint + go vet
just bootstrap    # first-time: go build + self-install
just install      # rebuild gale from current source
just tag 0.2.0    # run checks, update CHANGELOG, tag
just release 0.2.0 # push tag, create GitHub release
```

## Project Layout

```
cmd/gale/              CLI (cobra commands)
internal/generation/   gen dirs with symlinks, atomic swap
internal/installer/    install to store (binary or source)
internal/store/        package store (~/.gale/pkg/)
internal/build/        build-from-source orchestration
internal/download/     HTTP fetch, SHA256, tar extraction
internal/ghcr/         GHCR anonymous token exchange
internal/registry/     on-demand recipe fetch from GitHub
internal/recipe/       TOML recipe parsing
internal/config/       gale.toml and config.toml parsing
internal/env/          direnv hook, PATH building
internal/output/       colored terminal output
internal/lockfile/     gale.lock read/write
internal/repo/         recipe repository management
internal/trust/        ed25519 signing and verification
internal/ai/           Anthropic SDK integration
internal/homebrew/     Homebrew formula file parser
internal/lint/         recipe TOML validation
internal/attestation/  Sigstore attestation via gh CLI
internal/gitutil/      git clone, ls-remote, URL expansion
```

## Key Concepts

**Store** (`~/.gale/pkg/`): immutable package storage.
One directory per package per version. Append-only.

**Generation** (`~/.gale/gen/<N>/`): a snapshot of
symlinks into the store. `current` symlink points to
the active gen. Rebuilt declaratively from gale.toml
on every install/remove/sync. Atomic swap via
`os.Rename`. "gen" is short for generation.

**current** (`~/.gale/current`): symlink to the active
gen directory. User adds `~/.gale/current/bin` to PATH.
One symlink swap updates bin, lib, man — everything.

**Registry**: fetches recipes on demand from GitHub raw
URLs. Letter-bucketed: `recipes/j/jq.toml`. No git
clone needed.

## CLI Commands

```
gale install <pkg>[@ver]  Install package (binary or source)
gale remove <pkg>         Remove package from store + config
gale add <pkg> [pkg...]   Add to gale.toml without installing
gale sync                 Install all packages in gale.toml
gale update [pkg...]      Update packages to latest version
gale list                 List packages in gale.toml
gale gc                   Remove unused versions from store
gale doctor               Diagnose setup issues
gale env                  Print export PATH for current scope
gale init                 Bootstrap project (gale.toml, .envrc)
gale hook direnv          Print use_gale function for direnvrc
gale build <recipe.toml>  Build recipe from source
gale lint <recipe.toml>   Validate recipe files
gale search <query>       Search for packages
gale shell                Open shell with project environment
gale run <cmd>            Run command in project environment
gale import homebrew <n>  Import Homebrew formula as recipe
gale audit <pkg>         Rebuild and compare SHA256
gale verify <pkg>        Check Sigstore attestation
gale sbom [pkg]          Software bill of materials
```

### Key Flags

- `--local` (sync, update): resolve recipes from
  sibling `../gale-recipes/` directory. `gale build`
  auto-detects when the recipe is inside a recipes
  repo and resolves deps locally.
- `--source <dir>` (install, update): build from a
  local source directory, version from git hash
- `--git` (install, update, build): clone repo and
  build from HEAD instead of downloading tarball
- `--recipe <file>` (install): use a local recipe file
- `--no-color` (global): disable colored output
- `--dry-run` (gc): preview without removing
- `--json` (sbom): machine-readable JSON output
- `--vars-only` (env): print only [vars] exports

## Two-Repo Architecture

- **gale** (this repo) — the CLI tool.
- **gale-recipes** (`../gale-recipes`) — recipe TOML
  files. CI builds recipes, pushes binaries to GHCR.

Install flow: `gale install jq` fetches the recipe
from the registry, pulls a prebuilt binary from GHCR
if available, falls back to building from source.

## Environment Activation

**Global**: `~/.gale/current/bin` on PATH.

**Project**: direnv integration. `gale init` creates
`.envrc` with `use gale`. direnv calls `gale sync`
and adds `.gale/current/bin` to PATH. Project and
global share the same generation model.

`gale env` prints `export PATH=...` for CI/scripts.
Also exports `[vars]` from gale.toml. `gale run` and
`gale shell` auto-sync when gale.toml changes.

## Documentation

- `docs/` — user-facing guides (getting started,
  direnv, chezmoi, CI, troubleshooting, recipes)
- `docs/dev/` — development reference (design,
  style guide, build improvements)

## Principles

- Everything from source. GHCR binaries are a cache,
  not a substitute. See `docs/dev/design.md`.
- Prebuilt binaries only for compiler bootstraps.
- Declarative over imperative (gale.toml → generation).

## Code Reuse

New commands MUST reuse existing helpers. Do not
duplicate logic — call through to shared functions.

**`cmd/gale/context.go`** — all shared CLI helpers:
config resolution, registry, resolver, generation
rebuild, result reporting, install finalization.
Read this file before adding a new command.

**`cmd/gale/paths.go`** — `galeConfigDir()` and
`defaultStoreRoot()`.

**`internal/installer/`** — Installer struct:
- `Install(r)` — binary-first, source fallback
- `InstallLocal(r, sourceDir)` — build from local dir
- `InstallGit(r)` — clone and build from git
- `InstallBuildDeps(r)` — install build deps, return
  bin dirs. Exported for `gale build` to reuse.

**`internal/build/`** — three build paths:
- `Build(r, outputDir)` — download tarball + build
- `BuildLocal(r, sourceDir, outputDir)` — local dir
- `BuildGit(r, outputDir)` — clone + BuildLocal
- `buildFromDir()` — shared tail (steps, fixup, archive)
- `TmpDir()` — returns ~/.gale/tmp/ (exported, used
  by installer). Do not duplicate — import from build.

**`internal/download/`** — `HashFile(path)` returns
hex SHA256. Used by build and download.VerifySHA256.

When adding a new command that installs packages, use
`newCmdContext` + `ctx.Resolver` + `ctx.Installer.Install`.
For versioned installs, use `resolveVersionedRecipe`.
When adding a new build mode, delegate to `BuildLocal`
after obtaining the source directory.

## Adding a New Command

1. Create `cmd/gale/<name>.go` with cobra command
2. Use `newCmdContext(local)` for config/store/resolver
3. Use `ctx.Installer.Install(r)` to install packages
4. Use `resolveVersionedRecipe()` for @version support
5. Use `reportResult()` for consistent output
6. Use `finalizeInstall()` for config + generation
7. Register in `init()` with `rootCmd.AddCommand`

## Conventions

See `docs/dev/style-guide.md` for the full style guide
covering writing, documentation, code, and naming.

Key rules:

- TDD: write the failing test first
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Format with gofumpt, lint with golangci-lint
- Check `context.go` for shared helpers before
  writing new CLI code

## Gotchas

- Build PATH isolates individual tools via symlinks
  into a temp dir, preventing nix vibeutils (ls, mv)
  from leaking in and breaking autotools. See
  `buildPath()` in `internal/build/build.go`.
- Tar extraction handles PAX headers, hard links,
  symlinks, and validates paths against traversal.
  Shared `extractTar()` helper in `internal/download/`.
- Autotools builds need timestamp reset (`touchAll`)
  after extraction to avoid clock-skew errors.
- Recipe repo uses letter-bucketed layout
  (`recipes/j/jq.toml`).
- macOS `/var` is a symlink to `/private/var`. Tests
  that compare paths must `filepath.EvalSymlinks` both
  sides.
- Prefer static linking for CLI tools to avoid dylib
  path issues. Use `--disable-shared --enable-all-static`
  for autotools projects like jq.
- gale-recipes CI pushes binary sections to main
  after builds. Expect push rejections — use
  `git pull --rebase` before pushing.
- gosec G306 flags `os.WriteFile` with 0644. Use
  `//nolint:gosec` for world-readable files.
- Use `go:embed` to bake files into the binary
  (see `internal/generation/gale-readme.md`).
- `internal/attestation/` uses `Disable()`/`Enable()`
  for tests. Installer tests call `attestation.Disable()`
  in TestMain to avoid hitting real gh CLI.

## Testing Homebrew Formula

Test the `kelp/tap/gale` formula in an OrbStack VM
(avoids installing Homebrew on the dev machine):

```sh
# Install brew in the VM (one-time)
orbctl run -m ubuntu-24.04 bash -c \
  'NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'

# Install gale from tap
orbctl run -m ubuntu-24.04 bash -c \
  'eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)" && brew install kelp/tap/gale'

# Run brew test
orbctl run -m ubuntu-24.04 bash -c \
  'eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)" && brew test kelp/tap/gale'
```

Formula source: `~/code/homebrew-tap/Formula/gale.rb`
