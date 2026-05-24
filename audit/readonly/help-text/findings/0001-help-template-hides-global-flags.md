---
severity: medium
confidence: confirmed
commands: [list, info, search, doctor, env, hook, lint, verify, sbom, which, outdated, audit, generations]
area: help-text
---
## Summary
The custom `colorHelp` template prints only `cmd.LocalFlags()`,
so persistent/inherited flags like `--no-color`, `--plain`,
`--verbose`, `--quiet`, `--dry-run`, and `--error-format` never
appear in any `gale <cmd> --help` output — even though they are
accepted on every subcommand.

## Reproducer

```
$ gale list --help
List installed packages

USAGE
  gale list [flags]

FLAGS
  -h, --help
      help for list
  --scope string
      Filter by scope: all|shared|host
```

But cobra accepts and prints them when an error triggers the
built-in usage:

```
$ gale generations bogus
Error: unknown command "bogus" for "gale generations"
...
Global Flags:
  -n, --dry-run               ...
      --error-format string   ...
      --no-color              ...
      --plain                 ...
  -q, --quiet                 ...
  -v, --verbose               ...
```

Source: `cmd/gale/help.go:62`:

```go
flags := cmd.LocalFlags()
if flags.HasFlags() {
    yellow.Fprintln(w, "FLAGS")
    flags.VisitAll(...)
}
```

`LocalFlags()` excludes persistent flags from ancestors.
`cmd.Flags()` (or visiting both local and inherited) would show
them.

## Expected vs actual
- Expected: every accepted flag is documented in `--help`.
- Actual: 6 globally-accepted flags are invisible in per-command
  help. A user who sees only `--help` cannot discover
  `--no-color`, `--plain`, `-q`, `-v`, `-n`, `--error-format`.

## Suggested investigation
Decide whether to render inherited flags inline, in a separate
`GLOBAL FLAGS` block, or via a "see `gale --help`" pointer. The
cobra builtin separates them; the custom template should too.
