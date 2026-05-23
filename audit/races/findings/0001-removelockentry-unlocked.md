---
severity: high
confidence: confirmed
class: C
commands: [remove, install, add, update, sync, switch]
shared-state: gale.lock
---
## Summary
`cmd/gale/context.go:removeLockEntry` performs an unlocked
read-modify-write on `gale.lock`, so any concurrent
`updateLockfile` (used by every install/add/update/sync/switch
path) can be silently overwritten when the remove rename
runs last.

## Scenario
Two processes operating on the same project (or two threads
inside one process that called both helpers concurrently).

Reference paths in the repo:
- locked path:  `cmd/gale/context.go:332` `updateLockfile`
  (wraps `lockfile.Read` → `lockfile.Write` inside
  `filelock.With(lockPath+".lock", ...)`).
- unlocked path: `cmd/gale/context.go:347` `removeLockEntry`
  (calls `lockfile.Read` + `lockfile.Write` directly,
  no `filelock.With`).

Interleaving that loses an install update:
1. P_install (`gale install ripgrep@14.0.1`) acquires
   `<gale.lock>.lock`, reads `{jq:1.7.1, ripgrep:14.0.0}`.
2. P_remove (`gale remove jq`) reads
   `{jq:1.7.1, ripgrep:14.0.0}` (no lock).
3. P_install mutates its snapshot to
   `{jq:1.7.1, ripgrep:14.0.1}`, atomicfile.Write renames
   into place, releases the lock.
4. P_remove mutates its (stale) snapshot to
   `{ripgrep:14.0.0}` and atomicfile.Write renames into
   place — silently clobbering the new ripgrep version.

End state: `{ripgrep:14.0.0}`. The install of ripgrep@14.0.1
is lost in `gale.lock`, even though it succeeded in the
store and in `gale.toml`. The next `gale sync` will see
the stale lock entry, the project will rebuild the
generation against the wrong pin, and reproducible builds
silently regress.

The symmetric interleaving (steps 2 and 3 swapped) leaves
`jq` in the lockfile despite the user's explicit `gale
remove jq` — the lockfile claims jq is still pinned at
1.7.1.

## Observed
Deterministic Go reproducer at
`cmd/gale/race_repro_test.go:TestAudit_RemoveLockEntryRace`
seeds `{jq:1.7.1, ripgrep:14.0.0}`, runs the two helpers
in parallel goroutines, and reads the final file.

```
$ go test ./cmd/gale/ -run TestAudit_RemoveLockEntryRace -count=1 -v
=== RUN   TestAudit_RemoveLockEntryRace
    race_repro_test.go:103: trials=500
    race_repro_test.go:104: lost-install-updates=59
    race_repro_test.go:105: orphan-removes-still-present=229
    race_repro_test.go:114: CONFIRMED: 59/500 trials lost an install
        update due to unlocked removeLockEntry
--- FAIL: TestAudit_RemoveLockEntryRace (0.10s)
```

Failure rates this run: 11.8% lost install update,
45.8% orphan remove. Cross-process invocation
(`gale install` + `gale remove` as separate processes)
would exhibit the same race because both helpers go
through the same lockfile path and the lock file is
`flock(2)`-based, not in-process.

## Suggested investigation
Audit every caller of `lockfile.Write` for missing lock
acquisition. Look at `cmd/gale/context.go:347
removeLockEntry`. Also inspect callers of
`lockfile.Read` that proceed to write — the comment on
`updateLockfile` says "The file lock serializes
concurrent read-modify-write operations", and only that
one function honors it.
