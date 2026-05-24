---
severity: low
confidence: confirmed
commands: [all read-only]
area: tty-vs-nontty
---
## Summary
No read-only command reads `os.Stdin`, and none calls
`isatty.IsTerminal(os.Stdin.Fd())`. Every command terminates
cleanly when invoked with `</dev/null`. Today this is fine — but
the codebase has no guard against a future feature accidentally
adding an unguarded prompt (e.g. `gale doctor --repair`
asking "Proceed? [y/N]") that would deadlock under `cron`,
direnv, CI, or any non-interactive caller.

## Reproducer
```
$ rg 'os\.Stdin|isatty\.' /home/tcole/code/gale --type go \
    --no-tests --no-vendor
cmd/gale/shell.go:50:        c.Stdin = os.Stdin   # forwards to child
cmd/gale/run.go:39:          c.Stdin = os.Stdin   # forwards to child
cmd/gale/output_mode.go:61:  isatty.IsTerminal(os.Stderr.Fd())
```

Confirmed empirically — every read-only command exits within
5 seconds when stdin is closed:

```
HOME=/tmp/gale-ro-audit-tty-vs-nontty
for c in list "info jq" "search jq" doctor env "hook direnv" \
         sbom outdated "verify jq" "audit jq" which "generations"; do
  timeout 5 /path/to/gale $c </dev/null >/dev/null 2>&1
  echo "$c: rc=$?"
done
# (no rc=124; no command blocks)
```

## Expected vs actual
Expected: a project guideline that any new interactive prompt
must (a) check stdin-TTY and (b) provide a non-interactive
default or fail with a clear message — same pattern Cobra
uses for confirmation flags elsewhere.

Actual: no such guideline in `docs/dev/style-guide.md`, no
`isStdinTTY()` helper paralleling `stderrIsTTY()`, and no
existing prompt to anchor the pattern. `doctor --repair`
already mutates state silently without any confirmation — the
next natural step is to add `--yes` / prompt, and without a
guard it will hang under direnv.

## Suggested investigation
- Add `isStdinTTY()` next to `stderrIsTTY()` in
  `cmd/gale/output_mode.go`.
- Decide whether `doctor --repair` (and any future destructive
  read-mostly command, e.g. a hypothetical `gc --interactive`)
  should prompt by default and skip on `!isStdinTTY()`.
- Add a style-guide note: "interactive prompts require an
  stdin-TTY check + a non-interactive default or `--yes`
  flag".
