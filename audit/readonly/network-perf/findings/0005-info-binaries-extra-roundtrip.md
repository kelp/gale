---
severity: low
confidence: confirmed
commands: [info, outdated, sbom, doctor]
area: network-perf
---
## Summary
Every call to `Registry.FetchRecipe` makes a second HTTP request
to `<name>.binaries.toml` when the parsed recipe has no inline
`[[binary]]` entries — even when the recipe is purely
source-built and a `.binaries.toml` file has never existed.
Combined with the lack of a "negative cache" for 404s, every
read-only command that resolves recipes pays a second RTT per
package per invocation.

## Reproducer

Against a logging local server that 200s the `.toml` and 404s the
`.binaries.toml`:

```sh
$ /home/tcole/code/gale/gale info fakepkg
Name:    fakepkg
Version: 1.0 (latest)
...
$ cat /tmp/server.log
GET /recipes/f/fakepkg.toml If-None-Match=-
GET /recipes/f/fakepkg.binaries.toml If-None-Match=-

# Re-run:
$ /home/tcole/code/gale/gale info fakepkg > /dev/null
$ cat /tmp/server.log
GET /recipes/f/fakepkg.toml If-None-Match="v1-fakepkg"   # 304 — cheap
GET /recipes/f/fakepkg.binaries.toml If-None-Match=-     # still uncached, full 404 RTT
```

Caching is gated on `etag != ""` in `cache.go:94`; a 404 carries no
ETag, so the negative result is never recorded.

Source — `internal/registry/registry.go:118-129`:

```go
// If the recipe has no inline binary entries, try to
// fetch a separate .binaries.toml file.
if len(rec.Binary) == 0 {
    idx, err := r.fetchBinaries(name)
    ...
}
```

For real-world `outdated` over 8 packages, this means 8 *extra*
requests every run. Wall time measured against the real registry:

```sh
real    0m1.240s    # cold, 16 requests
real    0m0.380s    # warm, still 16 requests (8 of them full GETs)
real    0m0.374s    # warm
```

The 8 "always full GET" requests are what holds the warm wall time
at 380 ms instead of dropping near to RTT × parallelism.

## Expected vs actual
Expected: either
(a) honour `If-Modified-Since` against a recorded 404 timestamp so
    repeat fetches return 304 quickly, or
(b) treat the recipe `.toml` as authoritative — recipes can declare
    "no separate binaries index" in-band so the second fetch is
    skipped, or
(c) batch the listing once per process via the existing repo index
    so `.binaries.toml` doesn't need a probe-per-package.
Actual: every recipe with `len(rec.Binary) == 0` pays a full 404
RTT on every invocation of every cache-backed command.

## Suggested investigation
Smallest fix: persist a "last-checked 404" sentinel under
`<CacheDir>/registry/<hash>/404` and skip the request within a
short TTL (5 min mirrors the `Cache-Control: max-age=300` the
GitHub raw CDN already uses for the recipe index).

Cleaner fix: define a recipe convention (`[binaries] empty = true`
or move binary sections inline universally) so `fetchBinaries` is
only called when the recipe explicitly opts in.
