---
severity: high
confidence: confirmed
class: C
commands: [gc, remove, install, sync, update, switch]
shared-state: ~/.gale/pkg/
---
## Summary
`store.Remove` (and therefore `gc.removeUnreferencedVersions`
and `cmd/gale remove`) does not acquire the per-package
install lock (`<storeRoot>/<name>/<version>.lock`). It
proceeds to `os.RemoveAll(storeDir)` regardless of any
concurrent install holding that lock — and regardless of
the wider window between `Installer.Install` returning
(per-package lock released) and `ctx.FinalizeRecipeInstall`
writing gale.toml. gc reading the unfinished install's
empty config sees the just-installed package as
unreferenced and reaps it.

## Scenario
Reference paths:
- `internal/store/store.go:216 Remove` — `Stat` then
  `RemoveAll`, no `filelock`.
- `internal/installer/installer.go:1030 lockPackage` —
  the per-package lock that Install/Reinstall acquire,
  not consulted by Remove.
- `cmd/gale/install.go:115` calls
  `ctx.Installer.Install(r)` (lock released on return)
  then `ctx.FinalizeRecipeInstall(...)` separately.

Class-C interleaving:
1. P_install runs `gale install jq`. `Installer.Install`
   acquires the per-package lock, writes the store dir,
   releases the lock, and returns.
2. P_gc runs `gale gc` (cron, or hand-invoked from another
   shell). gc reads gale.toml, jq absent, so jq is
   "unreferenced". gc calls `store.Remove("jq",
   "1.8.1-3")` — no lock — `RemoveAll(...)`. Store dir
   gone.
3. P_install proceeds to `FinalizeRecipeInstall`: writes
   gale.toml ok, writes lockfile ok, calls
   `rebuildGeneration`. `generation.Build` →
   `populateGeneration` → `os.ReadDir(pkgDir)` returns
   ENOENT for jq's now-missing dir → strict Build returns
   `"jq@1.8.1 is missing from the store"`.
4. User sees a failed install where the store says
   "missing" for the package they just installed.

If the install is invoked by a project sync (direnv +
`use gale`), the user gets a hard sync failure on shell
`cd`, which is even worse than a manual install failure
because there's no obvious recovery path other than
re-running `gale install`.

Even with the per-package lock taken, the window between
its release and FinalizeRecipeInstall is open. Any
gc-style RemoveAll during that window has the same
effect. The correct fix is either to hold the per-package
lock through FinalizeRecipeInstall, or to have gc verify
the store dir is "settled" (e.g. has a sentinel file or
matches the lockfile snapshot) before reaping.

The same race is present for `gale remove`: if the user
runs `gale install jq` and `gale remove jq` from two
shells, the install can complete in the store, the remove
can `store.Remove` the dir, then the install's gen
rebuild fails. Sequence order matters here — but the lack
of a shared lock means the bug is plausible whenever both
commands run close in time.

## Observed
`cmd/gale/race_repro_test.go:TestAudit_GcVsInstall_WindowBetweenStoreWriteAndConfigWrite`
sets up a fresh store with jq pre-installed and an empty
gale.toml (mimicking the mid-install window), then calls
`store.Remove("jq", "1.8.1")` from outside any lock —
even while a goroutine still holds the per-package lock
file. Remove succeeds without blocking:

```
=== RUN   TestAudit_GcVsInstall_WindowBetweenStoreWriteAndConfigWrite
    race_repro_test.go:304: CONFIRMED: store dir for in-flight install
        was removed by gc-equivalent unlocked RemoveAll.
        A subsequent FinalizeRecipeInstall would fail in
        populateGeneration with ENOENT on the missing dir.
--- FAIL: TestAudit_GcVsInstall_WindowBetweenStoreWriteAndConfigWrite (0.00s)
```

100% deterministic.

## Suggested investigation
Two layers:
1. `store.Remove` should acquire the per-package lock
   (`lockPackage` in `internal/installer/installer.go:1030`)
   for the duration of the RemoveAll. That requires
   moving lockPackage to a shared location or exporting
   the helper.
2. The wider install window (store-write → gale.toml
   write) wants `Installer.Install` to hold the
   per-package lock through `FinalizeRecipeInstall`, OR
   gc to consult a sentinel (e.g. `<storeDir>/.installed`)
   before treating a store entry as reapable. The
   per-package lock is the cleanest fix and matches the
   existing locking model.
