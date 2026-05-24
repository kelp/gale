---
severity: medium
confidence: confirmed
commands: [doctor]
area: read-only-invariant
---
## Summary
`gale doctor` issues registry GETs while diagnosing stale
installs and writes the responses into a persistent
`~/.gale/cache/registry/` tree on first run. A health-check
command silently provisioning new on-disk state, including the
parent `~/.gale/cache/` directory itself, violates the
read-only invariant for the very command users are most likely
to reach for when something looks wrong.

## Reproducer
Scratch HOME with a fabricated store + global gale.toml (jq +
ripgrep declared and "installed"):

```sh
cd /tmp/gale-ro-audit-readonly-invariant   # see state.md harness
rm -rf .gale/cache
HOME=$PWD /home/tcole/code/gale/gale doctor
ls .gale/cache/registry | wc -l
```

After a single `doctor` run the scratch tree gains 20 new
paths:

```
.gale/cache/
.gale/cache/registry/
.gale/cache/registry/<sha256-of-url>/body
.gale/cache/registry/<sha256-of-url>/etag
... × 8 entries
```

The same effect reproduces with `gale -n doctor` — the global
`--dry-run` flag does not suppress these writes.

A second `doctor` invocation against the existing cache is
idempotent: the conditional-GET path returns 304 and nothing
under `.gale/cache/` is rewritten or re-touched. So the cost
is paid once per cache-miss URL, not per command invocation.

## Expected vs actual
Expected: `gale doctor` reads state, prints diagnostics, exits
with a status. No new files. Dry-run especially must be a pure
inspection.

Actual:
- `cmd/gale/doctor.go:54` registers
  `{"stale installs", checkStaleInstalls}` in the default
  check list.
- `checkStaleInstalls` (doctor.go:391) calls
  `ctx.cmdCtx.ResolveVersionedRecipe(name, version)` for every
  installed package.
- The resolver routes through the registry's `cachedGet`
  (`internal/registry/cache.go:51`), which on a 200 OK calls
  `writeCacheEntry` (cache.go:120) — that `MkdirAll`s
  `~/.gale/cache/registry/<key>/` and `atomicWrite`s a body +
  etag pair.
- `--dry-run` is checked at higher layers (config writes,
  store changes); the resolver/cache layer never sees it.

Per the file's own header comment, the cache layout is
shared with `internal/build` for source tarballs, so
"diagnostic" doctor runs end up bootstrapping the same disk
state a real install would.

## Suggested investigation
- The intentional-cache-write nature softens severity, but the
  fact that `doctor` is the first command users run on a
  half-broken system makes it the worst place to spawn new
  directories. Consider a "read-only resolver" mode that
  short-circuits `writeCacheEntry` when triggered from
  doctor/outdated/sbom/info paths.
- At minimum, honour the global `--dry-run` flag in
  `cachedGet`: when `cmd.Flags().Lookup("dry-run")` is true,
  fall back to `plainGet` without persisting.
- Worth checking whether the cache key includes any per-run
  variability that could cause unbounded growth — at a glance
  it's `sha256(url)`, so no, but the body sizes are bounded
  only by the recipe TOML size.
