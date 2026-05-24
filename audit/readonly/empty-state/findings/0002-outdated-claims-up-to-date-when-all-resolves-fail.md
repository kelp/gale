---
severity: high
confidence: confirmed
commands: [outdated]
area: empty-state
---
## Summary
`gale outdated` warns "Skipping <pkg>: <network error>"
for every package whose recipe cannot be resolved, then
prints `==> Everything is up to date.` and exits 0. When
*every* lookup fails (the offline / DNS-broken / registry-
down case), the user is told their packages are current
even though gale was unable to check a single one.

## Reproducer
```sh
mkdir -p $HOME/.gale
cat > $HOME/.gale/gale.toml <<EOF
[packages]
jq = "1.7.1"
EOF
HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 \
  gale outdated
echo "exit=$?"
```

Observed (verbatim, from the empty-state harness):

```
!!! Skipping jq: fetch recipe jq: Get "https://raw.githubusercontent.com/kelp/gale-recipes/main/recipes/j/jq.toml": proxyconnect tcp: dial tcp 127.0.0.1:1: connect: connection refused
==> Everything is up to date.
exit=0
```

## Expected vs actual
Expected: when every package was skipped due to errors,
exit non-zero (or at minimum, print a closing summary
like "0/1 packages checked — recipe resolution failed,
result is unreliable"). A script that does
`gale outdated && echo up-to-date` will treat the
network-down case as a confirmation the system is
current.

Actual: `cmd/gale/outdated.go:54-79`:

```go
var items []outdatedItem
for name, version := range cfg.Packages {
    r, err := ctx.Resolver(name)
    if err != nil {
        out.Warn(fmt.Sprintf("Skipping %s: %v", name, err))
        continue
    }
    ...
}
if len(items) == 0 {
    out.Success("Everything is up to date.")
    return nil
}
```

The `len(items) == 0` branch fires whether (a) every
package is genuinely current or (b) every package was
skipped. The two outcomes get the identical "Success"
line and exit 0.

This is exactly the "silent successes that mask a broken
state" pattern the audit was asked to look for.

## Suggested investigation
- Track skip count separately and gate the "Everything
  is up to date." message on `skipped == 0`. Exit non-
  zero when `skipped > 0 && len(items) == 0`, or split
  the message into "checked N, up to date" vs "could
  not check N — see warnings above".
- Same shape may exist in `cmd/gale/sbom.go` and
  similar fan-out commands that swallow per-package
  errors and present a green summary.
- The PR audit already shipped `tapsOfflineMode`
  detection at `outdated.go:32`; consider plumbing
  that signal into the final message so the offline
  case is at least labelled as such.
