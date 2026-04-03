# Audit Report: cmd-shared

## Summary

Four confirmed bugs in the command framework shared helpers,
ranging from High to Medium severity. The most critical is
`prependPATH` silently failing to expose project binaries in
`gale shell` and `gale run` due to duplicate PATH entries.
A nil return from `recipeFileResolver` causes a guaranteed
nil-pointer panic in all callers.

## Bugs Found

### BUG-1: prependPATH appends duplicate PATH entry; original wins

- **File:** `cmd/gale/shell.go:88`
- **Severity:** High
- **Category:** logic
- **Description:** `prependPATH` calls `os.Environ()`, which
  already contains the process's `PATH=...` entry, then
  appends a second `PATH=<binDir>:<existing>` entry to the
  slice. On Linux and macOS, `getenv(3)` scans `envp[]`
  from index 0 and returns the first match. Because
  `os.Environ()` places the real `PATH` at a low index and
  the new entry is appended at the end, `getenv` in the
  child process returns the original (un-prepended) `PATH`.
  The gale bin dir is never actually visible to programs
  running inside the shell or command.
- **Code path:** `gale shell` -> `prependPATH(binDir)` ->
  `exec.Command(shell)` with duplicate PATH. Same path via
  `gale run`.
- **Impact:** `gale shell` and `gale run` do not expose
  project-environment binaries. Any tool that relies on
  PATH lookup inside the spawned shell or command will use
  the ambient PATH, bypassing the project environment.

### BUG-2: recipeFileResolver returns nil on filepath.Abs failure

- **File:** `cmd/gale/recipes.go:119`
- **Severity:** High
- **Category:** nil-deref
- **Description:** When `filepath.Abs(recipePath)` fails,
  `recipeFileResolver` returns `nil` (a nil function
  pointer) instead of returning an error. All callers
  assign the return value directly to a `RecipeResolver`
  field and then call it without a nil check. Invoking a
  nil function pointer panics.
- **Code path:** Any `gale install --recipe <path>` or
  `gale build <recipe>` call where `filepath.Abs` fails.
  See `install.go:219`, `install.go:320`, `install.go:430`.
- **Impact:** Process panics with a nil pointer dereference
  instead of returning a clean error.

### BUG-3: lockfilePath performs unsafe string slicing

- **File:** `cmd/gale/context.go:203`
- **Severity:** Medium
- **Category:** edge-case
- **Description:** `lockfilePath` computes the lock file
  path by slicing the last five bytes off `configPath`:
  `configPath[:len(configPath)-len(".toml")] + ".lock"`.
  There is no check that `configPath` actually ends in
  `.toml`. If passed a path without that suffix, the slice
  expression produces a wrong result or, for very short
  paths, a negative-length slice that panics at runtime.
- **Code path:** Any call through `lockfilePath`. Called
  from `context.go:220`, `shell.go:78`, `sync.go:72`,
  `verify.go:35`, `sbom.go:50`, `audit.go:31`.
- **Impact:** Silent data corruption (wrong lock file path)
  or panic if an unexpected config path is passed.

### BUG-4: localRecipeResolver panics on empty package name

- **File:** `cmd/gale/recipes.go:21`
- **Severity:** Medium
- **Category:** nil-deref
- **Description:** `localRecipeResolver` indexes `name[0]`
  to derive the letter-bucket directory without first
  checking that `name` is non-empty. An empty string causes
  a runtime panic (index out of range).
- **Code path:** `gale install ""` or any code path that
  calls the resolver with an empty package name.
- **Impact:** Process panics with no useful error message.

## Test Coverage Gaps

- **prependPATH is not tested at all.** No test verifies
  the returned environment slice has exactly one `PATH`
  entry or that the prepended directory appears first.
- **recipeFileResolver nil return is not tested.** The
  error path has no test, and no caller tests the nil case.
- **lockfilePath has no tests.** Never unit-tested. No test
  for paths without a `.toml` suffix.
- **localRecipeResolver empty-name case is untested.**
- **scope_test.go does not test --global/--project mutual
  exclusion** in `syncCmd`.

## Files Reviewed

- `cmd/gale/context.go`
- `cmd/gale/paths.go`
- `cmd/gale/recipes.go`
- `cmd/gale/root.go`
- `cmd/gale/main.go`
- `cmd/gale/help.go`
- `cmd/gale/completion.go`
- `cmd/gale/shell.go`
- `cmd/gale/paths_test.go`
- `cmd/gale/recipes_test.go`
- `cmd/gale/scope_test.go`
- `cmd/gale/root_test.go`
