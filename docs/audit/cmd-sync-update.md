# Audit Report: cmd-sync-update

## Summary

Six bugs in the sync and update commands. A SHA256 mismatch
in sync does not prevent the compromised package from entering
the active generation; the `--project` flag in sync is silently
dropped; `update --git` cannot correctly detect "already up to
date" for semver-versioned packages; and `syncIfNeeded` discards
all errors. Test coverage for sync is structural only.

## Bugs Found

### BUG-1: SHA256 mismatch does not evict installed package from generation

- **File:** `cmd/gale/sync.go:115-136`
- **Severity:** High
- **Category:** logic
- **Description:** When the SHA256 of a freshly installed package
  does not match the lockfile, `sync` increments `failed` and
  `continue`s. But `Install` already wrote the package into the
  store at line 115. The `rebuildGeneration` call at line 142
  reads gale.toml and includes every listed package, so the
  mismatched package ends up in the active generation.
- **Code path:** `gale sync` when a package download or build
  produces a hash differing from `gale.lock`.
- **Impact:** A tampered or corrupted package is installed and
  made active on PATH, defeating the lockfile integrity check.

### BUG-2: syncIfNeeded silently swallows all sync errors

- **File:** `cmd/gale/shell.go:83`
- **Severity:** High
- **Category:** error-handling
- **Description:** `syncIfNeeded` detects a stale lockfile and
  calls `runSync`, but discards its return value:
  `_ = runSync("", false, false)`. Any error is silently
  suppressed. Both `gale shell` and `gale run` call this.
- **Code path:** `gale shell` or `gale run` when gale.toml is
  newer than gale.lock.
- **Impact:** Users run commands in an out-of-date or broken
  environment with no indication that sync failed.

### BUG-3: --project flag in gale sync is silently ignored

- **File:** `cmd/gale/sync.go:25-29`
- **Severity:** Medium
- **Category:** logic
- **Description:** `syncProject` is declared and validated for
  mutual exclusion with `syncGlobal`, but is never passed to
  `runSync`. Running `gale sync --project` from a directory
  without a `gale.toml` silently syncs the global config.
- **Code path:** `gale sync --project` from a non-project dir.
- **Impact:** A user expecting project scope gets global scope.

### BUG-4: update --git compares semver version against git short hash

- **File:** `cmd/gale/update.go:208`
- **Severity:** Medium
- **Category:** logic
- **Description:** `updateFromGit` compares `cfg.Packages[name]`
  (e.g., `"1.7.1"`) against `remoteHash` (a 7-char git hash).
  These are never equal for a normally-installed package, so the
  "already up to date" path is unreachable. Every
  `gale update --git` rebuilds unconditionally.
- **Code path:** Install normally, then `gale update <pkg> --git`.
- **Impact:** Wastes time; no "up to date" feedback.

### BUG-5: Non-deterministic update order causes unpredictable partial failures

- **File:** `cmd/gale/update.go:105`
- **Severity:** Medium
- **Category:** logic
- **Description:** `targets` is a `map[string]target`. Go map
  iteration is non-deterministic. When `writeConfigAndLock` fails
  mid-loop, which packages were installed but not recorded varies
  between runs.
- **Code path:** `gale update` with multiple packages, one fails.
- **Impact:** On partial failure, gale.toml diverges from the
  store unpredictably.

### BUG-6: Lockfile not updated when update lands on a cached store entry

- **File:** `cmd/gale/context.go:218`, `cmd/gale/update.go:155`
- **Severity:** Medium
- **Category:** logic
- **Description:** When `Install` returns a cached result,
  `result.SHA256` is `""`. `writeConfigAndLock` skips the
  lockfile when sha256 is empty. The lockfile retains the hash
  from a previous version.
- **Code path:** `gale update foo@1.0.0` when 1.0.0 is already
  in the store.
- **Impact:** Lockfile SHA256 diverges from installed version.

## Test Coverage Gaps

- `sync_test.go` only checks flag existence. No behavioral tests.
- No test for `updateFromGit` version comparison.
- `outdated_test.go` tests only string formatting.
- No test for `syncIfNeeded` error suppression.

## Files Reviewed

- `cmd/gale/sync.go`
- `cmd/gale/update.go`
- `cmd/gale/sync_test.go`
- `cmd/gale/outdated_test.go`
- `cmd/gale/context.go`
- `cmd/gale/paths.go`
- `cmd/gale/recipes.go`
- `cmd/gale/shell.go`
- `internal/lockfile/lockfile.go`
- `internal/gitutil/gitutil.go`
- `internal/installer/installer.go`
