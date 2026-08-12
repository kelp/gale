# Pre-Revision Convergence

Design proposal for gh#200. Status: proposal, not
implementation. Companion to `.codex-pair/design-gale-issue-182.md`
(the numbered design this document calls §N) and to
`content-addressed-store.md` (gh#191), which is evaluated in §7.

Counted and cited at `6d81908`.

## 1. Problem

A package source-built before revisions existed sits in a bare
`pkg/<name>/<version>/` directory — no `-<revision>` suffix, no
`.gale-provenance.toml`, and, if it predates v0.12.0 entirely, no
`.gale-deps.toml` either. Store resolution still finds it: a
canonical `1.0-1` request falls back to a bare `1.0` directory
(`internal/store/store.go:117-196`, documented at
`docs/revisions.md:78-93`), so nothing is broken until something
asks the directory to attest itself.

**The user-visible symptom is a two-command loop, each command
naming the other.**

`gale lock`, or any writer, resolves the identity and finds bytes:

- `lockRoot` (`cmd/gale/lock.go:296-341`) calls
  `Store.StorePath(name, r.Package.Full())`. Back-compat hands
  back the **bare** directory, so `occupied` is true.
- `provenance.ReadUnverified`
  (`internal/provenance/provenance.go:156-207`) returns
  `ErrAbsent`.
- `unprovenanced` (`cmd/gale/lock.go:426-442`) errors. Because
  `dir != canonical` it names exactly one remedy, and it is not
  `--refresh`: "`gale migrate`, since `<dir>` predates revisions
  and moving it to `<canonical>` is machine-wide work that one
  scope cannot do safely."

`gale migrate` then declines it:

- `classifyForMigrate` (`cmd/gale/migrate.go:112-151`) reads no
  provenance, resolves the recipe, and finds
  `BinaryForPlatform` nil (`migrate.go:145`), so the directory
  goes to `sourceOnly` rather than `candidates`.
- `reportSourceOnly` (`cmd/gale/migraterun.go:148-161`) splits by
  where the bytes sit. A canonical source directory goes to
  `reportRebuildable`, which names `gale lock --refresh <pkg>`.
  A bare one goes to `reportUnresolved`
  (`migraterun.go:193-214`), which prints: "Each stays
  unattested, so a locked environment will keep refusing to
  activate it. No gale command converges them today; the gap is
  tracked in gh#200."
- Exit 0. Nothing changed.

`gale lock --refresh <pkg>` is not a third option. `refreshable`
(`cmd/gale/lockrefresh.go:285-303`) `Lstat`s the **canonical**
path, which does not exist, returns false, and the run falls
through to `lockRoot` — producing the error above again.

So the error names a command, and the command names the issue
number. That loop is the symptom. Everything downstream follows
from it: the scope cannot mint a v1 lock, so under §9 its legacy
lock keeps hard-failing; a scope that already carries a v1 lock
gets `gateActivation` (`cmd/gale/activation.go:28-51`) refusing
`PATH_add` on every `cd`, for as long as its generation links the
bare path.

Two details sharpen the shape of the affected set.

**It is not only packages the user chose to build.** The dep
install path and `Store.IsInstalled` accept the back-compat
fallback (`internal/installer/installer.go:354-360`), so a
dependency in a bare directory counts as installed and is never
re-migrated even when its dependent is. Libraries — the packages
most likely to be source-method — are the ones most likely to
survive as bare directories.

**Half of what gh#200 prints today may already have a remedy.**
`reportUnresolved` reports every bare source directory, whether or
not any scope reaches it. A directory nothing links is a `gale gc`
candidate — retention is built from config-derived canonical keys
plus the active generation's symlink targets
(`cmd/gale/gc.go:416-432`, `:487-528`), and a bare directory that
neither names is swept. The report cannot currently tell a live
trap from a dead leftover, so a user with an orphan is told there
is no remedy when `gale gc` is one.

## 2. How common is this?

Honestly: very rare, and the window is closed.

The repository's first commit is 2026-03-23
(`git log --reverse`). Revisions shipped in v0.12.0 on
2026-04-18. A bare store directory can only have been written in
those 26 days, in gale's first month, before there was a Homebrew
tap story or a recipe registry worth pointing anyone at.

The window narrows further from both ends.

**Unlocked `gale sync` already converges declared roots.**
`installedStale` (`cmd/gale/sync.go:521-542`) reports stale for
any store directory with no `.gale-deps.toml`, and sync routes
stale packages through `Reinstall`
(`internal/installer/installer.go:294-296`), which stages into a
sibling and commits at the **canonical** path. This is the "Soft
migration" section of `docs/revisions.md:229-239`. Every declared
root on a machine that has run an unlocked sync since April 2026
is already canonical.

**What survives that pass** is what sync never visits:
dependencies (the back-compat cache hit above), packages dropped
from gale.toml but still linked by a retained generation, and
roots in a scope whose owner has not synced since April.

So the affected population is: machines in continuous use since
gale's first month, whose owner installed a source-method package
in a 26-day window, and which have not had an unlocked sync reach
that package since. That is a handful of early adopters and quite
possibly only the maintainer's own machines. Nothing in the
tracker reports an instance; gh#200 was found by review of #196,
not by a user hitting it.

The issue's own framing — "likely rare, and unbounded in cost when
it happens" — is right about the cost and, on this evidence, right
about the rarity too. That materially changes the answer: an
elaborate mechanism would be built, tested against stubs (§8), and
run approximately once.

## 3. Why each existing mechanism declines it

| Mechanism | Decision site | Why it declines |
| --- | --- | --- |
| `gale migrate` | `cmd/gale/migrate.go:145` | The recipe declares no binary for this platform, so a refetch cannot produce the bytes; §13 forbids stamping a record beside a directory migrate did not replace. |
| `gale lock` | `cmd/gale/lock.go:426-442` | The resolved directory is occupied and unprovenanced; adopting it would assert provenance for bytes gale never verified (§11). |
| `gale lock --refresh` | `cmd/gale/lockrefresh.go:298` | `Lstat` of the **canonical** path fails, so the bare directory is out of scope by design: other scopes' generations link it, and relocation is machine-wide work. |
| `gale sync` (unlocked) | `cmd/gale/sync.go:521-542` | Does **not** decline — it converges declared roots. It just never visits dependencies or undeclared leftovers. |
| `gale sync` (locked) | `cmd/gale/sync.go:544-575` | The staleness check is gone under a plan; §4 permits committing only absent canonical dirs. Moot in practice, since a trapped scope cannot mint a v1 lock. |
| `gale doctor` | `cmd/gale/doctor.go:614-660` | Flags it as a stale install (no `.gale-deps.toml`), but only under `--check-registry`, reads no provenance, and prescribes `gale sync`. |
| `gale gc` | `cmd/gale/gc.go:487-528` | Reaps it once nothing references it — a real remedy for the orphan case that nothing currently tells the user about. |

## 4. Options

### A. Synthesise provenance from what is on disk

Teach `refreshable` to accept a bare directory and have
`gale lock` write a `.gale-provenance.toml` describing the bytes
already there: hash the directory, record `method = "source"`, mint
a graph digest from `.gale-deps.toml`.

**Changes:** `refreshable`, plus a new writer in
`internal/provenance/`.

**Risk:** this is §13's rejected unverified marker under another
name, and both `lock.go:411-424` and `lockrefresh.go:176-179`
already say so in prose. A synthesised record is
indistinguishable from a real one — it would pass `VerifyShallow`
(`internal/provenance/provenance.go:415-421`) and §12's activation
gate. Fail-closed becomes authorized-unverified, which is the one
property the whole of #182 exists to remove.

**Verifiable:** trivially, and that is the trap. It is the
cheapest option to test and the only one that is wrong on
principle. Rejected.

### B. Teach `lock --refresh` to rebuild a bare directory

Relax `refreshable`'s canonical-occupied requirement and let
`replaceUnprovenanced` (`cmd/gale/lockrefresh.go:195-260`) build
the canonical directory from source, leaving the bare one behind
for gc.

**Changes:** `refreshable`, and then — unavoidably — every piece
of machinery migrate already owns.

**Risk:** the commit itself is fine (the canonical directory is
absent, so `Reinstall` is additive). What follows is not. Other
scopes' generations link the bare path, and refresh regenerates
only its own scope. The bare directory and the canonical copy both
claim the same sonames in the machine-global farm, which design
§4's `GuardPopulate` refuses — which is exactly why migrate sets
`DeferFarm` for its relocating shape
(`cmd/gale/migraterun.go:296-307`) and rebuilds the farm once, for
the whole machine, in `finishRelocations`
(`migraterun.go:83-130`, `internal/generation/farmclaims.go:482-503`).
Adding all of that to refresh reimplements migrate inside a
per-scope command, and §13 says explicitly that refresh "stays
per-scope and gains no all-scopes mode".

**Verifiable:** the per-scope half is; the cross-scope half is the
part that would be wrong, and testing it means building migrate's
fixtures a second time. Rejected.

### C. Teach `gale migrate` a source-rebuild relocation

A flag — `gale migrate --build`, say — that promotes bare
source-method directories from `sourceOnly` to candidates and
relocates them by **rebuilding** rather than refetching.

**Changes**, all in existing seams:

- `classifyForMigrate` (`migrate.go:112-151`): a third
  classification, gated on the flag.
- `migrateOne` (`migraterun.go:265-322`): the relocating shape
  with `BinaryOnly` off. `DeferFarm` still on, for the reason its
  doc comment gives.
- `canonicalAttests` (`migraterun.go:351-378`): a source form.
  It cannot compare against a declared SHA, because a source
  build's hash is what this machine produced and no recipe field
  describes it (§10). It can still require `MethodSource` and a
  record that verifies shallowly.
- `migratePreflight` (`migrate.go:346-388`): **this is the
  collision.** §13 requires failing before replacing on every
  recorded *and proposed* candidate hash, and a source build's
  proposed hash is unknowable until after the build.

The collision is survivable, and the reason is worth stating
because it is what makes the option coherent at all: for the
relocating shape the install is **additive** — the canonical
directory is absent, so nothing is destroyed by building into it.
The genuinely destructive steps are the farm rebuild and
`removeRelocatedDir` (`migraterun.go:485-515`), and both already
revalidate from scratch with the committed record in hand. So the
order becomes: reference-only preflight → build → hash-based
clearance against every scope → destructive commit. Whether that
satisfies §13's *intent* is a maintainer call (§9).

Two further points reduce the work and one increases the risk.

Reducing: store resolution already floats a bare reference onto a
populated canonical sibling (`store.go:159-196`), so once
canonical bytes exist, each scope's next generation rebuild leaves
the bare path by itself — `regenerateScope`
(`migraterun.go:440-457`) exists to make that happen in one pass
rather than eventually, and `migratewiring_test.go:701-712`
records the same fact.

Increasing: a committed project lock, checked into git from
another machine, can name a source hash for this identity. §10 is
explicit that source builds do not reproduce across machines, so
anything built here will disagree, that scope vetoes, and the run
refuses. The refusal is correct. It also means the most likely
outcome of running `--build` on a real multi-machine setup is that
it does not converge.

**Cost:** tier 3 under `docs/dev/change-discipline.md:29-32` —
migrate touches farm rebuild, generation rebuild, and store
removal.

**Verifiable:** classification, ordering, refusals and reporting
are all red-green testable against `migrateMachine`'s fixtures.
The property that matters most — that a real rebuilt artifact
attests and clears every scope — is not (§8).

### D. Remove and reinstall

Tell the user to `gale remove <pkg>` and install it again.

**What is actually lost:** the bytes, and nothing else. The store
is derived state; gale.toml keeps the pin, the lockfile is
untouched, and `Store.Remove`'s back-compat fallback
(`store.go:479-506`) does find and delete the bare directory. For
a **single-scope machine** and a **declared root**, the sequence
converges: remove deletes the bare directory, install finds the
canonical path absent, builds, and `recordProvenance` writes the
record.

**Risk, and why `reportUnresolved` refuses to print it:** it does
not generalize. If a second scope's generation links the bare
path, the removal leaves dangling symlinks until that scope syncs.
If the bare directory is a **dependency** rather than a root,
there is no package to remove — `gale remove` operates on declared
names. And the sequence is per-scope in a store that is
machine-wide, which is the exact hazard `refreshable`'s doc
comment describes.

**Verifiable:** yes, offline, as a documented sequence with its
preconditions asserted.

### E. Report accurately and document the escape

Keep every refusal exactly as it is. Change only what gale says.

**Changes:**

1. `reportUnresolved` (`migraterun.go:193-214`) splits its list by
   reachability, using machinery migrate already calls:
   `generation.AuthoritativeGenerationDirs` +
   `AuthoritativeClosure`, as in `checkNothingReaches`
   (`migraterun.go:549-583`). A bare directory no scope's active
   closure reaches is named as a `gale gc` candidate — true today,
   and currently unsaid. A reached one keeps the honest "no
   command converges this" line.
2. For the reached case, name option D's sequence **with its
   preconditions**: one scope, a declared root, and the warning
   that a dependency or a second scope makes it unsafe. Naming a
   sequence with its preconditions is different from naming one
   that converges nothing, which is what the current comment
   rejects.
3. A short section in `docs/revisions.md`, beside "Soft
   migration", describing the state and the escape.
4. The §13 amendment in §6 below.

**Risk:** the reached-and-multi-scope case still has no command.
That is stated rather than fixed.

**Cost:** tier 0-1.

**Verifiable:** entirely, offline. `migratewiring_test.go:738-758`
already pins the current message and becomes the red test.

## 5. Recommendation

**Take E. Hold C behind a report from a real machine.**

The reasoning is cost against population, and both sides are
unusually clear here. The population is bounded by a 26-day window
in the project's first month, further cut by unlocked sync's soft
migration, with no reported instance. The cost of C is tier-3 work
in the farm and generation subsystems — the area
`CLAUDE.md` names as most regression-prone — whose central
correctness property cannot be exercised by the test suite at all,
and whose most likely outcome on a real multi-machine setup is a
correct refusal rather than a convergence.

Meanwhile a measurable share of what gh#200 reports today is not
even a trap: an unreferenced bare directory is a `gale gc`
candidate, and the report says there is no remedy. Fixing that is
a message change, and it may empty the bucket entirely.

E is the small answer. It is also the correct one: it makes gale
tell the truth about three distinct states instead of one, gives
the two states that have escapes their escapes, and leaves the
third documented rather than silently unimplemented. If the
maintainer finds a real reached-and-multi-scope directory on a
real machine, C is designed above and ready to build; until then
it would be machinery in the highest-risk subsystem serving a
population that may be zero.

## 6. Does §13 need amending?

**Yes — one paragraph, and a small one.**

Not because a new mechanism is being authorized. Because §13's
silence about pre-revision *source* directories is what let the
loop in §1 form, and the fix is a documented decision, not code.
The issue is right that this is a §13 gap; it is wrong only in
guessing that the gap needs filling with a mechanism.

Draft amendment, to follow §13's "Source-method packages cannot be
migrated this way, so `migrate` prints the precise list of them
and what rebuilding costs":

> **Pre-revision source directories.** A source-method package
> installed before revisions existed sits in a bare `<version>`
> directory neither command may replace: migrate cannot refetch
> it, and `--refresh` acts on the canonical path alone. This
> section does not extend its exception to cover it. A record
> written beside those bytes would be the unverified marker
> rejected above; and rebuilding into the canonical path is a
> machine-wide relocation whose proposed hash cannot be known
> before the build, so the clearance this section requires before
> a destructive commit cannot run in the order the binary case
> uses.
>
> Migrate therefore reports these directories and says what is
> true of each. One that no scope's active closure reaches is a
> `gale gc` candidate. One that a single scope reaches, as a
> declared root, converges by removing the package and installing
> it again in that scope: the store directory is derived state,
> the manifest pin and the lockfile survive, and the reinstall
> commits at the canonical path. One reached by more than one
> scope, or reached only as a dependency, has no safe per-scope
> sequence; gale says so rather than naming one.
>
> Silence about that last case is what this paragraph replaces. A
> machine-wide rebuild relocation remains available as a future
> extension of `gale migrate`, on the same enumerate-clear-replace
> order, with the ordering caveat above; it is not authorized
> here.

That amendment is honest under either recommendation: it describes
the decided state today and names C as an extension rather than
smuggling it in.

## 7. Relationship to gh#191's proposal

**Independent. No free convergence — and the sibling proposal
overstates this in one line.**

`content-addressed-store.md` §5 lists, among phase 2's work:
"`reportUnresolved` (`migraterun.go:186-211`) loses one of its two
cases, since a source-built pre-revision directory now has
somewhere to land." That claim does not hold, and the reason is
worth stating before both documents are read as consistent.

Phase 2 mints a tagged sibling **on divergence from an occupied
canonical directory** — `commitStaged` chooses the sibling when
the staged hash differs from what the canonical directory records.
A pre-revision bare directory's canonical path is **absent**.
There is no occupant, no record, and therefore no divergence to
detect: the bytes already have somewhere to land, and always did.

The two things that actually block #200 are untouched by tagged
siblings:

- **Producing canonical bytes for a source package.** Requires a
  build. A content tag names bytes after they exist; it does not
  make them.
- **Relocating every scope off the bare path.** One farm link per
  soname (`internal/farm/farm.go:257-278`), machine-wide, however
  many store paths exist. #191's own §4 concedes this — "phase 3
  is not shippable without #198 at all" — and siblings make farm
  contention worse, not better.

Where the two proposals genuinely touch is smaller and runs the
other way: #191's phase-2 selection work would have to keep
`store.ResolveDir`'s bare-to-canonical fallback
(`store.go:117-196`) intact, since it is what lets a pre-revision
reference float onto the canonical sibling once one exists. That
is a constraint #200 places on #191, not help flowing back.

If both land, C (if ever built) and #191 phase 2 share
`migrateOne`'s shape-dispatch and `finishRelocations`. That is
reuse, not subsumption. Recommend striking the `reportUnresolved`
bullet from #191's §5, or restating it as "the canonical
source-directory case only".

## 8. Testability

**Red-green, offline, in the agent container:**

- The reachability split in `reportUnresolved`. Fixtures exist:
  `migrateMachine`, `seedStore`, `writeDepsMeta`, and
  `generation.Build` in `cmd/gale/migratewiring_test.go` seed a
  bare directory, a scope that links it, and a scope that does
  not.
- The message itself.
  `TestRunMigrateReportsWhatItCannotConverge`
  (`migratewiring_test.go:738-758`) already asserts the current
  `gh#200` string and is the red test for any change to it.
- `lockRoot`'s remedy text (`cmd/gale/lock.go:426-442`), which is
  string-asserted the same way.
- That gc reaps an unreferenced bare directory and retains a
  referenced one — `cmd/gale/gc_test.go` fixtures, no network.
- For option C, were it built: classification, candidate
  ordering, the preflight refusal, the resume branch, and the
  post-pass removal ordering. All of migrate's existing tests run
  against stubbed recipes and seeded store directories.

**Requires a real source build, and therefore cannot run here:**

- That a rebuilt source artifact actually attests — that
  `recordProvenance` writes a record whose graph digest recomputes
  against the closure on disk, for a package built now rather than
  seeded by a fixture.
- That the rebuilt artifact's hash clears every scope, and that a
  committed foreign lock vetoes it as §3 predicts.
- The farm rebuild across a real multi-soname package.

The container blocks `gale build`, `gale install` and `gale sync`
through a PreToolUse hook, and egress to upstream source hosts is
blocked regardless. Migrate's existing suite works around this by
seeding the store and stubbing recipes, which is legitimate for
ordering and refusal logic and is **not** evidence about a real
build. Stated plainly: option C's central property is not
demonstrable by the test suite. It is demonstrable only by the
maintainer, on a machine that has the directory — which is also
the machine that would tell us whether the directory exists at
all.

## 9. Open questions

1. **Does such a directory exist?** The cheapest possible answer,
   on each machine that has run gale since April 2026:
   ```sh
   find ~/.gale/pkg -mindepth 2 -maxdepth 2 -type d \
     ! -name '*-[0-9]*' -exec test ! -e {}/.gale-provenance.toml \; -print
   ```
   Zero across the maintainer's machines makes E's message change
   the entire fix and retires C without further design.
2. **Does the §13 ordering argument in §4C hold in the
   maintainer's reading?** "Fail BEFORE replacing" is satisfied
   for the destructive steps under the proposed order, since the
   build itself destroys nothing. If §13 is meant to forbid any
   *build* before the machine is cleared, C is not merely
   expensive but out of bounds, and the amendment in §6 should say
   so instead of naming it as an extension.
3. **Should the reachability split live in migrate or in gc?**
   Migrate already walks scope closures; gc already owns the
   remedy. Reporting it from both would be two derivations of one
   fact, which this codebase avoids on principle.
4. **Is a bare *dependency* directory reachable in practice?** A
   dependent's `.gale-deps.toml` names a version, and the closure
   walk resolves it through `ResolveDir`, so it floats onto a
   canonical sibling once one exists. If it never does, the
   dependency case may be entirely orphan-shaped — which would
   make E's split cover it completely.
5. **Should `gale gc` report pre-revision leftovers explicitly**,
   so a user gets positive confirmation the state is gone rather
   than inferring it from silence?
6. **Is the #191 §5 bullet worth a review comment on PR #218**, or
   should it be corrected when whichever proposal lands second is
   revised?
