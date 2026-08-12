# Transactional Install Finalization

Proposal for [#187](https://github.com/kelp/gale/issues/187).
Design only — no production code changes in this PR.
Line references are against `origin/main` at `6d81908`.

## 1. Problem

`FinalizeInstall` (`cmd/gale/context.go:1090-1097`) is the
one place an install becomes visible. It commits three
independent steps:

```go
ctx.WriteConfig(name, configVersion, lockVersion)  // gale.toml
ctx.WriteLock()                                    // gale.lock
rebuildGeneration(ctx.GaleDir, ...)                // current → gen/N
```

Each step is individually safe and individually locked:

- `WriteConfig` writes gale.toml atomically under
  `gale.toml.lock`.
- `WriteLock` writes gale.lock atomically under
  `gale.lock.lock` (`context.go:837`), reading the manifest
  *inside* that critical section (`context.go:850`).
- `rebuildGeneration` swaps `current` with one `os.Rename`
  under `<galeDir>/generation.lock`.

Three files, three locks, no lock spanning them. The
enclosing lock a caller does hold is the per-package store
lock from `InstallWithFinalize`
(`internal/installer/installer.go:1994`), keyed
`name/version-revision` by `lockPackage`
(`installer.go:1955-1958`). Two installs of *different
versions of one package* take different lock files and do
not exclude each other, and two installs of *different
packages* never did.

The issue conflates two failures under one heading. They
have different triggers, different blast radii, and
different costs to fix.

### (a) Divergence — config and lock disagree, permanently

The issue's stated mechanism no longer exists on main. It
describes a slow sync writing lock A over a manual install's
lock B. Sync is no longer a lock writer: `runSyncOne` ends at
`cmd/gale/sync.go:630-634` with "Sync never writes gale.lock
(design §11). It is a pure consumer of one." The lock-writing
call sites are now `cmd/gale/lock.go:171`,
`cmd/gale/update.go:275`, `cmd/gale/remove.go:181`, and
`context.go:1094` — no sync path among them.

What survives is narrower and still reachable:

1. `WriteConfig` commits the new pin to gale.toml.
2. `WriteLock` refuses. The refusal is *by design*:
   `lockwrite.checkRootsDeclared`
   (`internal/lockwrite/lockwrite.go:321-338`) rejects a
   write whose verified roots disagree with the manifest
   read under the lockfile lock, so a concurrent command
   that repinned the same package between steps 1 and 2
   makes this writer fail rather than commit a lock the
   manifest does not back. It can also fail for ordinary
   reasons: a provenance closure that cannot be resolved
   (`lockwrite.go:358`), a one-version-per-name conflict
   (`lockwrite.go:406`), an unreadable prior document
   (`context.go:860-863`).
3. `FinalizeInstall` returns the error. **Nothing is undone.**

The result: gale.toml declares the new version, gale.lock
does not name it, the store holds it, and `current` was never
rebuilt. Config, lock, and generation disagree three ways.

This is a gap in an otherwise-complete pattern, not a missing
idea. `gale remove` already compensates exactly this class of
failure: it captures a config witness
(`config.RemovePackageSections` → `priorConfig, wroteConfig`,
`remove.go:135`) and a lock witness (`writeLockWitnessed`,
`remove.go:181`), and every later refusal unwinds with
`config.RestoreUnderLock` + `restoreLock`
(`remove.go:159-162, 183-186, 216-222`). `restoreLock`
(`remove.go:513`) is a compare-and-swap on the `LockWitness`
tokens, so it stands down rather than clobbering a concurrent
writer. The install path calls none of it.

### (b) Non-atomicity — interruption leaves a partial commit

Independently of any race, the three steps have two crash
windows:

| Killed after | gale.toml | gale.lock | `current` |
|---|---|---|---|
| step 1 | new pin | old | old |
| step 2 | new pin | new pin | old |
| step 3 | new pin | new pin | new |

Only the third row is consistent. Row 1 is the same end state
as (a). Row 2 leaves both files agreeing on a version that is
not on PATH; the next `gale sync` repairs it, because
`syncDrifted`/`lockedGenerationDrifted` (`sync.go:288-335`)
compares the active generation against the plan and rebuilds.
Row 1 is the one that persists.

The window is small — three file writes plus a symlink swap,
after the download and build have already finished — but it
is not zero, and `^C` during a multi-package `gale update`
(`update.go:255-275`, one lock write for the whole run, after
N config writes) widens row 1 to the whole loop.

## 2. Why it does not self-repair

Trace the divergent state (row 1) forward:

1. `gale shell` / `gale run` call `syncIfNeeded`, which loads
   the manifest and calls `lockIsStale`
   (`cmd/gale/shell.go:117`, implemented at
   `cmd/gale/lockstale.go:33`). The lock's declared roots no
   longer match gale.toml, so `v1Stale` →
   `lockfile.CheckDeclared` → `ErrStaleLock` → **stale = true**,
   on every invocation, forever.
2. Stale means sync, so `runSync` runs (`shell.go:130`).
3. `runSync` loads the lock (`sync.go:100`) and builds the
   plan *before the first install* (`sync.go:112-124`).
   `lockedSyncPlan` (`cmd/gale/synclock.go:60-66`) hands a v1
   lock to `lockplan.Build`, which runs the same
   `CheckDeclared` and returns `ErrStaleLock`.
4. `runSync` returns that error at `sync.go:124` — before any
   install, before `finishSync`, before any store or
   generation mutation.
5. Sync cannot write the lock (`sync.go:630-634`). There is no
   step in the entire sync path that could make step 3's check
   pass.

So the state is a fixed point of the only command that runs
automatically. Every `cd` into the project runs a sync that
fails identically. This is what makes it a bug and not a
transient: convergence is structurally impossible, not merely
delayed.

Two things soften it relative to the issue's description, and
one thing sharpens it:

- **Softer:** sync now *fails loudly* rather than silently
  reporting `upToDate`. The error names the remedy —
  `staleRemedy` (`internal/lockfile/roots.go:249-254,
  265-...`) renders `gale install <pkg>` and/or `gale lock`
  for each disagreeing root, per-target.
- **Softer:** nothing is mutated on the failing path, so the
  state does not degrade with repetition.
- **Softer still:** the direnv hook does *not* hide the error.
  `internal/env/env.go:42` is `gale sync || true` with stderr
  deliberately kept, pinned by
  `TestDirenvHook_NeverSuppressesStderr`
  (`internal/env/env_test.go:101-108`). The hook is also
  mtime-gated (`[ "$manifest" -nt "$gale_dir/current" ]`,
  `env.go:41`), so it does not even run sync on every `cd`.
  The user who edits gale.toml and cds in sees the stale-lock
  error and its remedy.

That last correction matters for the recommendation: the
residue is loud on every path that reaches it, so the case for
buying automatic recovery is weaker than the issue assumes.

**Acceptance criterion 2 of #187 is already met on main**, on
every path except the direnv one that #186 owns: a
pre-existing divergence fails `gale sync` non-zero, changes
neither gale.lock nor the store nor `current`, and names the
command that updates the lock. That should be pinned by a
test, not re-implemented.

## 3. Options

### Option A — one spanning lock per environment

Take a new exclusive lock at `<galeDir>/finalize.lock` around
all three steps of `FinalizeInstall`, released after the
generation rebuild.

**What changes.** `FinalizeInstall` wraps its body in
`filelock.With`. `update.go` and `remove.go` need care — see
the ordering constraint below, which is the expensive part of
this option and the reason it is separable from D and C.

**Contention.** The lock is per environment (global
`~/.gale`, or one per project `.gale/`). Note precisely what
it does and does not serialize: **it serializes concurrent
mutating commands' finalizations** — two installs, an install
and an update, an install and a remove. It does **not**
serialize sync against an install, because sync does not
finalize through `FinalizeInstall` at all: it rebuilds through
`finishSync` (`sync.go:394-419`) and never writes the lock.
Making sync take a shared hold is possible but is a different,
larger proposal with a real stall cost; this option should not
be sold on a race it does not close.

**Deadlock — the constraint that shapes the option.** Existing
acquisition order inside finalization is `gale.toml.lock` →
`gale.lock.lock` → `generation.lock`, each taken and released
as a leaf. `WriteLock`'s comment (`context.go:783-789`)
already refuses to take the config lock inside the lockfile
lock to avoid an invertible order. A finalize lock outside all
three adds no cycle among *those*. The store's package lock is
where it gets dangerous.

Install enters finalization from *inside*
`lockPackage(name, version-full)`
(`installer.go:1994-1999` → `context.go:1091`), giving
`package → finalize`. Two commands invert that:

- **`update`.** Its per-package config write runs inside
  `InstallWithFinalize`'s package lock (`update.go:255-258`).
  Wrapping "the whole run" in the finalize lock — the obvious
  reading, since its N config writes, one lock write and one
  rebuild look like one transaction — yields
  `finalize → package`. That is AB-BA against install.
- **`remove`.** Its sequence ends in `dropFromStore`
  (`remove.go:312`), which calls `store.RemoveWithin`
  (`internal/store/store.go:507`), which locks `dir+".lock"`
  — byte-identical to `lockPackage`'s
  `filepath.Join(storeRoot, name, version+".lock")`
  (`installer.go:1956`). Wrapping remove's sequence gives
  `finalize → package` too. `remove.go:294-302` already spells
  out that this ordering is load-bearing and that inverting it
  closes an AB-BA cycle on two blocking flocks.

`internal/filelock.Acquire` takes `LOCK_EX` with no timeout
(`filelock.go:39`), so both of these are hard deadlocks, not
contention. The naive framing of option A is wrong.

There is also an internal contradiction in the naive framing:
holding the finalize lock across update's whole run means
holding it across N downloads and builds, which is the
opposite of "held only across finalization."

**Corrected invariant.** `finalize.lock` is only ever acquired
while holding *at most one* package lock, and **never before**
acquiring one. Concretely:

- `install` / `switch`: unchanged — finalize inside the
  package lock, as today.
- `update`: wraps each per-package config write (already
  inside that package's lock) and, separately, its tail —
  `WriteLock` plus the rebuild, held outside any package lock
  and acquiring none. Update loses whole-run atomicity, which
  it never actually had.
- `remove`: wraps config-write → lock-write → rebuild and
  **releases before `dropFromStore`**. Its late-refusal
  compensation (`remove.go:216-222`) re-acquires for the
  restore-and-rebuild sequence rather than holding across the
  store deletion.

That invariant is one sentence and greppable, but it is a
standing obligation on anyone who later moves a lock
acquisition, and getting it wrong hangs the process rather
than failing it. It is the main cost of this option.

Given no contender inside the direnv path (see Contention
above), the "blocking flock stalls the shell" worry is
largely moot. A `LOCK_NB` retry is still worth having so a
wedged holder produces a message instead of a hang, but it is
a refinement here rather than a prerequisite.

**Crash safety.** None. Row 1 of the crash table is untouched.

**Complexity.** Low. One new lock file, one new documented
ordering invariant.

### Option B — write-ahead journal with replay

Before step 1, write a durable intent record
(`<galeDir>/finalize.journal`, or one file per entry) naming
the section, package, config version and lock version; fsync;
run the three steps; remove the record. Every command replays
a surviving record at startup.

**What changes.** A new package (`internal/finalizejournal`),
a replay call in `newCmdContext` (`context.go`), gc
integration so abandoned records are swept alongside the other
crash debris (`sweepCrashLeftovers`, `cmd/gale/gc.go:942-950`),
and a documented on-disk format. Several hundred lines.

**Direction: roll forward, not back.** All three steps are
idempotent given the intent, and the store is append-only, so
the artifact the intent names is still there. Rolling *back*
is the harder half and is often wrong: by replay time another
command may have legitimately repinned the package, and
undoing to a pre-crash manifest would discard that work.
Replay must therefore re-run the steps and let
`checkRootsDeclared` refuse if the manifest has moved on —
which is a correct refusal but leaves the operator with the
same divergence the journal was bought to prevent.

**Failure modes.**

- Replay must be mutually exclusive with live finalization, so
  B strictly contains A. It is not an alternative to the
  spanning lock; it is A plus durable state.
- A poisoned record — store directory swept by `gale gc
  --force`, recipe withdrawn, a lock that now conflicts —
  blocks or noisily fails *every subsequent command*, which is
  worse than the state it prevents. It needs a bounded retry
  count, an escape hatch, and an age-based sweep.
- Replay at `newCmdContext` runs before scope resolution
  settles for commands that re-point their config path
  (`sync`'s `projectDir` override, `sync.go:59-62`), so
  "which environment's journal" has a genuinely fiddly answer.
- Interactions with `--host` declaration-only installs, where
  finalization deliberately skips the PATH-presence check
  (`context.go:1103-1108`), must be encoded in the record.

**What it is worth.** Less than it appears, because the state
it recovers from is not stuck. Finalization runs *after* the
install has committed, so by the time row 1 exists the store
already holds the artifact **and its provenance**
(`recordProvenance`, `installer.go:1932`). `gale lock`
(`cmd/gale/lock.go:130-175`) resolves the declared roots,
verifies each against that store state, and writes the lock —
which is exactly the config↔lock reconciliation row 1 needs,
with no network and no rebuild. The generation follows from
the next sync's drift check (`sync.go:155-170, 406-419`), and
`gale install <pkg>` does all three in one command. There is
no genuinely unrecoverable state for the journal to rescue.

**Crash safety.** This is the only option that meets
acceptance criterion 3 as literally written.

**Complexity.** High, and most of it is in the recovery paths
that are hardest to test (see §7).

### Option C — make detection authoritative

Leave the write path alone. Instead:

1. Run the divergence check at command entry for every
   mutating command, not just sync, and refuse before store or
   generation mutation.
2. Name one command that reconciles. `gale lock`
   (`cmd/gale/lock.go`) already is that command; `staleRemedy`
   already prints it.
3. Make `FinalizeInstall`'s own failure path print the same
   remedy, so the user who created the divergence is told how
   to end it in the same breath.

**What changes.** Little. `lockIsStale` is already cheap by
construction (`lockstale.go:28-31`: no recipe resolution, no
hashing) and already schema-tolerant. This is mostly wiring
plus error text.

**Crash safety.** None, and no concurrency guarantee. It does
not prevent divergence; it guarantees divergence is loud and
one-command repairable.

**Failure modes.** A check on every mutating command adds a
manifest+lock read to the hot path (cheap, but real), and it
converts some currently-succeeding commands into refusals —
a behavior change users will notice.

**Complexity.** Very low. Mostly already built.

### Option D — compensate in `FinalizeInstall` (the small one)

Mirror `remove.go` exactly: make `WriteConfig` return a
`(prior, wrote)` `config.FileState` pair, use
`writeLockWitnessed` instead of `WriteLock`, and unwind on
every failure after a committed step with
`config.RestoreUnderLock` + `restoreLock`.

**What changes.** `FinalizeInstall` grows from 8 lines to
~35, plus a `WriteConfig` signature change with three call
sites — `context.go:1091` (`FinalizeInstall`),
`context.go:1203` (`WriteConfigForRecipe`), and through the
latter, `update.go:257`. The five *finalize* call sites do not
call `WriteConfig` directly and need no edit. No new files, no
new on-disk state, no new lock. The
machinery, its correctness argument, and its tests
(`remove_farmguard_test.go:425` `TestRemoveLateRefusalRestoresTheLock`,
`:474` `TestRemoveLateRefusalRestoresAbsentLock`) already
exist and are already reviewed.

**Crash safety.** None — compensation is in-process.

**Failure modes.** Two, and both end in the same place:

1. The compare-and-swap stands down because another command
   owns the file now (`remove.go:502-512`). Correct trade,
   already the documented behavior on the remove path.
2. The restore itself fails on I/O. `remove` joins that error
   to the original with `errors.Join` and returns
   (`remove.go:159-162, 183-186, 216-222`); nothing retries.

**Say the end state plainly:** in both cases the user is left
with the row-1 divergence and two error strings. That is
acceptable *only* because of the §3B finding — the store holds
the artifact and its provenance, so `gale lock` reconciles it
in one command — and because §C guarantees they are told so.
Without §C, D alone leaves a user holding an error they cannot
act on.

**Complexity.** Lowest of the four that change anything.

## 4. Recommendation

**Ship D + A + C as one bounded change. Defer B.**

Concretely, phase 1:

1. **D.** Witness-and-compensate in `FinalizeInstall`, reusing
   `config.RestoreUnderLock` and `restoreLock` verbatim. This
   closes every non-crash path to a persistent divergence.
2. **A.** A per-environment `<galeDir>/finalize.lock` spanning
   the three steps, under §3A's **corrected** invariant —
   acquired while holding at most one package lock and never
   before acquiring one, which means rescoping update's hold
   to its tail plus each per-package config write, and
   releasing remove's before `dropFromStore`. The invariant
   goes into `context.go` beside `WriteLock`'s existing
   ordering comment and into `remove.go:294-302`, which
   already documents the half of it that exists today.
3. **C.** `FinalizeInstall`'s failure path names the remedy,
   reusing `lockfile.staleRemedy`'s rendering rather than
   inventing a second wording.
4. **Keep the store's per-version lock as it is.** Rekeying
   `lockPackage` to the bare package name would serialize
   unrelated multi-version store work and change store
   semantics to fix a problem that lives one layer up. The
   environment lock is the right granularity for
   config+lock+generation; the package lock is the right
   granularity for the store.

**D and C can ship independently of A.** They touch only
`FinalizeInstall`, `WriteConfig`, and error text; they need no
new lock and therefore inherit none of §3A's ordering
obligation. If the update/remove rescoping drags in review,
land D+C first — that is the change that closes the reachable
divergence path, and A is the one that closes the concurrent
one. Do not let A hold D+C hostage.

Why this and not B:

- **The state B recovers from is not stuck** (§3B). The store
  holds the artifact and its provenance before finalization
  begins, so `gale lock` reconciles row 1 in one command with
  no network. B buys automatic repair of a state that is
  already one command from repaired, and that phase 1 makes
  both harder to create and impossible to create silently.
- The residue is also *loud*: the direnv hook keeps stderr by
  design (§2), so the user is told.
- B's own failure modes — poisoned replay blocking every
  command, replay racing a legitimate repin — are worse than
  the state it prevents, and they are the modes hardest to
  test (§7).
- Phase 1 reuses machinery that is already written, already
  reviewed, and already exercised on the remove path. It is
  the same design, applied to the path that was skipped.

**What phase 1 actually meets:**

| AC | Met by phase 1 | Notes |
|---|---|---|
| 1. Concurrent install of pkg@A and pkg@B end only in agreeing states | **Yes** | D handles the refusal case on its own; A adds the mutual exclusion. D alone narrows this to "agree, or diverge loudly with a named remedy" |
| 2. Pre-existing divergence fails sync non-zero, mutates nothing, names the remedy | **Already met on main** | Phase 1 adds the test and the same message on the finalize path |
| 3. Interruption is rolled back or completed from durable transaction state | **No** | Phase 1 has no durable transaction state. It narrows the window and makes the residue loud; it does not recover from it |

Do not claim AC3 in the phase 1 PR. If the maintainer wants
AC3 as written, that is option B and it is a separate,
larger piece of work with its own testability problem.

## 5. Interaction with #186's `sync-state.toml`

#186 is not on main yet (no `sync-state`, `syncNeeded`, or
`--if-needed` in the tree). Composing against its stated
design: a completion stamp in `<galeDir>`, written from a
`defer` in `runSync`, consumed by `syncNeeded()` and by the
direnv hook's freshness gate.

**One file, not two.** Phase 1 introduces no durable state at
all — only a lock file, which is not state (it carries no
content, it is `flock` on an inode, and `filelock` already
keeps such files on disk by design, `filelock.go:12-14`). So
there is nothing to unify *yet*. If phase 2 ever ships a
journal, it should be a table inside `sync-state.toml` rather
than a second file: both are per-environment durable records
of "is this environment's committed state trustworthy", both
live in `<galeDir>`, and both need the same single-writer
discipline. Two files would mean two writers, two sweep rules
in gc, and two chances to disagree about the same
environment.

**The integration phase 1 does require** is the reverse
direction, and it matters:

- A successful `FinalizeInstall` changes exactly what #186's
  stamp asserts — that config, lock, and generation are
  mutually consistent for this environment. So finalization
  must **refresh or clear the stamp inside the finalize
  lock**. If it does not, a subsequent `gale sync --if-needed`
  consults a stamp written by an older sync and takes the
  fast path, and a divergence created after that sync is never
  looked at again. That is the same sticky-skip failure #186
  exists to remove, re-entering by the install door.
- `syncNeeded()` must remain a **disjunction**, not a
  replacement: sync is needed if the stamp is missing or stale
  **or** `lockIsStale` reports the lock no longer describes
  the manifest. A stamp-only predicate would drop the only
  check that currently notices a config/lock divergence on the
  `gale shell` path (`shell.go:117`), converting #187's loud
  failure back into a silent skip. This is the single most
  important line of coordination between the two issues.
- **`current`'s mtime is already a de-facto stamp**, and
  finalization already refreshes it. The direnv hook gates on
  `[ "$manifest" -nt "$gale_dir/current" ]` (`env.go:41`), and
  `rebuildGeneration`'s atomic swap is what makes `current`
  newer than the manifest again. So a successful install
  currently *does* clear the gate. Whatever #186's stamp
  becomes must preserve that property, or an install stops
  satisfying the freshness check and every post-install `cd`
  re-runs sync — or, worse if the stamp is only written by
  sync, an install never refreshes it and the gate reads a
  stamp that predates the install. This is the concrete
  coupling, and #186 is landing now.
- The stamp and the finalize lock want the same granularity
  (per environment, rooted at `galeDir`). That is convenient:
  one lock protects both, and the stamp write needs no lock of
  its own.

Recommended sequencing: land #186 first, then phase 1 on top,
so the stamp refresh is written once against the real API
rather than predicted here.

## 6. What this needs from `IsStale`, and #197

`lockfile.IsStale` (`internal/lockfile/lockfile.go:68-88`) is
already dead in production. The only non-test references are
its own definition and comments; `shell.go:117` calls
`cmd/gale/lockstale.go`'s `lockIsStale`, the schema-tolerant
replacement that separates "stale" (sync resolves it) from
"unusable" (an error, because sending sync to fix it would
hide a lock-unusable condition). `doctor.go:647` and
`sync.go:541` call `installer.IsStale`, a different function
about dependency staleness in the store.

So the issue's diagnosis cites `lockfile.go:81-89`, but the
live predicate on the path it describes is `lockstale.go:33`.

**What this design needs**, in order:

1. `lockfile.CheckDeclared` (`internal/lockfile/roots.go:210`)
   — the authoritative "do config and lock agree" answer, and
   the producer of the remedy text phase 1 reuses.
2. `lockfile.VersionMatches` (`lockfile.go:117`) — the
   bare-vs-canonical reconciliation everything above rests on.
   #197 already commits to keeping it.
3. `cmd/gale/lockIsStale` — the cheap boolean on the `gale
   shell` hot path, and (per §5) one half of `syncNeeded()`.

None of these is `lockfile.IsStale`. **#197's cull is safe for
this work and needs no coordination beyond ordering** — it can
land before, after, or alongside phase 1. Phase 1 adds one
requirement to whatever survives: the divergence predicate
must be callable from the finalize failure path to render the
message, which `CheckDeclared` already satisfies.

## 7. Testability

Precedent in the tree: `cmd/gale/race_repro_test.go` and
`internal/generation/race_repro_test.go`. Read both before
writing new concurrency tests — they are not equally good
models.

- `internal/generation/race_repro_test.go:118`
  (`TestAudit_RollbackVsBuildRace_Deterministic`) orders two
  goroutines with an 80ms lock hold and a 10ms sleep. It is
  called deterministic; it is really *probably* deterministic.
  Do not extend this pattern.
- `cmd/gale/lockwriter_test.go:625`
  (`TestWriteLockReadsTheManifestUnderTheLock`) is the model.
  It drives the interleave through the `beforeLockRead`
  function-pointer seam (`context.go:898-904`), which runs
  inside `WriteLock`'s critical section after the lockfile
  lock is held and before the manifest is read. No sleeps, no
  goroutines, no flakes.

### AC1 — concurrency property

**Red-green testable, with one new seam.** The property is
"config and lock agree after any interleaving of a finalize
with a competing repin". The existing `beforeLockRead` seam
covers the window *inside* `WriteLock`. It does not cover the
window *between* `WriteConfig` and `WriteLock`, which is
where row 1 is born.

Named seam: a package-level `var beforeLockWrite func()` in
`cmd/gale/context.go`, nil in production, invoked by
`FinalizeInstall` after `WriteConfig` returns and before
`WriteLock` is called — the same shape and the same nil-in-
production discipline as `beforeLockRead` and
`beforeGuardedRemoval`. With it:

- Red today: seam commits a competing manifest repin →
  `WriteLock` fails `checkRootsDeclared` → assert gale.toml
  still carries this command's pin while gale.lock does not.
- Green after D: same interleave → assert gale.toml is
  byte-identical to its pre-command content and gale.lock is
  unchanged.

The environment-lock half of AC1 (option A) is harder to
assert honestly. Taking `<galeDir>/finalize.lock` in the test
body and calling finalize gives a deterministic assertion only
if acquisition is non-blocking-with-timeout — then the
assertion is "returns a contention error". Under a plain
blocking `LOCK_EX` the only observable is that the call does
not return, which is a sleep test and should not be written.
So: if A ships with blocking acquisition, its exclusion
property is reviewed, not tested. Say that in the PR rather
than writing a timing test that looks like proof.

### AC2 — divergence refusal

**Testable today, no seam needed, no concurrency.** Construct
a gale.toml/gale.lock disagreement on disk, run `runSync`,
assert: non-zero, `errors.Is(err, lockfile.ErrStaleLock)`,
gale.lock byte-identical, `current` unchanged, store
unchanged, and the message contains the remedy. This is a
pure filesystem-state test at the `cmd/gale` layer, which is
also the layer `scripts/check-pipeline-tests.sh` requires for
this path.

### AC3 — crash recovery

**Not testable today, and not testable under phase 1.** There
is no seam that can terminate the process between finalize
steps, and — more fundamentally — there is no durable state to
recover *from*, so there is nothing to assert about. Naming
the two ways it could become testable, with their costs:

- **In-process replay test.** Phase 2 writes a journal record,
  the test hand-crafts a record plus a matching half-applied
  filesystem state, and calls the replay entry point directly.
  This is a real, deterministic test of the replay *logic*. It
  does not test that a crash produces the state the test
  hand-crafted — that remains an assumption.
- **Integration crash test.** Requires a production crash seam
  (`GALE_TEST_CRASH_AFTER=config`, honored in
  `FinalizeInstall`) so the real binary can be killed at a
  known point, then a second invocation asserts convergence.
  This *does* test the whole property, and it costs a
  panic-on-env-var branch in the most regression-prone command
  path in the repo. That is a guardrail cost the maintainer
  should price explicitly, not a detail.

Until one of those exists, no PR should claim AC3.

## 8. Blast radius and migration

**Tier 2–3** under `docs/dev/change-discipline.md`: it is the
finalize path and it is `cmd/gale/context.go`. A pre-change
trace and a `cmd/gale`-layer repro test are required, not
optional.

Code touched by phase 1:

- `cmd/gale/context.go` — `FinalizeInstall`, `WriteConfig`
  (returns witnesses), `WriteConfigForRecipe`, new seam.
- Five finalize call sites: `install.go:146, 241, 295, 625`,
  `switch.go:95`. These need no edit for D (the signature
  change is confined to `WriteConfig`'s three direct callers)
  and no edit for A (they already run inside a package lock).
- `update.go:255-275` — **A only.** The hold is rescoped, not
  wrapped around the run: each per-package config write inside
  its own package lock, plus the tail (`WriteLock` + rebuild)
  outside all of them. Wrapping the run deadlocks (§3A).
- `remove.go:135-222` — **A only.** Takes the lock across
  config-write → lock-write → rebuild and releases it before
  `dropFromStore`; the late-refusal compensation re-acquires.
  It must not end up with two overlapping exclusion schemes,
  and it must not invert the version→generation order
  `remove.go:294-302` depends on.
- `internal/config/gale.go:810-818` — `RestoreUnderLock`
  reused as-is; no change expected.

Behavioral changes users can see:

- Some installs that previously returned an error *and left a
  new pin in gale.toml* now return the same error with
  gale.toml untouched. This is strictly better but it is a
  visible change; the audit-fix tests around
  `FinalizeInstall` (`cmd/gale/audit_fix_U11_test.go:52, 107`,
  `context_test.go:171, 225, 298, 460`,
  `rebuild_generation_test.go:247, 312, 405`) all assert
  against the current signature and will need updating.
- A second concurrent gale command in the same environment may
  report contention instead of proceeding.

**Migration: none.** Phase 1 adds no on-disk format, no schema
version, and no file with content. `<galeDir>/finalize.lock`
is an empty flock target that older binaries simply never
acquire — a mixed-version machine loses the mutual exclusion
for the duration but nothing breaks and nothing needs
converting. Placement matters for one small reason: putting it
in `<galeDir>` (beside `generation.lock`) rather than beside
`gale.toml` keeps it out of project git without a new
`.gitignore` entry — `.gale/` is already ignored, and
`gale.lock.lock` needed an explicit line (`.gitignore:3, 23`).
gc's `sweepCrashLeftovers` (`gc.go:942-950`) needs no change:
lock files are meant to persist.

Portability is unchanged: `internal/filelock` is already
unix-only (`golang.org/x/sys/unix`).

## 9. Open questions for the maintainer

1. **Should sync take a shared hold on `finalize.lock`?** As
   proposed it does not, so install-vs-sync is not serialized
   (§3A). Closing that is a larger change and reintroduces the
   direnv stall risk that the staleness regressions cost us
   (013b4a4, 688ce7d, af4c3f6) — at which point `LOCK_NB` with
   a bounded retry becomes a prerequisite and needs a
   `filelock` API addition. Recommendation: no, not in this
   work.
2. **Is A worth its ordering obligation at all?** D+C close
   the reachable divergence path on their own. A closes the
   concurrent one and costs a standing invariant across
   `install`, `update`, `remove`, and anything added later,
   enforced only by review. A defensible answer is "ship D+C,
   revisit A if a concurrent-divergence report arrives."
3. **Is `gale lock` the named remedy**, or should there be a
   dedicated `gale reconcile`? #197 is already writing
   `docs/lockfile.md` remedies; this should agree with it
   rather than introduce a second vocabulary.
4. **Should `syncNeeded()` be a disjunction with
   `lockIsStale`?** (§5.) If #186 lands stamp-only, #187's
   detection regresses on the `gale shell` path. This wants a
   decision before #186 merges, not after.
5. **What evidence would justify phase 2?** Proposal: a
   reproducible field report of row 1 arising from an
   interruption rather than a refusal. Absent that, the
   journal is speculative durability with real recovery-path
   risk.
6. **Should the per-version store lock stay per-version?** This
   proposal says yes and puts the exclusion one layer up.
   Disagreeing changes the shape of the whole fix.

## 10. Review

Reviewed before merge; verdict sound-with-fixes. What changed
as a result, so the delta is visible rather than only the
final text:

- **§3A was wrong and is rewritten.** The first draft claimed
  the ordering invariant held because `update` and `remove`
  "write the lock outside any package lock." Both were
  falsified: `update`'s config writes run inside
  `InstallWithFinalize`'s package lock (`update.go:255-258`),
  and `remove`'s `dropFromStore` reaches
  `store.RemoveWithin`, which locks the same file
  `lockPackage` holds (`store.go:534` vs `installer.go:1956`).
  Wrapping either as first proposed is a hard deadlock under
  today's unconditional `LOCK_EX`, not contention. The
  invariant is restated as a constraint on acquisition order,
  and update's and remove's holds are rescoped. The draft also
  contradicted itself by holding the lock across update's
  downloads and builds; that is gone.
- **§3A's rationale shrank to what it actually buys.** The
  draft justified the lock by "direnv-triggered sync racing a
  manual install," which it does not serialize — sync never
  finalizes. It now claims only what it does: mutual exclusion
  between concurrent mutating commands' finalizations.
- **§2's direnv facts were stale and are corrected.** The hook
  is `gale sync || true` with stderr kept
  (`internal/env/env.go:42`, pinned by
  `env_test.go:101-108`), and it is mtime-gated rather than
  run on every `cd`. The residue is loud, which strengthens
  the case for deferring the journal.
- **§3B gained the evidence that carries the deferral.** The
  store holds the artifact and its provenance before
  finalization begins, so `gale lock` reconciles row 1 in one
  command. No genuinely stuck state exists. The draft
  under-used this; it is now the lead argument in §4.
- **§3D states the end state of a failed compensation.** CAS
  stand-down *and* restore I/O failure both leave the user
  with the divergence plus error strings — acceptable only
  because of the §3B finding and only if C ships alongside.
- **§4 says D+C can ship without A**, since A's cost is
  entirely the ordering obligation.
- **§5 names `current`'s mtime as the existing de-facto
  stamp** that finalization already refreshes, which #186's
  stamp must preserve.
- Minor: §3D's call-site count corrected to three direct
  `WriteConfig` callers; §7's option-A testability claim
  weakened to match blocking acquisition; open question 1
  replaced, since the stall risk was predicated on a
  contender that does not exist.
