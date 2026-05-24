---
severity: high
confidence: confirmed
commands: [--help, list, info, sbom, doctor, env, hook, search, outdated, lint, verify, audit, generations, which]
area: stream-discipline
---
## Summary
`gale --help` and every subcommand's `--help` write to stderr
instead of stdout, breaking `gale --help | less` and similar
pipelines and violating Unix convention.

## Reproducer
```
$ HOME=/tmp/gale-ro-audit-stream-discipline \
  /home/tcole/code/gale/gale --help 1>/tmp/h.out 2>/tmp/h.err
$ wc -c < /tmp/h.out   # 0
$ wc -c < /tmp/h.err   # 2234
```

Same behaviour for subcommands:

```
$ /home/tcole/code/gale/gale list --help 1>/tmp/h.out 2>/tmp/h.err
$ wc -c < /tmp/h.out   # 0
$ wc -c < /tmp/h.err   # 148
```

For comparison, `grep --help` / `ls --help` exit 0 with help
text on stdout.

## Expected vs actual
Expected: `--help` (and `help <cmd>`) goes to stdout with exit 0;
usage errors (unknown flag, etc.) go to stderr with non-zero exit.
This is the standard split (e.g. `grep`, `ls`, `git`).

Actual: every code path that triggers the help printer writes to
stderr because the custom help func chose `cmd.OutOrStderr()`,
overriding cobra's default of stdout for `--help`.

## Suggested investigation
`cmd/gale/help.go:19`:

```go
func colorHelp(cmd *cobra.Command, args []string) {
    w := cmd.OutOrStderr()
```

A fixer should likely switch to `cmd.OutOrStdout()` for the
`--help` / explicit-help paths, while still routing usage-error
messages (cobra's `UsageString`, the Errorln/Usage paths) to
stderr. Note `root.go` already sets `cmd.SilenceUsage = true` in
`PersistentPreRun`, so usage isn't being printed on RunE errors —
the only caller of `colorHelp` is the help dispatch.
