# Audit Report: cmd-info-query

## Summary

Four medium-severity bugs across the info, query, and SBOM
commands. Main themes: weak store-path validation in `which.go`,
a config-path inconsistency in `sbom.go` that breaks global
usage, misleading "outdated" output for registry regressions,
and a TOCTOU gap in `pin.go`.

## Bugs Found

### BUG-1: which.go store-path guard accepts paths missing the bin/ segment

- **File:** `cmd/gale/which.go:74-78`
- **Severity:** Medium
- **Category:** logic / edge-case
- **Description:** `resolveWhich` splits the relative store path
  with `strings.SplitN(rel, sep, 3)` and guards with
  `len(parts) < 2`. The documented store layout is
  `<name>/<version>/bin/<binary>` (4 segments). With `n=3`, a
  valid path yields 3 parts. The guard only rejects paths with
  fewer than 2 parts, so a two-segment path (binary at version
  root, not under `bin/`) passes validation.
- **Code path:** `gale which <binary>` -> `resolveWhich`.
- **Impact:** Corrupt generation symlinks silently produce wrong
  results instead of the "unexpected store path" error.

### BUG-2: sbom fails for global-only users

- **File:** `cmd/gale/sbom.go:37-43`
- **Severity:** Medium
- **Category:** error-handling / edge-case
- **Description:** `sbom` resolves config with
  `resolveConfigPath(false)`. When no project `gale.toml`
  exists, that returns `<cwd>/gale.toml` (nonexistent). The
  `os.ReadFile` call fails before the global fallback in
  `newCmdContext("")` is reached.
- **Code path:** `gale sbom` from any directory without a
  project config.
- **Impact:** `gale sbom` is broken for global-scope usage
  outside a project directory.

### BUG-3: outdated reports registry regressions as "outdated"

- **File:** `cmd/gale/outdated.go:51-56`
- **Severity:** Medium
- **Category:** logic
- **Description:** The comparison `r.Package.Version != version`
  flags any difference as outdated and labels the registry
  version "Latest". If the registry has a lower version than
  installed (recipe rollback, git-hash version), the package
  appears outdated with an older version labeled "Latest".
- **Code path:** `gale outdated` with locally-built or
  pinned-ahead packages.
- **Impact:** Spurious output suggesting a downgrade is
  available. Needs semver-aware comparison.

### BUG-4: pin.go TOCTOU gap allows pinning a removed package

- **File:** `cmd/gale/pin.go:34-40`
- **Severity:** Medium
- **Category:** race-condition / logic
- **Description:** `pinCmd` reads config and checks the package
  is present before calling `config.PinPackage`.
  `config.PinPackage` reads the config independently and writes
  unconditionally. Between the pre-check read and the write, a
  concurrent `gale remove` could delete the package. The result
  is a stale `[pinned]` entry for a removed package.
- **Code path:** `gale pin <pkg>` concurrent with
  `gale remove <pkg>`.
- **Impact:** Orphaned pinned entries in gale.toml.

## Test Coverage Gaps

- `info.go`: only tests command registration.
- `list.go`, `search.go`, `sbom.go`, `pin.go`: no test files.
- `outdated.go`: tests only `formatOutdated` string formatting.

## Files Reviewed

- `cmd/gale/info.go`
- `cmd/gale/which.go`
- `cmd/gale/list.go`
- `cmd/gale/search.go`
- `cmd/gale/sbom.go`
- `cmd/gale/outdated.go`
- `cmd/gale/pin.go`
- `cmd/gale/info_test.go`
- `cmd/gale/which_test.go`
- `cmd/gale/outdated_test.go`
- `cmd/gale/context.go`
- `cmd/gale/paths.go`
