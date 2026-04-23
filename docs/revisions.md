# Revisions, the Shared Library Farm, and Staleness

Gale identifies packages by upstream version (`1.8.1`) and
recipe revision (`-2`). Together they form the store identity
`<name>/<version>-<revision>/`. This document explains when a
revision bump happens, how the shared dylib farm lets one
binary absorb dep upgrades without a rebuild, and how gale
detects that an installed package has gone stale.

Shipped in v0.12.0; `gale update` was taught to honor revision
bumps in v0.12.2. See `CHANGELOG.md` for the rollout.

## What a revision is

A recipe can declare an integer `revision` under `[package]`:

```toml
[package]
name = "jq"
version = "1.8.1"
revision = 2
```

If omitted (or `<= 0`), the revision defaults to `1`. Recipe
parsing enforces this at `internal/recipe/recipe.go:40-59` and
`:179-184`.

The user-facing identity comes from `Package.Full()`
(`internal/recipe/recipe.go:53-59`):

- `revision = 1` (or omitted) → `Full()` returns the bare
  version, `"1.8.1"`.
- `revision = 2` → `Full()` returns `"1.8.1-2"`.

The bare form is the revision-1 form; the suffix only appears
when a recipe has been bumped past 1.

### When to bump

Bump the revision when **the binary should change but the
upstream source didn't**. Typical triggers:

- Build flag change (adding `--disable-docs`, switching from
  shared to static linking).
- Post-install cleanup (dropping a shared dylib jq bundles
  incidentally — see `recipes/j/jq.toml`).
- Dep soname changes that require re-linking dependents.
- Toolchain upgrade in CI that produces a materially different
  binary.

Do **not** bump the revision for doc-only edits or comment
cleanups. Revisions trigger rebuilds across every platform and
visible `outdated` entries on user machines.

## Addressing packages

`gale install <name>[@<version>[-<revision>]]` — three forms
the resolver understands:

```sh
gale install jq               # latest version, highest revision
gale install jq@1.8.1         # version pinned, highest revision of 1.8.1
gale install jq@1.8.1-2       # version and revision both pinned
```

The resolver picks the highest revision for a bare version
match in the registry's `.versions` index
(`internal/registry/registry.go:316-350`,
`cmd/gale/context.go:346-370`). For the third form to work,
CI must have published a revision-qualified entry in
`.versions` — covered below.

`gale.toml` entries may be bare or revision-qualified. The
installer writes store directories at the canonical
`<version>-<revision>` path regardless, so a bare entry still
resolves cleanly via the bidirectional resolver.

## Store layout

`~/.gale/pkg/<name>/<version>-<revision>/`. v0.12.0 and later
always write canonical dirs. Existing bare dirs from before
the revision rollout stay addressable through back-compat
lookups in `internal/store/store.go:54-86`:

- A bare request (`jq@1.8.1`) prefers `jq/1.8.1-1/` if it
  exists, falling back to bare `jq/1.8.1/`.
- A canonical request (`jq@1.8.1-1`) falls back to bare
  `jq/1.8.1/` if the canonical dir isn't there.

The bidirectional fallback is transitional. Once `gale sync`
has migrated every pre-revision install — it routes stale
packages through `Reinstall` to force a canonical write — the
bare dirs are `gale gc` candidates.

## The shared dylib farm

`~/.gale/lib/` is a flat directory of symlinks into the store,
keyed by versioned library basename (`libcurl.4.dylib`,
`libpcre2-8.so.0`). Binaries built under v0.12.0+ carry an
extra rpath to the farm alongside their per-version rpaths.
When a dep is upgraded to a SONAME-compatible revision, the
farm symlink flips to point at the new store dir and every
dependent binary keeps resolving through `@rpath` without a
rebuild. Implementation at `internal/farm/farm.go:74-327`,
invoked from the installer at
`internal/installer/installer.go:377-381`.

Farm invariants:

- One package claims each versioned basename. If two different
  packages both install `libonig.5.dylib` into the farm, the
  second install fails with a conflict error
  (`internal/farm/farm.go:110-127`). This is a recipe-level
  bug and must be fixed — usually by dropping the duplicate
  from the package that ships it incidentally (see the jq
  recipe's libonig cleanup).
- The farm is reconciled every time the generation is rebuilt
  (`internal/generation/generation.go:91-95` →
  `farm.Repopulate`). `gale doctor` detects drift (broken
  symlinks, missing entries) at `cmd/gale/doctor.go:250-280`
  and `--repair` triggers the rebuild path.

### Pre-farm prebuilts are brittle

GHCR prebuilts produced before the farm-rpath feature landed
**do not carry** `~/.gale/lib/` in their dyld search list.
They rely only on per-version rpaths, so a dep revision bump
that relocates a dep's store dir orphans them. Rebuilding
that dependent from a v0.12.0+ recipe embeds the farm rpath
and future upgrades work. There is no retrofit — only a
rebuild embeds new rpaths into an existing binary.

## Staleness and `.gale-deps.toml`

Each installed package's store dir carries a
`.gale-deps.toml` file listing the resolved dep closure the
binary was linked against. Format in
`internal/depsmeta/depsmeta.go`; writer path at
`internal/installer/installer.go:392-400`; stale-check logic
at `internal/installer/deps_metadata.go:46-149`.

Write ordering:

1. `gale build` writes the file into the archive prefix at
   build time with the exact version-revision of every linked
   dep.
2. At install, the installer preserves the archive's copy if
   present; otherwise it falls back to locally-resolved dep
   versions. The archive copy is always authoritative.

`IsStale` fires when:

- The file is missing entirely (pre-revision install).
- A declared runtime dep isn't recorded in the metadata.
- A recorded version-revision doesn't match the current
  recipe, unless the dep declares a version-range constraint
  that covers it:

```toml
[dependencies]
runtime = [
  { name = "expat", version = ">=2.7.5-2" },
]
```

A range constraint lets a recipe opt out of staleness for
SONAME-compatible revision bumps. Bare-string deps (`runtime
= ["expat"]`) remain strict: any revision bump of the dep
invalidates the dependent.

Constraints are also enforced at dep-install time
(`internal/installer/installer.go:installDepsInner`). When
a constrained dep resolves to a version that doesn't satisfy
the expression — e.g. the recipe pins `openssl >=3.6.0-1`
but the registry's current `openssl` recipe is `3.5.4-1` —
the install fails with a message naming the dep, the
constraint, and the resolved version. This covers the gap
where the staleness check only fires after install; a
constraint violation at resolve time stops the install
before it starts.

`gale sync` reinstalls stale packages automatically.
`gale doctor` surfaces them with a hint to run sync.

## gc and revision matching

`cmd/gale/gc.go:105-113`'s `isReferenced` helper expands a
bare `gale.toml` entry (`jq = "1.8.1"`) to also match the
canonical store dir (`jq/1.8.1-2/`). This mirrors the
resolver's back-compat logic in reverse — without it, gc
reaps live store entries and breaks every binary the
generation symlinks into (fixed in v0.12.1 after we hit it
live).

When gc runs, it keeps:

- Exact matches on `name@version` from any loaded gale.toml.
- Canonical matches where the store dir's `<version>-<N>`
  shares a base with a bare `gale.toml` entry.

Everything else is considered orphaned.

## Soft migration

The first `gale sync` after upgrading to v0.12.0+ reinstalls
every pre-revision install (bare store dir, no
`.gale-deps.toml`). That's expected — each one needs the
canonical-layout rewrite and a fresh deps-metadata file. On a
machine with 50+ global packages it can take a while. Sync
routes stale packages through `Reinstall` rather than the
regular `Install` path so bare dirs don't block the migration
via back-compat fallback.

## `.versions` index and revisions

`.versions` files in gale-recipes track the known versions of
each recipe along with the commit hash that landed them.
Format is one line per version:

```
1.7.1 e1de3eb6462b51d417989f3f6e410fcb9a3b956a
1.8.1 8cbb13977efd31d26c0ce050f5d50ad7214a3af4
1.8.1-2 a70c88d...
```

CI appends a `<version>-<revision>` line when a recipe ships
at revision > 1 (v0.12.2+ and the matching gale-recipes
workflow change). Older `.versions` files still list only
bare versions; the registry picker tolerates both formats
(`internal/registry/registry.go:316-350`).

Known edge: `gale install foo@1.2.3-2` fails with "version
not found in registry" unless the `.versions` file has a
matching revision-qualified entry. Until CI has rewritten
every recipe's `.versions` via the sweep (see the gale-recipes
rebuild pass referenced in that repo's CHANGELOG), you may
need to target a bare `foo@1.2.3` and let the resolver pick
the highest revision it can find.

## `.binaries.toml` resolved-closure index

Alongside the `sha256` for each platform, CI also writes the
resolved (name, version, revision) closure the prebuilt was
linked against:

```toml
version = "2.53.0-2"

[darwin-arm64]
sha256 = "..."
deps = [
  { name = "curl", version = "8.11.0", revision = 1 },
  { name = "openssl", version = "3.6.1", revision = 2 },
]
```

This is informational only at install time — the archive's
own `.gale-deps.toml` (written by `gale build` and preserved
through the tarball) remains the authoritative record the
installer consults for staleness. The registry-level `deps`
block lets `gale info`, audit tooling, and reviewers
inspect closures without fetching and extracting the
archive. Older `.binaries.toml` files (pre-C4) carry no
`deps` field; gale's parser returns an empty closure in
that case.

CI extracts `deps` from the archive's `.gale-deps.toml` in
`gale-recipes/.github/workflows/build.yml` (the "Save
build metadata" step), then emits the array-of-tables into
`.binaries.toml` in the "Write .binaries.toml files" step.

## Related reading

- `CHANGELOG.md` — v0.12.0 and v0.12.2 entries for the
  revision rollout.
- `gale-recipes/CLAUDE.md` — recipe-author guidance on when
  to bump `revision`.
- `docs/dev/design.md` — overall store and generation model.
