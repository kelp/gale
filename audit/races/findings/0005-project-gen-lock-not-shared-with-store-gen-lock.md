---
severity: medium
confidence: confirmed
class: C
commands: [sync, install, update, switch]
shared-state: ~/.gale/pkg/ (store dirs) and ~/.gale/lib/ (farm)
---
## Summary
The lock file used by `generation.Build` at *project* scope
(`<projGaleDir>/generation.lock`) is a different file from
the lock used by `installer.commitStaged` /
`installer.extractBuild` (`<filepath.Dir(storeRoot)>/generation.lock`,
i.e. the global galeDir's lock). A global `gale install`
and a project `gale sync` therefore do not serialize, and
the project sync can walk a store dir while the installer
is mid-finalize. This is acknowledged in
`internal/installer/installer.go:1051` as a known
residual race; the audit confirms it concretely.

## Scenario
Reference paths:
- `internal/generation/generation.go:269 generationLockPath`
  returns `<galeDir>/generation.lock`. Callers pass
  `galeDir` from `rebuildGeneration(galeDir, ...)`.
- `internal/installer/installer.go:1060 storeGenLockPath`
  returns `<filepath.Dir(storeRoot)>/generation.lock`.

At global scope these resolve to the same path
(`~/.gale/generation.lock`), so the installer and global
`generation.Build` serialize correctly.

At project scope:
- `cmd/gale/sync.go` calls `rebuildGenerationLenient`
  with the project `.gale/` as galeDir → project's gen
  Build acquires `<proj>/.gale/generation.lock`.
- A concurrent `gale install` (global scope) holds
  `~/.gale/generation.lock` plus the per-package lock.

These two locks are different files. Both processes can
hold their respective locks simultaneously. The project
sync's `populateGeneration` walks `~/.gale/pkg/jq/...`,
which the installer is concurrently mutating
(extract tar, FixupPkgConfig, RestorePrefixPlaceholders,
RelocateStaleRpaths, EnsureCodeSigned, farm.Populate).

Concrete failure modes when this race fires:
- Project sync's `os.ReadDir(pkgDir)` returns half the
  package contents (extract incomplete). Symlinks
  populated for missing entries; later requests fail
  with ENOENT.
- Project sync's `validateGenerationSymlinks` walks the
  newly-created gen dir while the install is still
  modifying its target; a symlink resolved during the
  walk may transiently fail Stat. Validate then errors
  the entire Build with `"generation has dangling
  symlink ...; store mutated during rebuild"`.
- Far worse on darwin: the installer's
  `RelocateStaleRpaths` rewrites Mach-O headers in place.
  A reader (project Build) Stat'ing the binary observes
  a partially-rewritten header; codesign verification or
  exec fails until the install completes.

direnv firing `gale sync` on every shell `cd` makes this
window practically reachable any time the user runs
`gale install` from one shell while another shell
re-enters the project. The CLAUDE.md docs explicitly
flag direnv-driven syncs as first-class.

## Observed
`cmd/gale/race_repro_test.go:TestAudit_ProjectGenLockNotSharedWithStoreGenLock`
acquires both locks from independent goroutines without
either contending:

```
=== RUN   TestAudit_ProjectGenLockNotSharedWithStoreGenLock
    race_repro_test.go:367: CONFIRMED: project gen lock acquired
        in 23.746µs while another holder still owns the store-gen
        lock — the locks are different files, so install and
        project sync do not serialize. Documented in
        installer.go:1051.
--- FAIL: TestAudit_ProjectGenLockNotSharedWithStoreGenLock (0.00s)
```

Acquiring both locks in 23µs (no blocking) proves they
target different files. The downstream Mach-O / extract
race is by inspection; reproducing it would require a
real install pipeline and is plausible by Class-B reading
of `installer.installBinaryTo`'s in-place finalize steps.

## Suggested investigation
Make `generation.Build` (and `Rollback`) also acquire the
store-rooted lock returned by `storeGenLockPath`. Either
take both locks in a defined order (avoiding deadlock —
always store-root first, then galeDir-local) or
consolidate into one lock keyed on the store root. The
comment at `installer.go:1051` already names the
direction; this audit just provides a concrete repro.

Verify also that the comment's mention of "the residual
install-vs-project-sync race is not closed" remains
accurate after the fix — and remove it if so.
