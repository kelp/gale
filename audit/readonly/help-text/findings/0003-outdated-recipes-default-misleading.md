---
severity: medium
confidence: confirmed
commands: [outdated]
area: help-text
---
## Summary
`gale outdated --help` shows the `--recipes` flag with
`(default: ../gale-recipes/)` baked into the usage string,
but the actual flag default is the empty string (no local
resolution). Passing `--recipes` with no value uses
`NoOptDefVal = "auto"`, not `../gale-recipes/`. The help is
actively misleading on three fronts.

## Reproducer

```
$ gale outdated --help
...
  --recipes string
      Use local recipes directory (default: ../gale-recipes/)
```

`cmd/gale/outdated.go:101`:

```go
outdatedCmd.Flags().StringVar(&outdatedRecipes, "recipes", "",
    "Use local recipes directory (default: ../gale-recipes/)")
outdatedCmd.Flags().Lookup("recipes").NoOptDefVal = "auto"
```

Concretely:
- The flag's true zero value is `""` (registry mode).
- A bare `--recipes` (no value) becomes `"auto"`, not
  `../gale-recipes/`.
- A user passing `--recipes ../gale-recipes/` because the
  help told them to is supplying the same string the help
  claims is already the default — but in fact the default
  behaviour without the flag is *not* to look at
  `../gale-recipes/`.

## Expected vs actual
- Expected: usage text either omits the "default" claim or
  documents the real behaviour ("Use local recipes directory;
  pass without value to auto-detect sibling
  `../gale-recipes/`").
- Actual: help suggests there is a sensible default that is
  already in effect; there isn't.

Compare to `install --recipes`, `add --recipes`,
`update --recipes`, `sync --recipes`, where the same pattern
likely recurs (CLAUDE.md "Key Flags" lists the same five
commands).

## Suggested investigation
`cmd/gale/outdated.go` and any other command registering a
`--recipes` flag with the same usage string. Either rewrite
the usage text or set the real default via `StringVar`'s
default arg.
