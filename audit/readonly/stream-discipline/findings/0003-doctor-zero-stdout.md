---
severity: medium
confidence: confirmed
commands: [doctor]
area: stream-discipline
---
## Summary
`gale doctor` writes every check result (success, warning, error,
and the `--repair` confirmations) to stderr via `out.Success` /
`out.Warn` / `out.Error`. Stdout is empty in every case, including
when all checks pass and when `--repair` succeeds. Anything that
captures the diagnostic report via stdout (`gale doctor > report.txt`,
`gale doctor | grep ^xxx`, `tee`) gets nothing.

## Reproducer
```
$ HOME=/tmp/gale-ro-audit-stream-discipline \
  /home/tcole/code/gale/gale doctor 1>/tmp/d.out 2>/tmp/d.err
$ wc -c < /tmp/d.out    # 0
$ wc -c < /tmp/d.err    # ~700+ bytes of pass/fail lines
$ echo $?               # 1 (problems found)
```

Even when all checks pass:

```
$ gale doctor > /tmp/d.out      # /tmp/d.out is 0 bytes
$ gale doctor | grep PATH       # never matches
```

## Expected vs actual
Expected: at minimum, decide whether `doctor` is a "report"
command (results → stdout, like `lsof`, `df`, `ps`) or a
"side-effect" command (status → stderr, exit code is the signal,
like `make`). Other gale data commands send results to stdout;
doctor is the odd one out among the read-only set.

Actual: results live on stderr unconditionally. Any tooling that
wants to archive a doctor report has to `2>&1` or redirect stderr
explicitly.

## Suggested investigation
`cmd/gale/doctor.go:66`:

```go
out := newCmdOutput(cmd)            // newOutput() → os.Stderr
```

`cmd/gale/output_mode.go:89-91`:

```go
func newOutput() *output.Output {
    return newOutputForWriter(os.Stderr)
}
```

A fixer should decide: keep on stderr (and document it in
`doctor --help`), or split — pass/fail summaries to stdout,
progress/notes to stderr. The current code reuses the generic
status helpers for what are arguably the command's primary data.
