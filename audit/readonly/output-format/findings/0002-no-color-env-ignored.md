---
severity: medium
confidence: confirmed
commands: [doctor, search, sbom, inspect, lint, outdated, env, verify, audit]
area: output-format
---
## Summary
Gale does not honor the `NO_COLOR` environment variable
(`https://no-color.org`). Under a TTY, ANSI sequences are
emitted regardless of `NO_COLOR=1`. Color can only be
suppressed by the `--no-color` / `--plain` flags or by
piping (TTY auto-detect).

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-output-format
script -qc "/path/to/gale doctor" /dev/null | cat -A | head -1
# ^[[32m==> ^[[0mGale home...
script -qc "NO_COLOR=1 /path/to/gale doctor" /dev/null | cat -A | head -1
# ^[[32m==> ^[[0mGale home...   (still colored)
```

Code path: `cmd/gale/output_mode.go:33-58` computes
`color` only from `tty`, `noColor` (the flag), and `plain`.
`cmd/gale/output_mode.go:64-73` `currentOutputMode()` does
not consult `os.Getenv("NO_COLOR")`.

`fatih/color`'s package-level `NoColor` default *does*
inspect `NO_COLOR`, but `cmd/gale/help.go:104`'s
`applyColorMode` overwrites it from gale's mode, and
`internal/output/output.go:45-54` calls `EnableColor()` on
each per-instance `*color.Color` whenever `opts.Color` is
true — bypassing the package default entirely.

## Expected vs actual
Expected: `NO_COLOR` set to any non-empty value disables
ANSI escapes, consistent with the de-facto standard adopted
by curl, GitHub CLI, ripgrep, fd, bat, etc.
Actual: `NO_COLOR` is silently ignored. Users have to
remember the gale-specific `--no-color` flag.

## Suggested investigation
`cmd/gale/output_mode.go:64-73` `currentOutputMode()`. Add
a check for `os.Getenv("NO_COLOR")` (non-empty) before the
TTY heuristic, and document the precedence with `--no-color`
and `--plain` in `--help`.
