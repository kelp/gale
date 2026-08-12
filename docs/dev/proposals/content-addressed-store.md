# Content-Addressed Store Paths

Proposal for gh#191. Not implemented. Written against
`6d81908` (post-#213). Line references are to that commit.

Related: gh#211 (stale reinstalls replace referenced
directories), gh#198 (scope the dylib farm), gh#183 (closed
in part by #213), `docs/dev/design.md:77-93`,
`docs/revisions.md`.

## 1. Problem

The store is keyed `<name>/<version>-<revision>/`. The path
is computed from the recipe before any bytes exist:

- `internal/installer/installer.go:317-319` —
  `storeVersion := r.Package.Full()`, then
  `canonicalDir := filepath.Join(inst.Store.Root, name,
  storeVersion)`.
- `internal/recipe/recipe.go:148` — `Full()` is
  `fmt.Sprintf("%s-%d", p.Version, rev)`. Nothing about the
  artifact enters it.
- `internal/store/store.go:52-58` — `Create` joins the same
  three components.

One identity therefore addresses exactly one directory. That
is policy — upstream artifacts are immutable, a change costs
a version or revision bump — and there is no mechanism
behind it: `replaceStoreDir`
(`internal/installer/installer.go:969`) renames new bytes
over the old pathname whenever a caller asks it to.

Three legitimate ways to end up with two byte-sets for one
identity, none of them a policy violation.

### 1a. Source builds are not byte-reproducible

`docs/troubleshooting.md:92-99` enumerates the causes and
states they are unfixable without Nix-level isolation:
Mach-O `LC_UUID`, libtool `.la` files carrying build-temp
absolute paths, pkg-config `.pc` files carrying the build
prefix, ar/ranlib timestamps in `.a` archives.

So two machines building `jq@1.8.1-1` from the same recipe
produce different bytes and record different hashes.
`internal/build/build.go:224-237` hashes the archive it just
produced; that hash lands in
`.gale-provenance.toml:sha256`
(`internal/provenance/provenance.go:54-65`) and in the
lockfile artifact (`internal/lockfile/v1.go:61-68`). Two
lockfiles committed from two machines disagree on `sha256`
for one node key, and both land on a third machine.

### 1b. `--recipes` / `--recipe` point one identity at
different bytes

`cmd/gale/recipes.go:67-74` resolves
`<recipesDir>/<letter>/<name>.toml` off the working tree.
Editing a recipe's `[build]` steps without touching
`version` or `revision` — the ordinary inner loop in
`../gale-recipes` — asks for the same identity with
different build instructions. `cmd/gale/build.go:43-47`
auto-detects a recipes repo and does the same without a
flag.

The failure is worse than a collision: at
`internal/installer/installer.go:354` an occupied canonical
dir short-circuits to `MethodCached` before anything is
built, so the edited recipe silently installs the *previous*
recipe's bytes. `installLocalLocked` has the same shape at
`:602-610`, mitigated for `--path` by #213's content-keyed
version but not for `--recipes`.

### 1c. A retagged upstream or a re-pushed GHCR tag

Source archives are hash-pinned
(`internal/build/build.go:1380-1382`) and binaries carry
both `sha256` and `manifest_digest`, so a retag is *detected*
rather than absorbed — the install fails with
`download.ErrSHA256Mismatch`
(`internal/installer/installer.go:455-466`).

The divergence lands one step later. When gale-recipes
updates the declared hash without a revision bump — the
correct response to a benign re-push, since the version did
not change — machine A holds the pre-retag bytes for
`x@1.2.3-1` and machine B holds the post-retag bytes. Both
are canonical, both attest themselves, and neither can be
converged without a revision bump nobody wants to spend.

## 2. What breaks today

**Rollback executes bytes the generation never described.**
`internal/generation/history.go:137-193` resolves a target
generation by *number*, reads its symlink targets
(`genVersions`, `generation.go:94-147`), swaps `current`,
and rebuilds the farm. It never checks what the store dirs
hold. A stale reinstall in between rewrote
`pkg/<name>/<v>-<r>/` at the same pathname, so `gale
rollback 7` restores generation 7's *names* pointing at
generation 9's *bytes*. This is gh#211's stated consequence
and it is live: `guardReplace`
(`installer.go:906-919`) is a no-op unless `ReplaceGuard` is
set, and only `cmd/gale/migraterun.go` and
`cmd/gale/lockrefresh.go` set it, each around one operation.

**A working-tree recipe edit installs the old bytes.** §1b.
The user sees `Installed (cached)` and a binary built from
the recipe they just changed away from. `gale build`
followed by `gale install` is the workaround people
discover; nothing tells them they need it.

**Two projects cannot hold different artifacts for one
identity.** This is the case gh#182 turned into a conflict
error. `gale lock --refresh` moves one scope's bytes and
`cmd/gale/storereplace.go:17-24` refuses when another
scope's lock names a different hash (`errScopeDisagrees`) —
correctly, because the store cannot represent the state, so
the human is asked to pick a winner. Two teammates on two
machines with two committed lockfiles have no winner to
pick.

**`gale audit` reports mismatches that are not actionable.**
`docs/troubleshooting.md:80-99` already tells users a
mismatch is normal. That is the same fact as §1a, surfaced
as advice to ignore an integrity check.

**Provenance disagreement is unrecoverable without a
revision bump.** `gale migrate` classifies a directory as
provenanced or not (`cmd/gale/migrate.go:migrateScan`); it
has no third state for "provenanced, but not the bytes this
scope was cleared against". `reportUnresolved`
(`migraterun.go:186-211`) already documents one such gap
(gh#200).

## 3. Options

Four, in increasing order of what they re-key.

### Option A — full content-addressing (Nix-style)

The store path is derived from a digest of the artifact.
Two sub-shapes, and the difference decides feasibility.

**A1: `pkg/<hash>-<name>-<version>/`** (literal Nix layout).
`internal/build/fixup_linux.go:226-231` and
`internal/build/fixup_darwin.go:399-417` bake
`$ORIGIN/../../../lib` and
`@executable_path/../../../lib` into every dynamically
linked artifact — a hard-coded assumption that a store
prefix is *exactly three* levels below the gale dir
(`pkg/<name>/<ver-rev>`). A1 makes it two, so every prebuilt
binary on GHCR, every attestation, and every installed
dynamically linked package on every user machine would
resolve the farm one directory too high. Migration cost is
"rebuild and re-attest the entire recipe repository, then
force every user to reinstall everything".

**A1s: `pkg/<2hex>/<hash>-<name>-<version>/`** (sharded).
The depth objection does **not** apply. Three components
below the gale dir is exactly what the baked rpath needs, so
a sharded content-addressed store keeps every published
artifact valid. This is the strongest form of the opposing
case and it survives the rpath argument intact.

It does not survive the other two. Sharding moves the
package name out of the path: `farm.packageName`
(`internal/farm/farm.go:646-652`) reads it from
`filepath.Base(filepath.Dir(storeDir))` and would return the
shard `<2hex>`, and `farm.DirFromStoreDir`
(`farm.go:75-86`) validates the layout by checking that the
grandparent is literally `pkg`. Both break for *every*
directory rather than only diverged ones — the §6 reversal
inventory becomes an unconditional rewrite instead of a
conditional one. And the set-selection problem below is
unchanged: sharding decides where a directory sits, not how
a bare pin picks among several.

**A2: `pkg/<name>/<version>+h.<hash>-<revision>/`** — depth
preserved, hash inside the single version component, name
still in the path. The best-shaped full-CA variant. What
changes:

- Path selection moves *after* the bytes exist. Today
  `installLocked` picks the directory at line 319 and
  extracts into it; A2 must always stage and only then
  choose. The staged shape already exists (`force` at
  `:361-370`), so this is a re-ordering, not new machinery.
- The self-reference problem is already solved by accident.
  `build.RestorePrefixPlaceholderTo`
  (`installer.go:1897`) writes the final store path *into*
  the installed files, which would normally make the hash
  depend on itself. But `build.go:202` replaces the prefix
  with `@@GALE_PREFIX@@` before `build.go:224-237` archives
  and hashes it — so gale's existing archive hash is
  already a modulo-self-reference digest. This is the single
  most encouraging fact in the proposal, and it is
  incidental rather than designed, so it needs an explicit
  test before anything depends on it.
- Every path→identity reversal breaks (§6).
- `resolveVersionFromEntries`
  (`store.go:117-193`) needs a second dimension. Today it
  answers "highest revision"; with hash siblings there is no
  ordering answer and it needs an explicit selector.

What breaks — and this is the real objection to full CA, in
every spelling: **every bare pin now addresses a set.** gc
retention keys, farm claimants, `gale list`, `gale which`,
`.gale-deps.toml` dep resolution and the lockfile's
cross-machine portability all need a selection rule that
does not exist today, and they need it for *every* package
rather than for the rare diverged one. Under full CA there
is no unhashed name to fall back to, so the selection rule
is on the critical path of every install on every machine
from the first day it ships.

Migration: every existing store directory is untagged and
stays valid only if the untagged form remains addressable
forever, which means shipping A2 *and* keeping the current
layout — so A2 is really A2-plus-back-compat, the same
bidirectional fallback `docs/revisions.md:78-93` calls
transitional and still carries three years on.

For #211: resolves it. A stale reinstall's new bytes hash
differently, land at a different path, and the old
generation keeps its own.

### Option B — narrow content tag, on divergence only
(recommended)

Keep `<name>/<version>-<revision>/` as the canonical name.
Mint a tagged sibling
`<name>/<version>+h.<12hex>-<revision>/` **only** when a
commit would otherwise replace an occupied canonical
directory with different bytes.

The spelling is not arbitrary. Putting the digest in the
semver build-metadata segment *before* the revision suffix
keeps the existing parsers correct:
`store.HasNumericRevisionSuffix("1.8.1+h.abc123def456-2")`
is true, `store.SplitRevision` returns
`("1.8.1+h.abc123def456", 2)`, and `store.CheckIdentity`
passes. Putting it after the revision would parse as
revision `12345678`.

**The grammar must be stated precisely, because the tag
composes with an existing one.** `devVersionWithDirt`
(`cmd/gale/install.go:516-525`) appends `.dirty.<hex>`
*inside* an existing `+` segment rather than opening a
second one, and `formatDevVersion` already emits
`0.2.0-dev.7+5395b8f` for any non-tagged checkout. So a dev
build's divergent sibling is spelled
`0.2.0-dev.7+5395b8f.dirty.<hex>.h.<hex>-1`, never
`+h.<hex>`. That is the headline case for phase 2 — #213's
documented gap (`installer.go:593-596`) is a dev rebuild
after a build-dep upgrade — so a splitter that matched only
`+h.` would miss its own primary case and fall through to
replacing in place: the exact bug the mechanism exists to
prevent.

The content tag is therefore defined as: **a terminal
`h.<12 lowercase hex>` dot-segment at the end of the
build-metadata segment**, immediately preceding the
`-<revision>` suffix. `+h.<hex>` appears only when no
build-metadata segment exists yet. `SplitContentTag` strips
the revision first, then matches that terminal segment, and
matches nothing else. An upstream version whose own build
metadata happens to end in `.h.` plus twelve hex characters
would alias; the splitter is a store-layout convenience and
`.gale-provenance.toml` remains the authority on what a
directory actually holds.

What changes:

- One new pair in `internal/store`: `TagVersion(canonical,
  digest) string` and `SplitContentTag(base) (canonical,
  digest string)`, implementing the grammar above. Every
  site that reverses a path into an identity (§6) routes
  through the splitter, so a tagged sibling reports the
  canonical identity to gc, the farm, `.gale-deps.toml` and
  the config layer.
- `commitStaged` (`installer.go:844-895`) gains a
  divergence check before `replaceStoreDir`: read the
  occupied directory's `.gale-provenance.toml`, compare to
  the staged artifact's hash, and on disagreement land at
  the tagged sibling instead of replacing.
- A default `ReplaceGuard` becomes possible, because a
  refusal now has somewhere to go.
- **Generation selection**, without which the store change
  does not converge — see §4, finding on forward
  convergence. `canonicalizeForBuild`
  (`cmd/gale/context.go:390-413`) already maps a config pin
  to the on-disk identity a rebuild should link, and it is
  the only such seam (called from `context.go:553` and
  `sync.go:365`). It gains one step: when a tagged sibling
  exists whose provenance hash matches what this scope's
  lock names, substitute the tagged basename. No resolver
  change is needed — a tagged basename carries a numeric
  revision suffix, so `resolveVersionFromEntries` takes the
  exact-match branch (`store.go:184-186`) and returns it
  unchanged.

What breaks: less than A2, because the common case is
byte-identical to today. A machine that never diverges never
sees a tag, and the selection step above is a no-op when no
sibling exists. The cost is concentrated in the §6 reversal
sites and in gc retention, which must keep a tagged sibling
alive as long as *any retained* generation links it — a
larger change than it sounds, see §4.

Migration: no flag day. Existing directories are already in
the canonical form and stay preferred. See §5.

For #211: resolves both halves, without spending a revision,
but only with the generation-selection bullet included. See
§4.

### Option C — status quo plus enforcement

No re-keying. Wire a default `ReplaceGuard` that refuses any
replacement of a directory an existing generation links —
the guard `cmd/gale/install.go:327-354` already implements
for the `--path` case, promoted to `newCmdContext`.

What changes: about 30 lines. What breaks: `gale sync` stops
converging. The comment at `install.go:322-326` says so
outright — a revision bump reinstalls the same identity on
purpose, and refusing that leaves the machine permanently
stale. The only remedy the store can express is a revision
bump per stale reinstall, which is gh#211's stated reason
for not fixing it there: it changes what a revision *means*,
and the `.gale-deps.toml` staleness model
(`docs/revisions.md:159-209`) and gc retention both read it.

A weaker variant — warn at the replace, refuse at rollback —
is gh#211's own interim suggestion and becomes phase 1 of
the recommendation (§4). It is worth shipping regardless of
what else is chosen.

For #211: fixes the rollback half, leaves forward
convergence untouched.

### Option D — key the path by closure digest

Fold `lockgraph.GraphDigest`
(`internal/lockgraph/digest.go`) into the store path instead
of the artifact hash. This is what
`installer.go:593-596` gestures at ("Folding the resolved
build closure into the identity is gh#191's business").

Attractive because the digest is known *before* the build,
so no path re-selection is needed. Rejected on two counts.
First, it re-keys on every dependency change: a bare runtime
dep's revision bump changes the digest and therefore the
path of every transitive dependent, so one openssl bump
mints a fresh directory for a hundred packages that are
byte-identical. Second, it does not address §1a at all — two
non-reproducible builds of one closure share a digest and
still collide. It solves the problem the revision mechanism
already solves and not the one gh#191 names.

## 4. Recommendation

**Build Option B, phased, and do not build phase 3 until a
user hits it.**

Reasons.

Full content-addressing is rejected on **set selection and
permanent back-compat**, not on the rpath depth. The depth
constraint (§3, A1) kills only the flat Nix layout; the
sharded variant A1s preserves it exactly, and A2 preserves
it too. What no full-CA spelling avoids is that a bare pin
stops addressing a directory and starts addressing a set —
for every package, on every machine, from day one — so gc
retention, farm claimants, dep resolution and display all
need a selection rule on the critical path immediately.
Alongside that, the untagged form must stay resolvable
forever for the installed base, which means shipping full CA
*and* keeping the current layout.

The common case must stay byte-identical to today.
`docs/revisions.md:78-93` already carries one transitional
back-compat fallback for a layout change made three years
ago; a second unconditional re-keying doubles the
resolution matrix permanently. Option B adds a case that
only exists on divergence, and divergence is rare on a
single machine.

Most of gh#191's value is in the *first* two phases. The
motivating harm — rollback executing the wrong bytes, an
edited recipe installing the old artifact, a stale reinstall
silently rewriting history — is entirely about *one* machine
replacing *its own* bytes. The cross-machine coexistence
case is real but has no reported instance in the issue
tracker, and it is gated on gh#198 regardless.

**A store change alone does not converge.** Landing
divergent bytes at a sibling without also selecting it
creates an unbounded loop, and the trace is short enough to
state in full. `generation.Build` links through
`resolveStoreDir(storeRoot, name, version)`
(`generation.go:29-31`, `:347`) — name and version only, no
hash. Sync decides staleness from
`Store.StorePath(name, r.Package.Full())`, the canonical
directory (`cmd/gale/sync.go:530-542`). So: divergent bytes
land at the sibling → the rebuilt generation still resolves
the *canonical* directory → that directory's
`.gale-deps.toml` is still stale → the next sync reinstalls
→ §1a non-reproducibility mints a *different* sibling → and
gc, taught to retain only what a generation links, reaps
each orphan as it is made. That is an unbounded
reinstall-and-mint loop, and it is precisely the class
CLAUDE.md records at 013b4a4 / 688ce7d / af4c3f6.

Generation selection therefore belongs in phase 2, not
phase 3. It is cheaper there than it first appears — the
seam already exists (`canonicalizeForBuild`,
`context.go:390-413`) and `store.ResolveDir` already
exact-matches a tagged basename (§3B) — but it is not
optional, and the phasing reflects that.

Phases:

1. **Detect, and refuse where the harm lands.** A default
   `ReplaceGuard` that compares the staged hash against the
   occupied directory's provenance record and warns when
   they differ and a non-active generation links the target.
   Refusing the *replace* is ruled out (§3C: sync stops
   converging), but `gale generations rollback` is where the
   harm actually lands, and refusing there blocks nothing:
   it verifies that the directories a target generation
   links still hold the hashes recorded for it, and requires
   `--force` on a mismatch rather than merely reporting one.
   A warning alone is not enough — gh#205 closes on exactly
   that observation, that "an unresolvable `@rpath` that only
   warns at build time becomes a dyld abort on a user's
   machine, with nothing pointing back at the build". A
   replace-time warning during sync will be read as often as
   that one was. Tier 2.
2. **Land divergent commits at a tagged sibling, and select
   it.** `TagVersion`/`SplitContentTag` in `internal/store`;
   the §6 reversal sites routed through the splitter;
   `commitStaged` choosing the sibling on divergence;
   **`canonicalizeForBuild` selecting the sibling this
   scope's lock names**; gc retention re-keyed onto
   generation symlink targets (below); `gale migrate`
   learning the fourth classification (§5). Tier 3. This is
   what closes gh#211.
3. **Cross-scope coexistence.** Two projects on one machine
   holding *different* siblings of one identity at the same
   time. Phase 2 gives each scope a winner; this makes two
   winners coexist, which the machine-global farm cannot
   express — one soname, one link (`farm.Populate`,
   `farm.go:257-278`). Gated on gh#198 and deferred until a
   concrete report; the trigger is a user hitting
   `errScopeDisagrees` (`cmd/gale/storereplace.go:24`) with
   two lockfiles neither of which is wrong.

**gc retention is the largest single item in phase 2**, and
§6 rates it critical for the right reason. Today's
referenced set is built from config-derived canonical keys
(`gc.go:473-480`), with one generation-derived contribution:
`addActiveGenerationRefs` (`gc.go:416-432`) reads the
**active** generation only. Retaining a sibling for as long
as any *retained* generation links it — which is what
rollback needs — means keying retention on the symlink
targets of every generation gc keeps, not only the current
one. `generation.genVersions` cannot express the answer
either: it returns `map[name]version`, first-seen-wins
(`generation.go:141-143`), so it cannot report that gen 7
links one sibling while gen 9 links another. That is new
machinery, in the subsystem whose cross-scope deletion bugs
recur (ad4e685, 289d13b).

**Relation to #213.** Extended, not subsumed, and not
replaced. `devVersionWithDirt`/`dirtDigest`
(`cmd/gale/install.go:366-525`) key a *version* by *source*
content; this proposal tags a *path* by *artifact* content.
They answer different questions and compose: #213 gives an
uncommitted tree a stable, user-visible name that shows up in
`gale list`, which a post-hoc artifact hash cannot do because
it is not known until after the build. #213's documented gap
— "the identity covers the SOURCE only. Rebuild an unchanged
tree after upgrading a build dependency and this returns the
bytes the old dependency produced"
(`installer.go:593-596`) — is exactly what phase 2 closes,
because the rebuilt artifact hashes differently and lands
beside rather than on top. Leave `devVersionWithDirt` alone;
the composition is a constraint on the *tag grammar* rather
than a change to #213, and §3B states it: a dev build's
sibling is spelled `…+5395b8f.dirty.<hex>.h.<hex>-1`, so the
splitter must match a terminal `.h.<hex>` segment and not
`+h.` alone.

**On gh#211.** Phase 2 resolves it — *including the
generation-selection bullet*, and not without it. With
selection, the stale reinstall gets a distinct path without
spending a revision, `Reinstall` → `installLocked` →
`commitStaged` no longer renames over a pathname an older
generation links, and the next generation links the bytes
that were just built. Both halves of gh#183's third
acceptance criterion then hold in general rather than for
`--path` alone: old generations keep their bytes, and the
scope converges forward.

Phase 1 alone fixes only the first half. It makes rollback
refuse to execute bytes the target generation never
described, which is the sharpest user-visible harm, but the
store still replaces in place and forward convergence is
untouched. If only one phase ships, the doc should say
plainly that gh#211 stays open.

Two caveats. First, it converts #211's cost from a
*revision* cost into a *retention* cost, and that cost is
the phase's largest item (above), not a footnote:
`removeUnreferencedVersions` (`cmd/gale/gc.go:487-528`)
compares `name@<basename>` against canonical keys, so an
un-taught gc reaps every sibling on its first run. Second,
it only holds if the divergence check reads the *committed*
record and not a caller's prediction — the same reason
`ReplaceGuard` takes the committed result rather than a
predicted hash (`installer.go:175-180`). Phase 1 should ship
first precisely so the divergence rate is observable before
phase 2 changes behaviour on it.

**On gh#191's own ask, stated plainly.** The issue's "what a
fix looks like" is two projects on one machine holding
genuinely different artifacts for one identity. That is
phase 3, and this proposal **defers it** behind a named
trigger and behind gh#198. What the recommendation delivers
is gh#211 (phase 2) and the rollback harm (phase 1) — the
single-machine, single-winner half. A maintainer approving
this should understand they are approving a fix for #211
that leaves #191's stated criterion unmet, not a fix for
#191.

**On gh#198's subsumption claim.** #198 is right, and this
proposal makes its case stronger rather than weaker.

The farm is a flat directory keyed by library basename:
`farm.Populate` (`internal/farm/farm.go:257-278`) links
`<farmDir>/<soname>` → `<storeDir>/lib/<soname>`, one link
per soname for the whole machine. Distinct store paths do not
help; they are distinct *targets* for one *link name*.
Worse, the flatness is not a farm implementation detail that
could be changed independently — every dynamically linked
artifact gale ships has `../../../lib` baked into its rpath
(`fixup_linux.go:226-231`, `fixup_darwin.go:399-417`), so the
single shared farm is part of the published binary contract.

Content-addressing raises the pressure on #198: more distinct
store paths per identity means more distinct claimants
competing for the same soname link, so
`farm.GuardPopulate`'s refusal fires *more* often once
siblings exist. Phase 3 is not shippable without #198 at all
— per-scope store selection with a machine-global farm gives
each project the store directory its lock names and then
loads the other project's dylib through the farm.

**Phase 2 is only partly insulated, and the earlier draft of
this section over-claimed it.** Once selection lands (§4), a
diverged sibling is what its scope's generation links, so if
that package provides farmed sonames and another scope still
links the canonical directory, the two claim one link name
and `GuardPopulate` refuses — the #198 conflict, reached
through phase 2. The honest bound: phase 2 improves the case
where the diverged package contributes nothing to the farm
(static CLI tools, the majority) and leaves the farmed case
behaving exactly as it does today, as a refusal. It does not
make the farmed case worse, and it does not fix it.

Sequence: #198 before phase 3, and before phase 2 can claim
to help contended libraries.

## 5. Migration path

The feasibility question. Answered for Option B phase 2.

**Existing store directories: untouched.** Phase 2 mints a
tagged sibling only when a commit diverges from an occupied
canonical directory. On upgrade day nothing diverges, because
nothing is being replaced; the store is bit-for-bit what it
was. There is no rewrite pass, no re-hashing of installed
packages, and no reinstall. Contrast the v0.12.0 revision
rollout (`docs/revisions.md:229-239`), which reinstalled
every pre-revision package on first sync and "on a machine
with 50+ global packages can take a while".

**`gale.lock` files stay valid.** The content tag is
store-local and is **never** written into a lockfile or into
`gale.toml`. This is load-bearing: source builds are not
reproducible across machines (§1a), so a lock naming
`jq@1.8.1+h.abc…-1` would name a path a teammate's machine
cannot produce, and the lock would be unusable rather than
merely stale. Locks keep naming `name@<version>-<revision>`
and keep binding bytes through `artifacts.<platform>.sha256`
(`internal/lockfile/v1.go:61-68`), which is already the right
mechanism — the store just gains the ability to hold two
directories the lock can distinguish by hash instead of one
it cannot.

`gale.toml` likewise never carries a tag.
`configVersionForRecipe` and `storeRetentionKey`
(`cmd/gale/context.go:478-501`) must canonicalize a tagged
basename back through `SplitContentTag` before writing or
comparing.

**Flag day: not required.** Three properties make it
avoidable:

- The canonical name remains the preferred resolution
  target, so a store with no siblings resolves exactly as
  today.
- Tagged siblings are additive; nothing is renamed.
- A sibling is only ever created by a gale that understands
  it.

**Downgrade is the real hazard, and it is one-directional.**
An older gale reading a store containing
`jq/1.8.1+h.abc123-2/`:

- `resolveVersionFromEntries` (`store.go:117-193`) ignores
  it for a bare `1.8.1` request — the prefix scan looks for
  `1.8.1-`, which the tagged name does not carry. Correct by
  accident.
- `store.List` (`store.go:327-366`) *does* return it, and
  `isReferenced` (`gc.go:478-480`) compares
  `name@1.8.1+h.abc123-2` against canonical keys and misses.
  An old `gale gc` therefore deletes tagged siblings that a
  new gale's generations link, breaking PATH.

Mitigation: teach `isTransientStoreEntry`-style skipping in
the *current* release before phase 2 ships, so a gale that
predates the sibling mechanism still declines to reap what it
does not understand. That is a small forward-compatibility
patch, and it must land at least one release ahead. This is
the same discipline as `lockfile.ErrUnknownField`
(`internal/lockfile/v1.go:33-39`), which refuses rather than
silently drops what it cannot model.

**`gale migrate` extension.** The command already has the
right shape and the right unit. `runMigrate`
(`cmd/gale/migraterun.go:28-69`) classifies the whole store,
clears every candidate against every scope, replaces, then
`finishRelocations` (`:81-129`) rebuilds the farm once,
regenerates every scope, and only then removes the superseded
directories. Phase 2 adds:

- A fourth classification in `migrateScan`
  (`cmd/gale/migrate.go:76-140`): *divergent* — a provenanced
  canonical directory whose recorded `sha256` disagrees with
  the hash some scope's lock names for that identity. Today
  the scan has three outcomes (provenanced, candidate,
  source-only) and no way to say this.
- A third shape in `migrateOne`
  (`migraterun.go:265-315`) beside the existing canonical and
  relocating shapes: install the disagreeing scope's artifact
  into the tagged sibling. `DeferFarm` applies for the same
  reason it applies to the relocating shape — the sibling
  proposes a soname at a path the existing claimants do not
  agree on, so the per-commit farm guard would veto the
  operation that ends the disagreement.
- Reuse of `finishRelocations` verbatim: one farm rebuild,
  then per-scope regeneration, then removal of anything no
  longer linked.
- `reportUnresolved` (`migraterun.go:186-211`) loses one of
  its two cases, since a source-built pre-revision directory
  now has somewhere to land.

No new command. `gale migrate` already exists to converge a
machine-wide store state that per-scope commands cannot
reach, which is exactly this.

## 6. Blast radius

Counted at `6d81908`.

**Files.** 33 non-test `.go` files under `internal/` and
`cmd/` reference a store root or `Store.Root`; 71 `_test.go`
files reference store layout; 40 of 84
`integration/scripts/*.txtar` mention `pkg/`. Total Go files
in `internal/` + `cmd/`: 307.

**Path construction** (identity → path): ~15 non-test sites.
All join `(root, name, version)` and all stay correct under
Option B, because the selection happens upstream of them: a
tagged basename is passed *as* the version and joins
unchanged (§3B). Only one site chooses — `canonicalizeForBuild`
(`cmd/gale/context.go:390-413`) — which is why phase 2's
selection is bounded despite being in tier-2 territory.

**Path reversal** (path → identity): 13 non-test sites, and
these are the dangerous half. Each treats
`filepath.Base(storeDir)` as *being* the version-revision:

| Site | Consumes it as |
|------|----------------|
| `internal/farm/farm.go:271` | conflict message + claim key |
| `internal/farm/farm.go:595-596` | drift check key |
| `internal/farm/guard.go:541` | claimant identity |
| `internal/generation/farmclaims.go:269` | name → version map |
| `internal/generation/generation.go:129-133` | gen symlink → (name, version) |
| `internal/generation/generation.go:206-212` | `ActiveVersions`, drives doctor's drift report |
| `internal/depsmeta/depsmeta.go:92` | `.gale-deps.toml` dep version + revision |
| `cmd/gale/gc.go:457-467` | retention key |
| `cmd/gale/gc.go:908` | retention key |
| `cmd/gale/context.go:486,495,501` | `storeRetentionKey` |
| `cmd/gale/remove.go:482` | cross-scope deletion guard |

Every one of these builds or compares a `name@<basename>`
key against canonical keys derived from configs and
lockfiles. A tagged basename silently misses in all of them,
and the failure modes are not symmetric: a farm miss is a
warning, a gc miss is **data loss**.

**Revision parsing.** 46 non-test `Full()` call sites; 13
`HasNumericRevisionSuffix` / 11 `SplitRevision` sites. Option
B's spelling is chosen so none of them change behaviour
(§3B); that choice must be enforced by a test, not by
inspection.

**The two rpath depth sites** — `fixup_linux.go:226-231`,
`fixup_darwin.go:399-417` — are load-bearing constants shared
with every published artifact. They do not change under
Option B and must not.

Subsystems by risk:

| Risk | Subsystem | Why |
|------|-----------|-----|
| **Critical** | `cmd/gale/gc.go` retention | a missed key deletes live store dirs; recurrence class named in CLAUDE.md (ad4e685, 289d13b) |
| **Critical** | `internal/build/fixup_*` rpath depth | wrong depth breaks every dynamically linked package on every machine; not testable by "runs on the build host" |
| **High** | `internal/generation` | gen symlink → identity reversal feeds gc retention, doctor drift, farm closure |
| **High** | `internal/farm` | one soname → one link; more siblings means more guard refusals (gh#198) |
| **High** | `internal/installer` commit paths | `commitStaged`/`replaceStoreDir` are where the change lands; `build.go` + darwin fixup already named the most regression-prone code in the repo |
| **High** | `cmd/gale/context.go` selection | `canonicalizeForBuild` is the only sibling-selection seam; getting it wrong is the reinstall-loop class, and context.go is tier 2 by name in change-discipline |
| **Medium** | `internal/depsmeta` staleness | a tagged basename mis-parsed as a version causes the infinite reinstall loop class (013b4a4, 688ce7d, af4c3f6) |
| **Medium** | `cmd/gale/migrate*.go` | new classification, but the pass structure already exists |
| **Medium** | `internal/store` resolution | new sibling dimension in `resolveVersionFromEntries` |
| **Low** | `internal/lockfile`, `internal/lockgraph` | unchanged by design — tags never enter a lock |
| **Low** | `cmd/gale/{list,which,info}.go` | display only |

## 7. How it would be tested

**Red-green TDD-able, in the owning package:**

- `store.TagVersion` / `store.SplitContentTag` round-trip,
  and the parser-compatibility assertions that make the
  spelling safe: for a tagged basename,
  `HasNumericRevisionSuffix` is true, `SplitRevision`
  returns the tagged base and the right revision, and
  `CheckIdentity` accepts it.
- **The composed round-trip**, which is the case a naive
  splitter gets wrong (§3B). Table the four shapes and
  assert the splitter recovers the pre-tag identity from
  each: `1.8.1-2` (no metadata), `1.8.1+h.<hex>-2` (tag
  opens the segment), `0.2.0-dev.7+5395b8f-1` (metadata, no
  tag — must return no digest), and
  `0.2.0-dev.7+5395b8f.dirty.<hex>.h.<hex>-1` (#213's
  output plus a tag). Pair it with a test that the *tagger*
  produces the fourth form rather than a second `+`, since
  that is the same rule `devVersionWithDirt` applies at
  `install.go:521-524` and the two must not drift.
- `resolveVersionFromEntries` given a fixture listing with
  canonical + N tagged siblings: canonical wins, an empty
  canonical does not win, an unknown tag is not preferred
  over a known one.
- `depsmeta.FromNamedDirs` given a tagged dir: records the
  canonical version and revision, not the tag.
- `farm` claim-key derivation and `generation.genVersions`
  given a tagged store dir: both report the canonical
  identity.
- gc retention: a table test asserting a tagged sibling
  linked by any *retained* generation is kept — including
  by a non-active one, which today's
  `addActiveGenerationRefs` would miss — and one linked by
  none is reaped. This is the test that has to exist before
  any of the rest.
- **Forward convergence**, the loop in §4. Fixture: a store
  whose canonical dir is stale, an install that lands
  divergent bytes at a sibling, then two successive syncs.
  Assert the second sync is a no-op and that exactly one
  sibling exists. Red before the `canonicalizeForBuild`
  change and green after, which makes the most serious
  finding in this design a genuinely red-green test rather
  than an argument. It belongs at the `cmd/gale/` layer,
  because `internal/generation` alone cannot see it.
- `migrateScan` classification of a divergent directory,
  from a fixture store with a planted
  `.gale-provenance.toml` and a planted lock.

**Testable, but not red-green from a failing assertion
first:**

- The rpath depth invariant. Assert the *emitted string* —
  `relativeFarmRpathLinux` and `relativeFarmRpath` return
  exactly three `../` for a `bin/` entry — rather than
  asserting a loaded binary works. That is a characterization
  test written to pin existing behaviour, not a red test for
  new behaviour, and it should be added now regardless of
  this proposal.
- The modulo-self-reference property (§3, A2): that
  `build.Build`'s archive hash is computed over the
  placeholder form and is therefore independent of the final
  store path. Assert by building the same recipe into two
  different store roots and comparing `BuildResult.SHA256`.

**Not testable in CI at all:**

- Real cross-machine byte divergence (§1a). `LC_UUID` and
  ar timestamps need two macOS hosts. Simulate by injecting
  two archives with different content under one identity
  through the installer's staging path, which exercises the
  divergence branch without exercising the cause.
- The GHCR retag case (§1c) needs a mutable registry.
  Simulate with the existing `integration/support` fixture
  registry.
- Whether divergence is *rare* — the assumption Option B's
  cost model rests on. Only phase 1's output answers it, from
  real machines. That is the main argument for shipping phase
  1 first.

**Layer discipline.** `internal/generation` and
`internal/farm` are on
`scripts/check-pipeline-tests.sh`'s sensitive list, so phase
2 requires a `cmd/gale/` or `integration/` test in the same
PR. Given 40 of 84 integration scripts already assert on
`pkg/` paths, the sibling case belongs in a new script rather
than by amending existing ones — the existing ones are the
regression net for "nothing changed in the common case".

## 8. Open questions

1. **Is the cross-machine case real for you?** Phase 3 is
   the largest part of this and is justified by two teammates
   with two lockfiles for one source-built package. Has that
   happened, or is gh#191 anticipating it?
2. **Digest length and source.** 12 hex chars matches
   `dirtLen` (`cmd/gale/install.go:385`) and reads like the
   abbreviated hashes beside it. Is the archive SHA256 the
   right input, or should the tag carry `GraphDigest` so a
   sibling names its closure rather than its bytes? The
   former is what the provenance record already holds.
3. **Does a tagged sibling ever get promoted?** If the
   canonical directory is later removed by gc, does a sole
   surviving sibling get renamed to the canonical name, or
   does the store keep a tagged directory forever? Renaming
   is a store mutation of exactly the kind design.md
   forbids; not renaming leaves a permanently odd-looking
   store.
4. **How many siblings before gale complains?** A pathological
   `--recipes` loop mints one per edit. A cap, an age sweep,
   or nothing?
5. **Is `rollback --force` the right escape hatch?** Phase 1
   refuses a rollback onto directories whose bytes no longer
   match what the target generation recorded, and `--force`
   is the override. The alternative is refusing outright and
   telling the user to reinstall. `--force` is the smaller
   change and keeps the command usable on a machine whose
   history is already divergent; refusing outright is more
   honest about the fact that the generation cannot be
   restored. Your call.
6. **Forward-compat patch timing.** §5's downgrade hazard
   requires the *current* release to skip store entries it
   does not understand, at least one release before phase 2.
   Is that worth a point release on its own?
7. **Does `gale audit` change?** It currently reports
   mismatches users are told to ignore
   (`docs/troubleshooting.md:80-99`). With phase 2, a
   mismatch could instead mint a sibling and report which
   scope wanted which. Larger scope than this proposal, but
   it is the same fact surfaced twice.
8. **`design.md` wording.** `design.md:77-93` says "no
   content-addressing, just `name/version-revision/`" and
   then describes #213's content-*keyed* identity. Option B
   makes that paragraph describe two mechanisms with similar
   names. Worth deciding the vocabulary — "content tag" vs
   "content key" vs "content address" — before the code
   picks it.

## Review

First review pass, recorded so the delta is visible. The
recommendation (Option B) did not change; the phasing did.

- **Phase 2 now includes generation selection.** As first
  written, phase 2 landed divergent bytes at a sibling that
  no generation ever linked, so the canonical directory
  stayed stale and every sync minted another sibling — an
  unbounded reinstall loop. `canonicalizeForBuild`
  (`context.go:390-413`) is the seam, and it moved from
  phase 3 into phase 2. §4 now carries the trace, §7 makes it
  a red-green test, §6 adds `cmd/gale/context.go` to the risk
  table. The "phase 2 closes gh#211" claim survives only
  because of this change; phase 1 alone fixes the rollback
  half and leaves gh#211 open.
- **The rpath argument was over-claimed.** Fixed depth 3
  (`fixup_linux.go:226-231`, `fixup_darwin.go:399-417`) kills
  the *flat* Nix layout, not content-addressing as such: a
  sharded `pkg/<2hex>/<hash>-<name>-<ver>/` preserves the
  depth exactly. §3 now names and dismisses the sharded
  variant on its own merits, and §4 rests the rejection of
  full CA on set-selection and permanent back-compat, which
  hold against every spelling.
- **The tag grammar is now specified.**
  `devVersionWithDirt` (`install.go:516-525`) appends inside
  an existing `+` segment, so a dev build's sibling is
  `…+5395b8f.dirty.<hex>.h.<hex>-1`. A splitter matching only
  `+h.` would miss the headline case and replace in place.
  §3B defines a terminal `.h.<12hex>` segment; §7 tables the
  composed round-trip.
- **gc retention was undersold in §4.** §6 rated it critical
  and §4 described it in a clause. `addActiveGenerationRefs`
  (`gc.go:416-432`) reads the active generation only, and
  `genVersions` returns `map[name]version` first-seen-wins
  (`generation.go:141-143`), so retention across retained
  generations is new machinery. §4 now says so.
- **Phase 1 refuses at rollback** rather than only warning,
  on the gh#205 precedent that a build-time warning becomes a
  user-machine abort. Refusing the *replace* is still ruled
  out. Open question 5 changed from "warn or refuse" to the
  shape of the escape hatch.
- **§4 now states outright** that this defers gh#191's own
  criterion — two projects, one identity, different bytes —
  to phase 3, behind gh#198, and delivers gh#211 instead.
- **Found while integrating the above, not raised in
  review:** pulling selection into phase 2 means a diverged
  sibling goes live for its scope, so a package that provides
  farmed sonames can reach the gh#198 conflict through phase
  2. The claim in §4's gh#198 subsection that phase 2 was
  insulated from the farm was wrong and is corrected there:
  phase 2 helps packages that contribute nothing to the farm
  and leaves the rest refusing as they do today.
