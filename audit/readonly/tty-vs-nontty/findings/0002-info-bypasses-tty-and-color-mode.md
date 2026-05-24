---
severity: low
confidence: confirmed
commands: [info, which]
area: tty-vs-nontty
---
## Summary
`gale info` and `gale which` write their data lines with raw
`fmt.Printf` / `fmt.Println`, bypassing the `output.Output`
helper, `--no-color`, `--plain`, `--quiet`, and the TTY auto-
detect entirely. The output happens to be safe today (no
embedded ANSI), but the path is structurally untestable for
TTY-mode regressions because nothing is wired through the
mode resolver.

## Reproducer
`cmd/gale/info.go:56-64`:

```go
fmt.Printf("Name:    %s\n", r.Package.Name)
fmt.Printf("Version: %s (latest)\n", r.Package.Version)
if r.Package.Description != "" {
    fmt.Printf("About:   %s\n", r.Package.Description)
}
if r.Source.URL != "" {
    fmt.Printf("Source:  %s\n", r.Source.URL)
}
fmt.Println("(not installed)")
```

`cmd/gale/which.go:30-31`:

```go
fmt.Printf("%s@%s\n", name, version)
fmt.Println(resolved)
```

Both also use `fmt.Print*` rather than `cmd.OutOrStdout()`,
so they cannot be redirected by test harnesses through cobra.

## Expected vs actual
Expected: every read-only command writes its data via
`cmd.OutOrStdout()` (or `cmd.OutOrStderr()` for status) and
routes through the existing output helper / mode resolver, so
that `--no-color`, `--plain`, `--quiet`, and TTY auto-detect
are honoured uniformly.

Actual: two commands hard-code `os.Stdout` via package-level
`fmt` calls and never consult the mode resolver. Quiet mode is
silently ignored; a future colored field (e.g. "(installed)" in
green) would leak ANSI to pipes.

## Suggested investigation
- Decide whether data on stdout should ever be colored. If yes,
  thread `cmd.OutOrStdout()` through `output.NewWithOptions`
  with `Color` keyed on stdout-TTY (see 0001).
- If no, leave the lines plain but at least swap `fmt.Printf`
  for `fmt.Fprintf(cmd.OutOrStdout(), ...)` so tests can
  capture them without `os.Stdout` redirection tricks.
- Cross-reference output-format-0005 (lint warnings use info
  prefix): the broader theme is "two parallel output paths".
