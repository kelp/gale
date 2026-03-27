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
- [x] 9 recipes: jq, just, fd, ripgrep, bat, git-delta,
  starship, fzf, eza
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

## GHCR Distribution

Two sides: gale pulls binaries, gale-recipes pushes them.

- [x] **GHCR pull in gale** — `internal/ghcr/` package
  handles anonymous token exchange. Installer detects
  GHCR blob URLs, fetches with bearer auth, falls back
  to source build. No full OCI client needed — uses
  direct blob URLs like Homebrew.

- [x] **Build farm in gale-recipes** — GitHub Actions
  builds each recipe on macOS arm64 and Linux amd64.
  Pushes tar.zst to GHCR via ORAS. Updates
  `[binary.<platform>]` sections and commits back.

- [x] **gale CI** — Tests, vet, gofumpt, build on
  macOS arm64 and Linux amd64.

## Install UX (next)

`gale install jq` should just work. Fetches recipe
from public registry, installs binary, updates config.

- [x] **Registry package** — `internal/registry/` fetches
  recipes by name from GitHub raw URLs on demand.
- [x] **Rewrite `gale install`** — fetch recipe from
  registry, install (binary preferred), add to
  gale.toml. Default global, prompt if project
  gale.toml exists. `-g`/`-p` flags skip prompt.
  Keep `--recipe` as escape hatch for local files.
- [x] **`gale add` command** — add to gale.toml without
  installing. Validates recipe exists. Accepts
  multiple packages. Project scope by default.
- [x] **Implement `gale sync`** — install all packages
  from gale.toml. Used by teammates after clone.
- [x] **Interactive scope prompt** — when gale.toml
  exists and no `-g`/`-p` flag, ask `[g/p]`. Default
  global. TTY-only; silent global in non-TTY.
- [x] **Registry URL config** — override default URL
  in `~/.gale/config.toml` `[registry]` section.

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

- [ ] **docs/design.md** — Document the generation model,
  terminology, and design decisions. Key terms: "gen"
  is short for generation (a numbered snapshot of
  symlinks into the store). `current` symlink points
  to the active gen. Atomic swap via os.Rename.
  Explain how global and project environments share
  the same model. Why direnv over shell hooks. Why
  static linking for CLI tools. How gale differs from
  Nix and Homebrew.
- [ ] **Update CLAUDE.md** — Add generation model to
  project layout, update gotchas, add pointer to
  design doc.

## CLI Polish

- [ ] **Colored help output** — Syntax-highlighted flags
  and subcommands in `--help`, similar to vibeutils.
  Explore whether cobra supports custom help templates
  with ANSI color, or if we need a custom help function.

## CI & Release

- [ ] **Release management** — Just targets for version
  bump, git tag, and GitHub release creation. Automate
  the full release flow.

- [ ] **RELEASENOTES.md** — Update on each version bump.
  The release process extracts the current version's
  notes and includes them in the GitHub release body.

- [ ] **Versioning infrastructure** — Embed version in the
  binary at build time via ldflags. `gale --version`
  should print the version.

## Distribution

- [ ] **Self-update** — `gale update-self` or similar to
  download the latest gale binary and replace itself.

## Auto-Update Agent

Daily GitHub Actions workflow in gale-recipes that
keeps recipes current with upstream releases.

### Recipe format additions

- [ ] **Add `[source].repo` field** — explicit GitHub
  owner/repo (e.g., `repo = "jqlang/jq"`). Used to
  query GitHub Releases API. Starting point for GitHub
  projects; will need other detection methods later
  for non-GitHub sources.
- [ ] **Add `[source].released_at` field** — date the
  current version was first seen (e.g.,
  `released_at = "2024-12-15"`). Used for cooldown
  enforcement. Agent skips if less than 3 days old.
- [ ] **Update all existing recipes** with `repo` and
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

## Build System

- [x] **Build dependency checking** — Installer resolves
  build deps from recipes, installs them (binary
  preferred, source fallback), adds bin dirs to the
  build PATH. Uses RecipeResolver for lookup.

- [ ] **Per-platform build overrides** — Allow `[build.*]`
  sections that override build steps for specific
  platforms (e.g., different configure flags on Linux
  vs macOS).

- [ ] Design / review where gale does it's builds. We want it to be in a
  place that is scratch, but safe from filling disk. Probably create a gale
  specific tmp dir in the users homedir, and clean it up after we're done.

## Language Toolchains

Design how gale manages compilers and language runtimes.
These distribute prebuilt binaries — recipes would be
pure `[binary.<platform>]` with no `[build]` block.

- [ ] **Go** — download official tarball from go.dev.
  Handles the `go/` prefix in extraction.
- [ ] **Rust (rustup + cargo + rustc)** — download
  rustup-init binary, or package cargo/rustc directly
  from static.rust-lang.org.
- [ ] **Zig** — single binary download from ziglang.org.
- [ ] **Node.js / npm** — download official tarball.
  Needed for recipes with npm build steps.
- [ ] **Design decisions** — how do these interact with
  system-installed versions? Should gale prefer its own
  Go/Rust over the host's? How do per-project envs
  pick a specific version? Relationship with rustup's
  own version management.
