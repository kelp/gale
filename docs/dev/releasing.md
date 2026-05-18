# Releasing Gale

Releases are immutable: once `v<version>` is published on
GitHub, the workflow refuses to overwrite it. To "fix" a
published release, cut a new patch version. To retry a
failed run before publish, see [Retrying](#retrying-a-failed-run).

## Flow

1. `just tag X.Y.Z` (local) updates `CHANGELOG.md` and tags.
2. `just release X.Y.Z` (local) pushes the commit + tag.
3. The `release` workflow runs on the pushed tag.
4. Workflow builds 4 platform binaries, drafts a release,
   then publishes the draft.

The local commands are thin: all build, signing, and
publishing happens in CI. The maintainer's job is to
prepare the `CHANGELOG.md` entry and push a tag.

## Step-by-step

```sh
# Pick the next version (semver: major.minor.patch).
# Latest released: see the v* tags or GitHub Releases.

# 1. Tag locally. Runs fmt + check first; refuses if
#    `v<version>` already exists.
just tag 0.16.0

# This:
#   - replaces "## Unreleased" in CHANGELOG.md with the
#     versioned heading + today's date
#   - commits the CHANGELOG change as "Release v0.16.0"
#   - creates the local tag v0.16.0

# 2. Push the tag + commit to trigger CI.
just release 0.16.0

# This:
#   - preflights: confirms the CHANGELOG section exists
#     (the workflow extracts release notes from it)
#   - pushes main + tag
#   - tells you to watch the Actions run

# 3. Watch the workflow.
gh run watch
# or open:
# https://github.com/kelp/gale/actions/workflows/release.yml
```

When the workflow completes, the release is live at
`https://github.com/kelp/gale/releases/tag/v0.16.0` with
four signed asset pairs (`gale-vX-<os>-<arch>.tar.gz` and
`.sha256`).

## What the workflow does

`.github/workflows/release.yml` triggers on `push` of any
`v*` tag (and supports manual `workflow_dispatch`).

- **build job**: matrix over `{darwin,linux} × {arm64,amd64}`.
  Builds with `CGO_ENABLED=0` and
  `-ldflags "-X main.version=<version>"`. Tars and
  sha256-sums each binary.
- **release job**: downloads all four artifact pairs,
  extracts release notes from `CHANGELOG.md` for the tag's
  version, deletes any leftover **draft** with the same
  tag (the immutability check kicks in here — if the
  matching release is already **published**, the job
  fails), creates a draft release with all eight assets,
  then flips the draft to published as `--latest`.

## Immutability

The release job has this guard:

```
state=$(gh release view "$TAG" --json isDraft -q .isDraft)
if [ "$state" = "true" ]; then
  gh release delete "$TAG" --yes
elif [ "$state" = "false" ]; then
  echo "Release $TAG is already published — refusing to overwrite."
  exit 1
fi
```

A leftover **draft** is safe to overwrite (rebuild,
retry). A **published** release is not. If you need to
republish a tag, you'd have to manually delete the GitHub
release first — but don't. The immutability is
intentional: anyone who downloaded the artifact gets a
stable, never-changed hash for that version. Ship a new
patch version instead.

## Retrying a failed run

If the workflow fails *before* the publish step (e.g. one
matrix job flakes on macOS), the release is still in
draft. You can retry without re-tagging:

```sh
just release-retry 0.16.0
```

This triggers the workflow via `workflow_dispatch` on the
existing tag. The release job will see the leftover draft
and overwrite it cleanly.

If the workflow failed *after* publish, you can't retry —
cut a patch version.

## Post-release

After the release is live, bump the recipe in
`gale-recipes`:

```sh
# In ../gale-recipes/
$EDITOR recipes/g/gale.toml      # set version = "0.16.0"
git add recipes/g/gale.toml
git commit -m "gale: 0.16.0"
git push
```

The recipes CI will build the new version, push binaries
to GHCR, and `gale install gale` from any machine will
pick up the update.

## CHANGELOG conventions

Each release section uses this shape:

```
## v0.16.0 — YYYY-MM-DD

### Added
- ...

### Changed
- ...

### Fixed
- ...

### Removed
- ...
```

Use whichever subset of `Added / Changed / Fixed /
Removed` applies. Keep an open `## Unreleased` heading at
the top between releases — `just tag` rewrites that exact
string to the versioned heading.

## When something goes wrong

- **`just tag` fails because the working tree is dirty**:
  commit or stash first. `just tag` runs `fmt` (which
  writes files) and `check` before tagging.
- **CHANGELOG section missing**: `just release` aborts;
  the workflow needs the section to extract release
  notes. Add a `## v<version> —` heading manually if you
  somehow tagged without `just tag`.
- **Workflow draft step fails**: usually a matrix build
  flake. Use `just release-retry`.
- **Workflow publish step fails after assets uploaded**:
  the release is still a draft. Open it in the GitHub UI
  and publish manually, or use `gh release edit
  v0.16.0 --draft=false --latest`.
- **You already published a version with a bug**: cut a
  patch. Don't try to overwrite the published release.
