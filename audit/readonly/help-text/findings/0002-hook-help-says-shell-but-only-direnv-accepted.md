---
severity: low
confidence: confirmed
commands: [hook]
area: help-text
---
## Summary
`gale hook --help` advertises `gale hook <shell>` (generic) but
only `direnv` is actually accepted — any other shell name
produces "unsupported shell".

## Reproducer

```
$ gale hook --help
Print a script that integrates gale with direnv.
...
USAGE
  gale hook <shell> [flags]
```

```
$ gale hook bash
Error: unsupported shell
$ gale hook zsh
Error: unsupported shell
$ gale hook fish
Error: unsupported shell
```

Source: `cmd/gale/hook.go:11`:

```go
var hookCmd = &cobra.Command{
    Use:   "hook <shell>",
    ...
    Args:      cobra.ExactArgs(1),
    ValidArgs: []string{"direnv"},
```

`ValidArgs` is set, but `Args` is `ExactArgs(1)` rather than
`MatchAll(ExactArgs(1), OnlyValidArgs)`, so the constraint is
not enforced and is not rendered in the help template either.

CLAUDE.md and the manpage both name the command as
`gale hook direnv`, suggesting `direnv` is the only intended
form. The help string disagrees.

## Expected vs actual
- Expected: `Use: "hook direnv"` (concrete) or
  `<shell>` placeholder with help text enumerating the
  accepted value, plus `OnlyValidArgs` to reject typos at the
  cobra layer instead of returning a generic
  "unsupported shell" error from `env.GenerateHook`.
- Actual: help suggests `bash`/`zsh`/`fish` might work; they
  don't.

## Suggested investigation
`cmd/gale/hook.go` — adjust `Use`, switch to `OnlyValidArgs`,
or expand `env.GenerateHook` to actually emit per-shell hooks
if that was the original intent.
