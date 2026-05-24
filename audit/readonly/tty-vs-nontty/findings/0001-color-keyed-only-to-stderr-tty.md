---
severity: low
confidence: confirmed
commands: [doctor, lint, verify, audit, outdated, generations]
area: tty-vs-nontty
---
## Summary
Color / steps / progress are all gated on a single test:
`isatty.IsTerminal(os.Stderr.Fd())`. Stdout-TTY state is never
consulted. Consequence: when a user redirects stderr (`gale doctor
2>/tmp/log`) but stdout stays a terminal, all colored / stepwise
status output silently disappears from the log file (good) AND
nothing colored ever reaches the still-interactive stdout (fine
today because no read-only command writes color to stdout — but
this couples a usability decision to an invariant that is not
written down).

## Reproducer
`cmd/gale/output_mode.go:60-72`:

```go
func stderrIsTTY() bool {
    return isatty.IsTerminal(os.Stderr.Fd()) ||
        isatty.IsCygwinTerminal(os.Stderr.Fd())
}

func currentOutputMode() outputMode {
    return resolveOutputMode(outputModeInput{
        tty:         stderrIsTTY(),
        ...
    })
}
```

`newOutputForWriter(w io.Writer)` then derives `Color`/`Steps`
from this mode no matter what `w` is. Demonstrating:

```
$ HOME=/tmp/gale-ro-audit-tty-vs-nontty \
  script -q -c "/path/to/gale doctor 2>/tmp/d.err" /dev/null \
  | head -3   # stdout is the script PTY (a TTY)
$ head -3 /tmp/d.err | cat -A
==> Gale home (~/.gale/)$
!!! No global gale.toml$           # no ANSI – stderr keyed off
```

Stdout (still a TTY) gets nothing because doctor writes only to
stderr. Color was disabled by the stderr redirection.

## Expected vs actual
Standard practice (e.g. `git`, `ls`, `grep --color=auto`) keys
each stream's color decision on whether that stream is a TTY:
stdout-color iff stdout is a TTY, stderr-color iff stderr is a
TTY. Gale conflates the two on stderr.

Today no read-only command writes color to stdout, so the only
observable effect is that redirecting stderr also turns off
color on interactive stdout. The bug is mostly latent — but it
will bite the next contributor who reaches for
`newOutputForWriter(os.Stdout)` (e.g. for a colored `gale list`),
because the color decision will be wrong for either possible
direction of the two streams.

## Suggested investigation
- `cmd/gale/output_mode.go`: add a per-writer TTY check, or pass
  the target writer into `currentOutputMode` so the resolver can
  call `isatty.IsTerminal(w.(*os.File).Fd())` when applicable.
- Decide and document: does color follow stdout, stderr, or
  per-stream? `docs/dev/style-guide.md` is the obvious home.
- The progress / step writers currently key off the same single
  bit (via `configureSubsystemOutput`). Their target is stderr,
  so making the test stream-specific is consistent.
