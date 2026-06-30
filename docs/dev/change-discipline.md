# Change Discipline

Most gale regressions are not wrong logic in one function.
They are right logic in the wrong place in a pipeline — or
one subsystem preserving an invariant another subsystem
breaks. Recent examples: revision pinning vs bare config
(gh#65), sync staleness vs orphan store dirs (#136), gc
retention vs generation rebuild (#137).

This document tells humans and coding assistants **how to
trace before editing**. It is invariant- and pipeline-based
so it stays useful while interfaces change. For current
helper names and call sites, grep at change time — do not
trust memory or an old session.

See also: [`design.md`](design.md) (architecture),
[`revisions.md`](../revisions.md) (version identity),
[`refactoring-plan.md`](refactoring-plan.md) (package
dependency order), [`style-guide.md`](style-guide.md)
(LLM guardrails).

## Change tiers

Not every edit needs a full trace. Pick a tier before
touching code.

| Tier | Typical touch | Upfront trace |
|------|---------------|---------------|
| **0** | Docs, help text, log wording | Skim only |
| **1** | Single function inside one `internal/` package; no version, config, lock, generation, or farm semantics | Read package `_test.go`; grep the symbol |
| **2** | `cmd/gale/context.go`, version strings, lockfile writes, registry resolution, scope (global/project/host) | **Mandatory trace** (below) |
| **3** | Generation rebuild, farm populate, gc retention, sync staleness/reinstall, binary fixup | **Full pipeline trace** + name affected commands |

When unsure, round up. Tier 2–3 bugs are expensive; tier 0–1
can stay fast.

## Invariants

These change slowly. **Preserve them** even when renaming
helpers or reshaping interfaces.

**Store**

- Append-only under `~/.gale/pkg/`. Identity is
  `<name>/<version>-<revision>/` (see revisions doc).
- Installer writes the store only. It does not touch
  gale.toml, gale.lock, generations, or farm layout.

**Version identity**

- Bare `1.8.1` means revision 1. Suffix `-N` appears only
  when `N > 1`.
- A bare pin in gale.toml must not silently resolve to a
  different revision than the one being installed, switched
  to, or canonicalized for rebuild — even when a higher
  revision orphan remains on disk.
- Lockfile and registry resolution each have their own
  canonicalization rules; a fix in one path does not fix the
  others.

**Generation**

- Active environment is a **function of gale.toml** (plus
  store contents and farm closure), not a history of
  imperative symlink edits.
- `current` swap is atomic. Partial or broken PATH during
  rebuild is a bug.
- Farm must reflect the **full closure** of the active
  generation (config packages + runtime deps in
  `.gale-deps.toml`), not only direct config entries.

**Convergence**

- `gale sync` must converge to the declared state. It must
  not loop (rebuild/reinstall every run) or leave stale
  binaries on PATH.
- `gale gc` retention keys must agree with generation
  rebuild and config canonicalization.

**Scope**

- Global vs project vs host-scoped `[hosts.*.packages]`
  change which gale.toml is read and where entries are
  written. A fix in one scope must not corrupt another.

## Pipelines

Shapes are stable; helper names move. Trace the pipeline
first, then grep for the current functions.

### 1. Install and finalize

```
command (install / update / switch / …)
  → resolve recipe (@version, --recipe, --recipes)
  → installer.Install* (store write; binary or source path)
  → write gale.toml + gale.lock  (finalize / writeConfigAndLock)
  → rebuild generation
  → farm populate (during generation build)
```

Entry points: `cmd/gale/context.go` (`finalizeInstall`,
`writeConfigAndLock`, `configVersionForRecipe`), command
`RunE` handlers that call them.

### 2. Sync and staleness

```
sync
  → load gale.toml (+ lockfile)
  → for each package: resolve recipe, compare installed vs desired
  → reinstall when stale (recipe revision bump, orphan mismatch, …)
  → rebuild generation (even when nothing reinstalled — guard no-op churn)
```

Staleness must use the **same canonical version-revision**
that a reinstall would write, not “highest revision on disk.”

### 3. Version resolution

```
user input (@ver, @ver-rev, bare pin in toml)
  → resolveVersionedRecipe / registry FetchRecipe*
  → recipe.Package.Full() for user-facing identity
  → store path construction (name + version-revision dir)
```

Touches: `internal/registry/`, `internal/version/`,
`internal/recipe/`, `cmd/gale/context.go`.

### 4. Generation and farm

```
rebuildGeneration
  → read config packages (+ effective closure)
  → generation.Build (symlinks into store)
  → farm: populate / reconcile dylibs for closure
  → atomic current swap
```

Rollback and gc generation rebuild must leave farm consistent
with the target generation, not the rolled-from one.

### 5. GC and retention

```
gc
  → load config + project registry (~/.gale/projects)
  → compute retention set (active gens, deps metadata, canonical keys)
  → prune store / generations / scratch
  → optional generation rebuild when active gen links superseded orphan
```

Retention must match `storeRetentionKey` / canonicalize logic
in `context.go`, not ad-hoc `store.List` string compares.

## Pre-change trace (tier ≥ 2)

Produce this **in writing before editing code**. For tier 3,
include all six points in the first response.

1. **Invariant** — one sentence: what must remain true?
2. **Pipeline** — which stage(s) from the list above?
3. **Caller grep** — ripgrep changed symbols **and** concept
   keywords (see cheatsheet). List every caller you will
   affect or rely on.
4. **Command surface** — which CLI commands hit this path?
5. **Test anchors** — existing tests to extend; which tier of
   test (package vs `cmd/gale` vs integration)?
6. **Blast radius** — if wrong, what does the user see?
   (wrong binary on PATH, direnv timeout, silent skip, gc
   data loss, misleading error text, …)

Then TDD: failing test at the right layer, then fix.

## Grep cheatsheet

Grep at change time. Seeds are starting points — follow
callers and string literals from hits.

| Area | Grep seeds | Commands | Test families |
|------|------------|----------|---------------|
| Version identity | `Full()`, `canonicalize`, `stripNumericRevision`, `configVersionForRecipe` | install, switch, update, sync, gc | `audit_fix_U1_*`, `internal/version/`, `rebuild_generation_test.go` |
| Finalize path | `finalizeInstall`, `writeConfigAndLock`, `FinalizeRecipeInstall`, `updateLockfile` | install, update, switch, remove, pin | `context_test.go`, `audit_fix_U1_*`, `audit_fix_U11_*` |
| Sync / staleness | `runSync`, `Reinstall`, `isSuperseded`, `canonicalizeForBuild` | sync | `sync_*_test.go`, `audit_fix_U1_*` (gh#49) |
| Generation / farm | `rebuildGeneration`, `generation.Build`, `farm`, `Rollback` | sync, gc, generations, rollback | `generation/audit_fix_*`, `audit_fix_U2_*`, `rebuild_generation_test.go` |
| GC / retention | `storeRetentionKey`, `generationLinksSuperseded`, `projects.Register` | gc, doctor | `audit_fix_U4_*`, `gc_test.go`, `projects_*_test.go` |
| Registry / resolve | `resolveVersionedRecipe`, `FetchRecipe`, `pickVersion`, `composeResolvers` | install, update, outdated, search | `audit_fix_U12_*`, `registry/`, `recipes_test.go` |
| Installer / store | `installer.Install`, `installBinaryTo`, `Store.Remove`, `filelock` | install, remove, build | `installer/audit_fix_*`, `store/audit_fix_*` |
| Binary fixup | `FixupBinaries`, `patchelf`, `install_name_tool`, `RestorePrefixPlaceholder` | install, build | `internal/build/fixup_*`, `build_test.go` |
| Scope / paths | `resolveScope`, `galeDirForConfig`, `registerProject`, `resolveConfigPath` | env, init, doctor, most mutating cmds | `audit_fix_U11_*` (gh#96), `scope_*_test.go` |
| Remove correctness | `remove`, farm depopulation, host entries | remove | `audit_fix_U9_*`, `remove_test.go` |

### `audit_fix_*` tests

Named regression tests from systematic audits. Pattern:
`audit_fix_U<n>_test.go` (unit) or `audit_fix_D<n>_test.go`.
Comments cite gh issues. **Extend the matching family** when
fixing a regression in that area — do not add a one-off test
in an unrelated package if a pipeline bug is involved.

### Integration fixtures

`integration/fixtures/` — end-to-end scripts via testscript.
Use when the bug needs a real gale binary, store layout, and
generation swap. Slower; reserve for tier 3 or cross-command
flows.

### Test layer choice

| Bug shape | Prefer test in |
|-----------|----------------|
| Pure helper / parser | owning `internal/` package |
| Config ↔ generation skew, finalize, scope | `cmd/gale/` |
| Full install → sync → PATH | `integration/` |

A passing `internal/foo` test alone does **not** prove a
tier 3 pipeline fix.

## Package dependency order

When a change spans packages, work **leaf → root** so each
commit stays testable:

```
config, lockfile, store, output, recipe
  → generation, download
  → build
  → installer
  → registry, ghcr, farm, …
  → cmd/gale
```

Full wave plan: [`refactoring-plan.md`](refactoring-plan.md).

## Workflow summary

```
pick tier → (tier ≥2) write trace → grep callers →
extend right test family → red → green → just test / just lint
```

Interfaces will keep changing. Invariants and pipelines
should not. When design iteration renames a helper, update
the cheatsheet row if the **concept** moved — not every
mention in prose.
