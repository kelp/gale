# Gale concurrency audit — shared-state inventory

Repo SHA: see `git rev-parse HEAD` in the worktree
(branch: worktree-races-audit, base: main).

Goal: enumerate every shared mutable resource that a gale
mutation command (install, add, remove, sync, update,
switch, gc, generations rollback) reads or writes, who
locks it, and what the documented protection model is.
Findings draft adversarial scenarios against each item.

## Scope of "mutation commands"

| command         | source                          |
|-----------------|---------------------------------|
| install         | cmd/gale/install.go             |
| add             | cmd/gale/add.go                 |
| remove          | cmd/gale/remove.go              |
| sync            | cmd/gale/sync.go                |
| update          | cmd/gale/update.go              |
| switch          | cmd/gale/switch.go              |
| gc              | cmd/gale/gc.go                  |
| generations rollback | cmd/gale/generations.go    |

direnv fires `gale sync` (via `use_gale`) and `gale env`
on every cd into a project — so cross-process races
between `gale sync` and an interactive `gale install`
are first-class.

## Locking primitives

| primitive                | path                                         |
|--------------------------|----------------------------------------------|
| `internal/filelock`      | `flock(LOCK_EX)` on a long-lived `.lock` file, kept on disk |
| `internal/atomicfile`    | tempfile + fsync + `os.Rename` (POSIX atomic rename) |
| store-gen lock           | `<galeDir>/generation.lock` — `storeGenLockPath(storeRoot)` derives `filepath.Dir(storeRoot)` (the *global* galeDir, even when project sync rebuilds a *project* generation — known limitation, see installer.go:1051) |
| per-package install lock | `<storeRoot>/<name>/<storeVersion>.lock` (filelock) |
| generation build lock    | `<galeDir>/generation.lock` (filelock, scope-local — global vs project galeDir produce different lock files) |
| gale.toml write lock     | `<configPath>.lock` (filelock) |
| gale.lock write lock     | `<lockPath>.lock` (filelock) |

`internal/filelock` is process-level (flock(2)), so it
serializes across goroutines AND across processes — good
for Class C as long as every reader/writer of a path uses
the same lock file.

## Shared-state inventory

### 1. gale.toml — project and global

- **Path**: `<project>/gale.toml`, `~/.gale/gale.toml`
- **Writers**: `config.AddPackage`, `config.UpsertPackage`,
  `config.RemovePackage`, `config.PinPackage`,
  `config.UnpinPackage`, `config.WriteGaleConfig` (raw).
- **Readers**: `ctx.LoadConfig`, `loadEffectiveConfig`,
  `cmd/gale/list.go`, `gc.mergeConfig`,
  `installer.installDepsInner` (indirectly via resolver),
  every dry-run and `gale env`/`gale sync` invocation,
  direnv on shell cd.
- **Protection**: writers all wrap in `withFileLock(path,
  ...)` → `filelock.With(path+".lock", ...)`, and writes
  go through `atomicfile.Write`. **Readers are unlocked**
  (`os.ReadFile`). atomicfile guarantees the reader sees
  the old or new content, never a torn write, so this is
  fine for single-key reads. But a reader that does
  read-decide-write (gc, sync) crosses the lock boundary
  and is not protected against intervening writes.
- **Class A**: no concurrent in-process writers — every
  command runs in a single goroutine.
- **Class B**: atomic-rename guarantees an unlocked reader
  cannot observe a torn file.
- **Class C**: two concurrent `gale add jq` runs will
  serialize via withFileLock. Two concurrent `gale add jq`
  + `gale add ripgrep` should both succeed serialized.
  Read-modify-write windows (e.g. `gc` + `add`) are NOT
  serialized because gc doesn't take the gale.toml lock
  while it inspects the config.

### 2. gale.lock

- **Path**: `<project>/gale.lock`, `~/.gale/gale.lock`
- **Writers**: `updateLockfile`, `removeLockEntry`,
  `lockfile.Write`.
- **Readers**: `lockfile.Read` from
  `cmd/gale/{sync,update,outdated}.go`,
  `context.writeConfigAndLock`.
- **Protection**: `updateLockfile` wraps the
  read-modify-write in
  `filelock.With(lockPath+".lock", ...)`.
  **`removeLockEntry` does NOT take the lock — it
  performs an unlocked read-modify-write directly via
  `lockfile.Read` + `lockfile.Write`.** This is the first
  smoking gun: a concurrent `gale install` and `gale
  remove` of *different* packages will race on
  gale.lock, and the loser silently overwrites the
  other's entry. See iteration 2's findings.
- **Class C**: `removeLockEntry` vs `updateLockfile`
  contend — confirmed as a finding.

### 3. `~/.gale/current` (symlink)

- **Writers**: `generation.swapCurrentSymlink` (called by
  `Build`, `BuildLenient`, `Rollback`).
- **Readers**: `generation.Current` (everywhere — used by
  every Build to decide the next gen number, by gc to
  decide which gens to keep, by env to find PATH, by
  doctor, by run/shell, by direnv on every cd).
- **Protection**: `Build`/`BuildLenient` hold
  `filelock.With(generationLockPath(galeDir), ...)`
  around the read of `Current()` and the eventual swap.
  **`Rollback` does NOT take the lock** — it just calls
  `swapCurrentSymlink` directly. So a `gale generations
  rollback N` racing against a `gale sync` produces a
  classic lost-update: Sync reads cur=10, picks 11,
  Rollback swaps to 5, Sync swaps to 11. Net effect:
  rollback silently discarded.
- The swap itself uses tmp-link + `os.Rename`, which is
  atomic per POSIX. So a reader never sees a half-swapped
  symlink.
- **galeDir scope mismatch**: when project sync calls
  `Build` with the project `.gale/`, the lock file is
  the project's `.gale/generation.lock`. The installer's
  store-gen lock uses the *global* galeDir
  (`filepath.Dir(storeRoot)`). So a project-scoped
  `Build` and an `Install` of one of its packages do NOT
  share a lock — known and documented in installer.go.

### 4. `~/.gale/gen/<N>/`

- **Writers**: `generation.Build` (creates gen N+1,
  populates it, validates, then swaps current);
  `gc.cleanOldGenerations` (RemoveAll on every non-current
  gen).
- **Readers**: anyone resolving symlinks (env, run, doctor,
  generations list/diff).
- **Protection**: `Build` holds the generation lock for
  the whole create+populate+validate+swap.
  `cleanOldGenerations` does **NOT** hold the lock, and it
  reads its `entries` list before calling
  `generation.Current()` — so it can observe a half-built
  gen N+1 from a concurrent Build, then read Current as
  the pre-swap value (still N), and conclude N+1 is
  reapable. RemoveAll'ing gen N+1 mid-populate produces
  the build's promised "store mutated during rebuild"
  ENOENT.
- **Class C**: gc vs concurrent install / sync; high
  blast radius (corrupts the active generation).

### 5. `~/.gale/pkg/` (the store)

- **Writers**: `installer.Install` (creates
  `<storeRoot>/<name>/<version>/...`), `installer.Reinstall`
  (stages into `.build-<random>/`, then atomic rename via
  `replaceStoreDir`), `installer.InstallLocal`/`InstallGit`,
  `store.Remove`, `gc.removeUnreferencedVersions` (via
  store.Remove).
- **Readers**: `generation.populateGeneration` (ReadDir +
  symlink), `store.IsInstalled`, `store.List`,
  `store.StorePath`, `store.resolveVersion` (single
  ReadDir, deliberate to avoid TOCTOU per CLAUDE.md
  "M2" fix).
- **Protection**:
  - Per-package install: `lockPackage` →
    `filelock.Acquire(<storeRoot>/<name>/<version>.lock)`.
    Serializes two `gale install jq@1.8.1-3` calls.
  - Forced-reinstall + binary/source extract: held under
    the *store-gen* lock so a concurrent gen Build sees
    pre or post state.
  - `store.Remove` — **NOT locked**. gc and remove
    commands invoke it directly, so it races with both
    concurrent installs of the same name (different
    version, no shared package lock) AND with a gen
    Build that's currently symlinking that dir.
- **Class C**: `gc` vs `install` of a different version
  of the same package, or `remove` vs gen `Build` (which
  walks the store).

### 6. `~/.gale/lib/` (the dylib farm)

- **Writers**: `farm.Populate` (creates symlinks for a
  package's versioned dylibs), `farm.Depopulate` (removes
  symlinks pointing into a given storeDir),
  `farm.Rebuild` (RemoveAll(farmDir) + recreate +
  Populate every active store dir — called from inside
  `generation.Build` AFTER the symlink swap).
- **Readers**: every binary that runs against the farm
  (via rpath), `farm.CheckDrift` (doctor).
- **Protection**:
  - `installer.commitStaged` populates the farm under
    the store-gen lock, BUT...
  - `generation.Build` calls `farm.Rebuild` AFTER
    releasing... wait, no — `farm.Rebuild` is called
    inside the `filelock.With` callback in `build()`,
    so it's actually inside the generation lock. Good.
  - However, `installer.installBinaryTo` and
    `installer.extractBuild` populate the farm in the
    inPlace path under the store-gen lock. Cross-scope:
    a project sync's gen Build holds the *project* gen
    lock, while installer.commitStaged holds the
    *store/global* gen lock — these are DIFFERENT FILES.
    So a project sync's Rebuild and an installer's
    Populate can race on the same farmDir. RemoveAll
    racing against a concurrent Populate symlinking is
    classic Class B/C.
  - `farm.Rebuild` calls `os.RemoveAll(farmDir)` then
    `os.MkdirAll`, then Populates each store dir.
    Anyone reading the farm during the RemoveAll window
    will see a missing dylib — binaries that resolved
    through farm rpath will get an ENOENT loading the
    library mid-rebuild.

### 7. `~/.gale/tmp/`

- **Writers**: `build.TmpDir()` returns
  `<storeRoot-parent>/tmp/`. `installer.InstallGit`,
  `installFromLocalSource`, `installFromSourceTo` create
  per-install `gale-install-*` subdirs via `MkdirTemp`.
- **Readers**: build pipeline.
- **Protection**: MkdirTemp produces unique names; no
  shared mutation within a temp dir across two
  installs. The shared parent
  `~/.gale/tmp/` exists. No cleanup race expected.
- **Class C**: low — MkdirTemp's randomness makes
  collision negligible.

### 8. In-memory state during long ops

- Installer is a single struct per command (`cmdContext`
  builds a fresh one). No goroutines fan out from any
  command's main path (verified by grepping `go func`).
- `installDepsInner` uses a local `seen` map per
  invocation — no sharing.
- **No** worker pools or `errgroup` parallelism in
  install/sync/update.
- **Class A**: nothing for the race detector to find
  in mutation paths, *probably*. Will verify with `go
  test -race ./...`.

## Cross-process race surface (Class C)

Plausible concurrent pairs (based on direnv firing
gale on every cd):

| pair                                  | shared state at risk                |
|---------------------------------------|--------------------------------------|
| `gale sync` (direnv) × `gale install` | gen lock (scope mismatch), store-gen lock, gale.lock |
| `gale sync` × `gale gc`               | gen dirs, store dirs, farm           |
| `gale install A` × `gale install B`   | gen lock, gale.toml, gale.lock, store |
| `gale rollback N` × `gale sync`       | current symlink, farm                |
| `gale remove A` × `gale install A@vN+1` | store dirs, gale.lock, gale.toml   |
| `gale install A` × `gale install A`   | per-package lock (should serialize) |
| `gale switch A vX` × `gale sync`      | gale.toml, generation, store         |

## Iterations completed

| iter | shared-state item                       | status | finding |
|------|------------------------------------------|--------|---------|
| 1    | Inventory                                | DONE   | —       |
| 2    | gale.lock — `removeLockEntry` vs `updateLockfile` | DONE | 0001 |
| 3    | `current` symlink — `Rollback` vs `Build` | DONE   | 0002 |
| 4    | gen dirs — `gc.cleanOldGenerations` vs `Build` | DONE | 0003 |
| 5    | store dirs — `gc.removeUnreferencedVersions` vs install | DONE | 0004 |
| 6    | dylib farm — project gen lock not shared with store-gen lock | DONE | 0005 |
| 7    | Class A: `go test -race ./...` sweep         | DONE   | clean (no Class A findings) |
| 8    | gale.toml — read-decide-mutate across lock boundary | DONE | parked in state.md (static-only) |
| 9    | `~/.gale/tmp/` — quick check                 | DONE | clean (MkdirTemp randomness; no sharing) |

## TODOs (suspicions parked here until I can reproduce)

- `generation.swapCurrentSymlink` uses a PID-scoped temp
  name (`current-new.<pid>`). If two `gale rollback` runs
  share a PID (impossible) or two long-lived processes
  collide on the same PID (impossible without exec) —
  no real risk. But the function does
  `os.Remove(tmpLink)` first; if a *different* PID's
  stale `current-new.<otherpid>` leaks, this code doesn't
  clean it up — minor litter, not a race.
- `installer.Reinstall`'s staging dir `.build-<random>`
  is removed in a defer. If `commitStaged` succeeded,
  the rename moved that dir, so the defer's
  `os.RemoveAll(stagingDir)` is a no-op (ENOENT, silently
  ignored). Fine.
- `store.Remove` falling back through `resolveVersion`
  for a "@version-rev" exact-miss could race against a
  concurrent install creating the exact dir between the
  exact-Stat and the resolveVersion fallback. Speculative.
- `generation.Rollback` does NOT validate that the target
  generation's symlinks all resolve. A user rolls back
  to gen 5; gc-rebuild later removed a store dir that
  gen 5 references; rollback succeeds and the user gets
  a broken PATH at next `command -v`. This is a usage
  bug, not a race, but the missing lock makes it worse.

### Static-only observations on gale.toml

- `ctx.LoadConfig` (cmd/gale/context.go:108) and the
  ad-hoc `os.ReadFile` callers in `cmd/gale/remove.go:174`
  (`locatePackageSections`), `cmd/gale/sync.go:72`,
  `cmd/gale/switch.go:51`, `cmd/gale/update.go` all
  perform an unlocked read. Each then makes a decision
  ("which sections to remove from", "which packages need
  install", "which version transition to display") before
  invoking a locked write via `config.UpsertPackage` /
  `config.RemovePackage`. A concurrent writer between the
  read and the locked write changes the gale.toml under
  these commands' feet. Concrete consequences observed
  statically:
    - `gale remove jq`: snapshots `cfg.Packages[jq]`,
      then iterates sections. A concurrent `gale install
      jq@1.8` between sections leaves jq in gale.toml
      under whichever section the racing install wrote to
      — remove proceeds to delete the store dir, leaving
      the config referencing a missing package.
    - `gale switch jq 1.8.0`: reads "current = 1.7.0",
      decides to transition, even if a concurrent
      `gale update` or `gale install` already moved jq
      to 1.8.0. Result: redundant reinstall and a
      misleading "Switching 1.7.0 → 1.8.0" line.
    - `gale sync`: snapshots the pkg map and iterates;
      a concurrent `gale add` during the loop adds a
      package that sync won't see this run (deferred to
      the next sync, which direnv may not trigger
      automatically).
  These are not data corruption per se — the atomic-rename
  + flock pair guarantees no torn writes — but they are
  consistency races that the audit caller may want to
  treat as bugs depending on UX expectations. Not
  filed as findings because the failure mode is "stale
  decision", not "corrupted file state", and the prompt
  defines confirmed as "race detector or 100%
  deterministic".

### Static-only observations on the dylib farm

- `farm.Rebuild` (internal/farm/farm.go:181) does
  `os.RemoveAll(farmDir)` then MkdirAll then per-package
  Populate. Any process that has dlopen'd a versioned
  dylib through farm rpath during this window will
  observe an empty farm; subsequent dlopens between
  RemoveAll and the re-Populate of that specific dylib
  will fail with ENOENT. The window is short (single-
  digit milliseconds typically) but real. Not filed
  because I cannot deterministically reproduce a
  running-binary failure without crafting a real linked
  artifact.
- `installer.replaceStoreDir` renames `storeDir → .bak`
  before renaming `buildDir → storeDir`. During the
  micro-window between the two renames, the original
  farm symlinks (pointing into the old storeDir, now
  named `.bak`) still resolve through the renamed
  inode, but a process that re-reads the farm symlink
  by name (e.g., a rebuild) sees the old target path
  no longer exists. Held under the store-gen lock so
  global-vs-global is safe; project-vs-global is not
  (see finding 0005).

### Static-only observations on the store cleanup path

- `store.cleanupEmptyNameDir` (store.go:248) reads
  `entries` and removes the name dir if empty. A
  concurrent install that has `filelock.Acquire`d the
  per-package lock but has NOT yet `Store.Create`d the
  version dir leaves only a `<version>.lock` file under
  `<name>/`. `cleanupEmptyNameDir` will see the lock file
  (`len(entries) != 0`) and NOT remove the parent dir.
  Safe. Worth noting because the comment says "if no
  version dirs remain" but the implementation actually
  counts any entry. Functionally equivalent here.
