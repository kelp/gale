# exit-codes dimension — state

Scratch HOME: `/tmp/gale-ro-audit-exit-codes`
Binary tested: `/home/tcole/code/gale/gale` (built from main @
`33901f6`).

## Coverage matrix

| Command           | not-found | empty state | malformed cfg | extra args |
|-------------------|:---------:|:-----------:|:-------------:|:----------:|
| list              |    n/a    |     0       |       1       |     1      |
| info <pkg>        |     1     |     1       |       1       |    n/a     |
| search <q>        |     0 !   |    n/a      |     n/a       |    n/a     |
| doctor            |    n/a    |   1 (fail)  |    1 (fail)   |     1      |
| env               |    n/a    |     0       |     0 !       |     1      |
| hook <shell>      |   1 !!    |    n/a      |     n/a       |     1      |
| lint <recipe>     |     1     |    n/a      |       1       |    n/a     |
| verify <pkg>      |     1     |     1       |     1 *       |    n/a     |
| sbom              |    n/a    |     1 !     |       1       |     1      |
| sbom <pkg>        |     1     |     1       |       1       |    n/a     |
| which <bin>       |     1     |     1       |     n/a       |    n/a     |
| outdated          |    n/a    |     0       |       1       |     1      |
| outdated (resolver fail) |  -  |   0 !!    |     -         |     -      |
| generations       |    n/a    |     0       |     n/a       |     1      |
| generations diff  |    n/a    |     1 !     |     n/a       |     1      |
| audit <pkg>       |     1     |     1       |     1 *       |    n/a     |

! = inconsistency captured in findings/.
* = inferred from code path (lockfile read precedes config parse).
!! = "unsupported shell" for `hook bash` is correct, but cobra's
     declared `ValidArgs: ["direnv"]` is not enforced — the error
     comes from `env.GenerateHook`, not arg validation. Out of
     scope for this dimension.

## Findings filed

1. `0001-env-swallows-malformed-config.md` — high
2. `0002-outdated-exit-zero-on-resolver-failure.md` — high
3. `0003-list-vs-sbom-empty-state-mismatch.md` — medium
4. `0004-search-no-match-exits-zero.md` — low
5. `0005-generations-diff-empty-state-mismatch.md` — medium

Count: 2 high, 2 medium, 1 low.

## Confirmed-but-cap-hit observations

These are real but didn't make the cap of 5. Promote later if
priorities shift.

- **`sbom` double-wraps the error**: `"reading config: reading
  config: open ... no such file"`. Cosmetic, cmd/gale/sbom.go:46-49
  wraps the inner `resolveSbomConfig` error which is already
  prefixed `reading config:`. Captured in 0003.
- **`hook bash` returns "unsupported shell" exit 1 from
  `env.GenerateHook`** rather than from cobra arg validation, even
  though `ValidArgs: []string{"direnv"}` is declared. The set is
  advisory only without `cobra.OnlyValidArgs`. Mostly a help-text /
  bad-input concern, deferred to those dimensions.
- **`verify` precedence**: gh-CLI check fires before the lockfile
  check, so a user without `gh` who runs `verify nonexistent` gets
  "gh CLI required" instead of "not installed". Defensible, but
  inverts the "validate args first" principle.

## Speculative TODOs

- **`inspect` uses raw `os.Exit(1)`** at `cmd/gale/inspect.go:144`
  — bypasses cobra error handling and the JSON error envelope
  (`executeRoot` in `root.go:48-66`). `inspect` is not in the
  read-only target list for this round, but the same pattern in
  any target command would silently emit no JSON on error when
  `--error-format json` is in effect. Worth checking the other
  targets for the same anti-pattern (none found so far).
- **`doctor`'s "warn but pass" granularity is undocumented**:
  `checkHostOverrides`, `checkStaleInstalls`, `checkOrphans`,
  `checkGhCLI`, `checkDirenvIntegration` (if no `.envrc`) all
  return true on warnings. The user-facing contract for "doctor
  exit code 0 means everything is fine" is not written down. A
  caller scripting `gale doctor || alert` may be surprised that
  orphaned versions don't trip the alert. Borderline-medium.
- **`gale env` with no `gale.toml` (global or project) and no
  `~/.gale/`** still prints `export PATH=".../current/bin:$PATH"`
  and exits 0. Reasonable — the PATH line is harmless if the
  directory doesn't exist — but `gale env` printing exports for a
  non-existent gale install may confuse new users.
- **Empty-string arg acceptance**: `info ""` exits 1 with
  "fetch recipe: name must not be empty" (good — caught by the
  registry layer). `search ""` exits 0 (filed in 0004). `which ""`
  exits 1 with " not found in gale" (works but the leading space
  in the message is ugly).
- **`gale outdated extraarg`** produces `unknown command "extraarg"
  for "gale outdated"` — confusing because `outdated` doesn't have
  subcommands. Stems from cobra treating positional args as
  candidate subcommands by default. Bad-input dimension territory.

## Cross-references

- `audit/behaviour/findings/` (cluster 0009–0011, commit `d5ca2ad`)
  fixed exit codes on partial-failure for mutation commands
  `sync`/`update`/`gc`. Finding 0002 here is the read-only
  counterpart — `outdated` was not touched in that pass.
- `audit/readonly/output-format/` (sibling dimension) — overlapping
  ground on the JSON error envelope. If a target command bypasses
  cobra (e.g. `os.Exit(1)`), `executeRoot`'s JSON envelope at
  `cmd/gale/root.go:51-65` never fires. None of the read-only
  target commands do this today.
