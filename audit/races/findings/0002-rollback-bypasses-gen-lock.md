---
severity: high
confidence: confirmed
class: B
commands: [generations rollback, install, sync, update, remove, add, switch]
shared-state: ~/.gale/current
---
## Summary
`generation.Rollback` does not acquire the generation lock
that `generation.Build` holds while populating and swapping
the `current` symlink. Any concurrent Build (or any other
holder of `generation.lock`) silently overrides the
rollback because Rollback's `swapCurrentSymlink` is followed
by Build's lock-protected `swapCurrentSymlink` — same
target file, last writer wins, no serialization.

## Scenario
Reference paths:
- `internal/generation/generation.go:190 build()` —
  wraps populate+validate+swap in
  `filelock.With(generationLockPath(galeDir), ...)`.
- `internal/generation/history.go:153 Rollback` — calls
  `swapCurrentSymlink` directly. No `filelock.With`.

Class-B interleaving that loses a rollback:
1. P_sync (called by direnv on `cd`) starts
   `generation.Build`. Acquires
   `<galeDir>/generation.lock`, reads `current`=N,
   creates and populates `gen/N+1`.
2. P_rollback runs `gale generations rollback 5` in a
   second shell. Skips the lock entirely, calls
   `swapCurrentSymlink(galeDir, 5)` → `current` →
   `gen/5`.
3. P_sync finishes populate, calls
   `swapCurrentSymlink(galeDir, N+1)` →
   `current` → `gen/N+1`. Releases lock.

End state: `current` → `gen/N+1`. The user's rollback
silently never happened. There is no warning; the
`gale rollback` CLI returns success.

Additionally, even the Build–Rollback–Build sequence (no
overlap) loses the rollback intent: after Rollback,
`current` = 5; the next Build picks `next = current + 1
= 6`, and gen 6 will already exist (it was the gen
*before* this rollback). Build happily writes new
symlinks into that gen dir and swaps to it. The user
gets gen 6's package set, not the rolled-back state.

## Observed
Two tests in
`internal/generation/race_repro_test.go`:

`TestAudit_RollbackBypassesGenLock` — direct proof that
Rollback ignores the gen lock:

```
=== RUN   TestAudit_RollbackBypassesGenLock
    race_repro_test.go:162: CONFIRMED: Rollback completed in
        18.288µs despite another holder owning
        generation.lock — Rollback bypasses the lock
--- FAIL: TestAudit_RollbackBypassesGenLock (0.00s)
```

`TestAudit_RollbackVsBuildRace_Deterministic` — proves
that Rollback's swap is overwritten by a Build holding
the lock:

```
=== RUN   TestAudit_RollbackVsBuildRace_Deterministic
    race_repro_test.go:107: CONFIRMED: Rollback to gen 1
        was lost; final=2 (Build's swap completed AFTER
        Rollback because Rollback did not hold the gen
        lock)
--- FAIL: TestAudit_RollbackVsBuildRace_Deterministic (0.01s)
```

Both are 100% deterministic on every run.

## Suggested investigation
Wrap `Rollback` body in `filelock.With(generationLockPath(galeDir), ...)`.
Independently, decide whether `Build`'s "next gen number"
should be `max(existing_gens) + 1` instead of
`current + 1` — the current scheme makes rollback
single-use because the next Build immediately walks
forward past the rollback target. That's a design issue
beyond the race, but worth noting alongside the fix.
