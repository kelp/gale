---
severity: medium
confidence: confirmed
commands: [outdated]
area: bad-input
---
## Summary
`gale outdated <pkg>` is rejected with a generic cobra "unknown
command" error even though `gale outdated [pkg...]` is the
documented surface in `CLAUDE.md`. The command uses
`cobra.NoArgs`, so any positional argument is treated as an
unknown subcommand.

## Reproducer
```
$ /home/tcole/code/gale/gale outdated nosuchpkg
Error: unknown command "nosuchpkg" for "gale outdated"

$ /home/tcole/code/gale/gale outdated jq
Error: unknown command "jq" for "gale outdated"
```

Code: `cmd/gale/outdated.go:23-25`

```go
var outdatedCmd = &cobra.Command{
    Use:   "outdated",
    Short: "Show packages with newer versions available",
    Args:  cobra.NoArgs,
    ...
}
```

Compare to `CLAUDE.md` `## CLI Commands` block:
```
gale outdated [pkg...]      Update packages to latest version
```
(The description there is also wrong — copy-paste from
`update` — but that's a docs nit.)

## Expected vs actual
Expected: either (a) accept package args and filter the
report to those names, or (b) update `Use:` to `outdated` and
remove the `[pkg...]` claim from CLAUDE.md.

Actual: the cobra error masquerades as a typo'd subcommand,
giving no hint that the user's mental model (filtering by
package) was the actual mismatch.

## Suggested investigation
- Decide whether filtering belongs (mirrors `pip list
  --outdated <pkg>`).
- Either set `Args: cobra.ArbitraryArgs` and filter, or fix
  the docs and add a clearer "outdated takes no positional
  arguments" error.
- Same shape is worth checking on `list`, `doctor`, and
  `generations` (no-args commands that reject extra args
  with a generic cobra message — but those aren't promised
  to accept args in docs).
