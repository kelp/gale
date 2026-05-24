---
severity: medium
confidence: confirmed
commands: [outdated]
area: read-only-invariant
---
## Summary
`gale outdated --no-refresh` and `GALE_OFFLINE=1 gale outdated`
both still issue registry GETs and write
`~/.gale/cache/registry/...` on first run. The flags suppress
git-fetch on configured taps but do not propagate into the
HTTP registry resolver, so the user-visible "offline / no
refresh" promise is half-implemented.

## Reproducer
```sh
cd /tmp/gale-ro-audit-readonly-invariant
rm -rf .gale/cache
HOME=$PWD /home/tcole/code/gale/gale outdated --no-refresh
ls .gale/cache/registry | wc -l                   # → 2
rm -rf .gale/cache
HOME=$PWD GALE_OFFLINE=1 /home/tcole/code/gale/gale outdated
ls .gale/cache/registry | wc -l                   # → 2
```

Both invocations make real network requests to
`raw.githubusercontent.com/kelp/gale-recipes/main/...` and
persist the responses. Disconnect a network cable mid-run and
both fail with HTTP errors instead of producing offline output
from local config alone.

## Expected vs actual
Expected, per `--no-refresh`'s help text ("Skip refreshing
configured recipe taps before resolving") and per the
documented `GALE_OFFLINE=1` semantics (`tapsOfflineMode` in
`repo_resolver.go:181`): when either is set, `outdated`
either uses local cache only or refuses to call out to the
network. In particular, no new disk state under `~/.gale/`.

Actual:
- `cmd/gale/outdated.go:32` checks
  `!tapsOfflineMode(outdatedNoRefresh)` and skips
  `refreshConfiguredTapsDefault` accordingly — this is the
  only consumer of the flag.
- The downstream `ctx.Resolver(name)` call at line 55 still
  flows into the HTTP registry resolver
  (`internal/registry/registry.go`), which has its own
  `CacheDir` and conditional-GET logic — unaware of
  `GALE_OFFLINE` or `--no-refresh`.
- `internal/registry/cache.go:51 cachedGet` issues a real
  network request and on 200 OK calls
  `writeCacheEntry(...)` → `MkdirAll` + `atomicWrite`.

The naming is part of the problem: `tapsOfflineMode` is
about *tap fetch* offline-ness, but the user reads
`GALE_OFFLINE=1` as global offline-ness. Two related but
distinct concepts share one knob.

## Suggested investigation
- Plumb `GALE_OFFLINE` (or a renamed `GALE_NO_NETWORK`) into
  `registry.Registry` so `cachedGet` falls back to the
  on-disk body if present and returns an error otherwise,
  without issuing the HTTP request.
- Either extend `--no-refresh` semantics to "no network at
  all" or add a parallel `--no-network` flag — but pick one
  and document it. The current state is the worst of both
  worlds: the flag implies offline but the registry still
  phones home.
- Worth grepping for every callsite of `cachedGet` and
  `cfg.Registry` to confirm none of them silently bypass the
  new gate.
