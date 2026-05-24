---
severity: medium
confidence: confirmed
commands: [sbom]
area: bad-input
---
## Summary
`gale sbom --json` emits the literal token `null` (not `[]`)
when the resolved config has no packages, breaking downstream
JSON consumers that iterate or `length` the array.

## Reproducer
```
$ export HOME=/tmp/gale-ro-audit-bad-input
$ mkdir -p "$HOME/.gale" && printf '[packages]\n' > "$HOME/.gale/gale.toml"
$ /home/tcole/code/gale/gale sbom --json
null
$ /home/tcole/code/gale/gale sbom --json | jq length
jq: error (at <stdin>:1): null (null) has no length
```

Code: `cmd/gale/sbom.go:84,123-134`

```go
var entries []sbomEntry        // nil when packages map is empty

for name, version := range packages { ... entries = append(entries, e) }

if sbomJSON {
    return outputJSON(entries) // json.Encode(nil-slice) -> "null"
}
```

A nil Go slice marshals to JSON `null`, but an empty slice
marshals to `[]`. The non-JSON path correctly prints just the
header row, so the bug is JSON-format-specific.

## Expected vs actual
Expected: `[]` (a JSON array, possibly empty) so consumers can
treat the output uniformly.

Actual: `null`. Even worse, the exit code is 0 and the table
path prints a clean header — so the failure is silent until a
downstream tool blows up.

## Suggested investigation
- `cmd/gale/sbom.go:84`: initialise as `entries :=
  make([]sbomEntry, 0)` or guard the encode path.
- Worth checking other JSON-emitting paths the audit's
  `output-format` dimension flags — likely the same idiom
  elsewhere (any command that builds a `var xs []T` and then
  JSON-encodes it).
- Same idiom in `outputTable` is fine because tabwriter just
  prints the header for an empty slice.
