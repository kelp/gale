---
severity: low
confidence: likely
commands: [outdated]
area: output-format
---
## Summary
`gale outdated` iterates `cfg.Packages` (a `map[string]string`)
without sorting before printing, so the order of result lines
varies run-to-run. Every other read-only command that prints
package lists (`list`, `sbom`, `env` vars, `inspect`) sorts
its keys before output.

## Reproducer
`cmd/gale/outdated.go:54-75`:
```go
for name, version := range cfg.Packages {  // map iteration
    ...
    items = append(items, outdatedItem{...})
}
...
for _, line := range formatOutdated(items) {
    fmt.Println(line)
}
```

`items` is appended in random order and `formatOutdated`
preserves that order. Compare with `cmd/gale/list.go:88`
(`sortedKeys`), `cmd/gale/sbom.go:119-121` (`sort.Slice`),
`cmd/gale/env.go:51-55` (`sort.Strings`), and
`cmd/gale/inspect.go:100-105` — all explicitly sort.

The behaviour wasn't reproduced because the audit scratch
store has no installed packages, but Go's map iteration is
explicitly randomised, so any user with ≥2 outdated packages
will see reorderings. Mark "likely" rather than "confirmed"
on the runtime evidence; the static evidence is conclusive.

## Expected vs actual
Expected: lines sorted by package name, consistent with peers.
Actual: order varies between invocations.

## Suggested investigation
`cmd/gale/outdated.go:54`. Build the keys, `sort.Strings`,
then iterate. Mirror `sbom.go:119`. Same fix the others
already apply.
