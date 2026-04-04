# Code Quality Audit

Cross-cutting analysis of function design, error handling,
struct/interface design, and package organization. Eight
parallel auditors (4 Claude, 4 GPT-5.4) analyzed the
full codebase independently, then findings were
consolidated and verified against source.

**Files analyzed:** 35 source files, ~6,400 lines  
**Auditors:** 8 (4× Claude Sonnet, 4× GPT-5.4)

---

## Critical Bugs

### Q1. `buildEnv` returns nil env on temp dir failure

**File:** `internal/build/build.go:328-329`

When `MkdirTemp` fails, `buildEnv` returns `nil` for
the env slice. The caller `runStep` sets `cmd.Env = env`.
A nil `cmd.Env` means **inherit the full parent
environment** — the exact opposite of the intended
sandboxed build isolation.

**Impact:** Builds could succeed with wrong toolchains,
producing corrupt binaries that get installed to store.

**Fix:** Return an error from `buildEnv`, propagate
through `runStep`.

### Q2. `build.go:286` — `%s` instead of `%w` destroys error chain

```go
return fmt.Errorf("build step %q failed: %s", step, err)
```

Uses `%s` which discards the error chain. Any
`errors.Is` or `errors.As` on the returned error fails.
This is the only build step error path — every build
failure loses its underlying cause.

**Fix:** Change `%s` to `%w`.

### Q3. `lockfilePath` panics instead of returning error

**File:** `cmd/gale/context.go:207`

```go
panic("lockfilePath: configPath must end with .toml, got " + configPath)
```

Called from every install/update/sync path. If the
`.tool-versions` fallback in `LoadConfig` produces a
non-`.toml` `GalePath`, the CLI crashes with a stack
trace.

**Fix:** Return `(string, error)`.

---

## God Functions (ranked by severity)

Score = line_count × responsibility_count. All 8 agents
converged on the same top-5 worst offenders.

| Rank | Score | Function | File:Line | Lines | Responsibilities |
|------|-------|----------|-----------|-------|------------------|
| 1 | 2,490 | `buildEnv` | build.go:325 | 166 | 15 |
| 2 | 2,052 | `updateCmd.RunE` | update.go:25 | 171 | 12 |
| 3 | 2,035 | `doctorCmd.RunE` | doctor.go:21 | 185 | 11 |
| 4 | 1,716 | `Lint` | lint.go:51 | 132 | 13 |
| 5 | 1,529 | `runSync` | sync.go:41 | 139 | 11 |
| 6 | 1,330 | `installCmd.RunE` | install.go:33 | 133 | 10 |
| 7 | 1,062 | `runCreateRecipe` | create_recipe.go:145 | 118 | 9 |
| 8 | 1,035 | `gcCmd.RunE` | gc.go:20 | 115 | 9 |
| 9 | 840 | `generation.Build` | generation.go:19 | 105 | 8 |
| 10 | 801 | `sbomCmd.RunE` | sbom.go:37 | 89 | 9 |
| 11 | 763 | `AddDepRpaths` | fixup_darwin.go:148 | 109 | 7 |
| 12 | 760 | `buildCmd.RunE` | build.go:28 | 95 | 8 |
| 13 | 688 | `removeCmd.RunE` | remove.go:22 | 86 | 8 |

### buildEnv — 166 lines, 15 responsibilities

Constructs PATH, dep library/include/pkgconfig paths,
per-dep `DEP_*` vars, compiler flags, platform-specific
rpath logic, cmake semicolons, Python site-packages
discovery, debug/release defaults, and `ZERO_AR_DATE`.

**Decompose into:**
- `baseEnvVars(prefixDir, jobs, version)` — core vars
- `depSearchPaths(deps)` — LIBRARY_PATH, C_INCLUDE_PATH
- `depCompilerFlags(deps, debug)` — CFLAGS/LDFLAGS
- `perDepEnvVars(deps)` — DEP_NAME vars
- `buildEnv` becomes a thin compositor

### doctorCmd.RunE — 185 lines, 11 checks

**Decompose into** a check registry:
```go
type Check struct {
    Name string
    Run  func(ctx *DoctorContext) (bool, string, error)
}
```
Each check becomes independently testable. The RunE
becomes a loop.

### Lint — 132 lines, 13 validators

**Decompose into** a pipeline of validators:
```go
type lintRule func(*recipe, string) []Issue
```
- `lintRequiredFields`, `lintSHA256Format`,
  `lintBuildSteps`, `lintOptionalFields`, etc.

---

## Too Many Parameters

Functions with >4 parameters (all agents flagged these):

| Function | Params | Consecutive Strings | File |
|----------|--------|---------------------|------|
| `runCreateRecipe` | **8** | 4 | create_recipe.go:145 |
| `runStep` | **8** | 6 | build.go:275 |
| `installFromGit` | **7** | 6 | install.go:214 |
| `installFromLocalSource` | **7** | 6 | install.go:288 |
| `buildEnv` | **6** | 4 | build.go:325 |
| `buildFromDir` | **6** | 4 | build.go:150 |
| `finalizeInstall` | **6** | 6 | context.go:240 |
| `installFromRecipeFile` | **5** | 4 | install.go:456 |
| `installBinary` | **5** | 3 | installer.go:220 |
| `runSync` | **5** | 1 | sync.go:41 |
| `BuildLocal` | **5** | 2 | build.go:134 |

### Worst offender: 6 consecutive strings

```go
func installFromGit(name, recipePath, configPath,
    galeDir, storeRoot, recipesFlag string,
    out *output.Output) error
```

Swapping `configPath` and `galeDir` compiles without
error and produces subtle bugs.

### Fix: introduce context structs

```go
type installContext struct {
    Name       string
    ConfigPath string
    GaleDir    string
    StoreRoot  string
    Out        *output.Output
}
```

```go
type buildContext struct {
    Recipe    *recipe.Recipe
    SourceDir string
    Workspace string
    OutputDir string
    Debug     bool
    Deps      *BuildDeps
}
```

---

## Error Handling Issues

### Swallowed errors (security-adjacent)

| File:Line | Code | Risk |
|-----------|------|------|
| `installer.go:70-80` | Binary install failure silently falls to source | Attestation failure invisible |
| `sync.go:184` | `_ = s.Remove(...)` after SHA256 mismatch | Corrupt package survives eviction |
| `gc.go:133-143` | `mergeConfig` swallows parse errors | Corrupt gale.toml → gc deletes everything |
| `build.go:106` | `_ = copyFile(tarball, cache)` | Slow builds, no signal |
| `build.go:328` | `buildEnv` returns nil on MkdirTemp failure | **Critical** — see Q1 |

### Naked returns (no error wrapping)

| File:Line | Code |
|-----------|------|
| `context.go:46` | `return nil, dirErr` |
| `context.go:68` | `return nil, dirErr` |
| `context.go:158,162` | `return nil, err` (loadAppConfig) |
| `installer.go:447,461` | `return "", err` |

### `os.IsNotExist` vs `errors.Is` inconsistency

**File:** `cmd/gale/context.go:98,113,133`

Uses deprecated `os.IsNotExist()` while the rest of
the codebase uses `errors.Is(err, os.ErrNotExist)`.
`os.IsNotExist` doesn't unwrap error chains.

### Error message style inconsistency

Same file (`context.go`) uses both:
- Gerund: `"getting working dir: %w"` (line 34)
- Bare verb: `"read config: %w"` (line 130)

Pick one convention (gerund is Go-idiomatic).

### Sentinel errors defined but never checked

| Sentinel | Package | Production `errors.Is`? |
|----------|---------|------------------------|
| `ErrUnsupportedPlatform` | build | ❌ |
| `ErrGaleConfigNotFound` | config | ❌ |
| `ErrPackageNotFound` | config | ❌ |
| `ErrNotInstalled` | store | ❌ |

These exist as documentation but no command gives
user-friendly messages for these specific error types.

---

## Struct & Interface Design

### DepPaths / BuildDeps — identical structs

**File:** `installer.go:225` and `build.go:299`

```go
// installer.go
type DepPaths struct {
    BinDirs   []string
    StoreDirs []string
    NamedDirs map[string]string
}
// build.go
type BuildDeps struct {
    BinDirs   []string
    StoreDirs []string
    NamedDirs map[string]string
}
```

Plus `depsToBuildDeps()` (installer.go:453) exists
solely to copy between them.

**Fix:** Use a single type. Delete the converter.

### InstallResult.Method — magic strings

**File:** `installer.go:36`

```go
Method string // "binary", "source", or "cached"
```

Callers do string comparison. A typo silently passes.

**Fix:** `type InstallMethod string` with constants.

### Missing interfaces for testability

| Package | Problem | Suggestion |
|---------|---------|------------|
| `output` | No interface, concrete struct everywhere | Define `Logger` interface |
| `ai/tools` | Direct HTTP calls, no injection | Define `HTTPClient` interface |
| `installer` | `RecipeResolver` is func type | Consider interface for consistency |
| `build` | Package-level mutable `var out` | Accept output as parameter |

### config.go mixes two concerns

Single file handles both `GaleConfig` (gale.toml) AND
`AppConfig` (config.toml). Different types, different
purposes, same package.

**Fix:** Split into `config/gale.go` and `config/app.go`.

### Repo operations missing file locking

**File:** `internal/config/config.go:289-320`

`AddRepo` and `RemoveRepo` write to config.toml without
file locking, but `AddPackage`/`RemovePackage` use
`withFileLock`. Concurrent repo operations corrupt.

### Package-level mutable state (anti-patterns)

| File | Pattern | Risk |
|------|---------|------|
| `build.go:22` | `var out` + `init()` mutates `download` | Import side effects |
| `ghcr.go:26` | `SetTokenEndpoint` | Test-only global mutation |
| `attestation.go:10` | `var lookPath` | Test-only global mutation |
| All cmd files | Package-level flag vars | Untestable commands |

### `cmdContext` is an ad-hoc service locator

**File:** `cmd/gale/context.go`

Bundles 6 fields plus 10 free functions that take
4-6 string parameters already on the struct.

**Fix:** Make the free functions methods:
```go
func (ctx *cmdContext) FinalizeInstall(name, version, sha256 string) error
func (ctx *cmdContext) WriteConfigAndLock(name, version, sha256 string) error
```

---

## Abstraction Level Mixing

Functions that mix high-level orchestration with
low-level I/O in the same function body:

| Function | High-level | Low-level |
|----------|-----------|-----------|
| `installCmd.RunE` | Scope resolution, installer dispatch | `os.Getwd()`, `config.FindGaleConfig()` |
| `removeCmd.RunE` | Scope + store + gen orchestration | `os.ReadFile(configPath)`, inline parsing |
| `gcCmd.RunE` | Package collection, cleanup | Direct `os.ReadDir`, `os.RemoveAll` |
| `doctorCmd.RunE` | 10 system health checks | Raw `os.Readlink`, PATH parsing |
| `buildEnv` | Env var policy decisions | `os.Getenv()`, `filepath.Glob()` for Python |

---

## Cobra Anti-Patterns

### Package-level flag variables everywhere

Every command file declares mutable package-scope vars:
```go
var (
    installGlobal  bool
    installProject bool
    installRecipes string
    ...
)
```

These are captured by `RunE` closures. Changes leak
between tests. Makes unit testing impossible.

**Fix:** Use flag structs:
```go
type installOpts struct {
    global, project    bool
    recipes, recipe    string
    path               string
    git, build         bool
}
```

### Inconsistent `noColor` checking

Two patterns coexist:
- `!noColor` (sync.go:42, gc.go:153)
- `!cmd.Flags().Changed("no-color")` (update.go:26)

Both work today but differ when `NO_COLOR` env is used.

---

## Package Organization Issues

### Too thin: `internal/env/env.go` — 37 lines

Only generates a static string for direnv. Could merge
into `generation` or `config`.

### Two flock implementations

- `config.go:160` — uses `unix.Flock`
- `installer.go:510` — uses `syscall.Flock` (deprecated)

Different imports, different API shapes, different
naming strategies.

**Fix:** Extract `internal/filelock` with single impl.

### `internal/ai/tools.go` — tightly coupled

Imports `internal/lint` and `internal/download`. Makes
direct HTTP calls. Can't test without network.

**Fix:** Inject dependencies via interfaces:
```go
type ToolDeps struct {
    HTTP         HTTPClient
    RecipeLinter RecipeLinter
    FileHasher   FileHasher
}
```

---

## Priority Roadmap

### P0 — Fix bugs

1. Make `buildEnv` return error on MkdirTemp failure (Q1)
2. Change `%s` to `%w` in build step error (Q2)
3. Make `lockfilePath` return error, not panic (Q3)
4. Add locking to `AddRepo`/`RemoveRepo`

### P1 — Decompose top god functions

5. Split `buildEnv` (166 lines → 5 helpers)
6. Split `doctorCmd.RunE` (185 lines → check registry)
7. Split `Lint` (132 lines → rule pipeline)
8. Extract per-package loop bodies in update/sync

### P2 — Reduce parameter counts

9. Introduce `installContext` struct for 7-param funcs
10. Introduce `buildContext` struct for build funcs
11. Make `cmdContext` free functions into methods
12. Unify `DepPaths`/`BuildDeps` into single type

### P3 — Error handling consistency

13. Replace `os.IsNotExist` with `errors.Is` (×3)
14. Add context wrapping to naked returns (×6)
15. Log warnings on swallowed errors (installer
    binary fallback, gc mergeConfig)
16. Standardize error message format (gerund)

### P4 — Structural improvements

17. Extract `atomicWriteFile` utility
18. Extract `swapCurrentSymlink` utility
19. Unify flock implementations
20. Split config.go into gale.go + app.go
21. Add `Logger` interface for `output.Output`

---

## Estimated Impact

| Category | Items | Lines affected |
|----------|-------|----------------|
| Bug fixes | Q1-Q3, repo locking | ~30 lines changed |
| God function decomposition | P1 | ~600 lines restructured |
| Parameter reduction | P2 | ~200 lines cleaner |
| Error handling | P3 | ~50 lines improved |
| Structural extractions | P4 | ~150 lines deduplicated |
| **Total** | | ~1,000 lines improved |

All 8 agents agreed: the `buildEnv` god function and
the `lockfilePath` panic are the highest-priority fixes.
The parameter-heavy install functions
(`installFromGit`, `installFromLocalSource`) are the
most likely source of future bugs due to swappable
string arguments.
