# Audit Report: internal/build

## Summary

Six bugs in build orchestration and platform fixups. The most
impactful is a per-build-step temp directory leak that
accumulates without bound, and a silent failure in Darwin's
binary fixup that can produce unrunnable binaries.

## Bugs Found

### BUG-1: buildEnv leaks a temp dir per build step

- **File:** `internal/build/build.go:317`
- **Severity:** High
- **Category:** resource-leak
- **Description:** `buildEnv` calls `os.MkdirTemp` to create an
  isolated tools directory but never returns the path or
  registers cleanup. Every build step creates a new
  `gale-tools-*` directory under `~/.gale/tmp/` that is never
  removed. A recipe with ten steps leaks ten directories.
- **Code path:** `buildFromDir` -> (loop) `runStep` ->
  `buildEnv` -> `os.MkdirTemp` (no cleanup).
- **Impact:** Unbounded disk usage in `~/.gale/tmp/`. `gale gc`
  does not remove them.

### BUG-2: Shared fallback toolsDir causes race in concurrent builds

- **File:** `internal/build/build.go:318-320`
- **Severity:** Medium
- **Category:** race-condition
- **Description:** When `os.MkdirTemp` fails, `buildEnv` falls
  back to a fixed path `filepath.Join(os.TempDir(), "gale-tools")`.
  Concurrent builds share this directory. `resolveTools` creates
  symlinks that may collide.
- **Code path:** `buildEnv` (MkdirTemp fails) -> fallback path
  -> `resolveTools` -> `os.Symlink`.
- **Impact:** Concurrent builds in the fallback path may invoke
  wrong binaries.

### BUG-3: setDefault reads host os.Getenv, not isolated env slice

- **File:** `internal/build/build.go:481-487`
- **Severity:** Medium
- **Category:** logic
- **Description:** `setDefault(env, key, val)` checks
  `os.Getenv(key)` against the host process environment. If the
  host has `CFLAGS` etc. set by direnv or the shell, their
  values (including host absolute paths) leak into the build env.
- **Code path:** `buildEnv` -> `setDefault` -> `os.Getenv`.
- **Impact:** Non-reproducible builds. Recipes that succeed in
  CI can fail on dev machines with custom flags.

### BUG-4: detectSourceRoot requires exactly one total entry at tarball root

- **File:** `internal/build/build.go:257`
- **Severity:** Medium
- **Category:** logic
- **Description:** `detectSourceRoot` only enters the single
  subdirectory when `len(dirs) == 1 && len(entries) == 1`. A
  tarball with a non-directory entry at the root alongside the
  package directory (e.g., a stray `README`) causes the function
  to return the extraction root instead. Build steps run in the
  wrong directory.
- **Code path:** `Build` -> `detectSourceRoot` (returns srcDir)
  -> `buildFromDir` -> `runStep` (CWD is wrong).
- **Impact:** Build failures for tarballs with any non-directory
  content at the archive root.

### BUG-5: FixupBinaries (Darwin) silently skips dep rewriting on otoolDeps error

- **File:** `internal/build/fixup_darwin.go:104`
- **Severity:** Medium
- **Category:** error-handling
- **Description:** When `otoolDeps(file)` returns an error, the
  dependency rewriting block is skipped entirely. The file
  retains build-time absolute paths. No error is returned;
  `FixupBinaries` returns nil.
- **Code path:** `FixupBinaries` -> `otoolDeps(file)` error ->
  dep rewrite skipped -> returns nil.
- **Impact:** Mach-O binaries with broken load paths ship
  without error. Binary crashes on first run with dyld "library
  not found".

### BUG-6: copyFile does not preserve source file permissions

- **File:** `internal/build/build.go:681`
- **Severity:** Medium
- **Category:** edge-case
- **Description:** `copyFile` creates destination with
  `os.Create` (mode 0666 minus umask). Source file mode bits
  are not read or applied.
- **Code path:** `Build` -> cache path -> `copyFile`.
- **Impact:** Currently benign for tarball caching. Latent bug
  for any future caller needing permission preservation.

## Test Coverage Gaps

- buildEnv toolsDir leak is not tested.
- detectSourceRoot edge case is not tested.
- otoolDeps error path is not tested.
- setDefault host-env leakage is not tested.
- Concurrent buildEnv calls are not tested.

## Files Reviewed

- `internal/build/build.go`
- `internal/build/fixup_darwin.go`
- `internal/build/fixup_linux.go`
- `internal/build/fixup_pkgconfig.go`
- `internal/build/build_test.go`
- `internal/build/fixup_darwin_test.go`
