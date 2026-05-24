---
severity: medium
confidence: confirmed
commands: [doctor, outdated, list, switch, inspect]
area: help-text
---
## Summary
`gale.1` is out of sync with the actual CLI in several places:
flags and even whole commands ship without being mentioned in
the manpage.

## Reproducer

Missing flags (present in `--help`, absent in `gale.1`):

- `doctor --repair`
  ```
  $ gale doctor --help | grep -A1 repair
    --repair
        Repair active generations from current config and store
  ```
  manpage shows `doctor` with no flags.

- `outdated --no-refresh` and `outdated --recipes`
  ```
  $ gale outdated --help
    --no-refresh
        Skip refreshing configured recipe taps before resolving
    --recipes string
        Use local recipes directory ...
  ```
  manpage shows `outdated` with no flags.

- `list --scope all|shared|host`
  ```
  $ gale list --help
    --scope string
        Filter by scope: all|shared|host
  ```
  manpage shows `gale list` flagless.

Missing global flags (root `--help` shows them; manpage's
"Global Flags" section lists only `-v`, `-n`, `--no-color`):

- `--error-format`
- `--plain`
- `-q`, `--quiet`

Missing commands entirely (registered in `cmd/gale/`, listed
by `gale --help`, absent from manpage):

- `gale switch` (`cmd/gale/switch.go`)
- `gale inspect` (`cmd/gale/inspect.go`)

Minor: manpage example for `outdated` reads
`jq 1.7.1 -> 1.8.1`, but `formatOutdated`
(`cmd/gale/outdated.go:94`) emits the Unicode arrow `→`.

## Expected vs actual
- Expected: `gale.1` lists every command and flag the binary
  accepts. Per CLAUDE.md the manpage is canonical mandoc and
  release-worthy documentation.
- Actual: at least 2 commands and 5 flags are undocumented,
  plus a stale example string.

## Suggested investigation
`gale.1` is hand-maintained mandoc. Either a generator step
should be added (cobra has built-in mandoc generation) or the
file should be brought up to date and a checklist item added
to the release flow (`docs/dev/releasing.md`).
