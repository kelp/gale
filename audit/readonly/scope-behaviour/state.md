# scope-behaviour state

## Coverage matrix (read-only commands × scope cases)

| command       | accepts -g/-p | default rule              | both-present winner | notes                                          |
| ------------- | ------------- | ------------------------- | ------------------- | ---------------------------------------------- |
| list          | no            | project-if-walk-up, else  | project (only)      | own `--scope all|shared|host` is unrelated     |
|               |               | global                    |                     |                                                |
| info          | no            | tries project then global | project (shadows)   | only command that auto-falls-through           |
| sbom          | no            | project-if-walk-up, else  | project (only)      | reads matching gale.toml + gale.lock           |
|               |               | global                    |                     |                                                |
| outdated      | no            | newCmdContext(false,false)| project (only)      | recipes refresh hits net regardless of scope   |
| env           | no            | PATH from active dir;     | project [vars] only | global [vars] silently dropped — finding 0002  |
|               |               | [vars] project-only       |                     |                                                |
| which         | no            | resolveGaleDir()          | project (only)      | cannot resolve global binary from project cwd  |
| verify        | no            | newCmdContext(false,false)| project lockfile    | finding 0004                                   |
| audit         | no            | newCmdContext(false,false)| project lockfile    | finding 0004                                   |
| generations   | no            | resolveGaleDir()          | project generations | rollback subcommand audited separately         |
| generations diff | no         | resolveGaleDir()          | project generations | same                                           |
| doctor        | no (rejects)  | inspects both anyway      | n/a                 | the one command that already crosses scopes    |
| hook          | no (rejects)  | scope-less                | n/a                 | prints direnv stub; emits no scope info        |
| lint          | no            | scope-less                | n/a                 | operates on a recipe file path                 |
| search        | no            | scope-less                | n/a                 | hits registry                                  |

## Findings filed
- 0001 high — read-only commands universally lack `-g`/`-p`
- 0002 high — `gale env` drops global `[vars]`
- 0003 medium — `gale info` cannot inspect a shadowed global pkg
- 0004 medium — `gale verify`/`audit` locked to cwd-derived lockfile
- 0005 medium — `gale list` has no cross-scope view; `--scope` is
  a naming collision

## TODOs (speculative, not finding-grade)

- The "auto-detect" rule from CLAUDE.md ("gale.toml present →
  project, absent → global") is consistent across read-only
  commands *but* "present" means "found by walking up from cwd",
  which can pick up a stray gale.toml in any ancestor directory
  (e.g. `~/somerepo/gale.toml` covers everything under `~`). Worth
  documenting; not a bug.
- `gale info` against a package present only in the registry
  (not installed in either scope) prints `(not installed)` —
  works regardless of scope. Did not file.
- `gale generations` from a fresh `$HOME` (no `.gale/` at all)
  exits 0 with `No generations found.` — covered by empty-state
  finding 0001 family. Did not duplicate.
- `gale doctor --repair` (mutating but reached via doctor)
  rebuilds *both* global and project generations
  (`doctor.go:567-577`). This is the right call but it is also
  the *only* place in the codebase that touches both scopes in
  one command. Worth noting in any future scope-flag rollout.
- Interactive scope prompt `[g/p]`: searched cmd/gale for any
  prompt of that form — none found. The mutation commands error
  ("no project found") rather than prompting. Read-only commands
  never prompt. Confirmed: there is no interactive scope prompt
  in the codebase today.
- `gale info` against `name@version` syntax — info doesn't parse
  versions (bad-input finding 0001), so scope-flag work should
  probably land alongside that fix.

## Methodology notes
- Static read: grepped each command file for scope flag
  declarations and `newCmdContext` calls.
- Dynamic: scratch `$HOME=/tmp/gale-ro-audit-scope-behaviour`
  with both `~/.gale/gale.toml` and `~/proj/gale.toml`, executed
  every target command from both cwds, captured help output, and
  tried `-g`/`--global`/`-p`/`--project` on each.
- Cross-checked behaviour-cluster 0001-0003 commit `d00185f` —
  it fixed mutation-command scope only; read-only commands were
  untouched. No findings here duplicate that cluster.
