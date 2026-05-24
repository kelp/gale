---
severity: low
confidence: confirmed
commands: [search]
area: exit-codes
---
## Summary
`gale search <query>` exits 0 when no packages match — including
the empty-query case `gale search ""`. This contradicts the
"not-found-is-non-zero" convention every other read-only `gale`
command follows (`info`, `verify`, `audit`, `which`, `sbom <pkg>`).

## Reproducer
```sh
/home/tcole/code/gale/gale search asdfasdfasdf
# stderr: !!! No packages found matching "asdfasdfasdf"
# exit:   0

/home/tcole/code/gale/gale search ""
# stderr: !!! No packages found matching ""
# exit:   0

/home/tcole/code/gale/gale info nonexistent-pkg-xyz
# stderr: Error: nonexistent-pkg-xyz: fetch recipe ... HTTP 404
# exit:   1
```

A shell pipeline `gale search foo && install-helper` will run the
helper even when foo doesn't exist.

## Expected vs actual
Two defensible choices, but they need to be the same across the
suite:
- Treat "query found zero" as exit 0 (POSIX `grep` style): then
  `info nonexistent`, `which nonexistent`, etc. should also be 0.
- Treat "queried item not present" as exit 1 (current `info`/`which`
  convention): then `search` should match.

Currently `search` is the lone outlier. `gale search ""` accepting
an empty query is also worth a separate `cobra.MinimumNArgs(1)`-
plus-non-empty validation.

## Suggested investigation
`cmd/gale/search.go:24-28`. The "No packages found" branch uses
`out.Warn(...)` then `return nil`. Settle the convention in one
place (probably the find-vs-don't-find document if one exists in
`docs/`) and align `search`, `info`, `which`, `verify`, `audit`
together. `cobra`'s `cobra.ExactArgs(1)` does not reject `""` —
an explicit empty-arg check is missing.
