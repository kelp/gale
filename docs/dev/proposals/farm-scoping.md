# Scoping the Shared Dylib Farm

Status: proposal (2026-08-12)
Issue: gh#198
Scope: `internal/farm`, `internal/generation`;
`internal/build/fixup_*` read-only
Verdict: **do not build a scoped farm.** It is not
reachable from a shipped artifact, and the thing that
makes it unreachable is the same fixed rpath depth that
killed flat content-addressing in gh#191.

## 1. Problem

`~/.gale/lib/` is one flat directory of symlinks keyed
by library basename. `farm.Populate`
(`internal/farm/farm.go:257-328`) links
`<farmDir>/<soname>` → `<storeDir>/lib/<soname>`, one
link per soname for the whole machine. One soname maps
to exactly one target, so two scopes needing different
targets for one soname cannot both be satisfied.

### 1a. Why the farm is machine-wide and not a choice

`farm.DirFromStoreRoot` (`farm.go:71-73`) returns
`filepath.Dir(storeRoot) + "/lib"`, and
`farm.DirFromStoreDir` (`farm.go:79-85`) re-derives the
same directory from any store dir. Its doc comment
already records why there is deliberately no accessor
taking a gale dir: a project-scoped rebuild pointing at
`<project>/.gale/lib` writes to a directory **nothing
resolves through**.

The reason is in `internal/build`:

- `relativeFarmRpathLinux` (`fixup_linux.go:226-231`)
  emits `$ORIGIN/` + `../` × (3 + depth) + `lib`.
- `relativeFarmRpath` (`fixup_darwin.go:399-417`) emits
  the same shape anchored on `@executable_path` or
  `@loader_path`.

The constant `3` is `pkg/<name>/<version-revision>`. For
`bin/exe` the depth is 1, so the emitted rpath is four
levels up: from `<galeDir>/pkg/<n>/<v-r>/bin` to
`<galeDir>`, then `lib`.

That path is resolved by the dynamic loader **relative
to the loaded object's own location**. Not the process,
not the scope, not the environment, not the active
generation. Therefore:

> The farm's identity is a function of the store path of
> the binary that loads through it. The farm can only be
> scoped as finely as the store is.

Everything in §3 follows from that one sentence.

### 1b. When two scopes genuinely need different targets

Four conditions must hold at once, and each one removes
a large slice of the population.

1. **Both scopes reach a package that ships versioned
   dylibs.** gale's linking policy prefers static for
   CLI tools (`design.md` "Static Linking"), so most
   packages contribute nothing to the farm at all. The
   contended set is small and known: openssl, libgit2,
   pcre2, oniguruma, curl, icu.
2. **At different versions, not different revisions.**
   Revision divergence is already collapsed:
   `farmStoreDirs` resolves each recorded dep by bare
   version so the store returns the highest installed
   revision (`generation.go:313-328`, gh#172), and
   `scopeClosureDirs` → `store.ResolveDir` floats the
   same way for lock-derived claims. Two scopes at
   `openssl@3.5.0-1` and `openssl@3.5.0-2` never
   collide.
3. **The two versions carry the same soname.** If they
   do not — `libssl.1.1.dylib` vs `libssl.3.dylib` —
   the flat farm holds both, because they are different
   keys. gale-recipes stages major ABI breaks as
   separate recipes for exactly this reason. The only
   casualty is the unversioned alias `libssl.dylib`,
   which `partitionAliases` (`farm.go:375-410`) drops
   for both providers without an error (gh#199).
4. **The user's lock names bytes rather than an ABI.**
   Where condition 3 holds, the two versions are in one
   ABI family *by the soname's own promise* — the
   promise the farm exists to exploit
   (`docs/revisions.md:95-104`). A binary linked
   against `openssl@3.0.1` loading `openssl@3.5.0`'s
   `libssl.3.dylib` is the designed behavior, not a
   defect.

So the genuine population of gh#198 is: **two scopes
diverging on the version of one soname family.** It is
not rare in the sense of impossible — a project synced
six months ago and never resynced pins an older version,
and that is ordinary — but it is bounded by condition 1
to a handful of packages, and for almost all of it the
right answer is *not* two farms. It is one farm holding
the highest member of the family, which is what the farm
already does inside a single scope.

The residue that genuinely needs distinct targets is
smaller still: upstream broke its own soname promise (a
recipe bug), or the user wants byte-identity rather than
ABI-compatibility (a **store** problem, gh#191, not a
farm problem).

**This changes the cost/benefit.** Scoping the farm is
the most expensive option available and it is aimed at
the smallest slice.

### 1c. What the state actually looks like today

The conflicting state is not merely broken — it is
**unreachable**. The per-commit guard runs at install
(`cmd/gale/context.go:209-215`), so the second scope's
`gale install openssl@3.0.1` is refused before the
store gains a second version. A user cannot get two
farm-visible versions of one package onto a machine
through gale at all.

## 2. What the gh#182/gh#195 guard does and does not buy

Bought, and worth keeping:

- No silent overwrite. `GuardPopulate`
  (`guard.go:123-139`) and `GuardRebuild`
  (`guard.go:250-287`) check the resulting farm mapping
  against every claimant, not the verb.
- Deletion is guarded as strictly as retargeting —
  `GuardDepopulate` (`:148`), `GuardRemoveLinks`
  (`:209`), `GuardStoreRemoval` (`:562`) — so a `gc` or
  `remove` cannot pull a link another scope loads
  through, nor delete the directory behind it
  (`guardClaimedDir`, `:574`).
- `GuardRebuild` returns the **union** of proposed and
  claimed dirs, which is what makes the
  wipe-and-recreate rebuild non-destructive and lets any
  scope's rebuild repair another scope's missing link.
- Refusals name both identities (`identityOf`, `:528`),
  so the message is actionable by a human.

Not bought:

- **The case still does not work.** Data loss became an
  error. Both scopes are now wedged instead of one being
  silently corrupted.
- **The refusal is version-exact, not ABI-exact.**
  `placedSonameTargets` (`:329-354`) builds the claim
  target as `filepath.Join(p.FinalDir, "lib", soname)` —
  the full version-revision path — and `GuardRebuild`
  refuses on any inequality (`:260-270`). It therefore
  refuses divergences the soname promise makes harmless,
  which is the entire population identified in §1b.
- **The guard is stricter than the mechanism it
  guards.** `Populate` already overwrites a farm entry
  when the existing target belongs to the *same* package
  at a different version (`farm.go:294-298`), and
  `CheckDrift` deliberately does not report that case
  (`farm.go:623-636`, the `pkgPrefix` test). Two of
  three sites tolerate same-package divergence; the
  guard does not.
- **The claimant set is a model, not the machine.**
  `farmclaims.go:26-29` states the limit: unregistered
  projects and already-open shells are invisible. No
  amount of guard strictness closes that.

## 3. Options

### Option 0 — nothing beyond the guard

Document the configuration as unsupported (already done
at `revisions.md:130-142`), improve the refusal message,
and add a `gale doctor` check that names the two scopes
and the soname before the user hits it mid-install.

- **Rpath:** untouched.
- **Migration:** none.
- **#184:** none — the guard's position relative to the
  swap is unchanged.
- **Cost:** near zero. **Buys:** nothing for the case.

### Option 1 — per-generation farm (`gen/<N>/lib`)

Give each generation its own farm and rebuild it into
the generation directory.

- **Rpath:** fatal, twice over.
  1. A store binary's rpath resolves to
     `<galeDir>/lib`. There is no spelling of
     `../../../..` from a store path that names a
     generation number, because the store dir does not
     know which generation links it. Pointing at
     `<galeDir>/current/lib` instead is expressible but
     names the **global** `current`; a project's
     binaries live in the shared global store, so they
     would resolve the global scope's farm, not the
     project's. The option does not deliver
     cross-project scoping at all.
  2. `gen/<N>/lib` is already occupied.
     `populateGeneration` mirrors every store subdir
     except `src`, `api`, `pkg`, `doc`, `misc`
     (`generation.go:536-542`), so `lib/` in
     a generation holds the packages' own lib trees. A
     farm there merges with or displaces them.
- **What it *would* buy** (and this is worth recording):
  temporal isolation. A retained generation keeps the
  farm it was built with, so rollback and late `dlopen`
  by a pre-swap process stay correct. That is gh#184's
  concern, and gh#184 solves it more cheaply with
  staging + per-name rename.
- **Migration:** flag day (see §5). **Reject.**

### Option 2 — per-closure farm (`~/.gale/lib/<hash>/`)

Key the farm directory on a hash of the closure.

- **Rpath:** the hash must appear in the baked rpath, so
  it must be known at build time. The build-time closure
  *is* known — it is `.gale-deps.toml`. But then a dep
  revision bump changes the hash and the dependent's
  baked path stops existing, so the dependent must be
  rebuilt. That destroys the farm's entire reason to
  exist (`revisions.md:95-104`): absorbing
  SONAME-compatible dep upgrades **without** a rebuild.
  This option is Nix, and Nix does not have a farm
  because it does not need one.
- **Migration:** flag day, plus a permanent regression
  in upgrade cost. **Reject.**

### Option 3 — keep one farm, make rpaths scope-aware

Resolve the farm through something the environment
supplies: `LD_LIBRARY_PATH` / `DYLD_LIBRARY_PATH` set by
`gale env` / the direnv hook, or an `@rpath` filled in
per scope.

- **Rpath:** on macOS this is dead on arrival.
  `DYLD_*` variables are purged when a
  SIP-protected/platform binary is executed, and the
  purge propagates: a hook evaluated by the system
  `/bin/zsh` loses the variable for every descendant.
  gale is macOS-first. Linux `LD_LIBRARY_PATH` would
  work but is a global override that leaks into every
  child process, including non-gale ones.
- It also inverts the invariant `relocatable-binaries.md`
  was written to establish: the artifact stops being
  self-sufficient and starts depending on ambient
  environment for correct loading.
- **Migration:** none needed, which is its only
  attraction. **Reject** on macOS infeasibility.

### Option 4 — per-scope store root

Give a conflicting scope its own store root at
`<project>/.gale/pkg/`, materialized (copied or cloned)
from the shared store. Its farm is then
`<project>/.gale/lib` by `DirFromStoreRoot`, with **no
rpath change at all**: a binary at
`<project>/.gale/pkg/<n>/<v-r>/bin/exe` resolves four
levels up to `<project>/.gale`, then `lib`. The
three-level depth is preserved — the same reason gh#191's
sharded variant (`A1s`) survived the rpath objection.

- **Rpath:** unaffected. No rebuild, no republish. The
  artifact is already path-independent by design
  (`relocatable-binaries.md`), so the copy is a copy,
  not a rebuild; only text-file prefix fixups
  (`RestorePrefixPlaceholderTo`, `.pc`/`.la`/scripts)
  need re-running at the new prefix.
- **Migration:** none on upgrade day; the spill happens
  only on conflict.
- **Cost:** large and structural. "The store is shared"
  is load-bearing in `gc` retention, `storeRetentionKey`,
  `projects`, the store-rooted generation lock
  (`generation.go:378-380`), and the whole of pipeline 5
  in `change-discipline.md`. Duplicated bytes per
  conflicting closure. This is a multi-PR tier-3
  project.
- **#184:** each scope stages and publishes its own
  farm; `Staged` is already parameterised on `farmDir`,
  so the mechanism composes unchanged.
- **Verdict:** the *only* option that actually scopes
  the farm without a flag day. Correctly deferred, not
  rejected.

### Option 5 — soname-compatible tolerance in the guard

Not scoping. Make the population of §1b work by aligning
the guard with the mechanism it guards.

When two claimants require one soname from different
store dirs of the **same package name**, do not refuse:
resolve to the highest version-revision
(`internal/version.KeyNewer`) and report it. Different
package names stay a hard error — that is a recipe bug
and `Populate` already treats it as one
(`farm.go:288-293`).

Highest, not arbitrary: a binary linked against 3.5.0
loading 3.0.1's `libssl.3.dylib` can hit a missing
symbol, while the reverse is what the soname promises.
The direction matters and must be pinned by a test.

- **Rpath:** untouched. No rebuild, no republish.
- **Migration:** none. It only *accepts* states currently
  refused.
- **Prerequisite (phase 0).** `Populate`'s same-package
  overwrite is **last-writer-wins**, not
  highest-wins: `farm.go:294-298` removes and re-links
  whatever the loop reaches last, and `Rebuild`
  (`farm.go:518-522`) feeds it the BFS order of
  `farmStoreDirs`. That is unreachable today only
  *because* the guard refuses first. Relaxing the guard
  exposes it, and it is exactly the 940a67a class:
  order-dependent output. Phase 0 makes `Populate`
  order-independent and version-ordered, with a
  shuffled-input test. **Phase 0 is worth landing on its
  own merits even if nothing else here is accepted.**
- **#184:** `Stage` calls `Populate`
  (branch `claude/issue-184-farm-staged-publish`), so
  phase 0's determinism is inherited for free. Phase 1
  touches `guard.go` only, which #184 does not.
- **Cost:** small — a comparison predicate plus phase 0.
  **Risk:** it lets a scope load a version its lock does
  not name (see §8 Q2), and it widens the set of store
  states `gc`, staleness and `.gale-deps.toml` must
  tolerate.

## 4. Recommendation

**Do not scope the farm. Land Option 5, phases 0 then 1,
and only after gh#184 and gh#194 have landed. Keep
Option 4 as the named route to real scoping, deferred
behind a trigger. Re-scope gh#198 itself.**

Decisively, in order:

1. **gh#198 as titled is not buildable.** "Scope the
   farm to a generation or closure" asks for a farm
   addressed by something the dynamic loader does not
   know. Options 1, 2 and 3 each fail on that, not on
   effort.
2. **The farm can only be scoped by scoping the store**
   (§1a). That makes Option 4, or gh#191 phase 3, the
   real mechanism — and both are store work, not farm
   work.
3. **The guard is sufficient for now**, and Option 5 is
   the cheap correction that makes the common divergence
   work rather than wedge. It is a policy change, so it
   belongs *after* gh#194 separates policy from
   observation. Adding a fifth hand-maintained coherence
   rule to `guard.go` before that refactor is precisely
   how gh#194's defects 1–6 were produced.
4. **If only one thing lands, land phase 0** (the
   `Populate` determinism fix) and the `gale doctor`
   diagnosis from Option 0. Both are cheap and neither
   changes what gale accepts.

**Revisit trigger for Option 4 / gh#191 phase 3:** a user
reports a conflict where the two versions carry the same
soname *and* the older one must be loaded as bytes — i.e.
a case §1b's condition 4 does not cover. Until then the
duplication cost is not justified.

## 5. The rpath consequence, in full

This section decides feasibility, so it states the cost
plainly rather than burying it.

### The recommendation forces no rebuild

Option 5 and Option 4 both leave
`fixup_linux.go:226-231` and `fixup_darwin.go:399-417`
untouched. **No artifact is rebuilt, no attestation is
reissued, no user reinstalls anything.** Option 4 works
*because* it preserves the three-level store depth; the
farm moves only because the store root moved with it, and
the emitted rpath string is unchanged.

### What Options 1, 2 and 3 would cost

Any farm at a path other than `parent(storeRoot)/lib`
changes what the baked rpath resolves to. That is a flag
day, and its shape is worse than "rebuild everything":

1. **Rebuild and re-attest the whole catalog.** Every
   gale-recipes recipe shipping a dynamically linked
   artifact needs a `[package] revision` bump so CI
   republishes to GHCR with the new rpath. Revision
   bumps cascade to dependents with bare runtime deps.
2. **Every user reinstalls every dynamically linked
   package.** An installed artifact is immutable and
   install no longer rewrites rpaths (gale 0.16.3+), so
   there is no retrofit. `revisions.md:149-157` already
   records this for the pre-farm prebuilts: "only a
   rebuild embeds new rpaths into an existing binary."
3. **The flat farm can never be retired.** Every
   artifact built before the flag day keeps looking at
   `<galeDir>/lib` for as long as it stays installed —
   forever, for anyone who does not reinstall. So the
   end state is not "the farm moved"; it is **two farms
   maintained in parallel indefinitely**, with the
   claimant guard having to reason about both. That is a
   permanent complexity increase, not a migration.
4. **The migration itself is unguarded.** Between the
   catalog rebuild and a user's reinstall, that user's
   old binaries and new binaries want different farms.

Point 3 is the one that makes these options unacceptable
rather than merely expensive.

### One test worth adding regardless

Pin the **emitted rpath string** — not the depth
constant, the string — for `bin/exe` and for a nested
`libexec/.../exe` on both platforms. It is a
characterization test for a published binary contract
that currently has no guard at all. gh#191's proposal
asks for the same test (`§7`); it should be written
once, in `internal/build`, by whichever lands first.

## 6. Composition with gh#184, gh#194 and gh#191 phase 3

Three changes are converging on the most
regression-prone code in the repo. Ordering:

```
#184 (Stage/Publish)  →  #194 (view + observations)
   →  #198 phase 0 (Populate determinism)
   →  #198 phase 1 (tolerance)          [optional]
   →  #191 phase 3 / Option 4           [triggered]
```

### gh#184 — staged publish

`Rebuild` becomes `Stage` + `Publish` with per-name
atomic renames, and `Stage` runs before the current-swap
while `Publish` runs after
(`claude/issue-184-farm-staged-publish`).

- **This proposal adds nothing #184 must accommodate.**
  Phase 0 lives inside `Populate`, which `Stage` calls;
  phase 1 lives in `guard.go`, which #184 does not
  touch.
- **What #184 must not do:** put the soname→target
  comparison into `publishOne`. It currently compares
  `os.Readlink` equality, which is *identity*, not
  policy — correct, and it must stay that. A policy test
  there would be a second place to keep coherent, the
  gh#194 mistake.
- **What #184 must not do:** narrow the store-dir set
  `Stage` receives. `pruneUnstaged` makes the staged
  image authoritative over the whole farm directory, so
  the union returned by `GuardRebuild`
  (`guard.go:274-285`) becomes *more* load-bearing, not
  less: a shrunk union now actively prunes another
  scope's link rather than merely failing to restore it.
- #184 keeping `[]string` rather than a typed
  `Placement` is right for this proposal too. Nothing
  here needs `Stage` to know about placements.

### gh#194 — one proposed store view, de-booleaned walk

- **Phase 1 must land after gh#194**, and is much
  cheaper there: a tolerant comparison is a *policy at
  the call site*, which is exactly the shape gh#194's
  observation/policy split creates.
- **What gh#194 must not do:** collapse the claim into a
  boolean or a set of soname names. The target **path**
  must remain available at the call site, or a tolerant
  comparison cannot be expressed and phase 1 becomes a
  refactor of gh#194 instead of a policy addition.
- **What gh#194 must not do:** drop `Claimant.Label`
  from the observations. Refusals must stay attributable
  to a named scope; that text is the only remedy the
  user gets.

### gh#191 phase 3 — content-addressed store paths

The sibling proposal (`content-addressed-store.md`,
PR #218) states that gh#198 must land before its phase 3.
**This proposal partly inverts that dependency, and the
maintainer should see the inversion.**

- For *scoping*, the dependency runs the other way.
  Per-closure store paths are the mechanism by which the
  farm becomes scopeable (§1a); a scoped farm is not a
  prerequisite for them, it is a consequence.
- For *phase 2*, gh#191's own analysis is right and this
  proposal's Option 5 does **not** rescue it. A diverged
  sibling is the same package at the *same*
  version-revision with different bytes. Phase 1's
  tie-break is `version.KeyNewer`, which is not a total
  order over equal identities — it cannot pick. So a
  diverged sibling that provides farmed sonames still
  reaches a refusal.
- Consequence for gh#191: phase 2 must either restrict
  divergence to farm-invisible packages (the static-CLI
  majority) or accept the refusal for farmed ones. It
  cannot rely on gh#198 landing first to fix it, because
  the fix gh#198 can afford does not reach that case.

## 7. Testability

**Red-green in Go, on Linux CI:**

- Phase 0: `Populate` over shuffled `activeStoreDirs`
  containing two dirs of one package, asserting the same
  winner every time and that the winner is the higher
  version-revision. The 940a67a rule makes this
  mandatory, not optional.
- Phase 1 policy: `GuardRebuild` / `GuardPopulate` with
  two claimants at same-package-different-version
  (allowed, highest wins) and
  different-package-same-soname (still
  `ErrClaimConflict`). Existing `guard_test.go` fixtures
  build fake `lib/` trees on a temp dir; no change of
  approach needed.
- The union `GuardRebuild` returns must still contain
  the loser's dir, or `gc` will collect a directory a
  scope still claims.
- `version.KeyNewer` ordering over
  `3.0.1-2` / `3.5.0-1` / `3.10.0-1`, so the natural-order
  comparison is pinned at the boundary that uses it.

**`cmd/gale` layer (required by
`scripts/check-pipeline-tests.sh`):** two scopes with
divergent farm-visible versions, asserting the second
scope's install is refused today and succeeds after
phase 1, with the farm link naming the higher identity.
`internal/farm` tests alone do not discharge this — the
guard's claimant set only exists at the `cmd/gale` seam.

**macOS CI only:**

- Everything about unversioned aliases.
  `isUnversionedAlias` returns false unless
  `runtime.GOOS == "darwin"` (`farm.go:135-141`), so on
  Linux `partitionAliases`, `linkAliases` and every
  alias consequence of a tolerance are structurally
  unobservable. A tolerance that changes which store dirs
  are in the closure changes which aliases are contested
  — and that half is invisible to Linux CI.
- Any real-Mach-O rpath assertion, and `install_name_tool`
  behavior. `just check-darwin` proves compilation and
  lint only.
- Note the mutation-check precedent from PR #216: a green
  `go test ./...` cannot distinguish "ran" from
  "skipped" for darwin-gated tests. If phase 1 lands,
  its darwin tests need the same treatment.

**Not observable in a Go test at all:**

- Whether `dyld`/`ld.so` actually loads the tolerated
  sibling correctly. That needs two real openssl builds
  and a real dependent on a real machine. "Runs on the
  build host" is not evidence here either.
- Already-open shells and unregistered projects
  (`farmclaims.go:26-29`). The claimant set is a model of
  the machine; no test can make the model true, and this
  proposal does not improve it.
- Late `dlopen` timing. gh#184's `beforeFarmPublish`
  seam is the only handle on that interval, and it
  observes ordering, not a real loader.
- The flag-day cost in §5. Nothing in CI can measure
  "every user reinstalls everything"; it is a judgement,
  which is why it is stated as one.

## 8. Open questions for the maintainer

1. **Have you actually hit the divergent-version case,
   or is it hypothetical?** §1b argues the genuine
   population is small and bounded by the static-linking
   policy. If you have never seen it, Option 0 alone is
   the right answer and phase 1 should not be built.
2. **Does a lock pinning `openssl@3.0.1` promise bytes
   or ABI?** Revision already floats (gh#172). Phase 1
   extends the float to version within one soname
   family. That is a product decision, not an
   implementation detail, and it is the single thing
   that makes phase 1 acceptable or not.
3. **Should the tolerance be silent, warn, or opt-in?**
   `Populate` prints a `farm: updated …` line for
   same-package replacement today. A cross-scope float
   is a bigger claim and probably deserves a `gale
   doctor` line naming both scopes rather than only a
   stderr line during a sync.
4. **Is per-scope store duplication (Option 4)
   acceptable in principle?** "The store is shared" is
   load-bearing across gc retention, the store-rooted
   generation lock and pipeline 5. If the answer is no,
   Option 4 should be struck and gh#198's scoping ask
   folded entirely into gh#191 phase 3.
5. **Should gh#198 be re-scoped?** Its title asks for
   something §1a shows is unreachable. Suggested
   re-framing: "make farm contention survivable" for the
   part that is buildable now, with the scoping ask moved
   onto gh#191 phase 3 as its acceptance criterion.
6. **Are you willing to hold this behind gh#184 and
   gh#194?** That is the ordering this proposal
   recommends, and it means nothing here lands for two
   PRs. The alternative — phase 1 first — puts a policy
   change into `guard.go` in exactly the state gh#194
   exists to fix.
