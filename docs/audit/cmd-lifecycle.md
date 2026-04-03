# Audit Report: cmd-lifecycle

## Summary

Four bugs in the remove, gc, and generations commands. The most
impactful is store deletion before config update with no
rollback on config write failure. GC summary conflates package
versions with generation directories.

## Bugs Found

### BUG-1: Store deleted before config updated -- no rollback on failure

- **File:** `cmd/gale/remove.go:70-84`
- **Severity:** High
- **Category:** error-handling
- **Description:** `remove.go` removes the package from the
  on-disk store (`st.Remove`) before removing it from
  `gale.toml`. If `config.RemovePackage` fails (disk full,
  permissions, concurrent write), the store entry is already
  gone but the package is still listed in config. The system
  is now inconsistent.
- **Code path:** `st.Remove(name, version)` at line 71, then
  `config.RemovePackage(configPath, name)` at line 80. If
  step 2 fails, step 1 cannot be undone.
- **Impact:** On next `gale sync`, gale attempts to reinstall.
  For packages installed with `--path` from a source that no
  longer exists, reinstall fails permanently.

### BUG-2: GC summary message conflates package versions with generation directories

- **File:** `cmd/gale/gc.go:56-124`
- **Severity:** Medium
- **Category:** logic
- **Description:** The `removed` counter accumulates both removed
  store package versions and removed old generation directories.
  The final summary says `"Removed %d version(s)"` with the
  conflated count.
- **Code path:** `removed` += store packages + generation dirs.
- **Impact:** Misleading output. User sees "5 version(s)" when
  2 were packages and 3 were generation directories.

### BUG-3: genRollbackCmd accepts zero and negative generation numbers

- **File:** `cmd/gale/generations.go:144-149`
- **Severity:** Medium
- **Category:** edge-case
- **Description:** After `strconv.Atoi`, the parsed `target` is
  not validated to be positive. The error from
  `generation.Rollback` is a confusing filesystem stat error
  rather than clear validation.
- **Code path:** `gale generations rollback 0` or `rollback -3`.
- **Impact:** Poor UX; incorrect input produces confusing error.

### BUG-4: remove.go silently no-ops when package is not in store

- **File:** `cmd/gale/remove.go:70-77`
- **Severity:** Medium
- **Category:** logic
- **Description:** If `st.IsInstalled` returns false, the store
  removal is silently skipped with no warning. Config removal
  and generation rebuild proceed, but the user gets no
  indication the store entry was already missing.
- **Code path:** Package in gale.toml but not in store (e.g.,
  manually deleted).
- **Impact:** Silent divergence between store state and user
  expectation.

## Test Coverage Gaps

- `remove.go` has no tests at all. The entire removal flow is
  untested.
- `generations_test.go` only checks command registration, not
  behavior.
- `gc_test.go` unnecessarily mutates the global `dryRun` var.
- No test covers the cross-project GC gap.

## Files Reviewed

- `cmd/gale/remove.go`
- `cmd/gale/gc.go`
- `cmd/gale/generations.go`
- `cmd/gale/gc_test.go`
- `cmd/gale/generations_test.go`
- `cmd/gale/context.go`
- `cmd/gale/paths.go`
- `internal/store/store.go`
- `internal/generation/generation.go`
- `internal/generation/history.go`
- `internal/config/config.go`
