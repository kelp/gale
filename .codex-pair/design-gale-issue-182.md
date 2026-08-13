# Design: enforce gale.lock (issue #182)

Status: agreed.
Scope: `gale.lock` becomes an enforced integrity lock instead of an
installation ledger.

**Store invariant.** One canonical identity, one set of bytes, per
machine. `name@version-revision` names exactly one artifact in the
store; two sets of bytes for one identity are never a supported
configuration. Upstream artifacts are immutable by policy: any
change requires a version or revision bump. The lock exists to
detect violations of that policy (GHCR tags are mutable, releases
get retagged, `--recipes` and local recipe files can point one
identity at different bytes, and source builds are not
byte-reproducible across machines, per section 10).

## 1. Problem

- `sync` installs from the current recipe, compares the resulting
  SHA to the lock afterward, and on mismatch warns and overwrites
  the lock (`cmd/gale/sync.go:381-398`). Changed artifacts are
  accepted and activated.
- An installed, non-stale package returns `upToDate` before any
  checksum logic runs (`sync.go:345-353`).
- `LockedPackage` is flat `{Version, SHA256, ManifestDigest}` keyed
  by name (`internal/lockfile/lockfile.go:16-24`): one hash per
  package, so a committed lock cannot represent both macOS and
  Linux artifacts.
- Transitive deps resolve by name against current recipes
  (`internal/installer/installer.go:1122+`); the lock is never
  threaded into the installer.
- `docs/ci-cd.md` promises enforcement that does not exist.

## 2. Schema v1 (breaking, versioned)

```toml
version = 1

[targets.default]
roots = ["jq@1.8.1-2", "ripgrep@14.1.1-1"]

[targets.host."ci-*,build-*"]
roots = ["jq@1.8.1-2", "zig@0.14.1-1"]

[targets.host."work-mbp"]
roots = ["jq@1.9.0-1"]

[packages."jq@1.8.1-2".artifacts."darwin/arm64"]
sha256 = "..."
manifest_digest = "sha256:..."
method = "binary"                # binary | source
runtime_deps = ["oniguruma@6.9.10-1"]
build_deps = ["autoconf@2.72-1"] # incl. resolved implicit tools
graph_digest = "sha256:..."      # see section 5
```

- Package nodes are keyed `name@version-revision`, so several
  versions of one package coexist across targets.
- Platform is an artifact dimension (`GOOS/GOARCH`), not a file.
- Host overlay graphs are keyed by the **original gale.toml
  selector string**, not the concrete hostname: selectors are what
  the manifest declares, and concrete keying would mint an entry
  per CI hostname that can never be committed ahead of time.
- `roots` separates declared from transitive so staleness compares
  roots against gale.toml.
- `runtime_deps` and `build_deps` are recorded separately: a binary
  install validates runtime deps only; a source install validates
  both.

**Downgrade guard.** A v1 file must not be silently destroyed by
an already-shipped gale. Today's `lockfile.Read` is a lenient
`toml.Decode` into `map[string]LockedPackage`, so a v1 file
yields near-empty packages, `IsStale` reports stale, and sync's
step h rewrites the file in the flat schema, discarding an
enforced lock. `min_gale_version` does not help: the old decoder
ignores unknown keys entirely.

So every v1 file carries this exact reserved entry inside
`[packages]`, mandatory and fixed, not an example:

```toml
[packages."!gale-lock-v1"]
version = 1
```

`version` is an **integer** where legacy `LockedPackage.Version`
is a string, so the legacy decoder fails on type and old gale
stops instead of rewriting. The key cannot collide with a real
node, which is always `name@version-revision`.

The guard is a wire-level concern, not part of the in-memory
model. A separate wire struct carries it: `ReadV1` requires the
exact key with the exact value, requires it to have no artifacts,
and strips it before returning a `V1`; `WriteV1` injects it. A
real package entry carrying the guard field is rejected. So plan
construction never sees a pseudo-node and never needs to skip
one. A v1 file with a missing or malformed guard is rejected
outright: accepting one would leave a nominally-v1 lockfile that
is still downgrade-destructible.

Its acceptance test asserts that legacy `Read` **refuses** a
freshly written v1 file and that a legacy sync leaves it
byte-identical: the test pins refusal, not destruction. Release
notes still lead with upgrading gale everywhere before committing
a v1 lock, since the guard converts silent data loss into a loud
failure rather than removing it.

Rejected: per-platform lock files (clutter, drift, unreviewable);
locking recipe hashes instead of artifact hashes (misses the exact
attack: artifact replaced upstream with no recipe change).

## 3. Resolution plan

**Contract.** The lock is the exclusive selector of versions,
artifacts, methods, and dependency edges. Recipes are still read,
because only a recipe knows how to fetch or build a node, but a
recipe never *selects* anything under a lock: its metadata is
validated against the locked node and disagreement is an error.
(The earlier phrasing "no live-resolver consultation" meant no
independent version selection; it does not forbid the recipe reads
section 4 requires.)

**Construction**, before any install:

1. Merge the root maps of every selector matching the current host
   using `matchingHostKeys` precedence (wildcards first, exact
   last, `internal/config/gale.go:53-76`; unexported today, so it
   is exported as part of this work), producing the effective
   root set. Two hosts matching one selector share one locked
   graph; divergence requires distinct selectors, as in gale.toml
   today.
2. Compare that root set against the effective `gale.toml`
   packages for the same host. Any difference is a stale-lock
   failure (section 9).
3. Traverse the locked dependency edges for the current platform
   from those roots, taking `runtime_deps` for every node and
   additionally `build_deps` for nodes whose method is `source`.
4. Require a node and a current-platform artifact for every name
   reached. A missing node or artifact is a hard error (section 9).
5. Resolve each node's exact recipe by canonical
   `name@version-revision`.
6. Validate each recipe against the locked node: canonical
   identity, declared SHA and manifest digest where the recipe
   carries them, method availability, dependency names and their
   runtime/build classification, and platform constraints.

7. Reject dependency cycles. The current installer tolerates them
   by deduplicating with a `seen` map
   (`internal/installer/installer.go:1111`), but section 4's
   topological commit order does not exist for a cyclic graph.
   Plan construction fails with an error naming the cycle. SCC
   handling is deliberately not attempted: gale has no legitimate
   cyclic-dependency case today, and a clear error beats a
   half-defined commit order.

The result is `{name -> exact version-revision, expected
sha256/manifest digest, method, dep edges, graph digest}`.
`installDepsInner`, `ResolveDirectDeps`, and the staleness check
all consume this plan. Plan construction fails on cross-root
version conflicts. Dep locking ships in this feature, not a later
phase.

## 4. Verified-unit commit model

Invariants:

- **A.** No artifact is committed to the store or the farm before
  *it* verifies against the lock. Binary: there is no on-disk
  archive to hash. `download.fetchAndExtractTarZstd`
  (`internal/download/download.go:781-845`) tees the HTTP body
  through a hasher directly into the extractor and compares the
  SHA after `extractTar` returns, removing the staging directory
  on mismatch, so verification is a streamed hash checked before
  anything leaves staging. The lock's SHA and manifest digest are
  additionally checked against recipe metadata before dep
  installation. Source: `BuildResult.SHA256` is compared before
  promotion; `internal/build` computes it from the finished
  archive via `download.HashFile`, before any store mutation.
  Never a directory rehash: `fixupExtracted` rewrites Mach-O load
  commands in staging before the commit, so post-fixup bytes
  cannot match the artifact SHA.
- **B.** The generation rebuild and swap run only after every plan
  entry has verified.

Consequences:

- Installs proceed in topological order, so source parents build
  against deps already committed at their final store paths. No
  staging-path embedding, no path rewriting, no SHA drift. (An
  all-staged two-phase sync was considered and rejected for exactly
  this reason: dep paths embed into built artifacts, changing the
  SHA being verified.)
- Only **absent** canonical store dirs may be committed
  incrementally. In-plan `replaceStoreDir` is prohibited; any
  provenance or graph disagreement with an existing canonical dir
  is a conflict error.
- All `farm.Populate` calls are deferred to the post-validation
  batch alongside the generation build. That batch is the
  generation rebuild itself, and it is not new machinery: the
  guard over the whole proposed closure runs before the `current`
  swap, and the population runs after it, both inside the single
  store-generation lock acquisition. Invariant A alone does not
  cover the farm: farm symlinks are version-independent, and
  `Populate` overwrites a link belonging to the same package at
  another version on sight (`internal/farm/farm.go:140-143`). So a
  per-node population of a *verified* version Y redirects a link
  the active generation's binaries already resolve through, and if
  a later plan node fails, that generation now loads Y with no
  swap ever having happened. Deferral is what preserves invariant
  B's no-partial-activation property. Source builds are unaffected
  either way: `internal/build/build.go:627` bakes the farm dir in
  as an `-Wl,-rpath` string, which does not require the farm to be
  populated at build time.
- The batch's target is the **shared** farm
  (`filepath.Dir(storeRoot)/lib`) in **both** scopes. This is the
  one thing the original text left implicit and got wrong. Store
  binaries carry an `-Wl,-rpath` derived from the store root
  (`farm.DirFromStoreDir`, `build.go:660`), so the shared farm is
  the only farm any binary resolves through, at either scope. The
  darwin fixup's relative `@executable_path/../lib`
  (`fixup_darwin.go:162-163`) is not a second consumer: dyld
  resolves the executable's symlink to its real path before
  substituting, and generation `bin` entries are symlinks into the
  store, so it lands in the package's own store lib dir. A
  project-scoped generation rebuild that targets
  `<project>/.gale/lib` therefore reaches nothing: no PATH entry
  adds it, no rpath names it, no `DYLD_*` or `LD_LIBRARY_PATH`
  export references it. Rebuilding it is dead work and stops, so
  the project-local farm directory is retired rather than
  redirected, and the guard's project-scope exemption is removed
  rather than narrowed: with the local directory untouched, an
  exemption for it means nothing.
- `farm.Dir(galeDir)` is retired with it. Once every call site
  takes the store-derived farm it has no production callers
  (`generation.go:482`, `history.go:160`, `doctor.go:538` are all
  of them), and the whole defect class is a scope's `galeDir`
  passed where the shared farm was meant. It is replaced by an
  explicit `farm.DirFromStoreRoot(storeRoot)`;
  `farm.DirFromStoreDir` stays for callers holding a package store
  directory. Deleting the accessor that makes the confusion
  expressible is the durable fix; correcting three callers is not.
- Unlocked mode keeps today's streaming behavior.

Rewritten acceptance criterion: *the mismatching artifact is never
committed; previously verified, lock-matching dependencies may
remain cached in the append-only store; no farm mutation and no
generation swap occur after an integrity failure.*

**The farm is shared across projects, and deferral alone does not
make it safe.** Deferral orders population within one plan. It
does nothing across plans: project B can run a completely
successful, fully verified locked sync whose `farm.Populate`
repoints `~/.gale/lib/<soname>` from the version project A locked
to B's version. A's store directories and provenance are
untouched, so A's activation gate passes while A's binaries load
B's bytes. Two projects locking different versions of one
dylib-providing dependency repoint the link on every sync.

Checking this at A's next activation is not sufficient: A can
pass the gate and B can repoint the farm before A next executes
or loads a library. The check must therefore run **before the
mutation**, not after it.

It also cannot be scoped to locked syncs. An unlocked sync, a
`--no-frozen` run, `install`, `remove`, a repair, a generation
rebuild, or a **generation rollback** mutate the same farm and can
violate a locked closure just as effectively. The guard therefore
sits at **every farm mutation boundary**, and what makes it apply
is the closure being harmed, never the privilege of the operation
asking.

Rollback is named explicitly because it is the same defect at a
second site, and a pre-existing one. `internal/generation/history.go`
mirrors the rebuild exactly — guard, swap, then
`farm.Rebuild(active, farm.Dir(galeDir))` — so at project scope it
wipes the retired local directory and never repoints the farm its
binaries actually resolve through. gh#44's rollback repair has
therefore only ever worked at global scope. It is fixed here rather
than deferred: a shared-farm reconciliation applied only to forward
builds would leave two implementations of one activation invariant.
Rollback guards the rolled-to package set plus the guarded union of
every other claimant, targets only the shared farm, and carries its
own project-scope regression test.

The rule guards **create, remove, and retarget** of a claimed
soname. All three verbs are load-bearing, because the mutation
boundary includes `remove` and the generation rebuild:
`farm.Depopulate` deletes a claimed soname rather than
repointing it, and `farm.Rebuild` deletes mappings before
recreating them. A rule written only against "repoint" would let
a faithful implementation leave a locked closure with its farm
entry simply gone.

It is a check on the **resulting state**, not a veto on the verb.
Rejecting raw mutation would deadlock every legitimate change: a
project updating or removing its own package still has a
pre-swap generation claiming the old soname, so it would block
itself, and repairing a missing link would be refused for
creating exactly the target every claimant wants.

The canonical rule, stated once so nothing downstream restates it
as a verb test:

1. Build the claimant set: the initiating scope's **proposed**
   closure, plus every other scope's **current active** closure.
   The initiating scope remains a claimant; only its *old*
   pre-operation closure is superseded.
2. Compute the farm mapping the operation would leave behind.
3. Allow it exactly when that mapping satisfies every claim in
   the set.

The claim is built **once per operation**, from the command's
eventual package set, at the rebuild boundary — never once per
commit. A per-commit claim models the active generation plus one
change, so a sync that installs every root before it rebuilds
cannot see a conflict between two roots that first meet in the
final closure. This is also why the guard belongs to the
generation rebuild rather than to a caller-supplied callback: the
rebuild is the only place that holds the lock across the swap and
already knows the eventual package set, and an invariant a caller
must remember to pass is a convention, not an invariant.

Everything follows from that and nothing else needs saying: an
external claimant that *agrees* with the proposed mapping never
blocks anything, so restoring a missing link or writing an
identical mapping is allowed; a proposed closure that conflicts
with itself fails on its own, with no external claimant needed;
and only a *conflicting* external claim refuses the operation.
Claimants are the registered projects from the `~/.gale/projects`
walk **plus the global scope**, which `registerProject` skips by
design (`cmd/gale/context.go:136-152`) and which a
registry-only scan would therefore miss entirely. A registered
project that is known but unreadable fails the check closed.
Unregistered projects and already-open shells remain the
acknowledged limitation until the scoped-farm follow-up, the same
limitation section 13 accepts for the store.

Correctness, as opposed to this interim rejection, requires a
farm scoped to a generation or a closure rather than one shared
tree. That is its own follow-up issue, and content-addressing the
store does not subsume it: distinct store paths still collapse
into one version-independent soname link. Until it lands, two
projects on one machine locking different versions of a
farm-visible dependency is unsupported, the farm-level sibling of
the store invariant.

**Invariant B binds every failure in a locked plan, not only
integrity failures.** A locked sync that cannot complete its plan
for any reason (download error, build failure, missing recipe)
leaves the generation untouched. This resolves the contradiction
with issue #20's rebuild-on-partial-failure behavior in favor of
the lock, on the grounds that a partial generation is unusable
under a lock anyway: section 12 refuses `PATH_add` when a root of
the target graph is absent from the active generation, so a
partial rebuild would produce a generation that cannot be
activated. Issue #20's behavior is preserved exactly in unlocked
mode, which is where its motivating case lives.

## 5. Dependency identity and the graph digest

Locked edges name `name@version-revision` only. That identifier is
not sufficient: `gale lock --refresh` can change a dependency's
artifact SHA without changing its version-revision, and a parent's
recorded dep list would then look unchanged although it was built
against different bytes.

Each artifact therefore carries a `graph_digest`, defined
recursively over the closure so a change anywhere below a node
changes that node's digest. An edge carries the dependency's
`graph_digest`, not its SHA: a digest over direct-dependency SHAs
alone would not propagate, since in `A -> B -> C` a change to C
alters B's digest but leaves A's edge tuple identical.

Canonical serialization, computed bottom-up over the DAG:

```
G(n) = "sha256:" + hex(SHA256(S(n)))

S(n) = "gale-graph-v1\n"
     + "node\0" + name + "\0" + version-revision
     + "\0" + goos + "/" + goarch + "\0" + method
     + "\0" + sha256 + "\0" + manifest_digest + "\n"
     + one line per edge, in ascending bytewise order of the
       full line:
       "edge\0" + kind + "\0" + dep-name
     + "\0" + dep-version-revision + "\0" + G(dep) + "\n"
```

`\0` is a single NUL byte, `\n` a single LF. Hex is lowercase.
`sha256` is the artifact's bare lowercase hex hash;
`manifest_digest` is the empty string when absent. `kind` is
`runtime` or `build`; build edges are included only when the
node's own method is `source`. No other whitespace, padding, or
normalization is applied. Provenance stores the resulting digest,
so the whole closure comparison is one field.

**Consequence for source-built nodes, stated because it is easy
to miss.** A node's own `sha256` is part of its digest, and for
`method = source` that is the build *output* hash, which section
10 says legitimately differs across machines. Since an edge
carries the dependency's digest, one source-built node changes
the digest of every node above it, binary parents included.

So portability is *guaranteed* only for an all-binary closure. A
source-containing closure is portable exactly when every source
build in it reproduces the locked output byte for byte, which
section 10 says is often not true today, but is not impossible
and is the direction reproducibility work moves. That follows
from enforcing source builds at all and is not a defect in the
formula: substituting an
input-derived value (the recipe's `source.sha256`, say) would not
make such a lock portable either, because machine B still fails
the source node's own strict output check. It would only hide the
divergence from ancestors while weakening the digest's binding to
the bytes actually built. If a logical build fingerprint is ever
wanted, it belongs alongside the artifact digest, not in place of
it, and it must cover the recipe's build instructions, platform
overrides, and local/git inputs, none of which `source.sha256`
carries.

Consequence for `--refresh`: changing any artifact's SHA changes
the `graph_digest` of every node above it. Refresh must therefore
regenerate the reverse-dependent closure, but that regeneration is
bounded by section 11's replacement rules: it proceeds only where
a reverse dependent's canonical store dir is absent, provably
unreferenced, or referenced without provenance (section 13), and
otherwise stops with the conflict error and the re-pin remedy.
Refresh never becomes a back door to replacing a referenced,
validly provenanced directory.

**Validation.** Plan construction recomputes `G(n)` bottom-up for
every node and rejects any disagreement with the stored
`graph_digest`, in the lock and in provenance alike. Writers
compute the same value and persist it. The digest is never
trusted as an opaque token.

## 6. TOCTOU and the validation callback

Per-artifact verify+commit runs under the store-gen lock only on
some paths today: `commitExtracted`
(`internal/installer/installer.go:793-796`) takes
`withStoreGenLock` when `inPlace` is true and commits without it
otherwise. Under a lock, **every** commit takes the store-gen
lock, `inPlace` or not.

Additionally, a final revalidation of every plan entry runs
**inside the same store-gen lock acquisition that builds and
swaps the generation**, via a validation callback. `generation.Build`
is `Build(pkgs map[string]string, galeDir, storeRoot string) error`
with no options struct, so the callback arrives as a new
`BuildWithValidate(pkgs, galeDir, storeRoot string, validate
func() error) error`, with `Build` kept as a nil-callback wrapper.
Call sites without a plan (`gc`, `doctor`) pass nil and behave
exactly as today.

Executable contract for `BuildWithValidate`:

1. Acquire the global store-generation lock.
2. Invoke the validation callback before creating or mutating any
   generation directory, symlink, or farm entry. The callback
   revalidates every plan entry, including `upToDate` cache hits:
   canonical version-revision, artifact SHA, method, manifest
   digest, and `graph_digest`.
3. On callback error, abort immediately, mutate nothing, and
   return the lock-integrity error.
4. Hold the lock through generation construction, the `current`
   swap, and the batched shared-farm population. The guard over
   the complete claimant set runs BEFORE the swap, so a conflict
   aborts with nothing activated; the population runs AFTER it.

Revalidate-then-release leaves a gap; a nested acquisition
deadlocks; holding every package lock for the whole plan blocks
other projects across a multi-minute source build. Late drift
costs at most orphaned verified store entries, never a partial
activation.

**One activation commit point, followed by non-transactional
reconciliation.** The original "all three complete or the
operation aborts" promised an atomicity the implementation cannot
deliver, so the boundary is stated explicitly. Before the
`current` swap, validation and claimant conflicts are fatal and
nothing activates. The swap IS the activation commit point. After
it, shared-farm population is progressive, derivable state: a
failure there does not roll back the generation, because undoing a
completed swap would be a second fallible transaction that cannot
reliably restore a partially mutated farm, which is worse than
keeping a verified generation active and reporting degraded
derived state.

It is not silent either, and this costs a behavior change rather
than only wording: `farm.Rebuild` currently logs each `Populate`
failure and returns success (`farm.go:218-228`). It must aggregate
and return them, and the generation rebuild must wrap that as a
post-commit failure distinct from every pre-swap one, saying
plainly that activation completed and farm reconciliation did not.
A later *successful* generation rebuild repairs it through the
same guarded-union reconciliation. `gale doctor` is deliberately
not named as the remedy: doctor's own project-scope farm check
reads the retired project-local directory (`doctor.go:538`), so it
can be named only once corrected alongside the other two sites.

Correcting it has a consequence worth stating. `CheckDrift`'s
first pass walks every entry in the farm directory
(`farm.go:249-266`), so a project-scope `gale doctor` reading the
shared farm now reports broken links outside its own closure.
That is intended: the state genuinely is shared. The diagnostic
distinguishes the two classes without asserting ownership, since a
broken extra may be stale or orphaned rather than owned by a live
scope — "required by this scope" when the entry is in its active
closure, "shared farm entry outside this scope's closure"
otherwise. The second pass is unaffected: it walks only the
checked scope's active store dirs, so a union-populated shared
farm reads as no drift for entries belonging to others, which is
what makes the redirection safe.

## 7. Provenance on store entries

`.gale-deps.toml` is unchanged: it remains the runtime-only,
hash-free built-against closure (`depsmeta.Metadata` holds
`{Name, Version, Revision}`) that build and installer already
exchange. It cannot support the validation above, so provenance is
a **separate sibling file** written into the staging dir before the
commit rename, holding:

- canonical package identity: `name`, `version-revision`
- `platform` (`GOOS/GOARCH`)
- `sha256`, `manifest_digest`, `method`
- `runtime_deps` and `build_deps` as canonical identifiers
- `graph_digest` (section 5)

**Provenance is all-or-nothing.** It is written only when the
artifact itself was verified *and* every serialized dependency edge
supplies a valid graph digest. In unlocked mode a dependency with
no provenance file causes the artifact to be committed **without**
provenance; the installation does not fail. An empty or partial
`graph_digest` is never written.

Unusable dependency provenance is treated exactly as unavailable,
never as a weaker form of usable. Unusable means **absent, or
failing full provenance validation**. That is deliberately open:
a closed list would be wrong the first time a field is added.
Examples, not the definition: unparseable; a platform other than
the one being installed; any required field missing, `graph_digest`
included; a malformed dependency list; a `graph_digest` that is
present but does not survive recomputation; an identity other than
the dependency actually resolved. In every case the unlocked
install succeeds and the parent commits without provenance, so
nothing untrustworthy ever supplies a digest to an edge.
Distinguishing degrees of unusable here would only create a second
tri-state one level down.

Under a lock the same disagreement is a conflict error instead,
checked at three points that between them cover every node without
claiming coverage a node's provenance cannot yet supply:

1. **A node being installed:** during that node's own pre-commit
   verification, which is where its provenance is written and
   therefore the first moment it exists. Plan construction runs
   before any install, so it cannot validate a fresh node's
   provenance; it validates the lock, not the store.
2. **A node that is a cache hit:** on the `upToDate` path below,
   against the lock entry.
3. **Every node, again:** in section 6's final validation
   callback, inside the store-gen lock acquisition that builds and
   swaps the generation.

Unlocked mode has nothing to check the dependency against, so
omission is the only honest outcome; a locked plan names exactly
what the dependency must be.

The presence of a provenance file therefore means one thing: this
record is complete and trustworthy under its contract. A partial
record would make the format tri-state, force every reader to
interpret the missing value, and collide with section 13's
provenanced/unprovenanced distinction, which is the same
unverified marker section 13 rejected under a third name. If
preserving the independently verified artifact facts ever becomes
useful, that is a separate receipt with weaker semantics, not
partial provenance.

"Every serialized edge" is section 5's rule, not "every
dependency": runtime edges serialize for every method, build edges
serialize only for a source artifact. So a binary artifact with no
runtime deps is a digest leaf even when its recipe names build
tools, while a source artifact with build deps is not.

Consequences, stated as consequences. A genuinely fresh machine
converges on its own, leaves first and parents after. A legacy
dependency stops that frontier and the omission propagates upward,
so an upgraded machine converges only bottom-up. `gale migrate`
(section 13) drives that for the binary part of the closure only:
it replaces unprovenanced binary-method dirs and merely *reports*
the source-method ones, which converge only when the user rebuilds
them. A legacy dir never gains
provenance by having a cache hit stamped; only a real refetch or
rebuild and replacement provenances it.

This also forecloses the stale-parent hazard above an
unprovenanced dep: no parent above it can hold valid provenance,
so there is no digest to go stale. Once a dep and its parent are
both provenanced, replacing the dep does leave the parent's
provenance describing the older closure. That is correct
historical information, not a false attestation: `--refresh`
regenerates and replaces the reverse-dependent closure and its
provenance (section 11), and never patches provenance in place.
An unlocked reinstall can likewise change a dep and leave an older
parent record behind; a later gate failure on that mismatch is the
correct outcome, because the runtime closure really did change
after the parent was recorded. It is never repaired by deleting or
rewriting the parent's provenance in place.

Provenance attests what was fetched and verified at commit time,
not the directory's current bytes. Nothing here detects
post-commit mutation or corruption of an installed tree; a
digest over the finalized tree would, and belongs with the
content-addressed store follow-up (#191) rather than in #182.
Within one uid that would be tamper-evidence rather than
prevention, and the commit is an atomic rename, so the window is
narrow.

The `upToDate` cache-hit path compares the lock entry against
stored provenance. Mismatch or missing provenance is a conflict
error, never an in-place replacement and never a directory rehash.
This closes the shared-store cross-project hole: the store root is
one `defaultStoreRoot()` shared by all scopes.

## 8. Failure semantics

Lock-integrity mismatch is a distinct error type **and a distinct
exit code**. A Go error type is invisible to the shell scripts
`docs/ci-cd.md` tells users to write, and a pipeline must be able
to tell "artifact tampered" from "build broke". gale uses only
exit 1 today (`cmd/gale/root.go:79`), so the taxonomy is new and
breaks nothing:

| Code | Class | Meaning |
| --- | --- | --- |
| 1 | ordinary failure | build error, network error, usage error: everything that fails today |
| 3 | lock integrity violation | artifact SHA, manifest digest, provenance, or `graph_digest` mismatch; store-dir provenance conflict; cross-project farm conflict |
| 4 | lock unusable | **any lock that is present but cannot be parsed or fully modeled**: stale lock, missing package, dep or platform entry, legacy schema, unknown schema version, malformed downgrade guard, malformed TOML, unknown field |
| 5 | activation drift | the active generation does not match the target graph, including carry-forward; remedy is `gale sync` |

The split that matters is 3 against 4 and 5. Code 3 means
something disagreed with bytes the lock names, which deserves a
human. Codes 4 and 5 mean the lock or the generation needs
regenerating, which a pipeline can often handle itself. Every
command that can fail these ways uses the same codes: `sync`,
`install`, `update`, `lock`, `migrate`, `shell`, `run`, `remove`,
and `env`. `remove` belongs because it reads and writes the lock
and mutates the farm, so it reaches codes 3 and 4. `env` belongs
because it is an activation command: it emits a `PATH` that CI
and scripts consume, so once section 12's gate exists it reaches
3, 4, and 5. An activation command that cannot fail is a hole in
the gate.
Documented in `docs/ci-cd.md` and pinned by an acceptance test
per class, not one test for "non-zero".

Under a lock,
`finishSync` is skipped on any plan failure: no generation
rebuild, no swap, even when the config changed (section 4).
Unlocked mode keeps today's semantics, including issue #20's
partial rebuild.

## 9. Fail-closed policy

- Lock present, and a missing platform entry, missing package,
  missing dep entry, legacy schema, or unknown future schema
  version: hard error naming the exact remedy (`gale lock`).
- `--no-frozen` downgrades to warn and proceed unlocked.
- No lockfile at all: unlocked mode, one warning.
- Changed gale.toml with a lock present: sync fails as stale-lock
  with instructions. Sync is a pure consumer and never follows the
  manifest unlocked. `IsStale`/`syncIfNeeded` are redesigned:
  roots-vs-gale.toml replaces the package-count comparison, which
  breaks as soon as transitive entries exist.
- `sync --build` is rejected at plan validation when the locked
  method is `binary`, before any dep or store mutation. It is
  honored only unlocked or when the locked method is source.
- A locked `binary` method never silently falls back to source.
- Where a writer cannot completely enumerate host-selector
  interactions, it treats all lock targets as potentially
  co-applicable.
- Every existing project has a legacy sync-written gale.lock, so
  the first sync after upgrade fails everywhere, including inside
  direnv. The error must be one actionable line, `gale doctor`
  gains a check, and the release notes lead with it. Silently
  treating a legacy lock as absent was considered and rejected: it
  is a silent downgrade of a security control.

## 10. Source builds

Enforced strictly, never warn-only. `docs/troubleshooting.md`
already states audit mismatches are "normal for most packages"
(Mach-O `LC_UUID`, `.la`/`.pc` absolute paths, ar/ranlib
timestamps), so the docs will state that locked source builds may
legitimately fail across machines until reproducibility improves;
the remedy is re-lock or a binary artifact.

Docs must also state the cascade from section 5 plainly: because
a source node's output hash feeds every digest above it, a
committed lock is portable across machines with certainty only
when its whole closure is binary. A closure containing a source
build is portable exactly to the extent that build reproduces,
which today is the exception rather than the rule.

## 11. Lock writers

Sync **never** writes gale.lock (today's step h is deleted).
Writers: `install`, `update`, `remove`, and a new `gale lock`.

Every writer resolves the complete closure successfully first,
then replaces gale.lock in a single atomic write
(`internal/atomicfile`). A partial or failed resolution leaves the
previous lockfile byte-identical.

Per-command target rules, mirroring what each command writes to
gale.toml:

| Command | Manifest section written | Lock target regenerated |
| --- | --- | --- |
| `install pkg` | `config.UpsertPackage(path, CurrentHost(), …)`: `[hosts.<CurrentHost>.packages]` when that exact section already lists the package, else `[packages]` | `[targets.host."<CurrentHost>"]` or `[targets.default]`, matching whichever section was written |
| `install pkg --host K` | `[hosts.K.packages]` | `[targets.host."K"]` |
| `update [pkg...]` | every section whose pins it rewrote | the target for each such section |
| `remove pkg` | the section(s) it removed from | the target for each such section |
| `gale lock` | none | `[targets.default]` only |
| `gale lock --host K` | none | `[targets.host."K"]` only |
| `gale add pkg` | `[packages]` or `[hosts.K.packages]` | none: manifest-only, the lock is not touched |

`K` is the **post-alias** selector: `resolveHostFlag`
(`cmd/gale/add.go:118-123`) expands `--host current` to
`config.CurrentHost()` before the config write, so the lock target
for that invocation is the concrete hostname. Every other
selector, wildcard or exact, stays literal and is never resolved
against the current machine. A second exception is the `install
pkg` row, where
location preservation mirrors `UpsertPackage`'s existing
exact-hostname behavior (`internal/config/gale.go:619-642`) and
the lock follows the manifest section actually written. Wildcard
profiles are never rewritten as a side effect of a concrete-host
operation. If a project declares no default packages, plain `gale
lock` errors and lists the declared selectors rather than writing
an empty default target.

A writer's target roots are the packages it verified in this run, plus
prior roots of the same target carried forward. A prior root is carried
when gale.toml still declares it, its pin still matches, and its
subgraph in the existing lock is **complete**, meaning all three of:
the root records at least one artifact; for every platform it records,
every serialized dependency resolves to a node that also records that
platform; and the serialized graph is acyclic. Reachability alone is
not enough, because a cyclic graph is closed under it and still has no
commit order.

A declared package that was **not** selected for this run and cannot be
safely carried is **omitted** from roots, producing the same stale
state `gale add` produces deliberately, and the writer names it so the
caller can print the remedy.

Omission never applies to a root this run selected. If any of those
fails verification the whole write fails atomically, leaving the
previous lockfile byte-identical; there is no partial success in which
a requested package silently disappears from the lock.

Omitting a sibling rather than failing follows the same rule as
other-platform minting below: omit what cannot be backed, and never
write an entry that looks supported and must fail on use. Failing
instead would make `gale install`, named below as a remedy for the
`gale add` stale state, a dead end whenever a second declared package
happened to be unlocked. Locking only the packages this run touched
would be worse still: it would replace a complete committed target and
destroy every sibling's locked data.

Roots subset-of declared is **stale**, a reachable and recoverable
state whose remedy is rewriting the lock. A root with no corresponding
package node is **not** a state any writer may produce: it reads as a
tampered lock, whose remedy is repairing its contents. That asymmetry
is why a carried root's subgraph is proven complete rather than
assumed.

Other rules:

- Package nodes referenced by any other target graph are
  preserved. This holds **within v1 only**: `toml.Decode` drops
  fields the struct does not model, so any writer round-trip
  would silently destroy data belonging to a future schema.
  Writers therefore decode `version` first and hard-fail on any
  version they do not fully model, before writing anything.
  Forward compatibility requires a version bump, not silent
  preservation.
- Other-platform artifact hashes are preserved only when that
  package's version, method, and `graph_digest` are unchanged;
  otherwise dropped, forcing a re-lock on those platforms.
- Writers also **create** other-platform entries, which nothing
  else in this design does. A recipe's binaries table already
  carries per-platform `sha256` and `manifest_digest`
  (`internal/recipe/binaries.go:42-45`), so `gale lock` mints a
  `method = "binary"` artifact for every platform whose **entire
  effective closure** is derivable from binary metadata.
  Minting is **opportunistic and never fatal**: `gale lock`
  succeeds, writing every complete binary-only platform, and
  prints a diagnostic naming each skipped platform and the
  source-built nodes that blocked it. Failing instead would, under
  the atomic-write rule above, prevent the whole lockfile from
  being written because of a platform the user may not even use.
  A platform whose closure contains a node with no binary artifact
  gets no entry at all: a recursive `graph_digest` cannot be
  computed over a dangling graph, and a partial entry would
  produce a lock that looks supported for that platform and must
  fail on it. Running gale on a skipped platform then produces
  section 9's missing-platform error, which is the correct
  outcome and the reason that error remains reachable in normal
  use rather than only by hand-editing.
- `remove` recomputes the closure so shared transitive deps
  survive.
- `gale add` writes gale.toml without installing
  (`cmd/gale/add.go`), so under a lock the next sync, including
  direnv on `cd`, fails as stale-lock. It stays manifest-only and
  prints the next step. The stale-lock error distinguishes "root
  added but not locked" from other staleness and names both
  remedies: `gale lock` to resolve the lock alone, `gale install`
  to lock and activate.
- Plain `gale lock` reuses installed binary provenance only when it
  matches the recipe's declared SHA and manifest digest; source
  provenance is reused as-is. When no installed provenance exists,
  it fetches or builds to obtain one. A dedicated lock writer that
  could only describe what happened to be installed would be
  unable to lock a newly added package at all. An **absent**
  canonical directory and an **occupied but unprovenanced** one
  are different cases: `gale lock` may populate the first, and
  may not adopt the second, since adopting it would assert
  provenance for bytes it never verified. The second is
  `--refresh` or `migrate` territory (section 13). So same-machine projects converge on
  one hash per `name/version-revision`, and the conflict error
  fires only when an upstream artifact was actually replaced or a
  non-reproducible source build crossed machines: both cases where
  failing is the point.
- `gale lock --refresh [pkg]` fetches per the recipe and replaces
  knowingly, but only where the **canonical store directory** is
  absent, provably unreferenced, or referenced without provenance
  (section 13). The constraint is on store dirs, not lock entries.
  The reference scan is global and reuses the same
  `~/.gale/projects` registry walk gc uses. Refresh regenerates
  the reverse-dependent closure per section 5, under these same
  rules: a reverse dependent whose dir is referenced and validly
  provenanced stops the operation with the conflict error.
- Conflict errors name both hashes. They offer `--refresh` only
  when the directory is actually replaceable under the rule above;
  for a referenced, validly provenanced directory the sole remedy
  is re-pinning to a new version-revision. The gc remedy is never
  offered: gc retains packages referenced by configs or active
  generations, so it cannot remove a conflicting artifact.
- Lock targets are keyed by gale.toml's host selectors, and a writer
  must decide which of them can apply to one machine in order to check
  the one-version rule against every graph a reader can plan. That
  decision is defined over a restricted grammar: ASCII letters,
  digits, `-`, `.`, `_`, `*`, commas separating alternatives, and the
  ASCII spaces or tabs that may pad them. Every other `filepath.Match`
  construct, including `?`, character classes, `\` escapes, and the
  `/` separator, is outside it. The restriction is what makes the
  decision complete rather than approximate, since over that grammar
  byte-wise and rune-wise matching agree and `*` has no separator
  exception. The implementation enforces exactly this grammar, as an
  allowlist rather than a list of forbidden metacharacters, because a
  denylist must enumerate every construct the matcher gives meaning to
  and missing one is silent.

  A selector outside the grammar is not rejected, because gale.toml
  has always accepted whatever `filepath.Match` accepts. Instead the
  writer loses the ability to enumerate effective selector sets and
  falls back to checking every target at once. Search exhaustion
  triggers the same fallback, so a bounded search never returns
  partial coverage.

  "Every target at once" means the concatenation of all targets' **raw
  root identities**, with no name-keyed replacement. Replacement is
  only legitimate between selectors known to co-apply, which is
  exactly what could not be determined; collapsing by name would let a
  more specific target's root overwrite a conflicting one and hide the
  disagreement. Two mutually exclusive versions therefore report a
  conflict, which is the intended conservative false positive: the
  fallback can report a conflict between targets that could never
  apply to one machine, and it cannot miss one.

  Coverage enumerates distinct effective selector *sets*, not selector
  pairs. A third selector matching the same hostname masks a
  disagreement between two others through replacement, while a
  different hostname matching only those two still exposes it.

## 12. Activation gating

The current hook syncs only when the manifest is newer than the
generation symlink (`internal/env/env.go:14-32`), so a gale
upgrade alone trips no mtime and a legacy lock would go
undetected. Watching gale.lock as well is necessary but not
sufficient. The hook therefore runs an **unconditional cheap gate
before `PATH_add`** on every activation, independent of the mtime
guard:

- The gate validates the lockfile schema version, then traverses.
  A generation holds `bin`, `lib`, and `man` symlinks, not
  per-package directories, so root identities come from
  `generation.CurrentVersions(galeDir, storeRoot)`
  (`internal/generation/generation.go:175`), which recovers
  name to version from the active generation. From those roots the
  gate walks the locked **runtime** closure through store
  provenance, since transitive deps are not generation entries at
  all. Every referenced runtime store directory must exist with
  provenance matching the locked identity, artifact SHA, method,
  and `graph_digest`.
- `carryForwardMissingVersions` can seed the active generation
  with a version the lock does not name. That is a **gate
  failure**, not a lock-integrity conflict: the remedy is `gale
  sync`, and the two error classes stay distinguishable so a
  carried-forward package never reads as tampering.
- Build dependencies need not still be installed; a source root's
  stored `graph_digest` must nonetheless match, which is what
  binds the build closure it was produced from.
- The gate reads provenance files and the lockfile only; it hashes
  nothing.
- The mtime guard still decides whether a full `gale sync` runs.
  The gate runs either way.
- Gate failure refuses project `PATH_add` (system PATH untouched)
  and prints a one-line remedy. Falling back to the previous
  generation was considered and rejected: at the upgrade boundary
  the previous generation was never verified at all.
- Integrity failures are never suppressed: the hook drops
  `2>/dev/null` on the error path.
- `gale shell` and `gale run` treat integrity failure as fatal.

**Global scope has no gate, deliberately.** `~/.gale/current/bin`
reaches PATH from the user's shell rc with no gale invocation, so
there is nothing to hook. Global therefore relies on sync-time
enforcement alone, and a locked global plan additionally forbids
carry-forward, since a version carried into the generation that
the lock does not name is exactly what no later check would
catch. Enforcement itself stays uniform across scopes: a
scope-shaped exemption is a hole, and the global store and farm
are the shared state every project's integrity depends on.

The risk global actually carries is not upstream mutation, which
cannot alter already-installed bytes, but mutation of the shared
store and farm plus generations built before enforcement existed.

`docs/chezmoi.md:27` must also be corrected: it tells users the
global lockfile is machine-specific and regenerated by sync. Under
v1 sync never writes it, and platform is an artifact dimension, so
the file is no longer inherently machine-specific. Whether it
should now be tracked depends on the section 10 cascade: only an
all-binary closure is portable with certainty, and a
source-containing one only as far as its builds reproduce.

## 13. Legacy migration (decided)

**`gale lock --refresh` may replace a referenced canonical store
dir that has no provenance at all (pre-upgrade legacy).** User
decision, on the store invariant stated at the top of this
document: two sets of bytes for one `name@version-revision` are
never supported, so replacing unverifiable legacy bytes with the
one canonical artifact is not a divergence, it is convergence on
the only supported state.

The alternative considered and rejected was never replacing a
referenced dir. It was rejected because the cost is not a corner
case: every package installed before the upgrade is
unprovenanced, section 12 then refuses `PATH_add` for all of them,
plain `gale lock` cannot fix it (no provenance to read), and
`--refresh` cannot fix it either (canonical path occupied), so the
only exits are re-pinning to a different version-revision or
deleting store dirs by hand, which gale offers no command for.

The residual risk, raised by Codex and accepted: project A may
hold a committed v1 lock requiring hash Y while its canonical dir
is still legacy and unprovenanced; project B runs `--refresh`,
writes X, and A's already-open shells resolve through that path.
Project registration is best-effort, so the reference scan cannot
prove absence. Two things bound it. First, A's next activation
compares its lock against the now-present provenance and refuses
`PATH_add` with a conflict error, so the divergence is loud rather
than silent. Second, the alternative does not protect A's open
shells either: it preserves whatever unverifiable bytes are
already in the dir, verified by nobody, indefinitely.

**Upgrade day, priced as a whole.** These rules compose into a
chain no single section states: section 9 hard-fails the legacy
lock, `gale lock` then succeeds, section 12 still refuses
`PATH_add` because every pre-upgrade store directory is
unprovenanced, and the only exit is `--refresh` per project per
machine. For a from-source-first tool that is hours, and an
upgrade users route around with `--no-frozen` defeats the
feature.

A `gale migrate` command absorbs the common case. It is a
**constrained bulk replacement**, not a stamping pass: for every
binary-method package in the closure it refetches, stream-verifies,
and replaces the unprovenanced directory with the newly verified
staging directory, which is the only way the result is honest.
Refetching proves the *newly fetched* artifact; it says nothing
about whether the directory already on disk came from those bytes
or still matches them. Writing provenance beside an untouched
legacy directory would be the rejected unverified marker under
another name. Source-method packages cannot be migrated this way,
so `migrate` prints the precise list of them and what rebuilding
costs.

**Pre-revision source directories.** A source-method package
installed before revisions existed sits in a bare `<version>`
directory neither command may replace: migrate cannot refetch it,
and `--refresh` acts on the canonical path alone. This section
does not extend its exception to cover it. A record written beside
those bytes would be the unverified marker rejected above; and
rebuilding into the canonical path is a machine-wide relocation
whose proposed hash cannot be known before the build, so the
clearance this section requires before a destructive commit cannot
run in the order the binary case uses.

Migrate therefore reports these directories and says what is true
of each. One that no generation links and no config pins is a
`gale gc` candidate, decided by gc's own retention predicate
rather than by migrate's closure walk — the two disagree, and only
gc's answer may be reported as gc's behaviour. One reached as a
**declared root** converges through `gale sync`, which reinstalls
a directory with no dependency metadata into the canonical path
additively, destroying nothing; where that scope carries a legacy
lock the spelling is `gale sync --no-frozen`. A reinstall whose
closure cannot be attested commits without a record, which is not
a failure but the next step: converge the closure bottom-up, then
`gale lock --refresh`. One reached by several scopes, or **only as
a dependency**, has no command; gale says so rather than naming a
sequence that converges nothing.

`gale remove` followed by a reinstall is not offered as the
primary route. It deletes the manifest pin from every section that
carries it, losing host-overlay placement; it destroys the only
copy of bytes whose version the registry may no longer serve; and
across scopes it does not even fail loudly — the store entry
another scope references is kept and the reinstall then takes the
back-compat cache hit.

Silence about the dependency case is what this paragraph replaces.
A machine-wide rebuild relocation remains available as a future
extension of `gale migrate`, on the same enumerate-clear-replace
order, with the ordering caveat above; it is not authorized here.
The full evaluation is
[`docs/dev/proposals/prerevision-convergence.md`](../docs/dev/proposals/prerevision-convergence.md).

That makes `migrate` a constrained form of `--refresh` rather
than an alternative to it: same replacement mechanism, restricted
to unprovenanced binary-method directories, in bulk. Both are
explicit user actions under this section, which is the property
that matters.

Stamping a source directory with an "unverified" marker was
considered and rejected: if the gate accepts that marker,
fail-closed migration becomes an authorized unverified
environment, which is the property this whole design exists to
remove; if the gate rejects it, the marker buys nothing.

Scope limits. The exception applies only to a directory with no
provenance file at all, **and only when no other lock gale can
read disagrees with it**.

Before replacing, the operation consults every active lockfile it
knows about, the registered projects and the global scope alike, in
either schema. Replacement proceeds only where each either names the
same hash for that identity or does not reference it at all. A scope
gale knows about and cannot read vetoes the replacement: the scan
exists to find disagreement, and a lock that will not parse is the
one case where gale cannot tell whether it disagrees. A v1 claim is
read for the current platform; a legacy claim is platformless.

A legacy lock is consulted rather than skipped or failed closed on.
A legacy SHA is treated as a conservative, platformless byte claim:
equality proves agreement on bytes, and disagreement vetoes
replacement even though it may represent another platform and
therefore over-refuse. Over-refusal is safer than destroying bytes
another scope names. A legacy entry whose package matches and whose
non-empty sha256 differs is a conflict. An absent entry, an entry
for another version, and an empty sha256 contribute **no explicit
hash claim** — which is not the same as contributing nothing: the
same scope may still reference the directory through its active
closure, and a reference whose required bytes are unknown vetoes on
its own.

Versions compare through VersionMatches, so a bare "1.7" matches a
canonical "1.7-1". Skipping legacy locks would discard a genuine
statement about bytes; failing closed on every legacy lock would
deadlock upgrade day, since no scope can mint a v1 lock before a
replacement has happened.

A legacy lock records roots only, so a transitive dependency carries
no hash. That reference is still visible: a scope's active closure
is derived from its generation links and each directory's
`.gale-deps.toml`. A directory inside another scope's active closure
for which that scope supplies no hash is a known reference with
unknown required bytes, and refuses.

The closure scan must be authoritative about its own completeness,
which requires a reader stricter than either existing one.
`depsmeta.Has` is not sufficient: it uses `os.Stat`, so it follows
symlinks and says nothing about whether the target is a regular file
or parses. `FarmStoreDirsStrict` is not sufficient either: it
rejects unreadable metadata while `depsmeta.Read` treats missing
metadata as an empty closure. The scan therefore uses an
authoritative reader built on `os.Lstat` that requires a regular
file and strict semantic parsing. Missing, unreadable, non-regular
or malformed metadata all mean **closure incomplete**, and a legacy
active scope containing any such directory vetoes per-scope
destructive replacement generally. A valid empty file is an
explicitly recorded empty closure; an absent file is not. It is not
a verified leaf: strict parsing verifies the metadata's
representation, not its authenticity, and the file sits inside the
very unprovenanced directory being replaced.

**The coordinated escape.** Those rules make per-scope replacement
refuse on upgrade day, which is correct and would be circular
without an escape. `gale migrate` is therefore machine-wide, not
per-scope: the store is machine-wide already, so a per-scope migrate
was the wrong unit and is what manufactures the race. To qualify it
must enumerate every known scope and the entire relevant store
before any mutation; fail before replacing on unreadable state or an
explicit hash disagreement, where disagreement covers **every
recorded and proposed candidate hash**, so two scopes resolving
different artifacts for one identity conflict even when neither lock
records a hash; treat all scopes as participants against one
proposed machine-wide state rather than exempting them one at a
time; revalidate concurrent lock and registry changes before each
destructive commit; and cover every unprovenanced binary-method
directory, not merely the closure recoverable from legacy metadata.

`gale lock --refresh` stays per-scope and gains no all-scopes mode.
Refresh ratifies a recipe change, and applying that across unrelated
manifests, targets and dependency roles is too broad; a coordinated
refresh would deserve its own command and plan semantics. When
`--refresh` refuses because another scope is still legacy, its error
names the remaining sequence, which migrate does not complete on its
own: run `gale migrate`, rebuild the source-method packages it
lists, run plain `gale lock` in every remaining legacy scope, then
retry `--refresh`.

**What the scan does not prevent.** Replacement can break later
accesses through already-open shells or running processes, in any
scope, legacy scopes included. This is the same exposure §13 accepts
for open shells in the v1 case, and the alternative does not protect
them either: it preserves an unprovenanced canonical directory,
indefinitely.
What the scan prevents is a scope's recorded REQUIREMENT being
contradicted, not a running process being interrupted.

The initiating scope is exempt from its own veto, evaluated
against the lock the operation is about to write rather than the
one on disk. `gale lock --refresh` exists precisely to move an
identity from hash Y to X, so a scan that counted the initiating
lock's own Y would reject every refresh as a conflict with
itself. Other scopes keep their veto in full.

Without that narrowing, the residual risk the user accepted gets
quietly worse for global. The project case at least surfaces:
project A's next activation compares its lock against the new
provenance and refuses `PATH_add`. Global has no activation gate
at all (section 12), so a global lock naming hash Y for a
directory that project B replaced with X stays silently violated
until someone runs a global sync, which may be never.
Unregistered projects remain the accepted residual risk, since
nothing can see them. A directory with valid provenance that
disagrees with the lock is always a conflict error, never a
replacement, and `--refresh` is not offered as its remedy
(section 11). Content-addressed storage remains the complete fix
(section 14).

## 14. Sequencing

- **Phase 0, immediately:** correct `docs/ci-cd.md`, which today
  promises enforcement that does not exist.
- The rest ships as one feature. It may land as stacked PRs, but
  nothing releases with the plan half-threaded.
- Second follow-up: **a farm scoped to a generation or closure**
  rather than one shared tree (section 4). Content-addressing the
  store does not subsume it, since distinct store paths still
  collapse into one version-independent soname link.
- Follow-up issue filed at design time: **content-addressed
  store**. It is the complete solution to two hashes coexisting
  for one `name/version-revision`, but it touches generation, gc,
  depsmeta, farm, and every path assumption in the repo, so it is
  out of scope for #182. Until it lands, the store invariant at
  the top of this document holds by fiat: coexistence is a
  conflict error, not a supported configuration.

## 15. Acceptance tests

1. Fresh mismatch: the mismatching artifact is absent from the
   store, the generation and farm are unchanged, and earlier
   verified lock-matching deps may remain in the store.
2. An existing store dir whose provenance or `graph_digest`
   conflicts with the lock fails and preserves the directory
   byte-for-byte (locked sync performs no stale replacement).
3. Cached (`upToDate`) provenance mismatch fails.
4. Lockfile bytes never change during sync.
5. Locked transitive versions and hashes win over current recipes.
6. Missing package entry, missing platform entry, missing dep
   entry, legacy schema, and unknown schema version each fail with
   the documented message.
7. Legacy-lockfile upgrade error message.
8. A locked `binary` method cannot fall back to source.
9. `sync --build` rejected against a locked binary method.
10. Source-build SHA mismatch fails.
11. Ordinary (non-integrity) failure under a lock leaves the
    generation untouched; the same failure unlocked still rebuilds
    per issue #20.
12. The validation callback rejects drift introduced after
    per-artifact verification, under a single store-gen lock
    acquisition, with no generation or farm mutation.
13. A dependency whose SHA changed while its version-revision did
    not is detected via `graph_digest`, at a grandparent as well
    as a direct parent (`A -> B -> C`, C's SHA changes, A's digest
    changes). With reverse dependents' dirs absent or
    unreferenced, `--refresh` regenerates the closure; with a
    reverse dependent's dir referenced and validly provenanced,
    it stops with the conflict error.
14. Writer atomicity: a failed closure resolution leaves gale.lock
    byte-identical.
15. Writer targeting: each command in the section-11 table mutates
    only its named target, and wildcard profiles are untouched by
    concrete-host operations. `install pkg` with no `--host`
    covers both branches: the package already listed under
    `[hosts.<CurrentHost>.packages]` updates that overlay and
    `[targets.host."<CurrentHost>"]`; otherwise it writes
    `[packages]` and `[targets.default]`. `--host current`
    writes `[targets.host."<CurrentHost>"]`, not a target keyed
    on the literal string `current`.
16. Parallel-sync mismatch matches the section-4 commit model.
17. Cross-project conflict produces the conflict failure.
18. Same-platform host-overlay version divergence resolves per
    selector precedence.
19. A build-vs-runtime edge change is detected as a graph change.
20. The farm is untouched after a mismatch, exercising the late
    failure specifically: an earlier plan node verifies and would
    have redirected an existing farm symlink from version X to Y,
    a later node then mismatches, and the farm still points at X.
    Failing before any population could occur does not test this.
21. The activation gate detects a legacy lock with no mtime change
    (gale binary upgraded, no file modified).
22. `gale shell` and `gale run` refuse after an integrity failure.
23. Cross-scope runs: global, project, and `--host`.
24. A cyclic locked dependency graph fails plan construction with
    an error naming the cycle, before any install.
25. `graph_digest` serialization is stable: identical closures
    produce identical digests across processes, and permuting the
    input edge order does not change the digest, because
    serialization sorts edges. Changing any serialized field
    (method, platform, artifact SHA, manifest digest, edge kind,
    a dependency's own digest) does change it. Plan construction
    recomputes every digest and rejects a stored value that
    disagrees.
26. Legacy migration: `--refresh` replaces a referenced dir with
    no provenance. The accepted-residual case makes project A
    **unregistered**, so gale cannot see A's lock: A is then
    refused `PATH_add` with a conflict error at its next
    activation. That fixture matters, because a registered A
    would be vetoed instead, and the two expectations would
    contradict. Replacement is refused outright when any *other*
    readable active v1 lock, **including the global one**, names
    a different hash for that identity, and when a registered
    project is known but unreadable.
27. Downgrade guard: legacy `Read` refuses a freshly written v1
    lockfile, and a legacy-style sync leaves that file
    byte-identical. The test asserts refusal, not rewriting.
28. Cross-project farm conflict: an operation whose proposed farm
    mapping fails a **conflicting** external claim is refused
    before any farm mutation and before the generation swap,
    naming both versions. The fixture must make the external
    claim conflict; an agreeing claimant is test 37's territory.
    Covers **removal** (`farm.Depopulate` via `gale remove`) as
    well as retargeting, an **unlocked** sync violating a locked
    closure, and a project operation violating the **global**
    locked closure, not only two registered locked projects. A
    registered but unreadable project fails the check closed. A
    proposed closure that conflicts with itself is refused with
    no external claimant present.
29. Other-platform minting: `gale lock` succeeds and writes
    artifacts for every platform whose entire closure is
    binary-derivable, writes none for a platform whose closure
    contains a source-built node, and prints a diagnostic naming
    the blockers. Running on a skipped platform then produces the
    missing-platform error.
30. `gale add` under a lock leaves the lockfile untouched, and
    the next sync fails as stale-lock naming `gale lock` and
    `gale install` rather than a remedy that cannot work.
31. `gale lock` fetches or builds for a package with no installed
    provenance.
32. Exit codes: one test per class (3 integrity, 4 lock unusable,
    5 activation drift) across sync, install, update, lock,
    migrate, shell, run, remove, and env, asserting the specific
    code rather than merely non-zero. Code 4 covers a malformed
    or unknown-field lock, not only the enumerated schema cases.
33. `gale migrate` **replaces** unprovenanced binary-method
    directories with newly verified staging directories and lists
    source-method packages needing a rebuild. It never writes
    provenance beside a directory it did not replace, and no
    unverified marker is ever accepted by the activation gate.
34. A locked global plan refuses to carry forward a version the
    lock does not name.
35. Downgrade guard details: a v1 file with a missing or
    malformed guard is rejected; a real package entry carrying
    the guard field is rejected; the guard never reaches plan
    construction as a node.
36. `gale lock` populates an absent canonical directory but
    refuses to adopt an occupied unprovenanced one.
37. The guards do not deadlock their own scope. Positive cases,
    all expected to SUCCEED: a project updates a package whose
    old soname its own pre-swap generation still claims; a
    project removes such a package; a repair recreates a missing
    farm link with exactly the target every claimant expects; and
    `gale lock --refresh` supersedes the initiating lock's own
    hash for an identity. One more positive case, which is the
    one an implementation is most likely to get wrong: an
    external claimant that **agrees** with the proposed mapping
    is present and the operation still succeeds. Each negative
    twin requires a **conflicting** external claimant, not merely
    the existence of one.
