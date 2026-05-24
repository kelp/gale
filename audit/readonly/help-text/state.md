# help-text dimension state

Help-text content accuracy audit for read-only commands.
Scope is what flags/args/examples the help advertises vs what
the cobra command actually accepts. Stream-discipline (help on
stderr) is owned by `stream-discipline/0001` — not refiled here.

## Coverage matrix

Per-command help dumps under
`/tmp/gale-ro-audit-help-text/help/*.txt`.

| Command            | Help dumped | Flags read | Cross-check | Notes |
|--------------------|:-----------:|:----------:|:-----------:|-------|
| list               | yes         | yes        | yes         | `--scope` valid; not in manpage |
| info               | yes         | yes        | yes         | `ExactArgs(1)`; `@version` quietly hits registry and 404s |
| search             | yes         | yes        | yes         | `<query>`, ExactArgs(1) |
| doctor             | yes         | yes        | yes         | `--repair` flag undocumented in manpage |
| env                | yes         | yes        | yes         | `--vars-only` documented; no scope flags despite resolveGaleDir |
| hook               | yes         | yes        | yes         | finding 0002 |
| lint               | yes         | yes        | yes         | MinimumNArgs(1); CLAUDE.md shows singular `<recipe.toml>` |
| verify             | yes         | yes        | yes         | `<package>` ExactArgs(1); Long mentions gh CLI requirement |
| sbom               | yes         | yes        | yes         | `--json` documented |
| which              | yes         | yes        | yes         | `<binary>` ExactArgs(1) |
| outdated           | yes         | yes        | yes         | finding 0003 (recipes default) |
| audit              | yes         | yes        | yes         | finding 0005 |
| generations        | yes         | yes        | yes         | "list" and "show" subcommands referenced in audit README don't exist |
| generations diff   | yes         | yes        | yes         | RangeArgs(0,2); accepts `[from] [to]` |
| generations list   | yes (err)   | n/a        | n/a         | not a real subcommand — bare `generations` is the list |
| generations show   | yes (err)   | n/a        | n/a         | not a real subcommand |
| hook direnv        | yes         | n/a        | yes         | `<shell>` only accepts `direnv` |

## Cross-doc consistency

- CLAUDE.md "CLI Commands" section omits commands that the
  binary registers: `outdated`, `which`, `inspect`, `pin`,
  `unpin`, `repo`, `create-recipe`, `completion`. Not filed
  as a finding — it's a docs-vs-implementation drift that
  affects Claude's onboarding, not user-facing help.
- `audit/readonly/README.md` lists `diff` as a target read-only
  command. There is no top-level `gale diff`; the actual
  command is `gale generations diff`. The README likely meant
  the latter — verify before audit closeout.
- Root help lists `pin`, `unpin`, `inspect`, `repo`,
  `create-recipe` but neither manpage nor CLAUDE.md mentions
  several of them. Most are out of scope (mutating). Worth
  cross-checking in `read-only-invariant` wave-2 if `inspect`
  truly is read-only.

## TODOs (speculative, did not make the finding bar)

- `info <package>` quietly falls through to registry for
  uninstalled packages, but help only says
  `Show package information`. Users won't know `info` also
  works on uninstalled packages. Speculative — could be
  intentional; CLAUDE.md mentions registry fallback. Not
  refiled.
- Description-style consistency: most Shorts are "Show X" /
  "List X" / "Print X" / "Check X" / "Validate X" / "Search
  for X". No egregious outliers. Skipped.
- `lint --help` shows usage `<recipe.toml> [recipe.toml...]`,
  CLAUDE.md uses singular. Minor; not refiled.
- Inherited-flag visibility (finding 0001) — also affects
  every mutating command. Out of scope but worth carrying to
  any future "help template overhaul" ticket.
- `sbom` Long mentions "install method" as a column but
  `Method` is heuristically `binary` if `ArchiveSHA256` matches
  the recipe's binary hash, otherwise `source`. Help doesn't
  explain the heuristic. Borderline finding; not refiled.
- Generations subcommand help template (when an unknown
  subcommand is passed) uses cobra's default usage block,
  which is visually different from `colorHelp`. Two help
  styles coexist. Stream/format concern more than content.
- Manpage `outdated` example uses ASCII `->`, code emits `→`.
  Folded into finding 0004 rather than its own file.

## Wave-1 findings noted (do not refile)

- `stream-discipline/0001-help-on-stderr.md` — every command's
  help goes to stderr because `colorHelp` uses
  `cmd.OutOrStderr()`. Content audit deferred to that
  finding for the routing concern. Content-accuracy issues
  filed independently here.
