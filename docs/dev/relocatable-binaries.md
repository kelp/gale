# Relocatable Binaries: Stop Mutating at Install

Status: proposed (2026-05-28)
Scope: `internal/build`, `internal/installer`, gale-recipes
rebuild

## Problem

The binary-install path verifies provenance, then rewrites
the binary on disk. The artifact we run is not the artifact
we verified.

`installBinaryTo` (`internal/installer/installer.go:563-669`)
runs in this order:

1. Fetch the GHCR `tar.zst` and verify its SHA256
   (`bin.SHA256`) — line 595.
2. Verify the Sigstore attestation against the raw archive
   bytes (`v.VerifyFile(archiveOut, …)`) — line 609. The
   attestation covers the published archive digest.
3. Extract, then **mutate** the tree: `FixupPkgConfig`,
   `RestorePrefixPlaceholderTo`, `RelocateStalePathsInTextFiles`,
   and — the one that matters — `RelocateStaleRpaths`
   (line 659), which runs `install_name_tool -rpath OLD NEW`
   on each Mach-O **and re-signs it** (`fixup_darwin.go:441`,
   `:448`).

So we attest artifact X and install artifact X′. The
installed binary hashes to neither `bin.SHA256` nor anything
Sigstore signed, and nothing reconciles the two afterward:

- `gale verify` checks the **remote** GHCR artifact
  (`VerifyOCI`), never the on-disk file.
- `gale audit` rebuilds from source and compares **archive**
  hashes; its help text already says "a mismatch does not
  indicate tampering." It never reads the installed binary.

The mutation is also non-reproducible — `install_name_tool`
plus ad-hoc `codesign` produce machine- and time-dependent
bytes — so there is no deterministic way to re-derive X′ from
X and check it. "Verify then mutate" should be "verify equals
run."

This also produces a hard install failure today: when the
destination gale home path is longer than the CI builder's
(`/Users/runner/.gale`), the rewritten rpath no longer fits
the Mach-O load commands and `install_name_tool` aborts, so
the package silently falls back to a source build. The perf
baseline harness, which installs into a long isolated
`/tmp/.../gale-perf-baseline.XXXX/.gale` home, hits this for
every Rust package with a dylib dep (ripgrep, bat, eza).

## Background: what's already relative

Most of gale's rpath handling is already relocatable. The
build-time fixup bakes relative paths:

- `FixupBinaries` (`fixup_darwin.go:127-134`): a package's
  own libs resolve via `@executable_path/../lib`
  (executables) and `@loader_path` (dylibs). Dylib install
  IDs become `@rpath/<name>` (line 95); in-prefix dep
  references become `@rpath/<base>` (line 115).
- Linux (`fixup_linux.go:78`): `$ORIGIN/../lib`.

The **only** absolute rpaths baked into a shipped binary
come from `AddDepRpaths` (`fixup_darwin.go:177`, Linux
`fixup_linux.go:95`):

- the **shared dylib farm** `~/.gale/lib` (`needed[farmDir]`,
  `fixup_darwin.go:271`), and
- per-dep **store lib dirs**
  `~/.gale/pkg/<dep>/<ver-rev>/lib`.

On CI these bake `/Users/runner/.gale/...`, and
`RelocateStaleRpaths` exists purely to rewrite that CI home
to the local one at install time. Remove the absolute paths
and the relocation step — and the binary mutation — goes
away.

## Goal

A shipped binary is **final**: byte-identical from CI build
through GHCR to the installed store path, on any machine, at
any `$HOME`/`GALE_HOME` depth. Install verifies and places;
it never edits or re-signs a correctly-built artifact.

Invariant after this change:

> If CI ships a Mach-O that is already ad-hoc signed and
> carries only relative rpaths, `gale install` performs zero
> writes to it. `sha256(on-disk) == sha256(attested member)`.

## Design

### 1. Bake the farm rpath relative

The store layout depth is invariant: a binary lives at
`<galeDir>/pkg/<name>/<version-revision>/bin/<exe>` and the
farm is always `<galeDir>/lib`. `<version>-<revision>` is a
single path component, so the depth from `bin/` to the farm
is a constant four levels regardless of version:

- macOS executables: `@executable_path/../../../../lib`
- macOS dylibs reached via the farm: `@loader_path` already
  works (see Risks), so dylibs keep their existing relative
  rpath.
- Linux: `$ORIGIN/../../../../lib`

`AddDepRpaths` computes the relative prefix from each file's
position under the prefix (it walks the whole tree, so
`libexec/.../bin` layouts get a deeper `../`), then emits the
farm rpath relative instead of `farmDir`.

### 2. Rely on the farm, drop per-dep store rpaths

The farm already symlinks every runtime dep dylib and is
reconciled on every generation rebuild (`farm.Repopulate`,
see `docs/revisions.md`). A single relative farm rpath
therefore resolves all dep dylibs and is version-agnostic —
which is the farm's whole purpose. We can stop adding the
per-dep absolute store-dir rpaths entirely. Fewer rpaths,
all relative, nothing to relocate.

(Link-time `-Wl,-rpath` to dep store dirs during the build
itself stays — that is build-phase only and never ships; see
the invariant comment at `fixup_darwin.go:159-176`.)

### 3. Rust: gale does the right thing, recipes stay trivial

A cargo recipe must stay as simple as:

```toml
[build]
steps = ["cargo install --path . --root ${PREFIX}"]
```

`cargo`/`rustc` ignore `LDFLAGS`, so the existing
`-Wl,-headerpad_max_install_names` that `compilerFlags`
injects (`build.go:511`) never reaches Rust links. That is
why C packages (jq) are fine and every cargo package with a
dylib dep is not.

Fix in `buildEnv` (`build.go`, near the `compilerFlags`
merge at line 747), keyed on `bc.System == "cargo"`: inject

```
RUSTFLAGS += -C link-arg=-Wl,-headerpad_max_install_names
```

so the post-build `install_name_tool` fixup has room to add
the relative rpath. (Appending to any inherited `RUSTFLAGS`,
not clobbering.) No recipe change. The farm-rpath baking in
step 1 then runs identically for cargo and autotools output.

Headerpad here is consumed at **build** time to produce the
final binary; it is no longer in service of install-time
surgery.

### 4. Installer: already correct — no code change needed

Investigation showed the install path needs **no change** for
relocatable binaries; #1 and #2 are the whole fix. Two things
were verified empirically:

- **Relative rpaths are already skipped.**
  `RelocateStaleRpaths`'s switch matches only `.gale/pkg/` and
  `.gale/lib`; a relative `@executable_path/…` / `@loader_path/…`
  rpath matches neither and hits `default: continue`. So a
  binary built under this design is never rewritten.
- **The re-sign is byte-idempotent, not a mutation.**
  `RelocateStaleRpaths` re-signs every Mach-O it walks. An
  ad-hoc signature (`codesign --sign -`) is a deterministic
  hash of the code pages with no timestamp, so re-signing an
  unchanged binary yields **byte-identical** output (confirmed
  by `TestRelocateStaleRpathsLeavesRelativeRpathBinaryByteIdentical`).
  `sha256(installed) == sha256(attested member)` holds.

`EnsureCodeSigned` stays as a safety net, already a no-op for
binaries that arrive signed. CI signs the final form (rpaths
baked before signing), so a correct artifact is signed once,
on CI, and never re-signed in a way that changes bytes.

**Legacy prebuilts (absolute rpaths) keep current behavior:**
`RelocateStaleRpaths` rewrites them, and if the rewrite cannot
fit the Mach-O header (long destination home), the install
fails over to a source build. That hard-fail → source-fallback
is the *correct* outcome — it preserves a working binary —
so it is deliberately left as-is. The branch can be deleted
once the catalog is rebuilt (see Migration).

## Migration

This alters how gale-built binaries are laid out, so it
follows the umbrella `Breaking-Change Workflow`:

1. **Land gale changes** behind tolerance: new gale bakes
   relative rpaths for source builds AND still relocates
   legacy absolute rpaths from old prebuilts. Tag + release.
2. **Bump `gale-recipes/recipes/g/gale.toml`** and
   `gale/gale.toml` to the new version.
3. **Rebuild the binary catalog** in gale-recipes with a
   `[package] revision` bump per package, so CI republishes
   relative-rpath artifacts to GHCR.
4. Old prebuilts keep working via the migration shim until
   replaced; new prebuilts need no relocation.
5. **Remove the legacy relocation branch** in a later gale
   release once no absolute-rpath prebuilts remain.

## Risks and open questions

- **`@loader_path` through farm symlinks (primary risk).** A
  binary resolves `libgit2.dylib` via the farm symlink
  `~/.gale/lib/libgit2.dylib`; libgit2 then resolves its own
  transitive deps via its rpath. How dyld computes
  `@loader_path` across a symlink hop has shifted across
  macOS versions. Mitigating evidence: gale already relies on
  `@loader_path` for in-farm dylibs in production, so that
  hop is proven; the only new hop is binary→farm. Validate
  before the catalog rebuild (see below).
- **Depth for non-`bin/` layouts.** Compute the relative
  prefix per file from its position under the store root, not
  a hardcoded `../../../../`. Covered by walking the tree as
  `AddDepRpaths` already does.
- **Headerpad sufficiency.** `-headerpad_max_install_names`
  reserves generous space; a single relative rpath is well
  within it. Confirm on the largest Rust binary in the
  catalog.
- **Text-file fixups remain.** `.pc`/`.la`/scripts still get
  rewritten at install (placeholders, CI store paths). These
  are not the executable and not in the runtime code path of
  a CLI invocation, so they are out of scope here; can be
  addressed separately via placeholders.

## Validation plan

Prototype on ripgrep (Rust + pcre2 dylib dep) before any
catalog work:

1. Build ripgrep with gale carrying steps 1-3 (relative farm
   rpath + cargo headerpad).
2. Record `sha256` and `otool -l` of the built `rg`; assert
   every LC_RPATH is relative (`@executable_path/…`).
3. Install into a deliberately **deep** isolated home
   (`HOME=/tmp/.../really/deep/path/.gale`, longer than
   `/Users/runner`).
4. Assert: `sha256(installed rg) == sha256(built rg)` — i.e.
   install mutated nothing — and `rg --version` plus a real
   search run (exercises pcre2 via the farm).
5. Repeat for bat/eza (libgit2, transitive deps) to exercise
   the `@loader_path`-through-symlink case directly.

Success = stable hash across install + working binary at a
home depth that breaks today's relocation.

## Rollout checklist

- [x] gale: cargo `RUSTFLAGS` headerpad injection (TDD)
- [x] gale: relative farm rpath in `AddDepRpaths` (TDD)
- [x] gale: byte-identity regression guard for
      `RelocateStaleRpaths` (no code change needed — see §4)
- [ ] ripgrep/bat/eza prototype passes validation plan
- [ ] release gale; bump both `gale.toml`s
- [ ] gale-recipes: revision-bump + rebuild binary catalog
- [ ] later: delete legacy relocation branch
