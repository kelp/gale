# read-only-invariant — coverage and TODOs

## Harness

`/tmp/gale-ro-audit-readonly-invariant/run.sh` is a
baseline-reset + snapshot harness. For each target
command:
1. Reset `$HOME` to a fabricated state (jq + ripgrep
   "installed" with symlinked gens, project gale.toml,
   global gale.toml).
2. Snapshot paths + sha256 + mtimes of every file under
   `$HOME`.
3. Run `HOME=$HOME /home/tcole/code/gale/gale <args>`.
4. Re-snapshot and diff.

Outputs preserved under
`/tmp/gale-ro-audit-readonly-invariant/out/<name>.{paths,hashes,mtimes}.diff`
along with stdout/stderr/exit per run.

Built binary: `/home/tcole/code/gale/gale`
(0.16.2-dev.21+39c9485).

## Coverage matrix

`+` = ran, no mutation observed. `M` = ran, observed
mutation (finding filed). `e` = errored before write
(no observable effect on the invariant).

| Command                 | mutation? | notes                                  |
|-------------------------|-----------|----------------------------------------|
| list / list -g / -p     | +         | clean                                  |
| info (installed)        | +         | reads local config                     |
| info (uninstalled)      | M         | writes ~/.gale/cache/ (finding 0003)   |
| info (404)              | +         | clean — only 200 writes                |
| search                  | +         | clean (registry not reachable for q)   |
| doctor                  | M         | writes ~/.gale/cache/ (finding 0002)   |
| env / env --vars-only   | +         | clean                                  |
| hook direnv             | +         | clean                                  |
| verify                  | e         | gh auth error before any disk write    |
| sbom / --json / [pkg]   | M         | writes ~/.gale/cache/ (finding 0003)   |
| which                   | e         | resolver error, no writes              |
| outdated                | M         | writes ~/.gale/cache/ (finding 0003+5) |
| outdated --no-refresh   | M         | flag does NOT suppress (finding 0005)  |
| GALE_OFFLINE=1 outdated | M         | env var does NOT suppress (finding 5)  |
| generations             | +         | clean                                  |
| lint <recipe>           | +         | clean (offline)                        |
| audit <pkg>             | M         | installs deps into store! (finding 1)  |

## Findings shipped

5/5 cap reached.

1. `gale audit` writes new package dirs into
   `~/.gale/pkg/` via `InstallBuildDeps` — critical
2. `gale doctor` populates `~/.gale/cache/registry/`
   on first run — medium
3. `gale sbom`, `gale outdated`, and `gale info` (on
   uninstalled packages) all write the same registry
   cache — low
4. The global `-n / --dry-run` flag does not suppress
   any of these cache writes — medium
5. `--no-refresh` and `GALE_OFFLINE=1` only gate tap
   git-fetch, not HTTP registry GETs / cache writes —
   medium

## Re-run idempotence

Re-running `doctor` against a populated cache returns
304 Not Modified for every URL and writes nothing
(verified via mtime + content diff). The
"intentional cache write" classification is accurate
for the steady state — only first-run/expired cache
triggers writes. Severity reflects this.

## Out-of-scope speculation (TODOs)

- The source-tarball cache at `~/.gale/cache/<sha256>`
  (separate from registry cache, both live under same
  parent dir) is written by `gale build` paths. `gale
  audit` could also touch it during its rebuild. Not
  surfaced as a finding because audit's "rebuilds from
  source" carve-out arguably licenses it. Worth a
  cross-check once finding 0001 lands.
- `gale outdated` with configured taps will issue
  `git fetch` against `~/.gale/repos/<tap>/.git/` on
  every run. The audit harness had no configured taps,
  so this code path was not exercised dynamically. The
  code is at `cmd/gale/repo_resolver.go:132` and the
  invariant violation is identical in shape to the
  registry cache writes. Likely-confidence, not
  filed.
- `~/.gale/tmp/` is lazily `MkdirAll`'d in
  `internal/build/build.go:1153` (`TmpDir`). Audit
  exercises this. If audit's `InstallBuildDeps` fails
  before reaching `Build`, the empty `~/.gale/tmp/`
  may linger. Subsumed into finding 0001.
- `which` failed with "lstat .gale/gen/pkg: no such
  file or directory" against the fabricated layout
  (relative symlinks under `gen/1/bin`). Possibly a
  separate `which` bug in path resolution, but not
  invariant-related. Recorded here as a TODO for
  whichever wave owns `which`'s behaviour matrix.
- The harness fabricates the gale state by hand
  rather than `./gale install`-ing real packages — a
  full-build attempt OOM'd the rust dep tree on
  /tmp's 27 GB free. The fabricated state covers
  every code path that matters for the invariant
  (config + lockfile + pkg + gen + current), but a
  real install run would be a tighter reproducer.
