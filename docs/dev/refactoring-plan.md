# Refactoring Plan

Addresses all findings from `duplication-audit.md` and
`code-quality-audit.md`. Organized into 7 waves,
ordered by the dependency graph (leaf packages first)
so each wave can be fully tested before the next begins.

All waves use strict red-green-refactor TDD. New
utility packages use the full TDD pipeline (test
writer → red gate → implementer → review). Bug fixes
and refactors in existing packages use inline TDD.

**Dependency graph (leaf → root):**
```
config, lockfile, store, output, recipe  (0 internal deps)
  └─ generation                          (0 internal deps)
  └─ download                            (0 internal deps)
  └─ build                               (download, recipe, output)
  └─ installer                           (build, download, store, recipe)
  └─ cmd/gale                            (everything)
```

**Invariant:** `go test ./...` passes after every
individual commit within every wave.

---

## Wave 1 — New utility packages (leaf, no dependents)

Create two new packages that the rest of the plan
depends on. These have zero internal imports so they
can't break anything. Full TDD pipeline for each.

### 1A. `internal/atomicfile` — atomic write utility

**Addresses:** D2, Q3 partial

**New file:** `internal/atomicfile/atomicfile.go`
**New test:** `internal/atomicfile/atomicfile_test.go`

```go
// Package atomicfile provides crash-safe file writes.
package atomicfile

// Write atomically replaces path with data.
// Creates parent directories if needed.
// Uses temp file + fsync + rename.
func Write(path string, data []byte) error
```

**Tests (write first, must fail):**
1. Writes file that doesn't exist yet
2. Overwrites existing file atomically
3. Creates parent directories
4. Cleans up temp file on write error
5. Content matches after write
6. Concurrent writes don't corrupt (goroutine test)

**After green:** Migrate callers in Wave 4.

### 1B. `internal/filelock` — unified file locking

**Addresses:** I5, I6

**New file:** `internal/filelock/filelock.go`
**New test:** `internal/filelock/filelock_test.go`

```go
// Package filelock provides flock-based file locking.
package filelock

// With acquires an exclusive lock on path, runs fn,
// and releases the lock. The lock file is created if
// needed and kept on disk (never deleted).
func With(path string, fn func() error) error

// Acquire acquires an exclusive lock on path. Returns
// an unlock function. Caller must defer unlock().
func Acquire(path string) (unlock func(), err error)
```

Uses `golang.org/x/sys/unix.Flock` exclusively. No
`syscall` package.

**Tests (write first, must fail):**
1. With() runs fn and returns its error
2. With() creates lock file if missing
3. Acquire/unlock round-trip works
4. Two goroutines serialize (second blocks until first
   unlocks)
5. Lock file persists after unlock
6. With() releases lock even if fn panics

**After green:** Migrate callers in Wave 4.

---

## Wave 2 — Bug fixes in leaf packages (inline TDD)

Fix the 6 confirmed bugs. Each is a single
red-green-refactor cycle against existing test files.

### 2A. `buildEnv` returns nil on failure (Q1)

**File:** `internal/build/build.go:325`

**Red:** Write test in `build_test.go` that calls
`buildEnv` with an invalid temp dir (e.g., set
`GALE_HOME` to a non-writable path). Assert the
returned env is non-nil OR an error is returned.
Currently returns `(nil, func(){})` — test must fail.

**Green:** Change `buildEnv` signature to return
`([]string, func(), error)`. Propagate error through
`runStep`. Return the error from `MkdirTemp` instead
of silently returning nil.

**Callers to update:** `runStep` (same file),
`buildFromDir` (same file).

### 2B. Build step error uses `%s` not `%w` (Q2)

**File:** `internal/build/build.go:286`

**Red:** Write test that creates a recipe with a
failing build step, calls `Build`, and uses
`errors.Is(err, exec.ExitError{})` or similar
unwrapping. Currently fails because `%s` breaks the
chain.

**Green:** Change `%s` to `%w`.

### 2C. `lockfilePath` panics (Q3)

**File:** `cmd/gale/context.go:207`

**Red:** Write test in `context_test.go` that calls
`lockfilePath("nottoml")`. Currently panics. Test
uses `recover` to catch, asserts we get an error
return instead.

**Green:** Change signature to
`lockfilePath(configPath string) (string, error)`.
Update all callers (6 sites in context.go) to handle
the error.

### 2D. `remove` never updates lockfile (B1)

**File:** `cmd/gale/remove.go`

**Red:** Write integration test in `remove_test.go`:
install a package (writes lockfile entry), remove it,
read lockfile, assert the entry is gone. Currently
the entry persists — test fails.

**Green:** After `config.RemovePackage`, add:
```go
lp, _ := lockfilePath(configPath)
removeLockEntry(lp, name)
```
Add `removeLockEntry` to `context.go`.

### 2E. `sync` never writes lockfile hashes (B2)

**File:** `cmd/gale/sync.go`

**Red:** Write integration test in `sync_test.go`:
create a gale.toml with a package, run sync, read
lockfile, assert SHA256 is present. Currently empty.

**Green:** After successful install in the sync loop,
call `updateLockfile(...)`.

### 2F. `syncGit` flag is dead code (B3)

**File:** `cmd/gale/sync.go:18,198`

**Red:** Not applicable — this is a deletion.

**Green:** Either wire up `syncGit` to set
`ctx.Installer.GitOnly = true` (if that makes sense)
or delete the flag entirely. Deleting is safer — the
flag is undocumented and does nothing.

### 2G. `AddRepo`/`RemoveRepo` missing file locking

**File:** `internal/config/config.go:289-320`

**Red:** Write test in `config_test.go` that spawns
10 goroutines each calling `AddRepo` concurrently.
Assert no data loss (all 10 repos present in final
file). Currently races — test fails with `-race`.

**Green:** Wrap both functions in `withFileLock(path, fn)`.

### 2H. Remove ordering — store delete before gen rebuild (I4)

**File:** `cmd/gale/remove.go:82-99`

**Red:** Write test where gen rebuild would reference
the store path. With current order (delete store
first), if gen rebuild reads the store, it fails.
Alternatively, test that after remove, the generation
doesn't have dangling symlinks.

**Green:** Reorder: (1) remove from config, (2)
rebuild generation, (3) remove from store.

### 2I. Update gen rebuild on partial failure (I1)

**File:** `cmd/gale/update.go:183`

**Red:** Hard to test directly. Instead, wrap the gen
rebuild in a `defer` so it always runs if any packages
were updated, even on config write failure.

**Green:** Add `defer` block:
```go
defer func() {
    if updated > 0 && !dryRun {
        rebuildGeneration(ctx.GaleDir, ctx.StoreRoot, ctx.GalePath)
    }
}()
```

---

## Wave 3 — Build package decomposition

This is the highest-severity god function (`buildEnv`,
score 2,490). Refactor it in place with inline TDD.
No API changes to callers outside `internal/build`.

### 3A. Introduce `BuildContext` struct

**File:** `internal/build/build.go`

Replace the 8-parameter `runStep` and 6-parameter
`buildEnv` with a struct:

```go
type BuildContext struct {
    Recipe    *recipe.Recipe
    PrefixDir string
    SourceDir string
    Workspace string
    OutputDir string
    Jobs      string
    Version   string
    System    string
    Debug     bool
    Deps      *BuildDeps
}
```

**Red:** Write tests that construct a `BuildContext`
and call the new decomposed methods. They fail because
the methods don't exist yet.

**Green:** Mechanically extract methods. Existing tests
must still pass.

### 3B. Decompose `buildEnv` into 5 helpers

Extract from the 166-line function:

```go
func (bc *BuildContext) baseEnv() []string
func (bc *BuildContext) depSearchPaths() (lib, inc, pc string)
func (bc *BuildContext) depCompilerFlags() (cppflags, ldflags string)
func (bc *BuildContext) perDepEnv() []string
func (bc *BuildContext) defaultCompilerFlags() []string
```

Keep `buildEnv` as a thin compositor that calls all 5.

**Red per helper:** Write a test for each extracted
function. Must fail initially (function doesn't exist).

**Green per helper:** Move the relevant lines from
`buildEnv` into the new method. Run full suite.

### 3C. Unify `DepPaths` / `BuildDeps`

**Files:** `installer.go:225`, `build.go:299`

**Red:** Write test that uses `build.BuildDeps`
directly from installer code. Currently doesn't
compile (different types).

**Green:** Delete `DepPaths` from installer. Use
`build.BuildDeps` everywhere. Delete `depsToBuildDeps`
converter function. Update `InstallBuildDeps` return
type.

---

## Wave 4 — Migrate to shared utilities

Now that `atomicfile` and `filelock` exist (Wave 1)
and are green, migrate existing callers.

### 4A. Migrate config.go to `atomicfile`

**File:** `internal/config/config.go`

Replace `WriteGaleConfig` and `WriteAppConfig` guts
with `atomicfile.Write`. Keep the TOML encoding.
Run existing 46 config tests.

### 4B. Migrate lockfile.go to `atomicfile`

**File:** `internal/lockfile/lockfile.go`

Replace `Write` guts with `atomicfile.Write`. Run
existing 28 lockfile tests.

### 4C. Migrate config.go to `filelock`

**File:** `internal/config/config.go`

Replace `withFileLock` body with `filelock.With`.
Delete the local `withFileLock` function. Run tests.

### 4D. Migrate installer.go to `filelock`

**File:** `internal/installer/installer.go`

Replace `lockPackage` body with `filelock.Acquire`.
Remove `syscall` import. Run 25 installer tests.

### 4E. Wrap `updateLockfile` with file lock

**File:** `cmd/gale/context.go`

Wrap the read-modify-write in `filelock.With`. Fixes
I5 (lockfile write has no locking).

### 4F. Extract `swapCurrentSymlink`

**File:** `internal/generation/generation.go`

**Red:** Write test that calls `swapCurrentSymlink`
directly. Must fail (doesn't exist).

**Green:** Extract the 13-line PID-scoped swap from
`Build()` into:
```go
func swapCurrentSymlink(galeDir string, genNum int) error
```

Update `Build()` and `Rollback()` (history.go) to call
it. Run existing 29 generation tests.

---

## Wave 5 — Command layer: context unification

Refactor `cmdContext` and the command files. This is
the riskiest wave because it touches every command.
Do one command at a time, running `go test ./cmd/gale/`
after each.

### 5A. Add scope params to `newCmdContext`

**File:** `cmd/gale/context.go`

Change signature:
```go
func newCmdContext(recipesPath string, global, project bool) (*cmdContext, error)
```

Auto-detect scope when both are false (current
behavior). Override when either is true. This unifies
I2 (install doesn't use newCmdContext) and D6 (manual
scope resolution).

Update all existing callers: `update.go`, `sync.go`
pass `false, false`. New callers: `install.go`,
`remove.go`, `gc.go`.

### 5B. Convert free functions to methods on `cmdContext`

**File:** `cmd/gale/context.go`

Move these free functions to methods:
```go
func (ctx *cmdContext) FinalizeInstall(name, version, sha256 string) error
func (ctx *cmdContext) WriteConfigAndLock(name, version, sha256 string) error
func (ctx *cmdContext) RebuildGeneration() error
func (ctx *cmdContext) UpdateLockfile(name, version, sha256 string) error
func (ctx *cmdContext) RemoveLockEntry(name string) error
func (ctx *cmdContext) LockfilePath() (string, error)
func (ctx *cmdContext) ResolveVersionedRecipe(name, version string) (*recipe.Recipe, error)
func (ctx *cmdContext) ReportResult(result *installer.InstallResult, verb, sourceLabel string)
```

This eliminates 4-6 string args per call (galeDir,
storeRoot, configPath are all on ctx). Fixes the
"too many parameters" findings for `finalizeInstall`
(6 strings → 3 strings).

### 5C. Rewrite `install.go` to use `cmdContext`

Replace the manual scope resolution (lines 43-63)
with `newCmdContext(recipesPath, global, project)`.
Convert `installFromGit` and `installFromLocalSource`
to methods on `cmdContext`. Delete `resolveConfigPath`
and `resolveScope` if no longer needed (check gc.go).

Delete `newInstallerForLocalSource` (D1 — identical
twin).

### 5D. Rewrite `remove.go` to use `cmdContext`

Replace inline config reading (I3) with
`ctx.LoadConfig()`. Use `ctx.RemoveLockEntry(name)`
for lockfile cleanup. Use `ctx.RebuildGeneration()`
for gen rebuild. Fixes I3 (swallowed parse errors)
and B1 (lockfile not updated).

### 5E. Rewrite `gc.go` to use `cmdContext`

Replace inline config reading with `ctx.LoadConfig()`.
Use `ctx.RebuildGeneration()` for gen rebuilds.

### 5F. Extract `resolveRecipeResolver`

**File:** `cmd/gale/context.go`

Extract the 4× duplicated resolver construction
(D4) into:
```go
func resolveRecipeResolver(recipesFlag, cwd string) (
    installer.RecipeResolver, *registry.Registry, error)
```

Update `newCmdContext`, `installFromGit`,
`buildCmd.RunE` to call it.

### 5G. Extract `loadRecipeFile`

**File:** `cmd/gale/install.go` or `context.go`

Extract the 3× duplicated recipe file read (D5):
```go
func loadRecipeFile(path string, local bool) (*recipe.Recipe, error)
```

---

## Wave 6 — God function decomposition (non-build)

These are lower priority than Waves 1-5 but still
high-value. Each is independent — they can be done
in parallel by separate agents.

### 6A. Split `doctorCmd.RunE` (185 lines → check registry)

**File:** `cmd/gale/doctor.go`

Define check interface:
```go
type doctorCheck struct {
    name string
    run  func(galeDir, storeRoot string, out *output.Output) (ok bool, msg string)
}
```

Extract 11 checks. RunE becomes a loop. Each check
is independently testable.

### 6B. Split `Lint` (132 lines → rule pipeline)

**File:** `internal/lint/lint.go`

Define rule type:
```go
type lintRule func(r *recipe, filePath string) []Issue
```

Extract: `lintRequiredFields`, `lintSHA256Format`,
`lintFilePath`, `lintOptionalFields`,
`lintBuildSteps`, `lintSourceRepo`.

### 6C. Split `generation.Build` (105 lines)

**File:** `internal/generation/generation.go`

Extract:
```go
func populateGeneration(genDir string, pkgs map[string]string, storeRoot string) error
```

`Build` becomes: get next number → create dir →
populate → swap symlink → write readme.

### 6D. Split `gcCmd.RunE` (115 lines)

Extract `collectReferencedPackages()` and
`removeUnreferencedVersions()`.

---

## Wave 7 — Error handling & polish

Mechanical fixes. Low risk, high readability impact.
Can all be done in a single pass.

### 7A. Replace `os.IsNotExist` with `errors.Is`

**Files:** `cmd/gale/context.go:98,113,133`

Three instances. Find-and-replace.

### 7B. Add error wrapping to naked returns

**Files:** `context.go:46,68,158,162`,
`installer.go:447,461`

Six instances. Add `fmt.Errorf("context: %w", err)`.

### 7C. Log warnings on swallowed errors

- `installer.go:70-80` — log when binary install
  fails and falls back to source
- `gc.go:133-143` — log parse errors in `mergeConfig`
  instead of silently skipping
- `sync.go:184` — log `Remove` error in
  `evictOnSHA256Mismatch`

### 7D. Standardize error message format

Pick gerund style everywhere. Grep for
`fmt.Errorf("` and normalize to `"doing thing: %w"`.

### 7E. Use sentinel errors in CLI output

Check `errors.Is(err, build.ErrUnsupportedPlatform)`
in install/sync to give user-friendly messages instead
of generic "install failed".

### 7F. Split `config.go` into `gale.go` + `app.go`

Move `AppConfig`, `WriteAppConfig`, `AddRepo`,
`RemoveRepo`, `ParseAppConfig` to
`internal/config/app.go`. Keep `GaleConfig` and
related functions in `internal/config/gale.go`.
Rename existing `config.go` to `gale.go`.

### 7G. Add `InstallMethod` type

**File:** `internal/installer/installer.go`

```go
type InstallMethod string
const (
    MethodBinary InstallMethod = "binary"
    MethodSource InstallMethod = "source"
    MethodCached InstallMethod = "cached"
)
```

Update `InstallResult.Method` type and all callers.

---

## Execution Strategy

### Agent assignments

Each wave runs sequentially (Wave 1 before Wave 2,
etc.), but items WITHIN a wave can be parallelized.

| Wave | Agents | TDD Style | Estimated Size |
|------|--------|-----------|----------------|
| 1 (utilities) | 2 parallel | Full pipeline | ~200 lines new |
| 2 (bug fixes) | 3 parallel batches | Inline TDD | ~100 lines changed |
| 3 (build decomp) | 1 sequential | Inline TDD | ~200 lines restructured |
| 4 (migrations) | 2 parallel | Inline TDD | ~150 lines changed |
| 5 (context) | 1 sequential | Inline TDD | ~400 lines restructured |
| 6 (god functions) | 4 parallel | Inline TDD | ~300 lines restructured |
| 7 (polish) | 2 parallel | Inline TDD | ~100 lines changed |

### Parallel batches for Wave 2

- **Batch A** (leaf packages): 2A, 2B, 2G (build,
  config — no cross-deps)
- **Batch B** (context): 2C, 2D, 2E (context.go,
  remove.go, sync.go — test independently)
- **Batch C** (cmd layer): 2F, 2H, 2I (sync.go,
  remove.go, update.go)

### Parallel batches for Wave 6

All four (6A-6D) are independent packages — run 4
agents in parallel.

### Verification gates

After each wave:
1. `go test ./...` — all pass
2. `just lint` — no new warnings
3. `just fmt` — formatted
4. Manual smoke test: `gale install jq`, `gale remove jq`,
   `gale sync`, `gale gc`

---

## Risk mitigation

1. **Wave 3 (buildEnv)** is the riskiest change. The
   build environment is the core of gale. All existing
   build tests must pass unchanged. Do NOT change what
   env vars are set — only how the code is organized.

2. **Wave 5 (context unification)** touches every
   command. Do one command at a time. Run the full
   suite after each. Git commit after each command.

3. **`lockPackage` migration** (4D) is delicate. The
   persistent lock file behavior must be preserved.
   Test concurrent installs after migration.

4. **Generation symlink swap** (4F) affects both Build
   and Rollback. Run the full generation + history
   test suite after extraction.

---

## Items explicitly NOT included

These were flagged in the audits but are lower priority
or higher risk than the return justifies:

- **Cobra flag structs** — would improve testability
  but requires rewriting every command's init(). Do
  after Wave 5 when commands are already cleaner.
- **Output Logger interface** — nice but not blocking
  any refactoring. Do as a separate effort.
- **AI tools HTTP injection** — isolated package, not
  on the critical path. Do separately.
- **`internal/env` merge** — 37 lines, not worth the
  churn.
- **`create_recipe.go` decomposition** — AI-specific,
  changing rapidly, defer.
