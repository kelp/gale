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
Rejected on one fact:
`internal/build/fixup_linux.go:226-231` and
`internal/build/fixup_darwin.go:399-417` bake
`$ORIGIN/../../../lib` and
`@executable_path/../../../lib` into every dynamically
linked artifact — a hard-coded assumption that a store
prefix is *exactly three* levels below the gale dir
(`pkg/<name>/<ver-rev>`). A1 makes it two. Every prebuilt
binary on GHCR, every attestation, and every installed
dynamically linked package on every user machine would
resolve the farm one directory too high. Migration cost is
"rebuild and re-attest the entire recipe repository, then
force every user to reinstall everything". Not viable.

**A2: `pkg/<name>/<version>+h.<hash>-<revision>/`** — depth
preserved, hash inside the single version component. Viable
mechanically. What changes:

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

What breaks: every bare pin now addresses a *set*. gc
retention keys, farm claimants, `gale list`, `gale which`,
`.gale-deps.toml` dep resolution and the lockfile's
cross-machine portability all need a selection rule that
does not exist today.

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
revision `12345678`. The shape already ships: git installs
write `0.2.0-dev.7+5395b8f-1` today, and #213 added
`0.2.0+dirty.<hex>` beside it
(`cmd/gale/install.go:501-525`).

What changes:

- One new pair in `internal/store`: `TagVersion(canonical,
  digest) string` and `SplitContentTag(base) (canonical,
  digest string)`. Every site that reverses a path into an
  identity (§6) routes through the splitter, so a tagged
  sibling reports the canonical identity to gc, the farm,
  `.gale-deps.toml` and the config layer.
- `commitStaged` (`installer.go:844-895`) gains a
  divergence check before `replaceStoreDir`: read the
  occupied directory's `.gale-provenance.toml`, compare to
  the staged artifact's hash, and on disagreement land at
  the tagged sibling instead of replacing.
- A default `ReplaceGuard` becomes possible, because a
  refusal now has somewhere to go.
- Generation build prefers the canonical directory; a scope
  selects a tagged sibling only when its own lock names that
  hash.

What breaks: less than A2, because the common case is
byte-identical to today. A machine that never diverges never
sees a tag. The cost is concentrated in the §6 reversal
sites and in gc retention, which must keep a tagged sibling
alive exactly as long as some generation links it.

Migration: no flag day. Existing directories are already in
the canonical form and stay preferred. See §5.

For #211: resolves it, without spending a revision. See §4.

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

A weaker variant — warn instead of refuse, plus have
`rollback` verify the bytes a target generation links
against the hashes recorded for it — is gh#211's own
interim suggestion. It is worth shipping regardless of what
else is chosen (§4).

For #211: makes it visible, does not resolve it.

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

The rpath depth constraint (§3, A1) makes the Nix layout a
recipe-repo-wide rebuild, and gale's value proposition rests
on the GHCR binary cache. Any design that invalidates every
attested artifact is not a store change, it is a
re-bootstrap.

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
case that needs full per-scope selection is real but has no
reported instance in the issue tracker.

Phases:

1. **Detect and report.** Default `ReplaceGuard` that
   compares the staged hash against the occupied directory's
   provenance record and warns when they differ and a
   non-active generation links the target. `gale rollback`
   verifies the linked directories still hold the hashes
   recorded for the target generation and says so when they
   do not. This is Option C's weak variant, it is gh#211's
   own interim suggestion, and it is a tier-2 change.
2. **Land divergent commits at a tagged sibling.**
   `TagVersion`/`SplitContentTag` in `internal/store`, the
   reversal sites routed through the splitter, `commitStaged`
   choosing the sibling on divergence, gc retaining tagged
   siblings any generation links, `gale migrate` learning the
   fourth classification (§5). Tier 3. This closes gh#211.
3. **Per-scope selection.** A scope's lock selects which
   sibling its generation links, so two projects on one
   machine hold different artifacts for one identity. Tier 3
   again and larger than phase 2, because it changes what a
   generation is built *from*. Defer until a concrete report;
   the trigger is a user hitting `errScopeDisagrees`
   (`cmd/gale/storereplace.go:24`) with two lockfiles neither
   of which is wrong.

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
note only that the tag must append into an existing
build-metadata segment rather than open a second `+`, the
same rule `devVersionWithDirt` already applies at
`install.go:521-524`.

**On gh#211.** Yes, phase 2 resolves it, and #211's own
reading of why is correct. The stale reinstall gets a
distinct path without spending a revision, so `Reinstall` →
`installLocked` → `commitStaged` no longer renames over a
pathname an older generation links, and the third acceptance
criterion of gh#183 — "every committed store identity remains
byte-stable for as long as any generation references it" —
holds in general rather than for `--path` alone.

Two caveats worth stating plainly. First, it converts #211's
cost from a *revision* cost into a *retention* cost: gc must
now keep N siblings alive while N generations link them, and
`removeUnreferencedVersions` (`cmd/gale/gc.go:487-528`)
compares `name@<basename>` against canonical keys, so an
un-taught gc reaps every sibling on its first run. Second, it
only holds if the divergence check reads the *committed*
record and not a caller's prediction — the same reason
`ReplaceGuard` takes the committed result rather than a
predicted hash (`installer.go:175-180`). Phase 1 should ship
first precisely so the divergence rate is observable before
phase 2 changes behaviour on it.

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
siblings exist. Phase 3 in particular is not shippable
without #198 — per-scope store selection with a
machine-global farm gives each project the store directory
its lock names and then loads the other project's dylib
through the farm. Phase 2 is safe because a divergent sibling
is a transient state converged by the next sync, not a
steady state two scopes both depend on.

Sequence: #198 before phase 3, independent of phase 2.

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
All join `(root, name, version)` and all are correct under
Option B as long as `version` is canonical; they only need to
change in phase 3, where the join needs a selector.

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
  linked by any generation is retained, and one linked by
  none is reaped. This is the test that has to exist before
  any of the rest.
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
  cost model rests on. Only phase 1's telemetry-free warning
  output answers it, from real machines. That is the main
  argument for shipping phase 1 first.

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
5. **Phase 1 default: warn or refuse?** #211 suggests warn.
   Refuse is the honest answer to "rollback executes the
   wrong bytes", but it makes sync stop converging (§3C), so
   warn is the only phase-1-safe choice — is a warning users
   will ignore worth shipping on its own?
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
