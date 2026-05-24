---
severity: medium
confidence: confirmed
commands: [sbom]
area: output-format
---
## Summary
`gale sbom --json` emits the JSON literal `null` (not `[]`) when
no packages are configured, contradicting the sibling
`gale inspect --json` convention and breaking consumers that
iterate over the result.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-output-format
mkdir -p "$HOME/.gale"
printf '[packages]\n' > "$HOME/.gale/gale.toml"
gale sbom --json    # prints "null\n"
gale inspect --all --json  # prints "[]\n"
```

`cmd/gale/sbom.go:84` declares `var entries []sbomEntry` and
never re-initialises it before `json.Encode` if the package
map is empty. Go encodes a nil slice as `null`.

By contrast, `cmd/gale/inspect.go:130-133` explicitly converts
nil to an empty slice with a comment "JSON consumers expect []
not null."

## Expected vs actual
Expected: `[]\n` (matches inspect, matches the documented "list of
packages" shape, and lets `jq '.[]'` succeed without erroring on
`null`).
Actual: `null\n`. Calls like `jq -e '.[] | .name'` fail because
`null | .[]` is a jq type error.

## Suggested investigation
`cmd/gale/sbom.go:131` (`outputJSON`). Treat a nil/empty
`entries` slice the same way inspect does. Also check whether
other future JSON-emitting commands need the same guard at a
shared helper.
