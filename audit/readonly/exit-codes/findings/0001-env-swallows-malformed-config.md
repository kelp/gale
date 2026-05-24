---
severity: high
confidence: confirmed
commands: [env]
area: exit-codes
---
## Summary
`gale env` silently swallows malformed `gale.toml` and exits 0,
emitting no `[vars]` exports — a script `eval "$(gale env)"` gets a
partial environment with no error signal.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-exit-codes
mkdir -p "$HOME/proj"
printf 'this is = not [ valid toml\n' > "$HOME/proj/gale.toml"
cd "$HOME/proj"
/home/tcole/code/gale/gale env
# stdout: export PATH="..."
# stderr: (empty)
# exit:   0
/home/tcole/code/gale/gale env --vars-only
# stdout: (empty)
# exit:   0
```

`gale list`, `gale info`, `gale outdated`, and `gale doctor`
against the same malformed file all exit 1 with a parse error.
Only `env` masks the failure.

## Expected vs actual
Expected: parse failure should exit non-zero (consistent with
every other read-only command). At minimum a warning on stderr.
Actual: exit 0, no diagnostic, missing vars look identical to
"no vars configured".

## Suggested investigation
`cmd/gale/env.go:33-48`: four sequential `return nil //nolint:nilerr`
calls intentionally swallow `os.ReadFile`, `config.ParseGaleConfig`,
and `os.Getwd` failures. The `nilerr` lint suppressions trace back
to a "best-effort" design choice, but the parse-error branch is
materially different from "no project config" — it should surface.
Compare against the explicit parse-error handling in
`cmd/gale/list.go:60-63` and `cmd/gale/sbom.go:51-54`.
