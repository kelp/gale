# Read-only command audit

Audit of gale's read-only / near-read-only commands for bugs and
behavioural inconsistencies. Follows the same convention as
`audit/behaviour/` and `audit/races/` (already closed out — see
recent commits starting at `d00185f`).

## Target commands

Read-only / near read-only:
- `list`, `info`, `search`, `doctor`, `env`, `hook`, `lint`,
  `verify`, `sbom`, `which`, `diff`, `outdated`
- `generations` (the `list` / `show` subcommands only —
  `rollback` was already audited and fixed)
- `audit` (rebuilds from source but does not mutate
  store/config/lockfile — treat as read-only of state, but
  do check the read-only invariant; see Dimension 8)

**Out of scope** (already audited in the prior round):
`install`, `add`, `remove`, `sync`, `update`, `switch`, `gc`,
`generations rollback`.

## Dimensions

Each subagent owns one dimension and writes to
`audit/readonly/<dim-slug>/`.

Wave 1:
- `output-format` — `--json` consistency, color/no-color, structure
- `exit-codes` — when does each command exit non-zero?
- `stream-discipline` — stdout vs stderr; pipe-cleanliness
- `bad-input` — nonexistent pkgs, malformed `@version`, unknown flags
- `empty-state` — fresh `$HOME`, no `~/.gale`, no packages, offline

Wave 2 (dispatched after Wave 1 returns):
- `scope-behaviour` — `-g`/`-p`/default on read-only commands
- `help-text` — flags described match flags accepted
- `read-only-invariant` — does anything labelled read-only write?
- `tty-vs-nontty` — prompts, color, progress on TTY vs pipe
- `network-perf` — offline behaviour, caching, redundant calls

## Scratch store

Gale has no `GALE_HOME` env var (see `cmd/gale/paths.go`).
Redirect via `$HOME`:

```sh
export HOME=/tmp/gale-ro-audit-<DIMSLUG>
mkdir -p "$HOME"
cd "$HOME"
/home/tcole/code/gale/gale <subcommand> ...
```

Built binary lives at `/home/tcole/code/gale/gale` (`just build`).
Never touch the real `~/.gale`.

## Finding schema

`audit/readonly/<dim-slug>/findings/NNNN-slug.md`:

```markdown
---
severity: critical | high | medium | low
confidence: confirmed | likely | speculative
commands: [info, sbom]
area: <dim-slug>
---
## Summary
One sentence.

## Reproducer
Concrete steps OR file:line + code excerpt.

## Expected vs actual

## Suggested investigation
Where a fixer should look — not the fix itself.
```

## Quality bar

- Concrete reproducer per finding. Vague "could be a problem"
  goes in `state.md` as a TODO, **not** in `findings/`.
- Cap of 5 findings per dimension per wave. Severity > volume.
- Confidence: confirmed (reproduced), likely (code clearly buggy
  but not exercised), speculative (suspicion only — state.md TODO).
- Don't propose fixes. Document, surface, stop.

## State file

`audit/readonly/<dim-slug>/state.md` tracks the matrix of commands
× sub-cases covered, what's outstanding, and speculative TODOs that
didn't make the bar for `findings/`.
