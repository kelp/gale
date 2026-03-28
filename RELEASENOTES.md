# v0.1.2

## New

- **gale gc** — removes unused package versions from the
  store. Versions not referenced by any gale.toml (global
  or project) are cleaned up. `--dry-run` previews what
  would be removed.

## Fixed

- **Binary install fallback** — when a GHCR binary pull
  fails (bad digest, 404, network error), the store
  directory is now cleaned before falling back to a source
  build. Previously partial downloads could break the
  fallback.
