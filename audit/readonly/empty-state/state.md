# empty-state — coverage and TODOs

## Harness

`/tmp/gale-ro-audit-empty-state/run.sh` builds each
state under `sandbox/`, then runs every read-only
target command, capturing stdout/stderr/exit per
(state × command). All raw output preserved under
`/tmp/gale-ro-audit-empty-state/out/<state>/`.

Built binary: `/home/tcole/code/gale/gale` (0.16.2-dev).

## Coverage matrix

States exercised (rows) × commands (cols).
`+` = run with output captured, `e` = errors (recorded
but not always a finding), `-` = command does not
exist (see TODO).

|                          | list | info | search | doctor | env | env --vars-only | hook direnv | verify | sbom | sbom --json | which | outdated | generations | generations diff |
|--------------------------|------|------|--------|--------|-----|-----------------|-------------|--------|------|-------------|-------|----------|-------------|------------------|
| fresh ($HOME, no ~/.gale)| +    | +    | +      | +e     | +   | +               | +           | +e     | +e   | +e          | +e    | +        | +           | n/a              |
| ~/.gale, no gale.toml    | +    | +    | +      | +e     | +   | +               | +           | +e     | +e   | +e          | +e    | +        | +           | n/a              |
| empty gale.toml          | +    | +    | +      | +e     | +   | +               | +           | +e     | +    | +           | +e    | +        | +           | n/a              |
| [vars]-only gale.toml    | +    | +    | +      | +e     | +   | +               | +           | +e     | +    | +           | +e    | +        | +           | n/a              |
| declared, not installed  | +    | +    | +      | +e     | +   | +               | +           | +e     | +    | +           | +e    | +        | +           | n/a              |
| store but no current     | +    | +    | +      | +e     | +   | +               | +           | +e     | +    | +           | +e    | +        | +           | n/a              |
| dangling current symlink | +    | +    | +      | +e     | +   | +               | +           | +e     | +    | +           | +e    | +        | +           | n/a              |
| gens exist, no current   | +    | +    | +      | +e     | +   | +               | +           | +e     | +    | +           | +e    | +        | +           | n/a              |
| offline (registry block) | +    | +    | +e     | +e     | +   | +               | +           | +e     | +    | +           | +e    | +        | +           | n/a              |
| malformed gale.toml      | +e   | +e   | +      | +e     | +   | +               | +           | +e     | +e   | +e          | +e    | +e       | +           | n/a              |

The `e` annotations are "errored" — sometimes correct
(verify/which on missing pkg), sometimes the actual
bug (sbom on missing config, env on malformed toml).
See findings for which is which.

`generations diff` was not exercised — its real
behaviour was outside the empty-state matrix once the
README's `list`/`show` subcommands turned out to be
nonexistent (see TODOs below).

## Findings shipped

5/5 cap reached.

1. doctor misses dangling current symlink — high
2. outdated says "Everything is up to date" when
   every recipe lookup fails — high
3. env silently drops [vars] on malformed gale.toml — medium
4. list reports declared packages as installed — medium
5. sbom inconsistent empty-state and `null` JSON — medium

## Speculative / cross-dimension TODOs

These didn't make the finding bar (low confidence,
better fit for another dimension, or trivial doc nits)
but are worth a look from the right reviewer.

- **doc bug, audit-meta**: `audit/readonly/README.md`
  lists `generations list` / `generations show` and
  `diff` as target subcommands. The actual subcommands
  registered in `cmd/gale/generations.go` are `diff`
  and `rollback`; the bare `gale generations` acts as
  the list. README needs an update *or* the missing
  subcommands need restoring.
- **bad-input dimension**: `gale list -g` / `gale list -p`
  fail with "unknown shorthand flag" — CLAUDE.md
  documents these as scope flags, but `list` uses
  `--scope all|shared|host` instead. Several other
  commands (`info`, `search`, `outdated`, `sbom`,
  `verify`) also lack `-g`/`-p`. Either the docs are
  stale or the flags should be added uniformly.
  Belongs in scope-behaviour or help-text dimension.
- **output-format dimension**: `info <pkg>` shows
  totally different fields depending on whether the
  package is in gale.toml or only in the registry —
  installed/declared form drops the description,
  source URL, and "(latest)" hint. Worth a look as
  an output-format inconsistency.
- **stream-discipline dimension**: `outdated`'s
  warning lines and the final "Everything is up to
  date." both go to stderr; the actual `name old → new`
  rows go to stdout. Mixed but at least documented.
  The doubled `reading config: reading config:` in
  `sbom` (see finding 0005) is another wrap nit.
- **bad-input / stream-discipline**: `gale diff` is
  not a registered command — `Error: unknown command
  "diff" for "gale"`. README lists it. Either doc
  bug or missing implementation.
- **read-only-invariant dimension**: `doctor` calls
  `newCmdContext("", false, false)` — best-effort —
  but doesn't otherwise mutate. Did not see any
  writes during the empty-state runs. Worth a focused
  check by the read-only-invariant agent.
- **dangling current — secondary**: `which jq` against
  a dangling current symlink errors with
  `jq not found in gale` (generic), not "current
  symlink dangles". Same root issue as finding 0001.
  Would fold into that fix.
- **vars-only env, project scope**: in a project that
  has `[vars]` set but no `[packages]`, `gale env
  --vars-only` printed nothing (the harness only
  exercised global scope). Worth re-running with a
  project gale.toml to confirm project [vars] are
  picked up correctly.
- **verify**: rejects `gale verify jq` against a
  package present in gale.toml but absent from the
  lockfile with "install it first" — the message is
  correct but misleading when the user did install,
  then deleted the lockfile. Edge case, low value.
- **sbom --json `null`**: covered in finding 0005,
  but worth a sweep for other JSON outputs that may
  encode nil slices. Currently only `sbom` has
  `--json`; future-proofing concern.

## TODO count: 9
