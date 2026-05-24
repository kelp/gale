---
severity: high
confidence: confirmed
commands: [info]
area: bad-input
---
## Summary
`gale info <pkg>@<version>` doesn't parse the `@version` suffix
— the entire argument is passed verbatim as the recipe name,
producing opaque `HTTP 404` errors that hide the user's actual
mistake.

## Reproducer
```
$ /home/tcole/code/gale/gale info 'jq@1.7'
Error: jq@1.7: fetch recipe jq@1.7: HTTP 404

$ /home/tcole/code/gale/gale info 'jq@'
Error: jq@: fetch recipe jq@: HTTP 404

$ /home/tcole/code/gale/gale info 'jq@@'
Error: jq@@: fetch recipe jq@@: HTTP 404

$ /home/tcole/code/gale/gale info 'jq@abc.def.ghi'
Error: jq@abc.def.ghi: fetch recipe jq@abc.def.ghi: HTTP 404
```

Code: `cmd/gale/info.go:19,51`

```go
name := args[0]
...
r, err := reg.FetchRecipe(name)
```

The CLI help string in `CLAUDE.md` and the `install`/`update`
commands document `@version` as universally supported on
"version-aware commands" — `info` is clearly version-aware in
spirit (it prints `Version:` and "latest"), but it bypasses the
shared `resolveVersionedRecipe()` helper entirely.

## Expected vs actual
Expected: `gale info jq@1.7` should resolve version 1.7 via
`FetchRecipeVersion` and show that version's metadata, or at
minimum report `unknown version "1.7" for jq`.

Actual: the literal string `jq@1.7` is bucketed into the URL
`recipes/j/jq@1.7.toml`, the HTTP server returns 404, and the
user sees a misleading "not found" error for a package that
exists.

## Suggested investigation
- `cmd/gale/info.go`: should use the shared
  `resolveVersionedRecipe()` helper from `context.go` (also
  used by `install`/`update`) instead of raw `FetchRecipe`.
- Verify the same fix isn't needed in `gale outdated` (which
  refuses positional args at all — see separate finding) and
  `gale verify` (which falls back to "not found in lockfile"
  — also misleading; finding 0003).
