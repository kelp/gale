# TODO

## Done

- [x] Project scaffolding (Go module, cobra CLI, deps)
- [x] Recipe TOML parsing and validation
- [x] Config parsing (gale.toml, config.toml)
- [x] Package store directory management
- [x] Colored terminal output with NO_COLOR support
- [x] HTTP download, SHA256 verification
- [x] tar.gz, zip, and tar.zst extraction and creation
- [x] Symlink profile management (~/.gale/bin/)
- [x] Lock file read/write and stale detection
- [x] Environment management and shell hooks (fish/zsh/bash)
- [x] Recipe repository clone/fetch/search
- [x] Letter-bucketed recipe repo layout (recipes/j/jq.toml)
- [x] ed25519 signing and verification
- [x] Anthropic API client with graceful degradation
- [x] Binary platform sections in recipe format
- [x] Build-from-source module (download, verify, build,
  package as tar.zst)
- [x] Installer module (binary or source, store, profile)
- [x] CLI: install, remove, list, shell, run, hook, update,
  sync, build, search, import, create-recipe, repo
- [x] Linux test suite via Docker/OrbStack
- [x] Homebrew formula file parser (heuristic, no API)
- [x] Default store root to ~/.gale/packages (no root needed)
- [x] PAX header support in tar extraction
- [x] Symlink handling in tar.zst create/extract
- [x] Clean build environment (avoids nix tool interference)
- [x] Autotools timestamp fix (touchAll after extraction)
- [x] Build tool discovery from host PATH (nix, Homebrew,
  gale — resolves compilers without importing full PATH)
- [x] 18 recipes: jq, just, fd, ripgrep, bat, git-delta,
  starship, fzf, eza, direnv, go, rust, cmake, pkgconf,
  patchelf, lazygit, actionlint, gale
- [x] GHCR binary pull with anonymous token exchange
- [x] Authenticated HTTP fetch (FetchWithAuth)
- [x] Installer GHCR integration (auto-detect, auth, fallback)
- [x] Hard link support in tar extraction
- [x] Package upgrade moves symlinks (no manual cleanup)
- [x] Build dep auto-install (resolve, install, add to PATH)
- [x] Auto-update agent (daily cron, cooldown, PRs)
- [x] Recipe source.repo and released_at fields
- [x] Binary verification in CI before GHCR push
- [x] Signed bot commits via GitHub API
- [x] Registry package: on-demand recipe fetch from GitHub
- [x] Install UX: install, add, sync, remove all work
  end-to-end with registry fetch and GHCR binary pull
- [x] Interactive scope prompt ([g/p]) with TTY detection
- [x] Registry URL config override in config.toml
- [x] Full remove: unlinks symlinks, cleans store, updates
  config
- [x] golangci-lint clean: all issues fixed or suppressed
- [x] CI: golangci-lint, race detector, govulncheck
- [x] Shared extractTar helper (deduped tar.gz/tar.zst)
- [x] Hard link path traversal validation in tar extraction
- [x] Generation model: declarative bin/ via atomic
  symlink swap (internal/generation/)
- [x] Installer decoupled from symlinks — store only,
  commands rebuild generation after store changes
- [x] Build PATH isolation: symlink individual tools
  to prevent nix vibeutils contamination
- [x] jq recipe: static build (--disable-shared
  --enable-all-static)
- [x] All 9 recipes verified with local builds
  (8 pass, eza needs newer Rust)
- [x] Local source builds (`--source` flag, `BuildLocal`,
  `ParseLocal`, `InstallLocal`, git hash versioning)
- [x] Recipe auto-find in sibling gale-recipes directory
- [x] Gale recipe in gale-recipes (Go build pattern)
- [x] Self-install via `just bootstrap` / `just install`
- [x] `gale --version` via ldflags (git hash for dev)
- [x] `gale update` command with `--local` and `--source`
- [x] `gale lint` recipe validator
- [x] Man page (`gale.1`) in mandoc format
- [x] Shared `cmdContext` for sync/update CLI commands
- [x] Git source builds (`--git` flag on install/build/
  update). Clones repo, builds from HEAD, versions by
  short hash. Update checks remote HEAD first.
- [x] Code reuse refactoring: shared `buildFromDir`,
  `extractBuild`, `TmpDir`, `HashFile`, `reportResult`,
  `finalizeInstall`, `loadAppConfig`, `LoadConfig`.
  Moved helpers to context.go and paths.go.
- [x] CLI consistency redesign: strict sync respects
  pinned versions, all config writes honor scope,
  `--local` on all commands, `@version` support on
  install/update/add, shared `addToConfig` and
  `resolveVersionedRecipe` helpers.
- [x] Versioned registry: `.versions` index files
  map version→commit hash. `FetchRecipeVersion`
  fetches recipes at specific commits. Backfilled
  from git history.
- [x] Semver dev versions: `--source` builds use
  `git describe` formatted as semver
  (`0.2.0-dev.7+5395b8f`).

## Declarative Environments

- [x] **Generation model** — `internal/generation/`
  builds gen directories with bin/ symlinks into the
  store. Atomic swap via `current` symlink. Replaces
  the old imperative profile model.
- [x] **Installer decoupled** — installer only writes
  to the store. Commands rebuild generation after
  store changes.
- [x] **`gale env` command** — prints
  `export PATH=<dir>/current/bin:$PATH` for the
  current scope. Used by CI and direnv.
- [x] **`gale init` command** — bootstraps a project:
  creates gale.toml, .envrc, updates .gitignore.
- [x] **direnv integration** — `gale hook direnv`
  outputs `use_gale` function for direnvrc.
- [x] **direnv recipe** — direnv 2.37.1, single Go
  binary, builds and runs.
- [x] **Deleted old shell hooks** — removed fish/zsh/bash
  chpwd hooks, replaced with direnv + `gale env`.
- [x] **Removed `internal/profile/`** — replaced by
  `internal/generation/`.
- [x] **Embedded README** — `go:embed` README.md written
  into .gale/ on every generation rebuild.
- [x] **Shortened paths** — `packages/` → `pkg/`,
  `generations/` → `gen/`.

## Remote Environments

- [x] **`gale remote sync <host>`** — SSH to host,
  bootstrap gale if missing, push gale.toml, sync.
- [x] **`gale remote export <host>`** — push
  gale.toml and sync without bootstrap check.
- [x] **`gale remote diff <host>`** — compare
  local vs remote package versions.

## Shell Completions

- [x] **Generate completions** — `gale completion`
  generates scripts for bash, zsh, fish, powershell.

## Package Pinning

- [x] **`gale pin <pkg>`** — lock a package version
  so `gale update` skips it. `[pinned]` section in
  gale.toml. `gale unpin <pkg>` to remove.

## Recipe Creation

- [x] **`gale create-recipe <repo>`** — agentic
  workflow using Anthropic SDK. Fetches repo info,
  detects build system, downloads and hashes source,
  generates recipe, lints, and iterates. Prompt
  extensible via `prompt_file` in config.toml.

### Other AI features (on hold)

- **AI-enabled import fallback** — deferred. The
  heuristic parser works well enough, and Claude
  Code can fix edge cases interactively.
- **AI-enabled search** — deferred. Substring
  matching across 113 recipes is sufficient.

## Documentation

- [x] **docs/design.md** — generation model, terminology,
  design decisions, bootstrap flow.
- [x] **CLAUDE.md rewrite** — updated project layout,
  key concepts, gotchas, pointer to design doc.

## Refactors

- [x] **Update `gale shell` and `gale run`** — now use
  generation model (current/bin on PATH). Removed dead
  code from internal/env/.

### cmd/gale/ cleanup

- [x] **Split context.go** — recipe resolution moved
  to `recipes.go`. context.go keeps config, generation,
  and install finalization helpers.
- [x] **Unify recipe resolution** — all recipe
  resolution functions consolidated in `recipes.go`.
- [x] **syncIfNeeded calls runSync directly** —
  extracted `runSync()` from sync command. No more
  subprocess.

### Attestation testability

- [x] **Verifier interface** — Installer takes a
  `Verifier` field (nil = skip). Removed Disable/Enable
  global state.

### SBOM format

- [ ] **CycloneDX or SPDX output** — current JSON is
  custom. Add a standard format option when someone
  needs compliance tooling.

### Audit usefulness

- [x] **Document audit limitations** — help text and
  troubleshooting docs explain that mismatches are
  expected until builds are deterministic.

## Recipe Linter

- [x] **`gale lint` command** — validates recipe TOML
  files. Errors: missing required fields, invalid SHA256,
  wrong file path. Warnings: missing description/license/
  homepage/repo, bad released_at, no ${PREFIX} in steps.

## CLI Polish

- [x] **Colored help output** — custom help function
  with colored section headers, command names, and
  flags via fatih/color. Supports `--no-color` flag
  and `NO_COLOR` env var.

## CI & Release

- [x] **Release management** — `just tag <ver>` runs
  checks, updates CHANGELOG, commits, and tags.
  `just release <ver>` pushes and creates GitHub release
  from RELEASENOTES.md.

- [x] **Versioning infrastructure** — `gale --version`
  prints git hash via ldflags. Semver tags to come.

## Distribution

- [x] **Cross-compiled release binaries** — CI builds
  gale for darwin-arm64, darwin-amd64, linux-amd64,
  linux-arm64 and attaches binaries to GitHub releases.
  Release workflow triggers on `gh release create`.
- [x] **Install script** — `curl | sh` one-liner that
  downloads the right binary for the platform.
  Detects OS/arch, supports GALE_VERSION env var.
- [x] **Homebrew tap** — `brew install kelp/tap/gale`
  as a migration bridge for Homebrew users. Source
  build with Go, installs man page. Bottles to come
  once release binaries are live.

## Supply Chain Security

Each layer builds on the previous one. Implement in
order — later layers assume earlier ones exist.

Two independent protection layers:

- **Git layer** — signed commits enforced on
  gale-recipes `main`. Protects the repo. Attacker
  needs SSH signing key + push token to modify
  anything, including CI workflows.
- **Recipe layer** — ed25519 signatures verified by
  gale at fetch time. Protects the delivery path
  (raw.githubusercontent.com → gale). Gale can't
  see git signatures, so it needs its own.

The git layer guards the CI secrets that hold the
recipe signing key. Together they create a two-factor
barrier: compromise requires both git signing
credentials and the recipe signing key.

### Layer 0: Signed commit enforcement

Prerequisite. Protects the repo that holds recipes
and CI workflows. No code changes — GitHub settings.

- [x] **Enable signed commit requirement** — ruleset
  on gale-recipes `main` requires verified signed
  commits. Bot commits via GitHub API are signed by
  GitHub's key. Pushes are signed with SSH key.

### Layer 1: Recipe signing

Foundation for all client-side verification.

- [x] **Generate ed25519 keypair** — one-time setup.
  Private key stored as `RECIPE_SIGNING_KEY` secret
  in gale-recipes GitHub Actions. Public key embedded
  in gale binary at `internal/trust/pubkey.txt`.
- [x] **Sign recipes in CI** — gale-recipes CI signs
  each recipe TOML with ed25519 on commit/merge.
  Detached `.sig` files stored alongside recipes.
  Scripts: `scripts/sign-file.go`,
  `scripts/sign-recipes.sh`.
- [x] **Embed public key in gale** — `go:embed` bakes
  `pubkey.txt` into the binary.
  `trust.RecipePublicKey()` exposes it.
- [x] **Verify signatures on fetch** — registry
  fetches `.sig` alongside recipe and binaries
  index. Verifies with `trust.Verify()` before
  parsing. Rejects unsigned or bad-signature
  recipes. `FetchRecipe`, `FetchRecipeVersion`,
  and `fetchBinaries` all verify.
- [x] **Local recipes skip verification** — `--local`
  and `--recipe` bypass the registry entirely, so
  no signature check. No explicit flag needed.

### Layer 2: Source URL validation

- [x] **Enforce `source.repo` consistency** — lint
  warns when a GitHub source URL points at a
  different repo than `source.repo` declares.
  Only checks GitHub shorthand repos against
  GitHub URLs. Non-GitHub URLs (official CDNs,
  mirrors) are skipped — no false positives.
  Host allowlist dropped: signing already guards
  recipe authenticity, and the allowlist would
  need constant maintenance.

### Layer 3: Binary attestation verification

Proves prebuilt binaries were built by our CI from
our source. Requires Layer 1 so we trust the recipe
that tells us which binary to expect.

- [x] **Verify Sigstore attestations on install** —
  shells out to `gh attestation verify` after SHA256
  check. Skips gracefully when gh CLI not installed.
  Falls back to source build on attestation failure.
- [x] **`gale verify <pkg>`** — verifies attestation
  for installed packages via OCI URI. Requires gh.
- [x] **Doctor check** — `gale doctor` warns when gh
  CLI is not available.

### Layer 4: Reproducible build verification

Proves that a prebuilt binary matches what you'd get
from a source build. The strongest guarantee: even
a compromised CI can't serve a different binary than
what the source produces.

- [x] **`gale audit <pkg>`** — rebuilds from source,
  compares SHA256 against lockfile hash. Reports
  match or mismatch with both hashes.
- [x] **Deterministic build investigation** — documented
  in docs/troubleshooting.md. Mach-O LC_UUID, libtool
  .la paths, pkg-config .pc paths, and ar timestamps
  prevent full determinism without Nix-level isolation.
  Archive packaging itself is deterministic.

### Layer 5: SBOM

- [x] **`gale sbom`** — lists packages with version,
  license, source host, and install method. Supports
  `--json` for machine-readable output and single
  package filtering.

## Auto-Update Agent

Moved to gale-recipes TODO. Recipe format additions
(`source.repo`, `source.released_at`) are complete.

## Package Lifecycle

- [x] **Version cleanup** — `gale gc` removes store
  versions not referenced by any gale.toml. `--dry-run`
  previews what would be removed.

## Installer Resilience

- [x] **Binary pull fallback** — when binary install
  fails, the store directory is cleaned before falling
  back to source build. Prevents partial downloads from
  breaking the source fallback.

## Build System

- [x] **`gale build --local` flag** — resolves build
  deps from sibling gale-recipes. Reuses
  `Installer.InstallBuildDeps()`.

- [x] **Build dependency checking** — Installer resolves
  build deps from recipes, installs them (binary
  preferred, source fallback), adds bin dirs to the
  build PATH. Uses RecipeResolver for lookup.
- [x] **Build dep library/header paths** — build env
  injects LIBRARY_PATH, C_INCLUDE_PATH,
  PKG_CONFIG_PATH, CMAKE_LIBRARY_PATH, and
  CMAKE_INCLUDE_PATH from installed build deps'
  store paths. Recipes can link against dep
  libraries without explicit -L/-I flags.

- [x] **Build system presets** — `SystemDeps()` auto-adds
  build deps for cmake, go, cargo. `CMAKE_PREFIX_PATH`
  set for cmake builds. Deduplicates against explicit
  deps.

- [x] **Per-platform build overrides** — `[build.<platform>]`
  sections override `[build]` for specific platforms.
  Used by Go and Rust recipes for platform-specific
  bootstrap URLs.

- [x] **Source download cache** — cache downloaded
  source tarballs in `~/.gale/cache/` keyed by SHA256.
  Skip download if cached file matches.

- [x] **tar.xz and tar.bz2 extraction** — xz via
  ulikunitz/xz, bz2 via stdlib. `ExtractSource`
  dispatcher auto-detects format from filename.

- [x] **Stream build output** — switched from
  CombinedOutput to streaming stdout/stderr. Long
  builds no longer crash from memory pressure.

- [x] **Build directory location** — builds use
  `~/.gale/tmp/` instead of system TMPDIR.

## Recipes: Shared Library Support

Now that the generation model puts lib/ alongside bin/
in each gen directory, recipes can ship shared libraries
properly. The gen's lib/ dir has a stable, known path
(`~/.gale/current/lib/`) so dylib references can use
`@executable_path/../lib` on macOS or `$ORIGIN/../lib`
on Linux.

- [x] **Post-build dylib fixup** — `FixupBinaries`
  rewrites dylib paths with `install_name_tool` (macOS)
  or `patchelf --set-rpath` (Linux) after every build.
- Static linking is the right default for CLI tools.
  Shared libs add complexity with no practical benefit.
  The generation model supports lib/ if ever needed.

## Language Toolchains

Design how gale manages compilers and language runtimes.
These distribute prebuilt binaries — recipes would be
pure `[binary.<platform>]` with no `[build]` block.

- [x] **Go** — recipe builds from source using a
  bootstrap binary. Binary sections for both platforms.
- [x] **Rust** — recipe builds from source with vendored
  OpenSSL. CI build in progress.
- [x] **Zig** — recipe in gale-recipes.
- [x] **Node.js** — recipe in gale-recipes.
- [x] **Design decisions** — PATH ordering resolves
  conflicts with system-installed versions. Gale's
  project environments override global via direnv.
  Rustup coexists: use gale's Rust for build deps,
  rustup for Rust development. Documented in
  docs/project-environments.md.

## Developer Workflow

- [x] **`gale doctor`** — checks config, store,
  generation, PATH, direnv, symlinks, and orphaned
  versions. Shows fix suggestions for each issue.
- [x] **`gale which <binary>`** — show which package
  provides a binary and its store path. Resolves
  symlinks from current generation back to store.
- [x] **`gale diff`** — show what would change if
  `gale sync` ran now (new installs, version
  mismatches).
- [x] **`gale outdated`** — show packages with newer
  versions available upstream. Queries the registry
  for each package in gale.toml.

## Recipe Ecosystem

- [x] **Fuzzy search** — `gale search` with substring,
  prefix, and subsequence matching against a registry
  index (`index.tsv`). Searches name and description,
  ranks by relevance.
- [x] **Lockfile pinning** — `gale.lock` stores
  version + SHA256 per package. Written by install
  and update. Lockfile format: TOML with
  `[packages.<name>]` sections containing version
  and sha256 fields.

## Project Environments

- [x] **Auto-sync on run** — `gale run` and `gale shell`
  sync first if gale.toml changed since last sync.
- [x] **Environment variables** — `[vars]` section in
  gale.toml, exported by direnv via `use_gale` and
  `gale env`. Supports `--vars-only` flag.
- [x] **`.tool-versions` compatibility** — fallback
  to `.tool-versions` when no gale.toml exists.
  Parses asdf/mise format, maps names (golang→go,
  nodejs→node). Both files can coexist for mixed
  teams.


# Testing

- [ ] Integration testing suite that tests all flags
  and functionality. Every CLI permutation, end to end.
  Tests many possible recipe builds of all types.

# Build Infrastructure

- [x] Fix gale-installed git rpath issue — AddDepRpaths
  adds LC_RPATH entries for dep store lib dirs,
  headerpad enables post-build rpath additions,
  FixupBinaries scans entire prefix tree
- [x] pkg-config fixup — FixupPkgConfig rewrites .pc
  files to relative ${pcfiledir} paths on both source
  builds and binary installs
- [ ] Rebuild all GHCR binaries with pkg-config fixup
  so prebuilt packages have correct .pc files
