---
severity: medium
confidence: confirmed
commands: [search]
area: network-perf
---
## Summary
`gale search` bypasses the ETag cache and re-downloads the full
`index.tsv` on every invocation, despite the registry serving an
`ETag` and `Cache-Control: max-age=300`.

## Reproducer

```sh
$ curl -sI https://raw.githubusercontent.com/kelp/gale-recipes/main/index.tsv | grep -iE 'etag|cache'
Cache-Control: max-age=300
ETag: "dfb366c30aa54761251bcc28d58beb960c09206391c88c228864bfdae002b381"

$ export HOME=/tmp/gale-ro-audit-network-perf
$ rm -rf "$HOME/.gale/cache"
$ for i in 1 2 3 4 5; do
    { time /home/tcole/code/gale/gale search jq > /dev/null; } 2>&1 | grep real
  done
real    0m0.335s
real    0m0.321s
real    0m0.329s
real    0m0.325s
real    0m0.321s

$ find "$HOME/.gale/cache" 2>/dev/null || echo "no cache"
no cache
```

Five back-to-back searches, ~325 ms wall time each (≈ 1.6 s total),
zero cache entries written.

Source — `internal/registry/search.go:21-39`:

```go
func (r *Registry) Search(query string) ([]SearchResult, error) {
    url := r.BaseURL + "/index.tsv"
    client := &http.Client{Timeout: 30 * time.Second}
    resp, err := client.Get(url)   // <-- plain GET, not cachedGet
```

By contrast, `FetchRecipe`/`fetchBinaries`/`FetchRecipeVersion` all
route through `cachedGet` (`registry.go:107,147,199`) and revalidate
with `If-None-Match`.

## Expected vs actual
Expected: `gale search` issues a conditional GET; on 304 it returns
the cached `index.tsv` and the wall time drops to roughly TCP+TLS
RTT minus the 2.4 KB body transfer.
Actual: every invocation is a fresh full-body GET. No cache files
are written under `~/.gale/cache/registry/`.

## Suggested investigation
Route `Search` through `cachedGet(client, url, r.CacheDir)` the same
way `FetchRecipe` does. Confirm `parseIndex` handles a body that
came back via the cached-body path (it already operates on a
string, so trivially yes).
