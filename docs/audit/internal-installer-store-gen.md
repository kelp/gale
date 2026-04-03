# Audit Report: internal/installer + store + generation

## Summary

Six bugs across the installer, store, and generation subsystems.
The most critical are a race condition in concurrent installs,
non-deterministic symlink conflict resolution in generation
builds, and a shared hardcoded temp-link path that corrupts
atomic symlink swaps when two processes run concurrently.

## Bugs Found

### BUG-1: Race condition in concurrent Install calls

- **File:** `internal/installer/installer.go:45-54`
- **Severity:** High
- **Category:** race-condition
- **Description:** `Install` does a non-atomic check-then-act:
  `Store.IsInstalled` then `Store.Create` then download/build.
  Two concurrent callers for the same package+version both pass
  `IsInstalled`, both call `Create` (idempotent MkdirAll), and
  both write into the same store directory simultaneously. No
  mutex, no file lock, no pid-file exists.
- **Code path:** `Install` -> `IsInstalled` -> `Create` ->
  concurrent writes to `storeDir`.
- **Impact:** Silent data corruption in the store.

### BUG-2: Map aliasing in InstallBuildDeps recipe copy

- **File:** `internal/installer/installer.go:302-311`
- **Severity:** Medium
- **Category:** logic
- **Description:** `InstallBuildDeps` creates a shallow copy of
  the recipe. `Build.Platform` and `Binary` are map fields that
  share backing storage between original and copy. Any downstream
  write to either map mutates the original.
- **Code path:** `InstallBuildDeps` -> struct literal copy ->
  `Install` (recursive).
- **Impact:** Latent mutation hazard. Will surface the first time
  downstream code modifies these maps.

### BUG-3: Non-deterministic symlink conflict resolution in generation.Build

- **File:** `internal/generation/generation.go:43`
- **Severity:** High
- **Category:** logic
- **Description:** `Build` iterates `pkgs map[string]string`
  directly. Go map iteration is randomized. When two packages
  install the same filename, `symlinkDir` silently skips
  conflicts. Which package wins depends on random map order.
- **Code path:** `Build(pkgs, ...)` -> `for name, version :=
  range pkgs` -> `symlinkDir` skips on existing dest.
- **Impact:** Irreproducible generations. Two `gale sync` runs
  with the same config can produce different `current/bin/`.

### BUG-4: Store.IsInstalled false positive for empty directories

- **File:** `internal/store/store.go:41-48`
- **Severity:** Medium
- **Category:** edge-case
- **Description:** `IsInstalled` returns true for any existing
  directory, regardless of contents. `Store.Create` creates the
  directory before any download. If the process is killed after
  `Create` but before completion, an empty dir remains. Next
  `Install` returns "cached" immediately.
- **Code path:** `Install` -> `Create` -> kill -> next run:
  `IsInstalled` true -> return "cached".
- **Impact:** Silent broken install. `gale sync` does not repair.

### BUG-5: No rollback when generation.Build fails after store install

- **File:** caller contract between installer and generation
- **Severity:** Medium
- **Category:** error-handling
- **Description:** If `Install` succeeds but `generation.Build`
  fails, the package is in the store but never referenced by
  any generation. No cleanup occurs.
- **Code path:** `Install(r)` succeeds -> `generation.Build()`
  error -> store has unreferenced package.
- **Impact:** Package appears installed (IsInstalled true) but
  is absent from PATH. Requires `gale gc` + re-install.

### BUG-6: Shared temp-link path corrupts concurrent atomic symlink swaps

- **File:** `internal/generation/generation.go:85`,
  `internal/generation/history.go:163`
- **Severity:** High
- **Category:** race-condition
- **Description:** Both `Build` and `Rollback` use the same
  hardcoded `tmpLink := filepath.Join(galeDir, "current-new")`.
  The three-step swap (`Remove`, `Symlink`, `Rename`) races
  when two processes run concurrently. Process A's temp link can
  be overwritten by process B before rename.
- **Code path:** `Build` (generation.go:85-94) and `Rollback`
  (history.go:163-170) both write `galeDir/current-new`.
- **Impact:** `current` symlink points at the wrong generation
  after concurrent swap.

## Test Coverage Gaps

- Partial-install persistence not tested.
- Concurrent Install not tested.
- Non-deterministic generation order not tested.
- `current-new` collision not tested.
- Map aliasing not tested.
- `TestInstallSkipsAlreadyInstalled` normalizes the BUG-4
  behavior rather than guarding against it.

## Files Reviewed

- `internal/installer/installer.go`
- `internal/store/store.go`
- `internal/generation/generation.go`
- `internal/generation/history.go`
- `internal/installer/installer_test.go`
- `internal/installer/deps_test.go`
- `internal/store/store_test.go`
- `internal/generation/generation_test.go`
- `internal/generation/history_test.go`
