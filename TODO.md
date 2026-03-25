# TODO

## Done

- [x] Project scaffolding (Go module, cobra CLI, deps)
- [x] Recipe TOML parsing and validation
- [x] Config parsing (gale.toml, config.toml)
- [x] Package store directory management
- [x] Colored terminal output with NO_COLOR support
- [x] HTTP download, SHA256 verification
- [x] tar.gz and zip extraction
- [x] tar.zst extract and create (klauspost/compress)
- [x] Symlink profile management (~/.gale/bin/)
- [x] Lock file read/write and stale detection
- [x] Environment management and shell hooks (fish/zsh/bash)
- [x] Recipe repository clone/fetch/search
- [x] Letter-bucketed recipe repo layout (recipes/j/jq.toml)
- [x] ed25519 signing and verification
- [x] Anthropic API client with graceful degradation
- [x] Homebrew API formula fetching (no local brew needed)
- [x] Binary platform sections in recipe format
- [x] Build-from-source module (download, verify, build,
  package as tar.zst)
- [x] Installer module (binary or source, store, profile)
- [x] CLI: install, remove, list, shell, run, hook, update,
  sync, build, search, import, create-recipe, repo
- [x] First recipe: jq 1.7.1 (built from source, tested)
- [x] Linux test suite via Docker/OrbStack

## AI Features

Use the Claude Code SDK for all AI features — no custom
agent loop. The SDK handles tool calling, retries, and
streaming. Our code provides focused prompts and tools.

- [ ] **AI-enabled import** — Use Claude Code SDK to
  translate complex Homebrew formulas. Ruby build logic,
  conditional deps, and patches need AI interpretation,
  not just JSON metadata scraping.

- [ ] **AI-enabled search** — `gale search` should use
  natural language via Claude API when a key is configured.
  Falls back to simple substring matching without a key.

- [ ] **AI-enabled recipe generation** — `gale create-recipe`
  invokes Claude Code SDK with tools for reading repos,
  running builds, and writing TOML. The SDK handles the
  agent loop.

- [ ] **Recipe generation prompt engineering** — Capture
  learnings from manually creating the first recipes
  (build quirks, configure flags, dependency patterns,
  timestamp fixes, symlink handling) and encode them
  into the prompt. The prompt should produce recipes
  that work on the first try.

## CLI Polish

- [ ] **Colored help output** — Syntax-highlighted flags
  and subcommands in `--help`, similar to vibeutils.
  Explore whether cobra supports custom help templates
  with ANSI color, or if we need a custom help function.

## CI & Release

- [ ] **GitHub Actions CI** — Run tests on macos-latest
  and ubuntu-latest. Build the binary. Run on push and PR.

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

- [ ] **OCI/GHCR binary hosting** — Store prebuilt packages
  in GitHub Container Registry via ORAS. Free for public
  packages, no bandwidth charges.

- [ ] **Build farm** — GitHub Actions workflows to build
  recipes on macos-latest and ubuntu-latest for
  darwin-arm64, linux-amd64, linux-arm64. Upload
  tar.zst packages to GHCR. Populate `[binary.*]`
  sections in recipes.

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

- [ ] **Build dependency resolution** — Resolve and install
  build dependencies before running build steps.
  Currently assumes build deps are on the host.

- [ ] **Per-platform build overrides** — Allow `[build.*]`
  sections that override build steps for specific
  platforms (e.g., different configure flags on Linux
  vs macOS).
