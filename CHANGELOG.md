# Changelog

## v0.5.0 — 2026-03-28

### Added

- Supply chain security layers 0-5 complete:
  signed commit enforcement, recipe signing,
  source URL/repo consistency lint, binary
  attestation verification via gh CLI, `gale audit`
  for reproducible build checks, and `gale sbom`
  for software bill of materials.
- `gale verify <pkg>` checks Sigstore attestation
  for installed packages via OCI URI.
- `gale audit <pkg>` rebuilds from source and
  compares SHA256 against the installed binary.
- `gale sbom` lists packages with version, license,
  source, and install method. Supports `--json`.
- `gale doctor` warns when gh CLI is not available
  for attestation verification.
- Auto-sync: `gale run` and `gale shell` sync
  automatically when gale.toml changes.
- Environment variables: `[vars]` section in
  gale.toml exported by direnv and `gale env`.
  `--vars-only` flag for variable-only output.
- User guides: getting started, bootstrapping,
  chezmoi, project environments, Homebrew migration,
  CI/CD, updates, troubleshooting, source builds,
  and recipe authoring.

### Changed

- Docs reorganized: user guides in `docs/`,
  development reference in `docs/dev/`.
- `--local` flag removed from `gale build`.
  Auto-detection handles it.

### Fixed

- `detectRecipesRepo` returned wrong path, causing
  `gale build` without `--local` to fail resolving
  build deps inside gale-recipes.

## v0.4.0 — 2026-03-28

### Added

- Fuzzy search: `gale search` matches against a
  registry index by name and description. Scores
  by exact, prefix, substring, and subsequence match.
- Lockfile pinning: `gale.lock` stores version +
  SHA256 per package. Written on install and update.
  Sync verifies SHA256 against lockfile.
- `.tool-versions` compatibility: `gale sync` falls
  back to `.tool-versions` when no gale.toml exists.
  Maps asdf/mise names (golang→go, nodejs→node).
- Build dep library/header resolution: build steps
  now get `LIBRARY_PATH`, `C_INCLUDE_PATH`, and
  `PKG_CONFIG_PATH` from installed build deps. Recipes
  with `build = ["bzip2"]` can link `-lbz2` without
  explicit `-L` flags.
- Binary index separation: `.binaries.toml` files
  alongside recipes. GHCR URLs derived at runtime.
  Backward compatible with inline `[binary.*]`.
- Lint: warns on missing build deps (`go build`
  without `build = ["go"]`, etc.). Validates
  platform strings.
- Runtime deps installed at build time. Recipes no
  longer need to list deps in both `build` and
  `runtime`.
- Transitive dep resolution with cycle detection.
  If A→B→C, all three paths in build env.
- Dynamic linker paths: `LD_LIBRARY_PATH` (Linux),
  `DYLD_FALLBACK_LIBRARY_PATH` (macOS) set from
  build deps.
- Recipe `platforms` field: restrict builds to
  specific platforms. `gale build` skips gracefully.
- Recipe `verify` field: CI can read custom verify
  commands instead of guessing `--version`/`--help`.
- Platform variables: `${OS}`, `${ARCH}`,
  `${PLATFORM}` available in build steps.
- Auto-detect `--local`: `gale build` inside a
  recipes repo auto-detects local dep resolution.
- `${VERSION}` build variable: recipe version
  available in build steps alongside `${PREFIX}`.
- Style guide: `docs/style-guide.md` covering
  writing, documentation, and code conventions.

### Changed

- Man page rewritten in OpenBSD mandoc style with
  EXAMPLES section.
- README rewritten: concise reference, practical
  examples, correct PATH setup.

- Build functions accept `*BuildDeps` struct instead
  of variadic path strings. Carries both bin dirs
  (for PATH) and store dirs (for lib/include paths).
- `lockfilePath()` and `writeConfigAndLock()` shared
  helpers replace duplicated lock path computation
  across install, update, and sync.
- Sync uses `resolveVersionedRecipe` instead of
  inlining the versioned recipe resolution logic.
- `InstallBuildDeps` refactored: public wrapper +
  private recursive `installDepsInner` with shared
  `seen` map for cycle detection and dedup.

### Fixed

- `installFromGit --local` now uses local resolver
  for build dep resolution. Previously hardcoded
  the registry, ignoring the `--local` flag for
  transitive deps.
- Transitive deps' lib/include/bin paths now
  available in build env (previously only direct
  deps' paths were returned).
- Lint: `cargo install` no longer falsely triggers
  missing `go` dep warning.
- `gale lint` skips `.binaries.toml` and `.versions`
  files.

## v0.3.0 — 2026-03-28

### Added

- `gale which <binary>` shows which package provides a
  binary and its store path.
- `gale outdated` shows packages with newer versions
  available in the registry.
- `gale diff` shows what `gale sync` would change.
- `@version` support: `gale install jq@1.7.1`,
  `gale update jq@1.8.1`, `gale add jq@1.7.1` pin
  specific versions. Fetches exact version from the
  versioned registry index.
- Versioned registry: `.versions` index files map
  versions to commit hashes. `FetchRecipeVersion`
  fetches recipes at specific commits.
- `--local` flag on `gale install` and `gale add`.
- `--recipe` flag on `gale update`.
- Cross-compiled release binaries for darwin-arm64,
  darwin-amd64, linux-amd64, linux-arm64. Built by
  GitHub Actions on each release.
- Install script: `curl -fsSL .../install.sh | sh`
  with OS/arch detection and version pinning.
- Homebrew tap: `brew install kelp/tap/gale`.

### Changed

- **Strict sync**: `gale sync` respects pinned versions
  in gale.toml. Checks store first, then tries versioned
  registry fetch. Errors with guidance when a version
  can't be found instead of silently installing latest.
- **Scope consistency**: `--source`, `--git`, and
  `--recipe` on install now honor `-g`/`-p` scope flags.
  Previously hardcoded to global config.
- **Semver dev versions**: `--source` builds use
  `git describe` formatted as semver
  (e.g., `0.2.0-dev.7+5395b8f`) instead of bare hashes.
- `gale add` uses proper scope resolution with `-g`/`-p`
  flags and interactive prompt.

### Removed

- `checkVersionMatch` — replaced by direct versioned
  recipe fetch via `FetchRecipeVersion`.
- `installPackage` method — replaced by direct
  `ctx.Installer.Install(r)` calls.

## v0.2.0 — 2026-03-28

### Added

- `gale doctor` diagnoses setup issues: config, store,
  generation, PATH, direnv, and orphaned versions.
- `gale build --local` resolves build dependencies from
  a sibling gale-recipes directory. Build deps are now
  installed automatically before building.
- `--git` flag on install, build, and update clones a
  git repo and builds from HEAD. Version is the short
  commit hash. Update checks remote HEAD before
  rebuilding.
- `internal/gitutil/` package for git clone, ls-remote,
  and repo URL expansion.
- `source.branch` field in recipe format for specifying
  a git branch to clone.

### Changed

- Release notes auto-extracted from CHANGELOG.md.
  Removed manual RELEASENOTES.md.
- Build scratch space moved from system TMPDIR to
  `~/.gale/tmp/`. Keeps build artifacts in user space.

## v0.1.2 — 2026-03-27

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
