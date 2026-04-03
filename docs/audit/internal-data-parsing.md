# Audit Report: internal/data-parsing

## Summary

Six bugs across the config, recipe, lint, lockfile, repo,
gitutil, output, and env packages. The most impactful are a
silent data-loss race on config writes and a `Build.Debug` field
that is structurally impossible to populate from a recipe file.

## Bugs Found

### BUG-1: Concurrent config writes clobber each other

- **File:** `internal/config/config.go:147`
- **Severity:** High
- **Category:** race-condition
- **Description:** All four config-mutation functions
  (`AddPackage`, `RemovePackage`, `PinPackage`, `UnpinPackage`)
  follow an unguarded read-modify-write pattern. No file lock
  or retry loop. Two concurrent callers each read the same state;
  whichever finishes last discards the other's change.
  `WriteGaleConfig` uses an atomic rename for the write itself,
  so the file is never corrupt, but the second writer's
  in-memory state does not reflect the first writer's addition.
- **Code path:** `AddPackage` -> `os.ReadFile` -> `ParseGaleConfig`
  -> (no lock) -> `WriteGaleConfig` (atomic rename). A racing
  call does the same and overwrites.
- **Impact:** Lost package entries in `gale.toml` when two
  install or remove operations run in parallel.

### BUG-2: Build.Debug is structurally unpopulatable from recipes

- **File:** `internal/recipe/recipe.go:100-200`
- **Severity:** High
- **Category:** logic
- **Description:** The `Build` struct defines a `Debug bool`
  field. However, `rawRecipe` decodes `[build]` as
  `map[string]interface{}`. The `parseBuild` function only
  extracts `"steps"` and `"system"` by key; all other keys are
  treated as platform-override sub-tables (checked with a
  `map[string]interface{}` type assertion). A TOML boolean like
  `debug = true` fails that assertion and is silently discarded.
  `Build.Debug` is always `false`.
- **Code path:** `Parse` -> `parseBuild(raw.Build)`. `raw["debug"]`
  is `true` (bool); `val.(map[string]interface{})` fails; key
  skipped.
- **Impact:** Recipe authors who set `debug = true` in `[build]`
  see no effect. The feature is silently broken.

### BUG-3: localRecipeResolver panics on empty package name

- **File:** `cmd/gale/recipes.go:20`
- **Severity:** Medium
- **Category:** nil-deref (index out of range)
- **Description:** `localRecipeResolver` indexes `name[0]` to
  derive the letter bucket without checking for empty string.
  Same unguarded pattern at `cmd/gale/install.go:406` and
  `cmd/gale/create_recipe.go:310,352`.
- **Code path:** Any command using `--recipes` with an empty
  package name argument.
- **Impact:** Process panic instead of user-facing error.

### BUG-4: Typo'd build section keys become silent phantom platform overrides

- **File:** `internal/recipe/recipe.go:174-197`
- **Severity:** Medium
- **Category:** edge-case
- **Description:** `parseBuild` iterates the raw build map and,
  after handling `"steps"` and `"system"`, treats every remaining
  key whose value is a table as a per-platform override. A typo
  like `[build.stteps]` silently becomes platform `"stteps"`.
  A key like `[build.darwin_arm64]` (underscores) never matches
  `BuildForPlatform("darwin", "arm64")` which uses hyphens.
- **Code path:** `parseBuild` -> range over raw -> any key not
  `"steps"` or `"system"` with a table value.
- **Impact:** Misconfigured platform build steps are silently
  ignored. No warning produced.

### BUG-5: lockfile.IsStale mtime comparison is fooled by clock skew

- **File:** `internal/lockfile/lockfile.go:102`
- **Severity:** Medium
- **Category:** logic
- **Description:** `IsStale` uses
  `tomlInfo.ModTime().After(lockInfo.ModTime())`. If the system
  clock jumps backwards between writing gale.toml and gale.lock,
  the lock file appears newer. The package-content comparison
  at lines 111-118 is a safety net but cannot catch the case
  where mtime lies and package versions also match.
- **Code path:** `IsStale` -> mtime comparison -> false positive
  "fresh" when clock-skewed.
- **Impact:** `gale run` or direnv hooks may skip a sync that
  should have run.

### BUG-6: Test helper panics on empty filename key

- **File:** `internal/repo/repo_test.go:612`
- **Severity:** Medium
- **Category:** edge-case (test code)
- **Description:** `setupCachedRepoWithLetterDirs` computes the
  letter bucket as `string(fname[0])`. An empty string key in
  the `recipes` map panics with index out of range.
- **Code path:** Test helper with empty filename key.
- **Impact:** Future tests that pass empty filenames get cryptic
  panics.

## Test Coverage Gaps

- No concurrent-write test for config mutations.
- No test for `Build.Debug` round-trip from recipe parse.
- No test for `parseBuild` with unknown/typo'd keys.
- No clock-skew test for `lockfile.IsStale`.
- No test for empty `ref` in `gitutil.RemoteHead`.
- No test for shell correctness of generated hook.

## Files Reviewed

- `internal/config/config.go`
- `internal/config/toolversions.go`
- `internal/recipe/recipe.go`
- `internal/recipe/binaries.go`
- `internal/lint/lint.go`
- `internal/lockfile/lockfile.go`
- `internal/repo/repo.go`
- `internal/gitutil/gitutil.go`
- `internal/output/output.go`
- `internal/env/env.go`
- `internal/config/config_test.go`
- `internal/config/toolversions_test.go`
- `internal/recipe/recipe_test.go`
- `internal/recipe/binaries_test.go`
- `internal/lint/lint_test.go`
- `internal/lockfile/lockfile_test.go`
- `internal/repo/repo_test.go`
- `internal/gitutil/gitutil_test.go`
- `internal/output/output_test.go`
- `internal/env/env_test.go`
