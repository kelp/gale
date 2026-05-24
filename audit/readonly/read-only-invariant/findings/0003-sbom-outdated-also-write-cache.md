---
severity: low
confidence: confirmed
commands: [sbom, outdated, info]
area: read-only-invariant
---
## Summary
`gale sbom`, `gale outdated`, and `gale info <uninstalled>`
all go through the same registry resolver as `doctor` and
therefore create `~/.gale/cache/registry/<key>/{body,etag}` on
first run. All three are documented read-only, none hints at
network or disk writes.

## Reproducer
With the same fabricated scratch HOME used by finding 0002:

```sh
rm -rf .gale/cache
HOME=$PWD /home/tcole/code/gale/gale sbom
ls .gale/cache/registry | wc -l          # → 5 entries
rm -rf .gale/cache
HOME=$PWD /home/tcole/code/gale/gale outdated
ls .gale/cache/registry | wc -l          # → 2 entries

# Even more striking: a completely empty HOME, no ~/.gale.
HOME=$(mktemp -d) /home/tcole/code/gale/gale info jq
ls "$HOME"/.gale/cache/registry          # 2 entries created
```

Path diffs from the audit harness
(`/tmp/gale-ro-audit-readonly-invariant/out/{sbom,outdated}.paths.diff`)
show only `.gale/cache/` tree creations; no other state moves.

## Expected vs actual
Expected: `sbom` and `outdated` are listings. They should not
create directories under `~/.gale/` that survive the process.

Actual:
- `cmd/gale/sbom.go:99` calls `ctx.ResolveVersionedRecipe`
  for each installed package to enrich the listing.
- `cmd/gale/outdated.go:55` calls `ctx.Resolver(name)` per
  declared package.
- `cmd/gale/info.go:50` calls `newRegistry().FetchRecipe`
  when the package isn't in any config.
- All three flow into `internal/registry/cache.go:51 cachedGet`
  which persists every 200 OK with an ETag.
- `info` is the worst offender from a "principle of least
  surprise" angle: it bootstraps `~/.gale/` from scratch
  (`MkdirAll` of `cache/registry/`) even when the user has
  never run `gale init` or installed anything.

Compared to finding 0002, the severity is lower because
`sbom` and `outdated` already imply "go look at the world" —
network traffic is less surprising. The on-disk write is still
unexpected.

## Suggested investigation
- Same fix surface as finding 0002 — a read-only resolver
  toggle or `--dry-run` plumbing into `cachedGet` removes the
  side effect for the whole `doctor`/`sbom`/`outdated` family.
- `outdated` has a documented `--no-refresh` flag for tap
  refresh; consider an analogous `--no-cache` or just rolling
  the suppress-writes behaviour into `--no-refresh`. The
  command's `Long` text should mention what it touches.
- Cross-check `info` (not affected in this audit; its lookup
  doesn't reach the resolver) to confirm the matrix.
