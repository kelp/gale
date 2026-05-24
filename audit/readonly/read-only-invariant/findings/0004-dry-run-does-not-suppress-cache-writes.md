---
severity: medium
confidence: confirmed
commands: [doctor, sbom, outdated]
area: read-only-invariant
---
## Summary
The global `-n` / `--dry-run` flag advertises a "show what
would happen" mode, but the registry cache writes triggered
from read-only commands ignore it. `gale -n doctor` against a
fresh `~/.gale/` creates `~/.gale/cache/registry/...` exactly
as a non-dry-run does.

## Reproducer
```sh
cd /tmp/gale-ro-audit-readonly-invariant
rm -rf .gale/cache
ls .gale/cache 2>&1                     # No such file or directory
HOME=$PWD /home/tcole/code/gale/gale -n doctor
ls -la .gale/cache/registry             # populated
```

The cache layout `MkdirAll(entryDir, 0o755)` +
`atomicWrite(body)` + `atomicWrite(etag)` is created
unchanged whether `-n` is set or not. The same applies to
`gale -n sbom` and `gale -n outdated`.

## Expected vs actual
Expected: `--dry-run` is the user's escape hatch for "look
but don't touch." Any persistent write under `~/.gale/` —
even an "intentional" cache write — should be suppressed.

Actual: `--dry-run` is wired into the install/sync/update
write paths (config + lockfile + generation) but not into
the registry layer. `cachedGet`
(`internal/registry/cache.go:51`) has no awareness of the
flag; it persists whenever the cacheDir argument is non-empty
and the server returned a fresh ETag.

The doctor entry point creates its own `cmdContext` at
`cmd/gale/doctor.go:79` (`newCmdContext("", false, false)`) —
the second `false` is presumably "dry-run", so the resolver
built inside this context does not even know it's in a
dry-run.

## Suggested investigation
- `newCmdContext` already takes a dry-run boolean. Propagate
  it into the `registry.Registry` (or the cacheDir passed to
  `cachedGet`) and short-circuit `writeCacheEntry` when set.
- Audit every other place a fresh `cmdContext` is created
  inside a command (instead of taking the one cobra sets up) —
  these all risk losing dry-run state.
- Once dry-run propagates, re-run the harness in
  `audit/readonly/read-only-invariant/` to confirm zero
  on-disk effects.
