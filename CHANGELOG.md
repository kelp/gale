# Changelog

## Unreleased

### Fixed

- `gale install`/`update`/`sync` no longer print a cascade
  of `farm: replacing libX.N.dylib: …-1 -> …-2` lines when
  older revisions are still on disk. The shared-lib farm
  rebuild used to walk every `<name>/<ver>` directory under
  `~/.gale/pkg/` and populate each in readdir order, so the
  older revision was symlinked first and overwritten by the
  newer one on every generation swap — producing dozens of
  spurious "replacing" lines per command. The rebuild now
  only considers the packages in the current generation;
  stale revisions wait quietly for `gale gc`.

## v0.12.3 — 2026-04-19

### Fixed

- `gale install`/`update`/`sync` no longer silently fall back
  to a source build when a binary install fails. Reaching the
  fallback path means the recipe advertised a binary for the
  current platform and the fetch/verify pipeline rejected it
  (404 from GHCR, hash mismatch, attestation failure, network
  error) — exactly the cases a user wants to see. The installer
  now prints a one-line warning naming the package and the
  underlying error before building from source. Internal:
  `Installer.BinaryFallbackLog` defaults to `os.Stderr`.
- Stripped, self-contained Mach-O binaries no longer trigger
  spurious `not enough header space` warnings during the
  build's "Fixing library paths" step. `AddDepRpaths` now
  only adds the shared-lib farm rpath
  (`~/.gale/lib/`) to binaries that actually reference an
  `@rpath/` dep. The previous gate added it whenever
  `otool -L` returned anything at all — even when every dep
  was a system library — so packages like vibeutils (Zig,
  built with `-Dstrip=true`) printed dozens of warnings per
  install.
- `TestRemoveWarnsWhenPackageNotInStore` was leaking into the
  user's real `~/.gale/pkg/` because `defaultStoreRoot()` is
  HOME-relative and the test had no isolation. On boxes where
  the store contained both `-1` and `-2` revisions of any
  package, `farm.Repopulate` flooded the captured stderr buffer
  and pushed the asserted warning past the 4 KiB read window.
  Test now sets `HOME` to the temp project dir, drains stderr
  with `io.ReadAll` on a goroutine, and runs against an
  isolated empty store.

### Changed

- The cached-install line printed by `install`/`update`/`sync`
  now reads `Updated <name>@<ver> (already in store)` instead
  of `<name>@<ver> already installed`. The old wording made
  `gale update` look like a no-op when in fact `gale.toml`,
  `gale.lock`, and the active generation had all been switched
  to the cached version. Also styled as success, not info, to
  match the binary/source paths.

## v0.12.2 — 2026-04-18

### Fixed

- `gale update` now honors recipe revision bumps. `update` was
  comparing bare `Package.Version` via `golang.org/x/mod/semver`,
  which has no notion of a revision suffix. As a result, a
  recipe bump from `1.8.1` (revision 1) to `1.8.1` revision 2
  left `gale outdated` correctly reporting the bump while
  `gale update <pkg>` said "up to date" and did nothing.
  Extracted the version-comparison rules into a new
  `internal/version` package so `update` and `outdated` share
  one ordering. Numeric `-<N>` suffixes are treated as gale
  revisions (newer than bare); non-numeric suffixes like
  `-dev.2` keep their semver pre-release semantics.

## v0.12.1 — 2026-04-18

### Fixed

- `gale gc` no longer deletes store entries actively referenced
  by the generation. The v0.12.0 revision rollout wrote canonical
  `<version>-<revision>` directories to the store while user
  `gale.toml` entries still use bare versions. gc's exact string
  match treated the canonical dirs as unreferenced and reaped
  them, emptying the store for every package and leaving every
  symlink in the active generation dangling. gc now expands each
  bare reference to also match its revision-suffixed canonical
  form, mirroring the resolver's back-compat lookup.

## v0.12.0 — 2026-04-18

### Added

- Debian-style recipe revisions. Recipes can declare
  `revision = N` in `[package]`, defaulting to 1. User-facing
  version becomes `<version>-<revision>` (e.g. `curl@8.19.0-2`).
  `gale install pkg@<version>` resolves to the latest revision
  of that version; `gale install pkg@<version>-<revision>` pins
  exactly. `gale outdated` treats a revision bump as outdated
  even when the upstream version is identical.
- Per-install `.gale-deps.toml` written at install time,
  recording the resolved `(name, version, revision)` closure
  the binary was linked against. Used by `gale sync` and
  `gale doctor` to detect dependents that need rebuild when a
  dep's recipe changes.
- Automatic dep propagation. `gale sync` now calls `IsStale`
  on each installed package; any package whose built-against
  deps no longer match the current recipes is reinstalled.
- Explicit version-range constraints in dep declarations.
  Recipes can write
  `runtime = [{name = "expat", version = ">=2.7.5-2"}]` —
  installed deps satisfying the constraint are not considered
  stale. Bare-string deps continue to work (behave as any
  revision bump invalidates).
- `gale doctor` now reports stale installs with a hint to
  run `gale sync`.
- Store layout includes revisions: new installs write to
  `~/.gale/pkg/<name>/<version>-<revision>/`. Existing
  bare-version dirs are still found via back-compat lookup
  (treated as revision 1).
- Registry `.versions` index supports multiple revisions per
  version. `gale install curl@8.19.0` now picks the highest
  revision in the index.
- Shared dylib farm at `~/.gale/lib/`. Installs symlink their
  versioned dylibs (e.g. `libcurl.4.dylib`) into the farm, and
  new binaries carry an extra rpath to it alongside the existing
  per-version rpaths. SONAME-preserving dep upgrades become a
  symlink flip, so dependents keep working without rebuilds.
  Auto-reconciles on install, remove, sync, and generation swap.
- `gale doctor` now checks for lib-farm drift (broken symlinks,
  missing entries for installed versioned dylibs, conflicts with
  another package). `gale doctor --repair` fixes drift through
  the existing `rebuildGeneration` path.
- `gale inspect` subcommand. Walks an installed package's
  binaries and reports linkage issues: unresolvable `@rpath`
  references, stale rpath entries, deps referenced but not
  declared in the recipe, recipe deps unused by any binary, and
  version skew across a package's own binaries. `--all` scans
  the whole store; `--json` emits machine-readable output; exit
  nonzero on findings so CI can gate on it.
- `gale build` now writes `.gale-deps.toml` into the prefix
  before sealing the archive, recording the exact
  version-revision of each linked dep. The installer
  preserves the archive's file when present and only computes
  locally as a fallback for legacy archives. Eliminates a
  staleness mis-detection class when a user has pinned deps
  that diverge from the build environment.
  (New `internal/depsmeta` package shared between build and
  installer.)
- `gale doctor --repair` now walks every installed package
  and runs `EnsureCodeSigned`, ad-hoc signing any Mach-Os
  that arrived unsigned. Pre-fix installs (before f00f2b7)
  no longer SIGKILL on Apple Silicon. No-op on Linux and on
  already-signed binaries.
- Bidirectional store-version resolution. A bare-version
  request prefers a canonical `<version>-1` directory when
  present, and a `<version>-1` request falls back to a bare
  directory when absent. Pre-revision configs find freshly
  migrated installs, and revision-aware configs find legacy
  installs, without any filesystem migration.
- Soft migration for pre-revision store entries. `gale sync`
  now routes stale packages through a `Reinstall` path that
  ignores the back-compat fallback and forces a canonical
  `<version>-<revision>` install, while fresh installs keep
  the fallback so dep installs don't needlessly re-migrate.

### Fixed

- Install-time ad-hoc signing of Mach-O binaries on macOS.
  `EnsureCodeSigned` now runs after every binary or source
  install, applying an ad-hoc signature to any unsigned
  Mach-O in the extracted prefix. Apple Silicon SIGKILLs
  unsigned binaries on exec, so prebuilt tarballs whose
  binaries had no gale-store rpaths (e.g. a zig build with
  no gale dep dylibs) previously extracted into an unusable
  state. Idempotent and no-op on Linux.
- Build-time codesign failures on macOS are now fatal instead
  of silently swallowed. `install_name_tool` and codesign calls
  in `fixup_darwin.go` used to discard errors with `_ =`,
  producing tarballs with unsigned binaries that SIGKILL on
  user machines. Signing errors now abort the build the same
  way a missing rpath rewrite does; only the optional
  `codesign --remove-signature` retry keeps a warn-only path.
- `gale sync` and `gale doctor` now flag an install as stale
  when `.gale-deps.toml` is missing, even if the recipe for
  that installed version can no longer be resolved (e.g. the
  version has fallen out of the registry's `.versions` index).
  Previously those pre-revision installs silently reported as
  up to date and never surfaced as migration candidates.

- Prebuilt binary installs now call `RestorePrefixPlaceholder`
  after extraction, so `@@GALE_PREFIX@@` markers baked into text
  files at build time (scripts, `.la` files) get rewritten to the
  real store path. Previously only source installs did this.
- Prebuilt binary installs also rewrite stale foreign `.gale/pkg/`
  paths embedded in text files (e.g. `curl-config --static-libs`
  emitting `/Users/runner/.gale/pkg/openssl/...`). Matches the
  existing Mach-O / ELF rpath relocation but for text.
- Issue #20: `gale sync` now rebuilds the active generation
  even when one or more packages fail to install. Packages
  that did succeed land on PATH; the install-failure error
  still propagates so the exit code stays non-zero. Previously
  a single broken recipe on a fresh machine left the user with
  no `current` symlink at all.
- Farm conflicts (two packages claiming the same versioned
  dylib) now fail the install instead of being silently logged
  to stderr. Surfaces recipe-level bugs immediately rather
  than letting the farm pick a winner. Ships after the jq
  recipe cleanup so existing installs migrate cleanly.

## v0.11.3 — 2026-04-09

### Fixed

- `gale update` now rebuilds the active generation even when a
  package is already up to date in config and store. This repairs
  stale activation states like `gale@0.11.2` being installed while
  `~/.gale/current/bin/gale` still points at `v0.11.1`.

## v0.11.2 — 2026-04-09

### Fixed

- Unsupported recipe platforms now return a clean `unsupported
  platform` error that recipe CI can treat as a skip instead of a
  failed build.
- Generation rebuild failures during `gale update` and `gale sync`
  are now fatal instead of warning-only, so Gale no longer reports
  success while leaving activation stale.
- Failed or partial syncs no longer advance `current` to an
  incomplete generation.
- Generation rebuilds are now serialized with a generation lock so
  concurrent installs and syncs cannot race on `gen/<N>`.
- Local source installs now replace existing store entries with a
  rollback-safe swap instead of deleting the old store dir first.
  A failed final rename no longer breaks active binaries.
- Generation rebuild and scope auto-detection now honor
  `.tool-versions` projects. Rebuilds no longer create an empty
  generation just because `gale.toml` is absent.
- `gale doctor --repair` can now rebuild global and project
  generations from the current config and store, including
  `.tool-versions` project environments.

## v0.11.1 — 2026-04-08

### Added

- Recipes can now declare platform-specific dependency overrides
  such as `[dependencies.linux-amd64]` and
  `[dependencies.linux-arm64]`.

### Fixed

- Build dependency resolution now honors platform-specific recipe
  dependencies. Recipes can require `llvm` only on Linux without
  forcing the same build dependency on macOS.

## v0.11.0 — 2026-04-06

### Added

- Recipes can now declare `build.toolchain`, with optional
  per-platform overrides such as `build.linux-arm64.toolchain`.
- Added initial `toolchain = "llvm"` support in the build
  environment. Gale now requires an explicit `llvm` build dep,
  activates clang/clang++ and LLVM binutils from `DEP_LLVM`,
  and on Linux adds libc++/lld defaults needed for modern C++
  packages.
- Added low-noise CLI output controls:
  - `--plain` disables color and progress output
  - `--quiet` suppresses non-essential status lines
  - `--error-format=json` emits machine-readable runtime
    errors on stderr

### Changed

- Non-TTY stderr now defaults to plain, low-noise output.
- Download progress meters are disabled in plain and quiet
  modes.

### Fixed

- Runtime command failures no longer print Cobra usage.
  Invalid flags and argument errors still show usage.
- `BuildForPlatform()` now preserves default build steps when a
  platform override sets only `toolchain` or other partial
  fields.

## v0.10.5 — 2026-04-06

### Added

- Mirror fallback for GNU source downloads. When
  ftpmirror.gnu.org returns an HTTP error (often 403
  from datacenter IPs), automatically tries
  mirrors.kernel.org and ftp.gnu.org before failing.

## v0.10.4 — 2026-04-06

### Fixed

- Linux builds now inject dep library directories into
  ELF RUNPATH via patchelf. Prebuilt binaries from CI
  will find shared libraries from deps (openssl, zstd,
  etc.) at runtime without LD_LIBRARY_PATH.
- Prebuilt Linux binaries with rpaths from a foreign
  store prefix are rewritten to the local store root
  on install.

## v0.10.3 — 2026-04-06

### Fixed

- `gale update` now reinstalls packages that are in the
  config but missing from the store. Previously it checked
  the registry version, reported "up to date", and then
  failed during generation rebuild.

## v0.10.2 — 2026-04-06

### Fixed

- Linux builds now inject `-fPIC` into default CFLAGS and
  CXXFLAGS. Static libraries must be position-independent
  so they can be linked into shared objects (e.g. bzip2
  and zlib into python's libpython.so).

## v0.10.1 — 2026-04-05

### Fixed

- Tar extraction now accepts bare root directory entries
  (`./`) that some upstream archives include. Previously
  rejected as a path traversal attempt.
- Tar extraction allows absolute symlink targets instead
  of rejecting them. Upstream archives (helix, helm)
  leak developer paths and test fixtures as absolute
  symlinks. These are created as-is and may be dangling.
- Darwin build environment no longer injects nonexistent
  dep subdirectories into search paths. A dep like cmake
  (no `lib/`) would add a bogus entry to LIBRARY_PATH,
  causing `ld: warning: search path not found` which
  broke configure scripts with strict stderr checking
  (e.g. Ruby's `ac_c_werror_flag=yes`).
- Darwin link-time `-Wl,-rpath` injection re-enabled for
  dep lib dirs. SIP strips `DYLD_*` environment variables
  from `/bin/sh` children, so link-time rpath is the only
  reliable way for binaries to find dep dylibs during the
  build phase (e.g. Python test-loading `_lzma` during
  `make`). `AddDepRpaths` still runs post-build as the
  authority, and `existingRpaths()` deduplicates.
- Build environment search-path guards are now
  independent: `LIBRARY_PATH`, `C_INCLUDE_PATH`,
  `PKG_CONFIG_PATH`, and `CMAKE_PREFIX_PATH` are each
  set when any dep provides that directory type.
  Previously a header-only dep (no `lib/`) could lose
  `C_INCLUDE_PATH` if no dep had a lib dir.
- Prebuilt binary install now relocates stale LC_RPATH
  entries. Binaries published to GHCR from CI have
  absolute rpaths like `/Users/runner/.gale/pkg/...`
  baked in. `RelocateStaleRpaths` rewrites any rpath
  containing `.gale/pkg/` to use the current store root.
- `isSuspiciousDepRef` diagnostic added to warn when a
  Mach-O binary references a dep dylib via an absolute
  store path instead of `@rpath`.

## v0.10.0 — 2026-04-04

### Fixed

- `gale remove` now deletes the lockfile entry for the
  removed package. Previously the stale entry could
  trigger spurious SHA256 mismatch evictions on
  reinstall.
- `gale sync` now writes SHA256 hashes to the lockfile
  after installing packages. The `add` → `sync`
  workflow previously produced an empty lockfile.
- `gale remove` now rebuilds the generation before
  deleting from the store, preventing dangling symlinks
  if the rebuild fails.
- `gale update` defers generation rebuild so it runs
  even when a config write fails mid-batch.
- Build environment creation returns an error instead of
  silently falling back to the unsandboxed parent
  environment when temp directory creation fails.
- Build step errors preserve the error chain (`%w`
  instead of `%s`).
- `lockfilePath` returns an error instead of panicking
  on invalid input.
- `AddRepo`/`RemoveRepo` now use file locking,
  preventing data loss under concurrent access.
- `updateLockfile` now acquires a file lock, preventing
  concurrent `gale install` from losing entries.
- Lockfile SHA256 mismatch eviction logs a warning when
  store removal fails instead of silently ignoring it.
- `gale gc` logs a warning on config parse errors
  instead of silently treating all packages as
  unreferenced.
- `gale install` and `gale sync` show a user-friendly
  message when a package doesn't support the current
  platform.

### Changed

- Unified `DepPaths` and `BuildDeps` into a single
  `build.BuildDeps` type. Removed the redundant
  converter function.
- `buildEnv` decomposed into focused helpers on a new
  `BuildContext` struct: `baseEnv`, `depSearchPaths`,
  `perDepEnv`, `compilerFlags`.
- `gale doctor` checks extracted into a registry of
  individual check functions.
- `gale lint` validators extracted into a composable
  rule pipeline.
- `generation.Build` delegates to `populateGeneration`
  and `swapCurrentSymlink` helpers.
- `gale gc` delegates to `collectReferencedPackages`
  and `removeUnreferencedVersions`.
- `cmdContext` methods replace free functions, reducing
  parameter counts (e.g., `finalizeInstall` from 6
  string args to 3).
- `newCmdContext` accepts scope flags (`--global`,
  `--project`), unifying scope resolution across all
  commands.
- Shared `resolveRecipeResolver` and `loadRecipeFile`
  helpers replace 4× and 3× duplicated patterns.
- `InstallResult.Method` is now a typed `InstallMethod`
  instead of a bare string.
- `config.go` split into `gale.go` (package config) and
  `app.go` (app config) for clarity.
- Error messages standardized to gerund form.
- All `os.IsNotExist` calls replaced with
  `errors.Is(err, os.ErrNotExist)`.

### Added

- `internal/atomicfile` package: crash-safe file writes
  with temp file + fsync + rename.
- `internal/filelock` package: unified flock-based file
  locking using `golang.org/x/sys/unix`.

### Removed

- `syncGit` flag (declared but never used).
- `syscall.Flock` usage (replaced by `filelock`
  package using `unix.Flock`).

## v0.9.0 — 2026-04-03

### Added

- `gale build` accepts `--recipes [path]` for local
  recipe resolution, matching install, sync, update,
  add, and outdated.

### Fixed

- Post-build fixup rewrites hardcoded build prefix paths
  in text files (Perl, Python, shell scripts, config).
  Replaces temp prefix with `@@GALE_PREFIX@@` placeholder
  at build time, restores to store path at install time.
  Fixes autoconf, automake, and other autotools packages.
- **Security:** SSH and SCP option injection via
  `gale remote` host argument. Hosts starting with `-`
  are now rejected and `--` is inserted before host args.
- **Security:** `gale remote sync` bootstrap pinned to a
  specific commit instead of piping `curl | sh` from
  `main`.
- **Security:** Symlink path-traversal bypass in tar
  extraction. Symlink destinations are now validated
  against the extraction directory.
- **Security:** GHCR bearer token leaked to non-GHCR
  hosts matching OCI path patterns. `isGHCR` now checks
  the URL host, not just the path.
- **Security:** Bearer tokens no longer sent over plain
  HTTP. `FetchWithAuth` requires HTTPS.
- **Security:** `install --recipe` and `install --path`
  now verify Sigstore attestation on GHCR binaries.
  Previously these paths constructed the installer
  without a Verifier.
- **Security:** AI recipe tool `lint_recipe` validated
  against path traversal. Agent-supplied paths must be
  within the temp directory.
- **Security:** Registry commit hashes validated as hex
  before URL construction.
- **Security:** Direct `Registry` struct construction no
  longer bypasses signature verification. `PublicKey` is
  now unexported.
- `prependPATH` now replaces the existing PATH entry
  instead of appending a duplicate. Previously `gale
  shell` and `gale run` failed to expose project
  binaries.
- `syncIfNeeded` warns on sync failure instead of
  silently swallowing errors.
- `gale shell --project` now syncs against the target
  project instead of cwd. Nested subdirectories are
  resolved to the project root via `gale.toml` or
  `.tool-versions`.
- `gale sync` SHA256 mismatch now evicts the package
  from the store before generation rebuild.
- `gale sync --project` flag is now honored (was
  silently ignored).
- `gale update --git` no longer rebuilds unconditionally
  when the installed version is semver.
- `gale update` iterates packages in deterministic
  sorted order.
- Lockfile updated even when install returns a cached
  result.
- `gale remove` updates config before deleting from
  store (was reversed, no rollback on config failure).
- `gale remove` warns when the package is not in the
  store.
- `gale gc` summary separates package version count from
  generation directory count.
- `gale generations rollback` rejects zero and negative
  generation numbers.
- `gale env` uses POSIX shell quoting instead of Go `%q`
  for variable exports.
- `gale sbom` falls back to global config when no
  project config exists.
- `gale outdated` uses semver comparison; registry
  regressions no longer reported as outdated.
- `gale audit` and `gale verify` use consistent scope
  for lockfile and installer context.
- `gale lint` displays errors with error-level output
  instead of warning-level.
- `gale repo add` and `gale repo remove` now persist
  changes to `config.toml`.
- `gale which` validates full store path structure
  including `bin/` segment.
- `gale pin` verifies the package exists before writing
  the pinned entry.
- `formatDevVersion` handles pre-release tags without
  panicking.
- `recipeFileResolver` returns an error instead of nil
  on `filepath.Abs` failure.
- `lockfilePath` validates the `.toml` suffix before
  slicing.
- Empty package names return errors instead of panicking
  across recipe resolution, AI tools, and install paths.
- AI recipe creation detects dependency cycles via a
  visited set.
- AI download tool uses unique filenames to prevent
  SHA256 collisions.
- AI `parseMissingDep` validates GitHub repo format.
- Build temp tool directories cleaned up after each step
  instead of leaking.
- `setDefault` in build env checks the env slice instead
  of host `os.Getenv`, preventing host flag leakage.
- `detectSourceRoot` ignores non-directory files at
  tarball root.
- Darwin `FixupBinaries` returns an error when `otool`
  fails instead of silently skipping.
- `copyFile` preserves source file permissions.
- HTTP clients use a 5-minute timeout instead of hanging
  indefinitely.
- File descriptor leak in `CreateTarZstd` walk callback
  fixed.
- Concurrent installs serialized via per-package file
  lock. Lock files persist to prevent inode-split race.
- `InstallLocal` builds into a staging directory and
  swaps atomically, preserving the active version during
  rebuild.
- `generation.Build` iterates packages in sorted order
  for deterministic symlink conflict resolution.
- `generation.Build` and `Rollback` use PID-scoped temp
  link paths to prevent concurrent swap corruption.
- `Store.IsInstalled` verifies directory has contents,
  not just existence.
- `Store.List` skips in-progress `.build-` staging dirs.
- `InstallBuildDeps` deep-copies recipe maps to prevent
  aliasing.
- Concurrent config writes serialized via file locking.
- `Build.Debug` field parsed from recipe TOML (was
  silently discarded).
- Unrecognized build section keys rejected instead of
  becoming phantom platform overrides.
- `lockfile.IsStale` always performs package-content
  comparison, immune to clock skew.
- `trust.Verify` test contract pinned to assert specific
  `(false, nil)` return for malformed signatures.

## v0.8.2 — 2026-04-03

### Added

- Build environment now exports `DEP_CPPFLAGS` and
  `DEP_LDFLAGS` with dependency include/library paths.
  Recipes that override `CPPFLAGS` inline can use
  `${DEP_CPPFLAGS}` to preserve dep paths.

### Fixed

- `gale update` no longer downgrades packages when
  the registry recipe has an older version than what
  is installed. Uses semver comparison to skip updates
  where the candidate version is not strictly newer.
- `gale build` and `gale audit` now resolve all
  dependency types (build, runtime, and implicit system
  deps). Previously only explicit build deps triggered
  resolution, so recipes with only runtime deps were
  built without their dependencies.
- Transitive dependency `DEP_*` environment variables
  are now available during builds. Previously only
  direct deps appeared in `NamedDirs`, so `DEP_*` vars
  for indirect deps were missing from the build
  environment.

## v0.8.1 — 2026-04-03

### Fixed

- `install --path` now adds the package to gale.toml
  and rebuilds the generation when the version is
  already in the store. Previously it returned early,
  leaving the package unlinked.
- `gale info` now shows the actual error when a
  registry fetch fails instead of always reporting
  "not found".
- `just bootstrap` installs gale globally (`-g`)
  instead of to project scope.

## v0.8.0 — 2026-04-02

### Breaking Changes

- `--local` flag renamed to `--recipes [path]` on
  install, add, update, sync, outdated. Accepts an
  optional path argument; defaults to sibling
  `../gale-recipes/` when used bare.
- `--source <path>` renamed to `--path <dir>` on
  install and update (follows cargo convention).
- `--source` (bool) renamed to `--build` on sync.
- `gale diff` removed. Use `gale sync --dry-run`.
- Scope defaults to project when `gale.toml` exists
  in the directory tree. Previously prompted
  interactively. Use `-g` to override to global.

### Added

- `--verbose`/`-v` global flag for verbose output.
- `--dry-run`/`-n` global flag on all mutating
  commands: install, remove, sync, update, gc.
- `--build` flag on install and update to build from
  source, skipping prebuilt binaries.
- `--git` flag on sync to clone and build all
  packages from git.
- `-g`/`--global` and `-p`/`--project` scope flags
  on remove (previously only on install, add, sync).
- `gale info <pkg>` shows package metadata: version,
  store path, scope, config path, pin status. Falls
  back to registry for uninstalled packages.
- `gale generations` lists all generations with
  active marker.
- `gale generations diff [N] [M]` compares packages
  between two generations.
- `gale generations rollback [N]` switches to a
  previous generation via atomic symlink swap.
- Generations are now retained after build instead
  of deleted. `gale gc` cleans up old generations.

### Fixed

- `FixupBinaries` and `AddDepRpaths` skip files inside
  `.dSYM` debug symbol bundles. These contain Mach-O
  DWARF data that install_name_tool cannot modify.
  Fixes ruby and other packages that emit dSYM bundles
  during build.

## v0.7.0 — 2026-04-01

### Added

- Build env exposes `DEP_<NAME>` env vars for each dep
  store directory. Recipes reference as `${DEP_READLINE}`,
  `${DEP_OPENSSL}`, etc. Uppercased, hyphens become
  underscores.
- Dep `-I`/`-L` flags added to CPPFLAGS/LDFLAGS so
  autotools configure scripts find dep headers and
  libraries. On macOS, `-Wl,-rpath` is also added so
  linked binaries resolve dep dylibs at runtime.
- `FixupPkgConfig` rewrites `.pc` files to use relative
  `${pcfiledir}/../..` paths. Runs on both source builds
  and binary installs. Fixes stale CI build paths in
  pkg-config files.
- PYTHONPATH auto-discovery: build env scans dep store
  dirs for `lib/python*/site-packages/` and adds them
  to PYTHONPATH. Fixes meson and other Python-based
  build tools finding their modules.
- `AddDepRpaths` adds LC_RPATH entries to binaries for
  dep store lib dirs. Scans Mach-O binaries for @rpath
  references, finds the dep containing each library,
  and adds the rpath via install_name_tool. Skips
  gracefully when header space is insufficient.
- `-Wl,-headerpad_max_install_names` added to LDFLAGS
  on macOS so install_name_tool can modify headers
  post-build.
- Generation dynamically scans package store dirs and
  symlinks all subdirectories (bin, lib, libexec,
  share, etc.) instead of a hardcoded list. Fixes
  packages like git whose helpers live in libexec/.
- Generation symlinks root-level files from packages
  (e.g., `go.env`, `VERSION`). Fixes Go's GOROOT
  discovery when running through the generation
  symlink — `GOPROXY` and other defaults now resolve.

### Changed

- `FixupBinaries` walks the entire prefix tree for
  Mach-O files instead of only scanning bin/ and lib/.
  Catches binaries in libexec/ and other non-standard
  directories.
- `AddDepRpaths` warns to stderr when an rpath cannot
  be added due to insufficient Mach-O header space,
  instead of failing silently.

- `create-recipe` agent now consults Homebrew formulas
  via `homebrew_formula` tool for configure flags and
  dependency handling. Raw Ruby source returned for the
  AI to interpret — no regex parsing.

### Removed

- `gale import homebrew` command and the Ruby formula
  parser (`internal/homebrew/`). Superseded by the
  `homebrew_formula` tool in `create-recipe`.

### Fixed

- `NamedDirs` not passed through when constructing
  `BuildDeps` in `gale build` and `gale audit`.

### Previously added

- `gale create-recipe <repo>` generates recipes using
  the Anthropic API. Agentic workflow detects build
  system, computes SHA256, lints, and iterates.
  Configurable via `[anthropic]` in config.toml.
  User-extensible prompt via `prompt_file` config.
- Default compiler flags: `-O2` CFLAGS/CXXFLAGS and
  `-Wl,-S` LDFLAGS in release mode (default).
  Debug mode (`--debug`, recipe `build.debug`, or
  config `[build] debug`): `-O0 -g`, no stripping.
  User-set flags are never overridden.
- `ZERO_AR_DATE=1` always set for deterministic
  ar archives.
- `--debug` and `--release` flags on `gale build`
  and `gale install`.
- Configuration reference: `docs/configuration.md`
  covers all gale.toml and config.toml sections.
- Man page: CONFIGURATION section, cmake/compiler
  env vars, debug/release flags.

- Build system support for meson, zig, python, and
  ruby in `create-recipe` prompt, lint dep checks,
  and `SystemDeps` auto-resolution.
- Recursive dependency resolution in `create-recipe`.
  Agent verifies each dependency has a gale recipe via
  `check_recipe` tool. Missing deps are created
  automatically before retrying the original recipe.
  Recursion capped at depth 3.

### Changed

- `create-recipe` agent uses `list_files` tool to
  discover build system files in one call instead of
  guessing with multiple `read_file` attempts.
- `create-recipe` max iterations increased from 10
  to 15, with prompt guidance to fix all lint errors
  in a single rewrite and stop looping on warnings.
- `create-recipe` now returns release asset URLs from
  `github_info`, so autotools projects use release
  tarballs (with pre-generated configure) instead of
  archive tarballs that require autoreconf + m4.
- `create-recipe` prompt detects cmake library deps
  by following `add_subdirectory()` into subdirectory
  CMakeLists.txt files for `find_package()` calls.
- Lint warns when build steps use `autoreconf`,
  suggesting a release tarball to avoid the autotools
  dependency chain (autoconf → m4).

### Fixed

- Build archives now deterministic: absolute symlink
  targets within the source tree are relativized, and
  zstd uses single-threaded encoding for consistent
  output. Fixes broken symlinks after extraction.
- Source builds with .tar.xz archives now work.
  `Build()` hardcoded the download filename as
  `source.tar.gz`, routing all archives through gzip
  regardless of actual format.

## v0.6.0 — 2026-03-29

### Added

- `gale completion` generates shell completion
  scripts for bash, zsh, fish, and powershell.
- `CMAKE_LIBRARY_PATH` and `CMAKE_INCLUDE_PATH`
  set in build environment from dependency store
  paths. Fixes cmake-based recipes failing to find
  gale-installed dependencies.
- `gale pin <pkg>` and `gale unpin <pkg>` for
  version pinning. `gale update` skips pinned
  packages. Pin state in `[pinned]` section of
  gale.toml.
- `gale remote sync|diff|export <host>` for
  managing packages on remote machines via SSH.
  Bootstraps gale on host if not installed.
- Build system presets: `build.system` auto-adds
  required build deps (cmake, go, rust). Sets
  `CMAKE_PREFIX_PATH` for cmake builds.
- tar.xz and tar.bz2 extraction support via
  `ExtractSource` dispatcher. Enables 16 recipes
  using non-gzip tarballs (git, nodejs, curl, etc).

### Changed

- Recipe resolution functions consolidated into
  `cmd/gale/recipes.go`.
- `syncIfNeeded` calls `runSync` directly instead
  of shelling out to `gale sync` as a subprocess.
- Attestation uses `Verifier` interface on Installer
  instead of global `Disable`/`Enable` state.
- `gale audit` help text documents that mismatches
  are expected until builds are deterministic.

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
