# Changelog

## v0.1.1 — 2026-03-27

### Added

- `--source` flag on `gale install` builds from a local
  source directory. Version detected from
  `git rev-parse --short HEAD`. Auto-finds recipe in
  sibling `gale-recipes/` directory.
- `--local` flag on `gale sync` and `gale update`
  resolves recipes from a sibling `gale-recipes/`
  directory instead of the remote registry.
- `gale update [package...]` checks for newer recipe
  versions and installs them. Supports `--source` for
  local source rebuilds.
- `gale --version` prints the version. Injected from
  `git rev-parse --short HEAD` via ldflags at build
  time; defaults to `dev`.
- `just cover` target shows per-package test coverage.
- `gale lint` command validates recipe TOML files.
  Checks required fields, SHA256 format, file path
  convention, and warns on missing optional fields.
- Man page (`gale.1`) in mandoc format. Installed to
  `man/man1/` and symlinked by the generation model.
- Colored help output with section headers, command
  names, and flag names. Respects `NO_COLOR` env var
  and `--no-color` flag.
- `just tag` and `just release` targets for the full
  release flow: checks, CHANGELOG update, tag, push,
  GitHub release.
- `gale gc` command removes unused package versions
  from the store. Supports `--dry-run`.

### Fixed

- Binary install fallback cleans partial downloads
  from the store directory before building from source.
- `build.BuildLocal()` builds a recipe from a local
  source directory, skipping download and verification.
- `recipe.ParseLocal()` parses recipes without requiring
  `source.url` and `source.sha256` fields.
- `installer.InstallLocal()` installs from a local
  source directory via `BuildLocal`.
- Source download cache in `~/.gale/cache/` keyed by
  SHA256. Skips re-downloading cached tarballs.
- Project `gale.toml` with dev dependencies: go, just,
  golangci-lint, gofumpt.
- `just bootstrap` target builds gale with `go build`,
  then self-installs via `gale install --source .`.
- `just install` rebuilds gale from current source
  using gale itself.
- Declarative environment model with atomic generation
  swap. `~/.gale/current` symlink points to a numbered
  generation directory containing bin/, lib/, man/,
  include/ symlinks into the package store.
- `gale env` command prints `export PATH=...` for CI
  and scripts.
- `gale init` bootstraps a project with gale.toml,
  .envrc, and .gitignore entry.
- `gale add` command adds packages to gale.toml
  without installing.
- `gale hook direnv` outputs `use_gale` function for
  direnv integration.
- Interactive scope prompt (`[g/p]`) when a project
  gale.toml exists and no `-g`/`-p` flag is set.
- Registry URL override in `~/.gale/config.toml`.
- Post-build dylib fixup rewrites dynamic library
  paths to `@rpath` (macOS) and `$ORIGIN/../lib`
  (Linux) for portable binaries.
- Per-platform build overrides in recipe format:
  `[build.darwin-arm64]` and `[build.linux-amd64]`.
- Embedded README.md written into `.gale/` on every
  generation rebuild.
- Streaming build output for long-running builds.
- golangci-lint v2 with strict configuration.
- CI: golangci-lint, race detector, govulncheck on
  macOS arm64 and Linux amd64.

### Changed

- `gale install` fetches recipes from the public
  registry, installs binary from GHCR (preferred) or
  builds from source, adds to gale.toml, and rebuilds
  the generation.
- `gale remove` cleans up store, removes from config,
  and rebuilds the generation.
- `gale sync` falls back to `~/.gale/gale.toml` when
  no project config exists.
- `gale shell` and `gale run` use the generation model
  instead of concatenating store paths.
- Installer decoupled from symlinks — only manages
  the store.
- Build PATH isolates individual tools via symlinks
  to prevent nix vibeutils contamination.
- Shortened directory names: `packages/` to `pkg/`,
  `generations/` to `gen/`.

### Removed

- `internal/profile/` replaced by
  `internal/generation/`.
- Fish, zsh, and bash shell hooks replaced by direnv.
- Dead code: BuildPATH, BuildEnvironment,
  MergePackages, DetectConfig from `internal/env/`.

## 2025-03-26

### Added

- On-demand recipe registry fetch from GitHub raw
  URLs. No git clone needed.
- Auto-update agent: daily cron workflow in
  gale-recipes, 3-day cooldown, PR per update.
- `[source].repo` and `[source].released_at` fields
  for upstream release tracking.
- Binary verification in CI before GHCR push.
- Signed bot commits via GitHub API.
- Hard link support in tar extraction.
- Hard link path traversal validation.
- Shared `extractTar` helper (deduplicated tar.gz
  and tar.zst extraction).

### Fixed

- Package upgrade now moves symlinks instead of
  failing on existing ones.

## 2025-03-25

### Added

- GHCR anonymous token exchange for pulling prebuilt
  binaries from GitHub Container Registry.
- Authenticated HTTP fetch (`FetchWithAuth`) with
  bearer tokens.
- GHCR integration in installer: auto-detects GHCR
  URLs, authenticates, falls back to source build.
- GitHub Actions CI on macOS arm64 and Linux amd64.
- Build dependency auto-install: resolver fetches and
  installs build deps, adds their bin dirs to the
  build PATH.
- Extra PATH parameter in `build.Build()` for build
  dep binaries.

### Changed

- Build tool discovery resolves compilers from host
  PATH via `exec.LookPath` instead of importing the
  full host PATH.
- Default TMPDIR to `/tmp` when unset (Linux CI fix).

## 2025-03-24

### Added

- Homebrew formula file parser with heuristic Ruby
  extraction. Handles deps, build steps, version
  detection, and Homebrew-specific helpers.
- `gale import homebrew <name>` command.
- Build-from-source module: download, verify SHA256,
  extract, run build steps, package as tar.zst.
- `gale build <recipe.toml>` command.
- Installer module: binary-preferred install with
  source fallback.
- `--recipe` flag for installing from local files.
- Letter-bucketed recipe repo layout
  (`recipes/j/jq.toml`).
- tar.zst extract and create support.
- Binary platform sections in recipe format
  (`[binary.darwin-arm64]`).
- Symlink handling in tar archives.
- Autotools timestamp reset (`touchAll`) to prevent
  clock-skew errors.

### Changed

- Import command reworked from Homebrew API to direct
  formula file parsing.

## 2025-03-23

### Added

- Project scaffolding: Go module, cobra CLI, justfile.
- Recipe TOML parsing and validation.
- Config parsing (gale.toml, config.toml) with
  directory walking.
- Package store directory management.
- Colored terminal output with `NO_COLOR` support.
- HTTP download with SHA256 verification.
- tar.gz and zip extraction.
- Symlink profile management (`~/.gale/bin/`).
- Lock file read/write with stale detection.
- Environment management and shell hooks
  (fish, zsh, bash).
- Recipe repository clone/fetch/search.
- ed25519 signing and verification.
- Anthropic API client with graceful degradation.
- CLI commands: install, remove, list, shell, run,
  hook, update, sync, search, import, create-recipe,
  repo add/remove/list/init.
- README with project description and usage.

## 2025-03-22

### Added

- Initial design document covering architecture,
  package management model, environment activation,
  AI features, federated repositories, and ed25519
  trust model.

### Changed

- Implementation language switched from Zig to Go.
