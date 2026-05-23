---
severity: critical
confidence: confirmed
class: C
commands: [gc, install, sync, update, switch, add, remove]
shared-state: ~/.gale/gen/<N>/
---
## Summary
`cmd/gale/gc.go:cleanOldGenerations` reads its directory
listing and the `current`-symlink target without acquiring
the generation lock. A concurrent `generation.Build` that
has created `gen/N+1/` but not yet swapped `current` is
visible to gc as "any non-current gen" — gc happily
`os.RemoveAll`s the in-flight generation, corrupting the
Build and leaving the user without that PATH.

## Scenario
Reference paths:
- `cmd/gale/gc.go:244 cleanOldGenerations` — reads
  `entries` then `curGen` and `RemoveAll`s every
  `n != curGen` entry, no lock acquisition.
- `internal/generation/generation.go:190 build()` — holds
  `<galeDir>/generation.lock` from before `Current()` until
  AFTER `swapCurrentSymlink`. So Build's gen N+1 exists on
  disk while `current` still points at N for the duration
  of populate+validate.

Class-C interleaving:
1. Initial: `current → gen/N`. Build acquires gen lock,
   reads `prev = N`, sets `next = N+1`, creates
   `gen/N+1/bin/` (and possibly some populated dirs).
2. User (or scheduled cron) runs `gale gc` in another
   shell:
   - reads `entries = {gen/1, ..., gen/N+1}` (snapshot
     includes the in-flight dir),
   - reads `curGen = N` (Build hasn't swapped yet),
   - iterates: every entry where `n != N` → RemoveAll.
3. `gen/N+1` is RemoveAll'd mid-populate.
4. Build's `populateGeneration` proceeds: next `os.Symlink`
   call returns ENOENT because the dst parent dir was just
   deleted. Build returns an error; the deferred
   `cleanup()` calls `os.RemoveAll(genDir)` which is now
   a no-op. The user's `gale install` / `gale sync`
   exits with `symlink ...: no such file or directory`.

Worse: if gc's RemoveAll wins the race after Build
finishes populate but before swapCurrentSymlink, gen/N+1
is deleted, then Build's `swapCurrentSymlink` makes
`current → gen/N+1` — pointing at a now-non-existent
directory. Every binary lookup through PATH fails until
the next successful sync.

The window is small (milliseconds) but real on a system
where direnv fires `gale sync` on `cd`. The blast radius
is "user shell can't find any tool" until the next sync.

## Observed
`cmd/gale/race_repro_test.go:TestAudit_GcVsBuildRace`
constructs the exact interleaving by holding the gen lock
from a separate goroutine (simulating Build mid-
populate), pre-creating `gen/2/`, and running the
verbatim body of `cleanOldGenerations`. The in-flight
gen/2 is deleted 100% of trials:

```
=== RUN   TestAudit_GcVsBuildRace
    race_repro_test.go:221: CONFIRMED: cleanOldGenerations
        deleted in-flight gen/2 while Build held the
        generation lock (curGen=1 because symlink not yet
        swapped). err=stat .../gen/2: no such file or
        directory
--- FAIL: TestAudit_GcVsBuildRace (0.00s)
```

The reproducer mirrors gc's body verbatim (cleanOldGenerations
is unexported); any future change to the gc function must
also update this test.

## Suggested investigation
Wrap `cleanOldGenerations` in
`filelock.With(generationLockPath(galeDir), ...)`. Read
`curGen` BEFORE `entries`, and consider only entries with
`n < curGen` for deletion (so an in-flight gen N+1 is
never even considered). Likely also want to wrap
`gc.removeUnreferencedVersions` in `withStoreGenLock` so
the store mutations don't race against a Build's
populate walking the store.
