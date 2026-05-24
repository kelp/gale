---
severity: medium
confidence: confirmed
commands: [doctor]
area: network-perf
---
## Summary
`gale doctor` is documented as "Diagnose setup issues" and presents
as a local-state check, but it issues one recipe fetch per package
in the effective gale.toml as part of orphan/stale detection. On
an unreachable registry, `doctor` blocks for `30 s × N` and emits
no warning about what it's actually waiting on.

## Reproducer

```sh
$ export HOME=/tmp/gale-ro-audit-network-perf
$ cd "$HOME"
$ cat > "$HOME/.gale/config.toml" <<'EOF'
[registry]
url = "http://192.0.2.1"
EOF
$ cat > "$HOME/.gale/gale.toml" <<'EOF'
[packages]
jq = "1.6"
EOF
$ { time /home/tcole/code/gale/gale doctor > /dev/null; } 2>&1 | grep real
real    0m30.104s   # 1 package × 30 s timeout
```

Against a logging local server with 2 packages, 4 requests are
emitted (no surprise: 2 × `.toml` + `.binaries.toml`):

```
GET /recipes/b/bat.toml If-None-Match=-
GET /recipes/b/bat.binaries.toml If-None-Match=-
GET /recipes/j/jq.toml If-None-Match=-
GET /recipes/j/jq.binaries.toml If-None-Match=-
```

Call chain — `cmd/gale/doctor.go:527-552` → `cmd/gale/gc.go:194-213`
→ `cmd/gale/gc.go:312-` (`expandRuntimeDeps`):

```go
// gc.go
func expandRuntimeDeps(
    s *store.Store,
    resolver installer.RecipeResolver,   // <-- this is FetchRecipe
    referenced map[string]bool,
) {
    ...
    for len(queue) > 0 {
        name := queue[0]
        ...
        r, err := resolver(name)         // network on every iteration
```

`doctor.checkStaleInstalls` adds further `ResolveVersionedRecipe`
calls for every *installed* package (see `doctor.go:412-413`).

`doctor` has no `--offline` flag and does not honour `GALE_OFFLINE=1`
(only `outdated`/`update` consult `tapsOfflineMode`).

## Expected vs actual
Expected: either `doctor` is purely local (preferred for the
"diagnose setup" framing — orphan detection over the local store
alone is still useful), or it gates its network calls behind
`GALE_OFFLINE=1` and a `--offline` flag, and prints a step header
("Checking orphans against registry...") so users know why a
30 s pause appeared.
Actual: silent network calls, blocks on bad connectivity, no flag
to suppress.

## Suggested investigation
- Split orphan detection into two modes: cheap (config vs local
  store) by default; thorough (runtime-dep expansion via resolver)
  behind a flag or when the connection is known-good.
- Honour `GALE_OFFLINE=1` in `doctor` exactly like `outdated` does.
- Mirror the per-package `out.Step(...)` prints from
  `cmd/gale/verify.go` so users see what's pending.
