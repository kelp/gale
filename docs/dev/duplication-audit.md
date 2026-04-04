# Code Duplication & Reuse Audit

Cross-command analysis of install, update, sync, remove,
and gc. Eight parallel auditors (4 Claude, 4 GPT-5.4)
analyzed every code path independently, then findings
were consolidated and verified against source.

**Files analyzed:** 16 core files, ~3,500 lines  
**Auditors:** 8 (4× Claude Sonnet, 4× GPT-5.4)  
**Agreement:** All 8 agents converged on the same top
findings. GPT agents found additional atomic-write and
locking issues.

---

## Bugs

### B1. `remove` never updates the lockfile

**All 8 agents flagged this.** After removing a package
from `gale.toml` and the store, the lockfile retains
the stale entry.

**File:** `cmd/gale/remove.go:72-105`

The command calls `config.RemovePackage` (line 74),
`st.Remove` (line 84), and `rebuildGeneration` (line 99),
but never touches `gale.lock`. Compare with install
(`finalizeInstall` → `writeConfigAndLock` →
`updateLockfile`) and update (`writeConfigAndLock`
at update.go:170).

**Impact:** After `gale remove foo && gale install
foo@2.0`, sync verifies the new binary's SHA256
against the **old** lockfile entry, triggering a
spurious mismatch eviction (sync.go:148-157).

**Fix:** Add lockfile entry removal after
`config.RemovePackage`:
```go
lp := lockfilePath(configPath)
if lf, err := lockfile.Read(lp); err == nil {
    delete(lf.Packages, name)
    lockfile.Write(lp, lf)
}
```

### B2. `sync` never writes lockfile hashes

**6 of 8 agents flagged this.** When sync installs a
package, it gets an `InstallResult` with `SHA256` but
never writes it to the lockfile. It only *reads* the
lockfile for verification.

**File:** `cmd/gale/sync.go:130-161`

After `ctx.Installer.Install(r)` succeeds (line 135),
sync calls `reportResult` (line 159) and increments
`installed` but never calls `updateLockfile` or
`writeConfigAndLock`.

**Impact:** The `gale add` → `gale sync` workflow
(the documented way to bootstrap) produces a lockfile
with **no SHA256 hashes**. `gale verify` and
`gale audit` are broken for these packages. Future
syncs have nothing to verify against.

**Fix:** After successful install in the sync loop:
```go
updateLockfile(lockfilePath(ctx.GalePath),
    name, version, result.SHA256)
```

### B3. `syncGit` flag is declared but never used

**File:** `cmd/gale/sync.go:18,198`

`syncGit` is declared as a `bool` flag and registered
with cobra, but `runSync` never reads it. The `--git`
flag is silently accepted and ignored.

Compare: `syncBuild` correctly sets
`ctx.Installer.SourceOnly = true` at sync.go:73.

---

## Consistency Matrix

| Operation        | install         | update (batch)  | sync     | remove   | gc       |
|------------------|-----------------|-----------------|----------|----------|----------|
| Config write     | ✅ AddPackage   | ✅ AddPackage   | ❌       | ✅ Remove | ❌       |
| Lockfile write   | ✅ updateLockfile| ✅ updateLockfile| ❌ **B2**| ❌ **B1**| ❌       |
| Gen rebuild      | 1 per pkg       | 1 total (end)   | 1 total  | 1        | 1-2      |
| Error strategy   | Hard fail       | Mixed†          | Soft     | Hard fail| Soft     |
| Uses newCmdContext| ❌             | ✅              | ✅       | ❌       | ❌       |
| Scope flags      | ✅ -g/-p        | ❌ (auto only)  | ✅ -g/-p | ✅ -g/-p | ❌ (auto)|

† Update: Install failure = soft (warn + continue),
config write failure = hard (abort), gen rebuild = hard.

---

## Inconsistencies

### I1. Finalization: `finalizeInstall` vs split calls

Install calls `finalizeInstall` which atomically does
config + lockfile + gen rebuild:

```
cmd/gale/install.go:155  finalizeInstall(...)
cmd/gale/install.go:277  finalizeInstall(...)
cmd/gale/install.go:332  finalizeInstall(...)
cmd/gale/install.go:477  finalizeInstall(...)
```

Update splits these into per-package config/lock writes
plus a single deferred gen rebuild:

```
cmd/gale/update.go:170  writeConfigAndLock(...)  // per pkg
cmd/gale/update.go:183  rebuildGeneration(...)   // once at end
```

**Risk:** If `writeConfigAndLock` fails for package N
(update.go:170), update returns without calling
`rebuildGeneration`. Packages 1..N-1 are in gale.toml
and gale.lock but not in the generation. The user's
PATH reflects old versions.

**Fix:** Either wrap gen rebuild in a `defer` or use
`finalizeInstall` for the update path too.

### I2. `install.go` doesn't use `newCmdContext`

Install manually resolves scope, config, galeDir, and
storeRoot (`install.go:43-63`), duplicating what
`newCmdContext` does (`context.go:37-48`). Update and
sync use `newCmdContext`.

**Behavioral difference:** `install.go` respects
`--global`/`--project` via `resolveScope()`.
`newCmdContext` auto-detects (project first, then
global). Update has **no scope flags at all** — users
who `gale install -g foo` can't `gale update -g foo`.

**Fix:** Make `newCmdContext` accept scope parameters.
All commands use it. Delete the manual bootstrap from
install.go.

### I3. `remove.go` reads config inline, not via `LoadConfig`

**File:** `cmd/gale/remove.go:48-57`

Remove does inline `os.ReadFile` + `ParseGaleConfig`
to look up the package version, **silently swallowing
parse errors**. If `gale.toml` has a syntax error,
remove reports "X is not in gale.toml" instead of the
actual parse error.

Every other command uses `ctx.LoadConfig()` which
returns parse errors properly.

### I4. Remove deletes from store BEFORE gen rebuild

**File:** `cmd/gale/remove.go:82-99`

Order: (1) remove from config, (2) remove from store,
(3) rebuild generation. If gen rebuild fails (disk
full, permissions), the package is gone from both
config and store. The generation has dangling symlinks.

**Correct order:** (1) remove from config, (2) rebuild
generation (no longer references the package), (3)
remove from store (safe — nothing references it).

### I5. Lockfile write has no file locking

**File:** `cmd/gale/context.go:250-258`

`updateLockfile` does a raw read → modify → write
without any lock. `config.AddPackage` protects the
identical pattern with `withFileLock`
(`config.go:146-163`). Concurrent `gale install`
processes can lose lockfile entries.

### I6. Two flock implementations using different syscall packages

**File:** `internal/config/config.go:160` — uses
`golang.org/x/sys/unix.Flock`

**File:** `internal/installer/installer.go:510` — uses
`syscall.Flock` (frozen/deprecated package)

Same concept, different imports, different API shapes
(callback vs closure-return), different lock file
naming strategies.

---

## Duplication

### D1. Identical twin functions (100% duplicate)

**File:** `cmd/gale/install.go:438-454`

```go
func newInstallerForRecipeFile(recipePath, storeRoot string) *installer.Installer {
    return &installer.Installer{
        Store:    store.NewStore(storeRoot),
        Resolver: resolverForRecipe(recipePath),
        Verifier: attestation.NewVerifier(),
    }
}

func newInstallerForLocalSource(recipePath, storeRoot string) *installer.Installer {
    return &installer.Installer{
        Store:    store.NewStore(storeRoot),
        Resolver: resolverForRecipe(recipePath),
        Verifier: attestation.NewVerifier(),
    }
}
```

Same signature, same body, different name. Delete one.

### D2. Atomic write boilerplate (×3)

Three copy-pasted implementations of CreateTemp →
Write → Sync → Close → Rename:

- `config.go:119-138` (`WriteGaleConfig`)
- `config.go:261-280` (`WriteAppConfig`) — also adds
  `MkdirAll` that the others lack
- `lockfile.go:55-79` (`Write`)

~25 lines each, identical error handling. Extract to
`atomicWriteFile(path string, data []byte) error`.
**Saves ~50 lines.**

### D3. Symlink swap logic (×2)

**File:** `generation.go:95-107` and `history.go:162-176`

Both `Build()` and `Rollback()` contain identical
13-line PID-scoped symlink swap:

```go
tmpLink := filepath.Join(galeDir,
    fmt.Sprintf("current-new.%d", os.Getpid()))
os.Remove(tmpLink)
os.Symlink(relTarget, tmpLink)
os.Rename(tmpLink, filepath.Join(galeDir, "current"))
```

Extract to `swapCurrentSymlink(galeDir, target)`.

### D4. Resolver construction (×4)

The `if flag != "" { auto→"" → findLocalRecipesDir →
localRecipeResolver } else { newRegistry }` pattern
appears in four places:

- `install.go:98-110`
- `context.go:59-72` (newCmdContext)
- `install.go:221-234` (installFromGit)
- `cmd/gale/build.go:47-63`

~12 lines × 4. Extract to
`resolveRecipeResolver(flag string) (Resolver, *Registry, error)`.

### D5. Recipe file read+parse (×3)

- `install.go:458-466` — uses `recipe.Parse` (strict)
- `install.go:296-304` — uses `recipe.ParseLocal`
  (lenient)
- `install.go:237-248` — uses `recipe.ParseLocal`

Extract `loadRecipeFile(path, local bool)`. Document
when `local=true` is appropriate.

### D6. Manual scope resolution (×2)

Both `install.go:43-63` and `remove.go:32-48` inline:

```go
cwd := os.Getwd()
useGlobal := resolveScope(global, project, cwd)
configPath := resolveConfigPath(useGlobal)
galeDir := galeDirForConfig(configPath)
storeRoot := defaultStoreRoot()
```

Should flow through `newCmdContext` with scope params.

---

## Extraction Opportunities (Priority Order)

### P0 — Fix bugs first

1. Add lockfile removal to `remove` (B1)
2. Add lockfile write to `sync` (B2)
3. Delete or implement `syncGit` (B3)

### P1 — High-value extractions

4. **`atomicWriteFile(path, data)`** — eliminates D2.
   Three call sites, ~50 lines saved. Also allows
   consistently adding `MkdirAll` (currently only
   `WriteAppConfig` does it).

5. **`swapCurrentSymlink(galeDir, target)`** — eliminates
   D3. Two call sites, ~24 lines saved. Prevents the
   two copies from drifting.

6. **`newCmdContext` with scope params** — eliminates D6,
   fixes I2. Make `newCmdContext(recipesPath, global,
   project)`. All 5 commands use it. Delete manual
   bootstrap from install.go and remove.go.

### P2 — Important consistency fixes

7. **Wrap lockfile writes in file lock** — fixes I5.
   Add `withFileLock` to `updateLockfile` matching
   `config.AddPackage`.

8. **Unify flock package** — fixes I6. Replace
   `syscall.Flock` in installer.go with
   `unix.Flock`. Or extract `internal/filelock`.

9. **Fix remove ordering** — fixes I4. Rebuild gen
   before deleting from store.

10. **Defer gen rebuild in update** — fixes I1. Ensure
    gen is always rebuilt even on partial failure.

### P3 — Code quality

11. **Delete `newInstallerForLocalSource`** — fixes D1.
    Rename survivor to `newInstallerForRecipe`.

12. **Extract `resolveRecipeResolver`** — fixes D4.

13. **Extract `loadRecipeFile`** — fixes D5.

14. **Make `installFromLocalSource` / `installFromGit`
    methods on `*cmdContext`** — they already receive
    all cmdContext fields as separate args.

---

## Estimated Impact

| Category | Items | Lines saved/fixed |
|----------|-------|-------------------|
| Bug fixes | B1, B2, B3 | ~15 new lines |
| Atomic write extraction | D2 | ~50 lines |
| Symlink swap extraction | D3 | ~24 lines |
| Context unification | D6, I2 | ~40 lines |
| Twin function deletion | D1 | ~8 lines |
| Resolver extraction | D4 | ~33 lines |
| Recipe load extraction | D5 | ~18 lines |
| **Total** | | **~190 lines** reduced/fixed |

All agents agreed: the bugs (B1, B2) are the highest
priority. The lockfile is conceptually part of the
contract but three of five commands that modify state
don't update it.
