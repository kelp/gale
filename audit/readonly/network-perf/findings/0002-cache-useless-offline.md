---
severity: high
confidence: confirmed
commands: [info, outdated, sbom, doctor]
area: network-perf
---
## Summary
The ETag cache only serves content on `304 Not Modified`. When the
registry is unreachable (DNS failure, refused connection, timeout),
`cachedGet` returns the network error verbatim and the cached body
on disk is never consulted. Every cache-backed read-only command
becomes hard-fail on a flaky connection even when a perfectly valid
cached copy exists locally.

## Reproducer

```sh
$ export HOME=/tmp/gale-ro-audit-network-perf
$ rm -rf "$HOME/.gale/cache" "$HOME/.gale/config.toml"

# Populate cache against the real registry.
$ /home/tcole/code/gale/gale info jq > /dev/null
$ ls "$HOME/.gale/cache/registry/" | wc -l
2                                   # entries are present

# Now point at a refused address; cache is still on disk.
$ cat > "$HOME/.gale/config.toml" <<'EOF'
[registry]
url = "http://127.0.0.1:1"
EOF

$ /home/tcole/code/gale/gale info jq
Error: jq: fetch recipe jq: Get "http://127.0.0.1:1/recipes/j/jq.toml": \
  dial tcp 127.0.0.1:1: connect: connection refused
```

Cached body at
`~/.gale/cache/registry/<sha>/body` is identical to what would
satisfy the request — but never read.

Source — `internal/registry/cache.go:51-101`:

```go
resp, err := client.Do(req)
if err != nil {
    return nil, err   // <-- bails before any read from bodyPath
}
defer resp.Body.Close()

switch resp.StatusCode {
case http.StatusNotModified:
    body, rerr := os.ReadFile(bodyPath) // <-- only path that uses cache
    ...
}
```

A `client.Do` failure (DNS, ECONNREFUSED, deadline) is propagated
without inspecting `bodyPath`/`etagPath`.

## Expected vs actual
Expected: on transport-level failure, fall back to the cached body
if present (mirroring how browsers behave with stale-while-error
semantics, or as Homebrew does with its `--offline` flag).
Actual: cache exists, is current, is never used; user gets a hard
network error from every cache-backed command. Combined with the
30 s `http.Client.Timeout`, a flapping connection turns `gale
outdated` into a multi-minute wait that returns nothing useful
(see `0003-outdated-serial-timeout.md`).

## Suggested investigation
- Decide policy: stale-on-error always, or only under
  `GALE_OFFLINE=1` (already recognised by `tapsOfflineMode`)?
- If always-on, audit caller expectations: `FetchRecipeVersion`
  serves pinned data, so stale is generally safer than no-answer;
  `Search` index is unsorted and bounded — also safe to serve
  stale.
- `defaultCacheDir()` returns "" when `os.UserHomeDir` fails; the
  fallback path skips the cache entirely. Confirm CI/container
  environments hit this path.
