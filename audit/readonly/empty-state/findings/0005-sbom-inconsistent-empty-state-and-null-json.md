---
severity: medium
confidence: confirmed
commands: [sbom]
area: empty-state
---
## Summary
`gale sbom` handles missing/empty config inconsistently
compared with its peer read-only commands, and its
`--json` output for an empty config emits the literal
`null` instead of `[]`. Both behaviours are footguns
for downstream consumers (CI pipelines, supply-chain
scanners) that expect a stable contract.

## Reproducer

### A. `sbom` fails on a fresh $HOME while peers succeed
```sh
# Truly fresh: no ~/.gale at all
HOME=/tmp/empty-home gale list      # "No packages installed." exit 0
HOME=/tmp/empty-home gale outdated  # "No packages installed." exit 0
HOME=/tmp/empty-home gale sbom
# Error: reading config: reading config: open /tmp/empty-home/.gale/gale.toml: no such file or directory
# exit 1
HOME=/tmp/empty-home gale sbom --json
# Error: reading config: reading config: open ... no such file or directory
# exit 1
```

`list` and `outdated` treat "no global config" as
"nothing declared, exit 0". `sbom` treats it as a
hard error. Same input, opposite outcomes.

### B. `--json` produces literal `null` instead of `[]`
```sh
mkdir -p $HOME/.gale && : > $HOME/.gale/gale.toml
gale sbom --json
# null
```

A consumer doing `jq '. | length'` on that output gets
`null` and a parse error in shell pipelines that expect
an array. The non-JSON table mode in the same state
prints the header row with no data — correct and
consistent.

### C. Double-wrapped error message
```
Error: reading config: reading config: open /.../gale.toml: no such file or directory
```

`resolveSbomConfig` returns `fmt.Errorf("reading config: %w", err)`
and the RunE wraps it again with `fmt.Errorf("reading config: %w", err)`.

## Expected vs actual
Expected:
- `sbom` mirrors `list` / `outdated`: no global
  config means "nothing to report", exit 0, table
  with header (or `[]` in JSON).
- `sbom --json` emits a JSON array in all empty
  cases, never a bare `null`.
- Error messages are wrapped once, not twice.

Actual: `cmd/gale/sbom.go:46-49`:

```go
data, configPath, err := resolveSbomConfig(cwd, globalDir)
if err != nil {
    return fmt.Errorf("reading config: %w", err)
}
```

`resolveSbomConfig` (sbom.go:163) only succeeds when
*some* gale.toml exists; if both the project search
and the global path miss, it returns the read error
unwrapped from `os.ReadFile`. There is no
"no config = empty result" branch.

The `null`-vs-`[]` issue is the standard Go pitfall:
`var entries []sbomEntry` is nil-by-default, and
`json.NewEncoder(...).Encode(nil-slice)` writes
`null`. The table writer is unaffected because it
ranges over a nil slice as zero rows.

## Suggested investigation
- Add a "no config -> empty SBOM" branch in
  `resolveSbomConfig` or its caller, matching the
  treatment in `list`/`outdated`.
- Initialise `entries := []sbomEntry{}` (or
  `make([]sbomEntry, 0)`) before the loop so the
  JSON encoder emits `[]`.
- Audit other JSON outputs (none currently, but
  the audit also calls out scope-behaviour and
  output-format dimensions) for the same nil-slice
  pitfall before they ship.
- De-duplicate the doubled "reading config:" wrap
  in the RunE.
