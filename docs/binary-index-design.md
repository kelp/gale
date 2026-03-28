# Binary Index: Separating Recipes from Binary Metadata

## Problem

Binary distribution metadata (`[binary.<platform>]` sections)
lives inside recipe TOML files. This creates several problems:

- CI modifies recipe files on every build, polluting git
  history with auto-generated content mixed into
  human-authored recipes
- Auto-update must strip binary sections before updating
  version/sha256, then CI re-adds them after building
- The sed/awk cleanup for binary sections is fragile
  (accumulated blank lines, end-of-file edge cases)
- Pull-before-push races when CI commits binary sections
  while a developer pushes recipe changes
- Recipes are harder to read — binary URLs and hashes
  are noise for anyone writing or reviewing a recipe

## Proposal

Move binary metadata out of recipe files into separate
binary index files. One index file per recipe, living
alongside the recipe TOML.

### File Layout

```
recipes/
  j/
    jq.toml           # recipe (human-authored)
    jq.binaries       # binary index (CI-managed)
  r/
    ripgrep.toml
    ripgrep.binaries
```

### Binary Index Format

```toml
[darwin-arm64]
sha256 = "839a6fb89610eba4e06ba602773406625528ca55c30925cf4bada59d23b80b2e"

[linux-amd64]
sha256 = "a903b0ca428c174e611ad78ee6508fefeab7a8b2eb60e55b554280679b2c07c6"
```

The GHCR URL is derived from the recipe name and SHA256:
```
https://ghcr.io/v2/<repo>/<name>/blobs/sha256:<sha256>
```

No need to store the URL — it's deterministic. This makes
the index file minimal and avoids redundancy.

### How Gale Uses It

When installing a package:

1. Fetch the recipe TOML (for version, source, build steps)
2. Look for a `.binaries` file alongside the recipe
3. If a binary entry exists for the current platform,
   construct the GHCR URL from the SHA256 and download
4. If no binary entry or download fails, fall back to
   source build using the recipe

The `recipe.Recipe` struct gains a `Binaries` map
populated from the index file. The installer logic
is unchanged — it already checks for binary availability
before falling back to source.

### Registry Fetch

For remote installs (`gale install jq`), the registry
fetch needs to return both files. Options:

- **Two fetches**: fetch `jq.toml` then `jq.binaries`
  from the raw GitHub URL. Simple, one extra HTTP call.
- **Single fetch with fallback**: try `.binaries` first.
  If 404, no prebuilt binary available — proceed to
  source build.

The `.binaries` file is small (two lines per platform),
so the extra fetch is negligible.

For `--local` resolution, gale reads both files from the
sibling recipes directory.

### CI Workflow Changes

The `update-recipes` job simplifies:

**Before** (current):
1. Download metadata artifacts
2. For each recipe: sed to strip existing `[binary.*]`
   section, append new section
3. GraphQL commit with base64-encoded modified recipes

**After**:
1. Download metadata artifacts
2. For each recipe: write a clean `.binaries` file
   (overwrite, not append/strip)
3. GraphQL commit with only `.binaries` files

No more awk stripping, no more blank line accumulation,
no more accidentally modifying recipe content. The
commit only touches `.binaries` files, so it never
conflicts with recipe changes pushed by developers.

### Auto-Update Changes

The auto-update script currently strips binary sections
before updating a recipe version:

```bash
sed -i.bak '/^\[binary\./,/^$/d' "$file"
```

This line is deleted entirely. Auto-update only modifies
the recipe TOML (version, url, sha256). CI builds the
new version and writes a fresh `.binaries` file. No
interaction between the two.

### Migration

1. Add `.binaries` file support to gale's recipe parser
   (read both files, merge into Recipe struct)
2. Update CI to write `.binaries` files instead of
   modifying recipe TOMLs
3. Strip all `[binary.*]` sections from existing recipes
4. Gale falls back gracefully: if no `.binaries` file
   exists, check for `[binary.*]` in the recipe TOML
   (backwards compatibility during transition)
5. Remove fallback after all recipes are migrated

### What Stays the Same

- Recipe TOML format (minus `[binary.*]` sections)
- GHCR as the binary store
- ORAS for pushing binaries
- `actions/attest` for provenance attestation
- Source build fallback when no binary available
- Auto-update workflow logic (minus the sed strip)

## Tradeoffs

**Pros:**
- Recipes are clean, human-readable, human-authored
- CI never modifies recipe files
- No more binary section stripping/appending fragility
- Simpler auto-update (no binary section interaction)
- Smaller, more reviewable diffs
- `.binaries` files are trivially overwritten (no
  append/strip logic)

**Cons:**
- Two files per recipe instead of one
- Registry fetch needs an extra HTTP call for the
  binary index
- Slightly more complex file discovery in gale

## Open Questions

- Should the file extension be `.binaries`, `.binary`,
  `.bins`, or something else? `.binaries` reads well
  as a noun ("the jq binaries").
- Should gale support a single consolidated binary
  index file as an alternative (for registries that
  want to serve one file per package)?
- Should the `.binaries` file include the version to
  guard against stale indexes?
