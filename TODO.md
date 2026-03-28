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

## AI Features

Use the Claude Code SDK for all AI features — no custom
agent loop. The SDK handles tool calling, retries, and
streaming. Our code provides focused prompts and tools.

- [ ] **AI-enabled import fallback** — When heuristic
  parsing produces warnings (empty version, missing
  build steps), offer to fix with Claude Code SDK.
  Heuristic first, AI as fallback.

- [ ] **AI-enabled search** — `gale search` should use
  natural language via Claude API when a key is configured.
  Falls back to simple substring matching without a key.

- [ ] **AI-enabled recipe generation** — `gale create-recipe`
  invokes Claude Code SDK with tools for reading repos,
  running builds, and writing TOML. The SDK handles the
  agent loop.

- [ ] **Recipe generation prompt engineering** — Encode
  learnings from building the first 9 recipes into the
  prompt: autotools timestamp sensitivity, clean build
  env, cargo --path flag, symlink handling, PAX headers,
  --with-oniguruma=builtin pattern, Go mkdir + -o flag.
  The prompt should produce recipes that work on the
  first try.

## Agent Teams for Recipe Development

- [ ] **Recipe creation team** — use Claude Code agent
  teams to parallelize recipe onboarding. One agent
  imports from Homebrew and writes the recipe, another
  builds and verifies the binary. They iterate until
  the build passes. Good for filling out our base set.
- [ ] **Build recovery team** — when the auto-updater's
  version bump breaks a build, spin up a team: one
  agent reads the error and fixes the recipe, another
  tests the fix. Falls back to opening an issue if the
  team can't resolve it.

## Documentation

- [x] **docs/design.md** — generation model, terminology,
  design decisions, bootstrap flow.
- [x] **CLAUDE.md rewrite** — updated project layout,
  key concepts, gotchas, pointer to design doc.

## Refactors

- [x] **Update `gale shell` and `gale run`** — now use
  generation model (current/bin on PATH). Removed dead
  code from internal/env/.

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

- [ ] **Self-update** — `gale update-self` or similar to
  download the latest gale binary and replace itself.

## Auto-Update Agent

Daily GitHub Actions workflow in gale-recipes that
keeps recipes current with upstream releases.

### Recipe format additions

- [x] **Add `[source].repo` field** — explicit GitHub
  owner/repo (e.g., `repo = "jqlang/jq"`). Used to
  query GitHub Releases API.
- [x] **Add `[source].released_at` field** — date the
  current version was first seen. Used for cooldown.
- [x] **Update all existing recipes** with `repo` and
  `released_at` fields.

### Workflow: version checker

- [ ] **Cron workflow** — runs daily in gale-recipes.
  For each recipe with `[source].repo`, queries
  `gh api /repos/{owner}/{repo}/releases/latest`.
- [ ] **Cooldown enforcement** — if new version is less
  than 3 days old (from upstream release date), skip.
  Security patches can be fast-tracked manually.
  See: nesbitt.io/2026/03/04 and
  simonwillison.net/2025/Nov/21/dependency-cooldowns/
- [ ] **PR per update** — each version bump creates a
  PR with updated version, SHA256, and source URL.
  CI builds it on both platforms. Reviewable and
  auditable.

### AI-assisted build recovery

- [ ] **Claude Code SDK integration** — when a version
  bump breaks the build, read the build error and
  attempt a recipe fix (adjust flags, deps, build
  steps). If fix works, push to the PR.
- [ ] **Issue fallback** — if AI fix fails, open a
  GitHub issue with the build error and recipe name.
  Human fixes it later.

## Package Lifecycle

- [ ] **Version cleanup policy** — when upgrading a
  package, should old versions be removed automatically
  or kept? Design options: keep N versions, keep for
  N days, explicit `gale gc` command, or always remove.
  Currently old versions stay in the store after upgrade.

## Installer Resilience

- [ ] **Binary pull fallback** — when a GHCR binary pull
  fails (digest mismatch, 404, network error), fall back
  to: (1) a previously cached version in the store, then
  (2) building from source. Currently a bad digest in a
  `[binary.*]` section breaks all recipes that depend on
  that package (e.g. stale Go binary sections break every
  Go recipe in CI).

## Build System

- [x] **Build dependency checking** — Installer resolves
  build deps from recipes, installs them (binary
  preferred, source fallback), adds bin dirs to the
  build PATH. Uses RecipeResolver for lookup.

- [x] **Per-platform build overrides** — `[build.<platform>]`
  sections override `[build]` for specific platforms.
  Used by Go and Rust recipes for platform-specific
  bootstrap URLs.

- [x] **Source download cache** — cache downloaded
  source tarballs in `~/.gale/cache/` keyed by SHA256.
  Skip download if cached file matches.

- [x] **Stream build output** — switched from
  CombinedOutput to streaming stdout/stderr. Long
  builds no longer crash from memory pressure.

- [ ] **Build directory location** — builds currently
  use system TMPDIR. Move to a gale-specific scratch
  dir in the user's homedir, clean up after build.

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
- [ ] **Re-evaluate Rust recipes** — bat, ripgrep, fd,
  starship, eza, git-delta, just currently static-link
  everything via cargo. Some could benefit from shared
  deps (libgit2, pcre2, oniguruma) to reduce binary
  size and allow dep updates without full rebuilds.
- [ ] **jq with shared libjq** — now that we can ship
  libs, revisit whether jq should build libjq as a
  shared library (for use by other tools) instead of
  the current --enable-all-static approach.
- [ ] **Generation lib/ symlinks** — extend
  `generation.Build()` to also symlink lib/ entries
  from the store into the gen directory, alongside
  bin/.

## Language Toolchains

Design how gale manages compilers and language runtimes.
These distribute prebuilt binaries — recipes would be
pure `[binary.<platform>]` with no `[build]` block.

- [x] **Go** — recipe builds from source using a
  bootstrap binary. Binary sections for both platforms.
- [x] **Rust** — recipe builds from source with vendored
  OpenSSL. CI build in progress.
- [ ] **Zig** — single binary download from ziglang.org.
- [ ] **Node.js / npm** — download official tarball.
  Needed for recipes with npm build steps.
- [ ] **Design decisions** — how do these interact with
  system-installed versions? Should gale prefer its own
  Go/Rust over the host's? How do per-project envs
  pick a specific version? Relationship with rustup's
  own version management.
