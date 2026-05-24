# tty-vs-nontty — state

## Coverage matrix

Tested under (a) stdout-piped, stderr-piped (Bash heredoc env);
(b) stdout=TTY/stderr=TTY via `script -q -c CMD /dev/null`;
(c) stdin from `/dev/null` (with 5s `timeout`); (d)
mixed-stream variants (`script -q -c "CMD >file"`, etc.) for
the high-output commands.

| Command            | Pipe | TTY | stdin=/dev/null | Notes |
|--------------------|------|-----|-----------------|-------|
| `list`             | ok   | ok  | ok              | no color anywhere (stdout-only path) |
| `info <pkg>`       | ok   | ok  | ok              | bypasses mode helper — see 0002 |
| `search`           | ok   | ok  | ok              | |
| `doctor`           | ok   | ok  | ok              | color on stderr under TTY; off when piped |
| `env`              | ok   | ok  | ok              | shell-safe (no color leak to stdout) |
| `hook direnv`      | ok   | ok  | ok              | shell-safe |
| `lint <file>`      | ok   | ok  | ok              | color on stderr under TTY |
| `verify <pkg>`     | n/a* | n/a*| ok              | uses `out.Step`; needs installed pkg to exercise |
| `sbom`             | ok   | ok  | ok              | empty-state on stderr (wave-1 0002) |
| `which <bin>`      | ok   | ok  | ok              | bypasses mode helper — see 0002 |
| `outdated`         | ok   | ok  | ok              | refresh status to stderr, no spinner |
| `audit <pkg>`      | n/a* | n/a*| ok              | needs installed pkg + network to exercise |
| `generations`      | ok   | ok  | ok              | bare command lists; no `list`/`show` subcommand |

`n/a*` = exit early in scratch HOME (no installed package).
Code paths inspected statically.

## Findings written

1. `0001-color-keyed-only-to-stderr-tty.md` — color/steps/
   progress all gated on `stderrIsTTY()`; stdout-TTY never
   consulted. Latent — no read-only command writes color to
   stdout today. Severity: low.
2. `0002-info-bypasses-tty-and-color-mode.md` — `info` and
   `which` write data with raw `fmt.Printf`, ignoring
   `--no-color`, `--plain`, `--quiet`, and the mode resolver.
   Severity: low.
3. `0003-no-tty-fast-path-for-stdin-but-no-prompt-guard.md` —
   no read-only command reads stdin; no `isStdinTTY()`
   helper; no style-guide rule for future prompts. The audit
   task hypothesised a `[g/p]` scope prompt that does not
   exist in the codebase. Severity: low.

## TODO (speculative — did not meet the bar)

- [ ] `gale generations list` / `show` mentioned in
  `audit/readonly/README.md` line 13 do not exist. `gale
  generations` itself is the listing command; subcommands
  are `diff` and `rollback`. Cross-cuts help-text /
  documentation, not strictly TTY. Flag for the README
  author.
- [ ] `doctor --repair` mutates state with no confirmation
  prompt and no `--yes` flag. Today it is technically write-
  scope so out of this audit, but worth tracking when a
  prompt is eventually added — see 0003 for the missing
  stdin-TTY guard.
- [ ] `ProgressEnabled` in `internal/download` is a
  package-level mutable global, set by
  `configureSubsystemOutput`. Only flipped when constructing
  an Output that targets `os.Stderr`. Could race if a future
  command builds Outputs for both streams in parallel
  goroutines. Not exercisable today (no concurrent Output
  construction in read-only paths). Speculative.
- [ ] `fatih/color`'s `color.NoColor` is also a mutable
  package-level global, set in `applyColorMode()`. Same
  concurrency concern; same lack of exercise. Speculative.
- [ ] No `FORCE_COLOR=1` / `CLICOLOR_FORCE=1` support.
  Cross-cuts wave-1 output-format 0002 (NO_COLOR ignored);
  same root cause (env vars not consulted).
- [ ] Wave-1 stream-discipline 0001 reports `--help` lands
  on stderr. Combined with no TTY-stream-specific color
  logic (this dim's 0001), `gale --help | less` will be
  uncolored — which is actually correct — but the path
  there is "wrong stream + wrong color test, two bugs
  cancelling".

## Scratch / methodology

```
HOME=/tmp/gale-ro-audit-tty-vs-nontty
GALE=/home/tcole/code/gale/gale

# Case A: both streams non-TTY (the bash shell here)
$GALE <cmd> > out.txt 2> err.txt

# Case B: forced TTY via util-linux script
script -q -c "$GALE <cmd>" /dev/null > both.txt 2>&1

# Case C: stdin from /dev/null (5s timeout to detect hangs)
timeout 5 $GALE <cmd> < /dev/null > out 2> err

# Case D: mixed-stream (interactive shell with stdout captured)
script -q -c "$GALE <cmd> > out.txt" /dev/null  # stderr=PTY
script -q -c "$GALE <cmd> 2> err.txt" /dev/null # stdout=PTY
```

No `unbuffer` / `expect` on this host. `script -q` is enough
for a single-stream PTY but cannot independently set TTY on
each stream — for "stderr=TTY only" we relied on the
shell-redirect inside `script -c`. Sufficient for the
findings here.
