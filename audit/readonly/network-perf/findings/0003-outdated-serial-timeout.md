---
severity: high
confidence: confirmed
commands: [outdated, sbom, doctor]
area: network-perf
---
## Summary
`gale outdated` iterates `cfg.Packages` serially and issues a
fresh recipe fetch for each one with a 30 s per-request HTTP
timeout. On an unreachable registry the total wall time is
`30 s × N`, the user sees one "Skipping <pkg>" warning per
package, and the final line is the misleading
`==> Everything is up to date.` `gale sbom` (no `--no-refresh`
flag at all) and `gale doctor` (orphan-detection path) have the
same shape.

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
bat = "0.20"
ripgrep = "13.0"
EOF

$ { time /home/tcole/code/gale/gale outdated; } 2>&1 | tail -10
!!! Skipping jq: fetch recipe jq: Get "http://192.0.2.1/recipes/j/jq.toml": \
  context deadline exceeded (Client.Timeout exceeded while awaiting headers)
!!! Skipping bat: fetch recipe bat: ... i/o timeout ...
!!! Skipping ripgrep: fetch recipe ripgrep: ... i/o timeout ...
==> Everything is up to date.

real    1m30.300s   <-- 30 s × 3 packages, serial
```

For comparison, against the real registry with a warm cache:

```sh
$ rm -rf "$HOME/.gale/cache"
$ # ... 8-package config, real registry ...
$ { time /home/tcole/code/gale/gale outdated > /dev/null; } 2>&1 | grep real
real    0m1.240s    # cold (16 requests)
real    0m0.380s    # warm
real    0m0.374s    # warm
```

Confirmed via local logging server (`/tmp/log_server3.py`): the
loop emits exactly `2 × N` requests (recipe `.toml` +
`.binaries.toml` per package), all serial, all reaching `cachedGet`.

Source — `cmd/gale/outdated.go:54-75`:

```go
for name, version := range cfg.Packages {
    r, err := ctx.Resolver(name)        // blocks 30s on timeout
    if err != nil {
        out.Warn(fmt.Sprintf("Skipping %s: %v", name, err))
        continue
    }
    ...
}
```

Then unconditionally:

```go
if len(items) == 0 {
    out.Success("Everything is up to date.")   // even if all skipped
    return nil
}
```

## Expected vs actual
Expected (perf): fan out the resolver calls under a bounded
errgroup (say `min(N, 8)`); the warm-cache case stays fast and the
worst-case wall time on a flapping connection drops to ~30 s
regardless of N.
Expected (correctness): if every package was skipped due to
network error, exit non-zero or at least print a different summary
than "Everything is up to date." — the user just learned nothing.
Actual: serial loop, success message regardless of skip count.

## Suggested investigation
- `outdated`, `sbom`, and `doctor.checkOrphans` all share the
  one-resolver-call-per-package shape. Solve once in a shared helper
  in `context.go`.
- Track `skipped` count alongside `items` in `outdated` and gate the
  success message on `len(items) == 0 && skipped == 0`.
- The 30 s per-request timeout is repeated literally four times
  (`registry.go:107,147,198`, `search.go:24`). A `[network] timeout`
  knob in `~/.gale/config.toml` would let users tune for flaky
  links.
