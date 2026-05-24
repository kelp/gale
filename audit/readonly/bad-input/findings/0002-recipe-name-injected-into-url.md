---
severity: high
confidence: confirmed
commands: [info]
area: bad-input
---
## Summary
`gale info <name>` interpolates the user-supplied package name
directly into the registry URL with `fmt.Sprintf`, with no
validation or URL-encoding. Any character that's meaningful in a
URL (`?`, `#`, `/`, `%`, ...) leaks through to the HTTP request,
and the first byte of the name becomes the "letter bucket" path
component verbatim.

## Reproducer
```
$ /home/tcole/code/gale/gale info 'jq?foo=bar'
Error: jq?foo=bar: fetch recipe jq?foo=bar: HTTP 404

$ /home/tcole/code/gale/gale info '%2e%2e/etc'
Error: %2e%2e/etc: fetch recipe %2e%2e/etc: parse
  "https://raw.githubusercontent.com/kelp/gale-recipes/main/recipes/%/%2e%2e/etc.toml":
  invalid URL escape "%/%"

$ /home/tcole/code/gale/gale info ';rm -rf /'
Error: ;rm -rf /: fetch recipe ;rm -rf /: HTTP 404

$ /home/tcole/code/gale/gale info '$(id)'
Error: $(id): fetch recipe $(id): HTTP 404
```

Code: `internal/registry/registry.go:98-110`

```go
func (r *Registry) FetchRecipe(name string) (*recipe.Recipe, error) {
    if name == "" {
        return nil, fmt.Errorf("fetch recipe: name must not be empty")
    }
    bucket := string(name[0])
    url := fmt.Sprintf("%s/recipes/%s/%s.toml",
        r.BaseURL, bucket, name)
    ...
}
```

The `%2e%2e` test case proves the name lands inside the URL
path without escaping (Go's URL parser only fails because the
caller's `%` becomes a malformed percent-escape).

## Expected vs actual
Expected: reject names that don't match a strict charset (e.g.
`[a-z0-9][a-z0-9_-]*`) before any network call, with a clear
error like `invalid package name "jq?foo=bar": must match
[a-z0-9-_]+`.

Actual: garbage names are converted to HTTP requests against
the public recipe registry. There's no remote-code-execution
here — the shell metachars are inert data — but:
- It enables low-grade SSRF / log-spam: an attacker who can
  influence the argument can probe arbitrary paths on
  `raw.githubusercontent.com`.
- The "fetch recipe: HTTP 404" error hides the real failure
  (`name is not a valid package name`).
- A name with a leading `/` would alter the bucket path
  (`bucket := string(name[0])` -> `/`).

## Suggested investigation
- Add input validation in `registry.FetchRecipe` (and
  `FetchRecipeVersion`) or in the calling commands.
- The bucket derivation `string(name[0])` panics if `name`
  were a multi-byte UTF-8 codepoint at position 0 *and* the
  caller relies on `name[0]` being a letter — currently
  `""` is caught but `"é..."` would produce `string(0xc3)`
  in the URL. Worth a follow-up check.
- Consider rejecting names containing any of `/.?#%` outright.
