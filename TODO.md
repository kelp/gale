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

## GHCR Distribution

Two sides: gale pulls binaries, gale-recipes pushes them.

- [x] **GHCR pull in gale** — `internal/ghcr/` package
  handles anonymous token exchange. Installer detects
  GHCR blob URLs, fetches with bearer auth, falls back
  to source build. No full OCI client needed — uses
  direct blob URLs like Homebrew.

- [ ] **Build farm in gale-recipes** — GitHub Actions
  workflows that build each recipe on macos-latest and
  ubuntu-latest. Produces tar.zst for darwin-arm64,
  linux-amd64, linux-arm64. Pushes to GHCR via ORAS.
  Updates `[binary.<platform>]` sections in recipe TOML
  and commits back.

- [ ] **gale CI** — Run tests on macos-latest and
  ubuntu-latest. Build the binary. Run on push and PR.

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

- [ ] **Upstream watcher** — Monitor upstream releases for
  each recipe. Bump version and hash in the TOML.
  Trigger a build, verify it passes.

- [ ] **AI-assisted build recovery** — When an upstream
  update breaks the build, use Claude or Claude Code
  to diagnose and fix the recipe. Fall back to opening
  a GitHub issue if automated fixes fail.

- [ ] **Dependency cooldown policy** — Wait 3 days before
  adopting new upstream versions by default. Protects
  against supply chain attacks and yanked releases.
  Security patches can be fast-tracked manually.
  See: nesbitt.io/2026/03/04 and
  simonwillison.net/2025/Nov/21/dependency-cooldowns/

## Build System

- [ ] **Build dependency checking** — Before building,
  check that required tools (cargo, go, autoconf, etc.)
  are available. Tell the user what's missing and how
  to install it. Assume stock macOS + Xcode CLT as the
  baseline.

- [ ] **Per-platform build overrides** — Allow `[build.*]`
  sections that override build steps for specific
  platforms (e.g., different configure flags on Linux
  vs macOS).
