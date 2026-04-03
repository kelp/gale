# Audit Report: cmd-install

## Summary

Four bugs found in the install and add commands. The most
impactful is a guaranteed panic in `formatDevVersion` triggered
by any repo tagged with a pre-release tag. A second structural
bug causes silent security bypass: two install code paths
construct `Installer` without a `Verifier`, meaning GHCR
binaries install without Sigstore attestation.

## Bugs Found

### BUG-1: formatDevVersion panics on pre-release tags

- **File:** `cmd/gale/install.go:394`
- **Severity:** High
- **Category:** edge-case
- **Description:** `formatDevVersion` splits the git-describe
  string on `"-"` with `SplitN(describe, "-", 3)` and then
  unconditionally indexes `parts[2]`. If the nearest tag is a
  pre-release like `v1.0.0-rc1`, `SplitN("1.0.0-rc1", "-", 3)`
  produces only 2 elements. Accessing `parts[2]` panics.
- **Code path:** `gale install <pkg> --path <dir>` where the
  source repo's nearest tag has a pre-release suffix. Triggers
  via `gitDevVersion` -> `formatDevVersion`.
- **Impact:** `gale install --path` crashes for any project
  whose most recent git tag contains a hyphen.

### BUG-2: installFromRecipeFile and installFromLocalSource skip attestation

- **File:** `cmd/gale/install.go:428` and `:318`
- **Severity:** High
- **Category:** security
- **Description:** Both functions construct `installer.Installer`
  without setting the `Verifier` field. The field defaults to
  `nil`. In `installBinary`, attestation is only checked when
  `v != nil && v.Available()`. A nil verifier silently skips
  the Sigstore check. The main registry path correctly sets
  `Verifier: attestation.NewVerifier()`.
- **Code path:** `gale install <pkg> --recipe <file>` or
  `gale install <pkg> --path <dir>` when the recipe references
  a GHCR binary.
- **Impact:** GHCR binaries installed via `--recipe` or `--path`
  bypass Sigstore attestation verification.

### BUG-3: recipeFileResolver computes wrong recipes root for non-bucketed paths

- **File:** `cmd/gale/recipes.go:119-128`
- **Severity:** Medium
- **Category:** logic
- **Description:** `recipeFileResolver` assumes the recipe file
  lives at `.../recipes/<letter>/<name>.toml` and derives the
  recipes root by calling `filepath.Dir` three times. When
  `--recipe` points to an arbitrary file path (e.g.,
  `/tmp/myrecipe.toml`), the three-level `Dir` walk produces a
  nonsensical path.
- **Code path:** `gale install <pkg> --recipe /path/to/file.toml`
  when the recipe has `[dependencies]`.
- **Impact:** Build dependency resolution fails with a misleading
  error for any recipe not inside a letter-bucketed repo.

### BUG-4: installFromGit with --recipe uses broken resolver for deps

- **File:** `cmd/gale/install.go:218-219`
- **Severity:** Medium
- **Category:** logic
- **Description:** Same root cause as BUG-3 applied to the
  `--git --recipe` code path. The resolver looks for deps in a
  nonexistent directory when the recipe file is not inside a
  letter-bucketed recipes repo.
- **Code path:** `gale install <pkg> --git --recipe /path/to/file`
  where the recipe has build dependencies.
- **Impact:** Dep resolution fails with misleading "no local
  recipe" error.

## Test Coverage Gaps

- **formatDevVersion missing pre-release tag case.** None of
  the four test cases covers a tag with a hyphen.
- **No test for --recipe with a non-bucketed path.**
- **installFromRecipeFile and installFromLocalSource have no
  test for Verifier presence.**
- **addToConfig failure modes untested.** Partial-write state
  on multi-package invocations is not covered.

## Files Reviewed

- `cmd/gale/install.go`
- `cmd/gale/add.go`
- `cmd/gale/install_test.go`
- `cmd/gale/context.go`
- `cmd/gale/paths.go`
- `cmd/gale/recipes.go`
- `internal/installer/installer.go`
- `internal/config/config.go`
