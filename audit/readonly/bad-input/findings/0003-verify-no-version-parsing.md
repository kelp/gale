---
severity: medium
confidence: confirmed
commands: [verify]
area: bad-input
---
## Summary
`gale verify <pkg>@<version>` doesn't parse `@version`; it
looks up the whole string verbatim in the lockfile, then
reports "not found in lockfile — install it first" — a
misleading message that implies the package isn't installed
when in fact the argument format was wrong.

## Reproducer
```
$ /home/tcole/code/gale/gale verify 'jq@1.7'
Error: jq@1.7 not found in lockfile — install it first

$ /home/tcole/code/gale/gale verify 'jq@'
Error: jq@ not found in lockfile — install it first
```

Code: `cmd/gale/verify.go:18,44`

```go
name := args[0]
...
pkg, ok := lf.Packages[name]
if !ok {
    return fmt.Errorf(
        "%s not found in lockfile — install it first", name)
}
```

There is no `@`-stripping or version-resolution step. The
lockfile stores entries keyed by bare package name with the
version in the value, so `lf.Packages["jq@1.7"]` will never
hit.

## Expected vs actual
Expected: parse `name@version` → look up bare `name` in
lockfile, then either verify the requested version's
attestation or report `jq@1.7 not installed (have jq@1.6)`.

Actual: a user who copies-and-pastes a versioned identifier
(the form documented and used by `install`, `update`,
`switch`) gets a confusing "install it first" error and may
re-run `gale install jq@1.7` unnecessarily.

The same issue likely affects `gale audit` (untested here —
`audit nosuchpkg` failed at the same lockfile check), and is
the lockfile-side mirror of finding 0001 (info) and 0004
(outdated). Symptom is consistent across read-only
version-aware commands: none of them route through the
shared `@version` parser.

## Suggested investigation
- `cmd/gale/verify.go`: parse `name@version[-rev]` before
  the lockfile lookup. Look for an existing helper in
  `internal/recipe/` or `cmd/gale/context.go`.
- Audit `cmd/gale/audit.go` for the same shape.
