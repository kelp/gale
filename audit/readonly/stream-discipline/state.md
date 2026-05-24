# stream-discipline state

## Coverage matrix

| Command       | stdout = data? | errors on stderr? | status on stderr? | pipe-clean? | notes |
|---------------|---------------|-------------------|-------------------|-------------|-------|
| `list`        | yes           | yes               | yes               | yes         | clean |
| `info`        | yes           | yes               | yes               | yes         | mixes "(latest)" / "(not installed)" status into stdout data block |
| `search`      | yes           | yes               | yes (no-match warn) | yes       | clean |
| `doctor`      | NO (empty)    | n/a               | yes (all results) | yes (empty) | finding 0003 |
| `env`         | yes           | yes               | n/a               | yes         | clean |
| `hook`        | yes (script)  | yes               | n/a               | yes         | clean |
| `lint`        | NO (empty)    | yes               | yes               | yes (empty) | check-style; OK |
| `verify`      | NO (empty)    | yes               | yes               | yes (empty) | check-style; OK |
| `sbom`        | yes (table)   | yes               | yes               | yes         | clean |
| `sbom --json` | yes (JSON)    | yes               | yes               | yes         | clean — `-v` not yet wired so safe |
| `which`       | yes           | yes               | n/a               | yes         | clean |
| `diff`        | yes (`+`/`-`) | yes               | n/a               | yes         | (`generations diff`) clean |
| `outdated`    | partial       | yes               | yes               | mostly      | finding 0002: empty states on stderr |
| `generations` | yes           | yes               | n/a               | yes         | clean |
| `audit`       | NO (empty)    | yes (via err)     | yes               | yes (empty) | finding 0004: bypasses helper for SHA lines |
| `--help` / `-h` | NO          | n/a               | n/a (help text)   | NO          | finding 0001: help text on stderr |

## Confirmed wins

- No ANSI escape codes leak to stdout when stdout is piped.
  Verified with `gale list | cat -v`, `gale outdated 2>/dev/null
  | cat -v`. The output helper writes only to stderr, and no
  command uses fatih/color directly on stdout.
- `--verbose` is not yet wired through `output.Output` (see TODO
  on `outputMode.verbose` in `cmd/gale/output_mode.go:29`). So
  today `-v` cannot corrupt `--json` pipes. This is luck more
  than design.
- Errors consistently go to stderr via cobra's error path
  (`Error: ...`) with non-zero exit.
- `gale --version` writes to stdout (cobra default). Not retested
  here because it was out of the README's named target list, but
  it appears unaffected by the help-text issue.

## Speculative TODOs (didn't make findings/)

- **Color decision keys off `stderrIsTTY()` only.** If anyone
  adds colored output that writes to stdout in the future, the
  current decision (`mode.color = stderrIsTTY()`) won't suppress
  it when only stdout is piped. Hardening idea: track stdout-TTY
  separately and have any stdout writer consult it. Not exercised
  by current code, so not a finding.
- **`info <pkg>` mixes "(latest)" / "(not installed)" status
  notes into the stdout data block.** Parseable as long as a
  consumer expects them, but it's a soft violation of "data only
  on stdout". Not a finding because no plausible script today
  depends on a stricter form.
- **`inspect` (out of scope per README, but in cmd/gale/) mixes
  data (`fmt.Println(pkgKey)`) with status (`out.Error/out.Warn`)
  across stdout/stderr.** Out of scope here; noting in case it
  comes up in the next wave.
- **`gale --version` was not retested for stream discipline.**
  Cobra default routes it to stdout; presumed OK.

## What I did not cover

- TTY-vs-pipe dynamic behaviour (deferred to wave-2 dimension
  `tty-vs-nontty`).
- Long-running progress lines from `download` / `build` — these
  only trigger from install/update/sync/build paths and are not
  on the read-only target list (only `audit` and `verify` touch
  them, and `audit` was covered).
- Behaviour with `--plain` and `--quiet`. Spot-checked that
  `out.Success` honours `Quiet`, but did not systematically run
  every command under each combination.
