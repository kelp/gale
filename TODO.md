# TODO

## Code Standards Backlog

The strict golangci-lint config (`.golangci.yml`, paired with the
expanded `docs/dev/style-guide.md`) enforces its rules on new and
changed code via `new-from-merge-base`. 80 pre-existing violations are
grandfathered; clear them as files are touched or in scoped follow-up
PRs, then drop the `issues:` block from `.golangci.yml` to enforce
repo-wide. Re-measure anytime: remove the `issues:` block and run
`just lint`.

Mechanical (low-risk, do first):

- [ ] **misspell (1)** — fix the flagged spelling.
- [ ] **predeclared (5)** — rename identifiers that shadow builtins.
- [ ] **forcetypeassert (2)** — add comma-ok to bare type assertions.
- [ ] **errorlint (2)** — match with `errors.Is`/`errors.As`, wrap
  with `%w`.
- [ ] **nilnil (3)** — return a sentinel error instead of `(nil, nil)`.
- [ ] **contextcheck (4)** — thread `context.Context` instead of
  dropping it.

Refactors (need judgment):

- [ ] **dupl in `build.go` (1 pair)** — `internal/build/build.go:1107`
  duplicates `:1155`; extract the shared block into a helper.
- [ ] **dupl in tests (19 hits)** — duplicated setup across
  `cmd/gale/*_test.go`, `internal/build`, `internal/download`,
  `internal/recipe`. Replace with shared `t.Helper()` fixtures or
  table-driven cases (dupl is enforced on tests by design).
- [ ] **revive (13)** — early-return / superfluous-else / naming.
- [ ] **funlen (16)** — split functions over 80 lines / 50 statements.
- [ ] **gocognit (13)** — reduce cognitive complexity below 30.

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
- [x] ed25519 signing and verification (removed in v0.13.0 — see Layer 1 note below)
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

## Pre-0.12 Release

Surfaced during 2026-04-18 local test pass of the revision
system. None block shipping on their own, but each is a
paper-cut a new user is likely to hit.

- [x] **Issue #20 — initial sync skips generation on any
  failure.** `finishSync` in `cmd/gale/sync.go` now
  rebuilds the generation first, then surfaces the
  install-failure error. Packages that succeeded land on
  PATH; the exit code stays non-zero so failures aren't
  swallowed.

- [x] **jq recipe ships libonig.5.dylib alongside
  oniguruma.** Added a post-install
  `rm -f ${PREFIX}/lib/libonig* ${PREFIX}/lib/pkgconfig/oniguruma.pc`
  step in `recipes/j/jq.toml` (gale-recipes). jq stays
  statically linked against oniguruma; the farm no
  longer sees a duplicate dylib.

- [x] **`gale doctor --repair` ad-hoc signs unsigned
  Mach-Os.** `repairDoctor` walks every installed
  package via `store.List()` and calls
  `build.EnsureCodeSigned`. No-op on Linux and on
  already-signed binaries.

- [x] **Farm conflicts warn but don't fail.** The
  installer no longer swallows `farm.Populate` errors —
  they propagate as install failures so a recipe-level
  bug surfaces immediately. Released after the jq
  cleanup so existing installs migrate cleanly.

- [x] **`.gale-deps.toml` records locally-resolved
  versions, not CI-linked versions.** `gale build` now
  writes the metadata into `<prefix>/.gale-deps.toml`
  before sealing the archive (new
  `internal/depsmeta` package shared between build and
  installer). The installer preserves the archive's
  file when present and only computes locally for
  legacy archives.

### Release notes callouts (not bugs)

- Soft migration: the first `gale sync -g` after
  upgrade reinstalls every pre-revision package,
  because missing `.gale-deps.toml` is treated as
  stale. Expected, but slow on machines with many
  installs. Worth calling out in the 0.12 notes.
- New installs land at
  `~/.gale/pkg/<name>/<version>-<revision>/`. Old bare
  `<version>/` dirs remain on disk until `gale gc`
  runs. Harmless other than disk footprint.

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

### Layer 1: Recipe signing (removed in v0.13.0)

This layer was implemented and then removed. The
`internal/trust/` package, `pubkey.txt`, `.sig` files,
and all `verifyRecipe`/`fetchSignature` logic were
deleted. See CHANGELOG.md and commit a848195 for
details. Layer 3 no longer depends on this layer.

- ~~**Generate ed25519 keypair**~~
- ~~**Sign recipes in CI**~~
- ~~**Embed public key in gale**~~
- ~~**Verify signatures on fetch**~~
- ~~**Local recipes skip verification**~~

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
our source.

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

- [ ] **`gale generations rollback` should rebuild the
  farm.** `internal/generation/history.go:153` only swaps
  the `current` symlink; the shared dylib farm at
  `~/.gale/lib/` stays pointing at the post-Build state,
  so binaries in the rolled-to gen may load dylibs from
  revisions that gen never included. Fix requires
  persisting the gen's package set (e.g. a small
  per-gen manifest file) so rollback can pass the
  active set to `farm.Rebuild`. Surfaced while fixing
  the store-walking farm rebuild.

- [ ] **Collapse install-time farm replace output.**
  During a revision bump, `installer.Populate` at
  `internal/installer/installer.go:398` prints one
  `farm: replacing …` line per dylib. For packages like
  postgresql or ruby that's 4+ lines per install. Replace
  with a single summary line
  (`farm: updated <pkg>@<ver> (N dylibs)`) so the output
  stays scannable.

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

## Performance & Distribution

Added 2026-05-24. Gale install + sync feel noticeably slower
than Homebrew on the same packages. Anecdotally: serial
per-package fetches, redundant GHCR token exchanges, GitHub raw
URLs for recipes (no CDN, no compression), and disk-buffered
extraction. Two angles to attack: gale-side code wins (cheap,
no infra changes) and distribution/infrastructure changes
(harder, bigger ceiling).

Threat model is *user patience*: the cumulative pain of slow
multi-package operations (`gale sync` over a project's
gale.toml, `gale outdated` on 20 declared packages) and slow
cold installs. The single-package cold install on a slow
connection is the most visible case but probably not the most
impactful in aggregate.

### Tier 0 — Measure first

Optimisation without numbers picks the wrong target. Do these
two before anything else.

- [x] **Phase timing under --verbose.** `timing.Phase` calls
  wired through `internal/ghcr/`, `internal/installer/`,
  `internal/download/`. `--verbose` install/sync prints
  `[timing] phase=… elapsed=…` lines per phase.

- [x] **Baseline benchmark vs Homebrew.** Harness at
  `scripts/perf-baseline.sh` (HOME-isolated, opt-in
  `--with-brew`; builds gale from HEAD and asserts the
  version). Reference runs recorded in
  `docs/dev/perf-baseline.md`: Linux v0.16.3, macOS M3 Max
  v0.16.3 (with an honest brew cold/warm comparison), a
  retroactive pre-Tier-1 run (`fd7ac90`) isolating the
  Tier-1 delta, and the Tier-2 parallel-closure A/B. Headline:
  gale cold still loses to brew cold, dominated by the
  per-install GHCR fetch + attestation cost.

### Tier 1 — Gale-side wins (no infra changes)

Implementable today against the existing distribution model.
Expected gain: 2–5× on multi-package operations; 10–30% on
single-package installs.

- [x] **Parallelise per-package operations.** 8-worker pool
  in `internal/parallel/`. `cmd/gale/sync.go`,
  `cmd/gale/outdated.go`, `cmd/gale/sbom.go` all use
  `parallel.Map`. Output collected and emitted in stable
  order after the barrier.

- [x] **Parallelise the dependency closure + configurable
  parallelism.** `installDepsInner` now fans out the dep
  install loop (mutex-guarded `seen` map; downloads bounded by
  a shared `parallel.Limiter` acquired around the leaf fetch in
  `installBinaryTo`, so the limit never deadlocks against the
  package pool). One knob sizes both the sync pool and the
  closure limiter: `GALE_JOBS` env > `[sync] parallelism` in
  config.toml > default 8 (`config.ResolveParallelism`).
  Correct and race-free, but the A/B (`docs/dev/perf-baseline.md`,
  2026-05-31) shows it is **wall-clock neutral for single heavy
  installs** — the closures are bandwidth-bound, not latency-
  bound, so parallel download over one pipe doesn't cut time.

- [x] **Reuse the GHCR anonymous token within a session.**
  In-process `tokenCache` in `internal/ghcr/ghcr.go` with
  TTL honouring the response's `expires_in` (5-min default
  fallback).

- [x] **HTTP/2 connection reuse across registry + GHCR
  requests.** Shared `*http.Client` in `internal/httpclient/`,
  consumed by registry, GHCR, and download. Connections
  to `ghcr.io`, `raw.githubusercontent.com`, and
  `pkg-containers.githubusercontent.com` are kept alive.

- [x] **Streaming tar extraction.** `FetchAndExtractTarZstd`
  in `internal/download/download.go` pipes the HTTP body
  through `io.TeeReader` to sha256 and a zstd→tar chain in
  one pass — no on-disk `.tar.zst` intermediate.

- [x] **Eager dep resolution.** `internal/prewarm/` warms
  the registry's ETag cache for a recipe's deps concurrently
  before the serial `InstallBuildDeps` walk. Fire-and-forget;
  errors are swallowed so the real fetch surfaces persistent
  failures.

**The #1 cost is attestation — and it's a gale-side win (measured
2026-06-02).** `--verbose` now times `gh attestation verify`
(commit `11b0287`). It is **~4–5s per artifact and ~73% of a
single install** (jq: 5.0s of 6.9s), recurring once per binary in a
closure. Crucially, `gh attestation verify` is network-latency-bound
and **parallelizes near-perfectly**: 1×=4.1s, 8× serial=34s, 8×
concurrent=**4.6s** (no per-call caching). So verifying a whole
closure at once is essentially free — yet a real `bat` install spends
~7×4s on it. That makes the highest-value perf work **gale-side
attestation handling**, ahead of any distribution change:

- [ ] **Cache attestation per store artifact.** Once a binary at a
  given sha256 is verified, record it; shared deps and reinstalls
  skip re-verification entirely.
- [ ] **Verify a closure's artifacts concurrently.** gh proves N
  concurrent verifies ≈ 1×. Confirm whether the existing closure
  parallelism already overlaps the verify step (timestamped trace),
  and if not, make it.
- [ ] **(Stretch) in-process Sigstore verification** to drop the
  per-artifact `gh` subprocess spawn entirely.

This corrects the earlier read that distribution infra (Tier 2/3)
was the next lever — it isn't, until attestation is addressed. The
client is *not* yet at its floor.

### Tier 2 — Recipe pipeline

Today every recipe fetch is a separate HTTPS GET to
`raw.githubusercontent.com`, uncompressed, no CDN.
`.versions` files help with version resolution but you still
make one request per recipe.

- [ ] **Bundled recipe index.** Periodically (CI) build a
  single `index.tar.zst` of all recipes + a manifest mapping
  name → contents. `gale sync`-style operations fetch the
  bundle once, read locally. Per-recipe fetch path still
  works as a fallback for cache misses or fresh recipes
  between bundle rebuilds. Roughly the `apt update` model.
  Repo: gale-recipes (build) + gale (consume).

- [ ] **HTTP compression on recipe fetches.**
  `raw.githubusercontent.com` doesn't serve `Content-Encoding:
  gzip` consistently. Recipes are small TOML — gzip is 5–10×.
  Solved naturally if we move recipe distribution off raw.
  githubusercontent.com (see Tier 3).

- [ ] **Local "registry mirror" model.** `gale update`
  (separate from `gale update <pkg>` — naming conflict to
  resolve) fetches the bundled index and writes it to
  `~/.gale/cache/registry/`. Subsequent commands consult the
  local copy first. Apt-style. Cuts steady-state recipe
  fetches to zero. Pair with TTL-based auto-refresh.

### Tier 3 — Binary distribution infrastructure

GHCR works but it's slow, requires an anonymous-token dance,
and isn't designed for fast public CDN delivery. Two of these
become tractable once the security-side OIDC keyless work
ships (Layer 6 Tier 2) — at that point the OCI registry
isn't carrying signing semantics, just bytes.

- [ ] **Cloudflare R2 (or similar) for binary hosting.** Zero
  egress fees, CDN-backed, no token dance, HTTP/2 native.
  At gale's scale ~$5/mo. Migrate gale-recipes CI to push to
  R2 alongside GHCR initially (parallel), then cut GHCR over
  after a soak period. Expected gain: 1.5–3× on binary
  download depending on user geography. Repo: gale-recipes
  (CI changes) + gale (alternate URL resolution).

- [ ] **Precompiled-recipe bundle on CDN.** Same R2 / CDN
  endpoint as binaries. Bundle + binaries from the same
  origin means one connection pool, one TLS handshake.

- [ ] **HTTP/2 multiplexing across the binary fetch.** If a
  package ships as multiple layered blobs (current archives
  are a single tar.zst, so this is mostly future-proofing
  for if we move to OCI-layer-style distribution).

### Tier 4 — Speculative

- [ ] **Async warmup on `gale install`.** Background-fetch
  the dep closure's recipes while the user is downloading
  the requested package's binary. Hides recipe latency on
  the deps almost entirely.

- [ ] **Binary deduplication via the shared dylib farm.**
  Already exists for source-built dylibs. Investigate
  whether binary-installed packages with shared deps could
  hardlink-share dylibs at install time. Saves disk on
  heavy projects.

- [ ] **Resume partial downloads.** GHCR / R2 both support
  HTTP Range. On a flaky connection, resume rather than
  restart. Cheap once a range-aware HTTP client exists.

### Comparison reference

Why Homebrew feels fast, when audited honestly:
- Bottles on GitHub Packages with aggressive CDN caching.
- Per-package parallelism on multi-install.
- HTTP/2 multiplexing.
- Formulae cached locally via the cloned core repo (zero
  network for recipe lookup in steady state).
- Bottles for nearly everything — no source fallback.

Gale will not match the "nearly everything" axis at our
recipe count, and we don't want to (curated registry is the
security story). The other four are matchable, and Tier 1 +
Tier 2 + Tier 3 cover them.
