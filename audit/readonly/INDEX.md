# Read-only audit — INDEX

46 findings across 10 dimensions. All confirmed except one (likely).

**Severity counts:** 1 critical · 12 high · 22 medium · 11 low.

## Cross-cutting themes

These keep showing up across dimensions and are worth treating as
clusters rather than per-finding fixes.

1. **`gale env` is broken three ways.** Swallows malformed
   gale.toml and exits 0 with no exports (exit-codes 0001,
   empty-state 0003); reads `[vars]` only from project gale.toml
   so a global cwd silently drops every global var
   (scope-behaviour 0002); a script using `eval "$(gale env)"`
   has no way to detect failure. Highest-impact correctness gap
   in the audit because `env` is the one command explicitly
   designed to be machine-sourced.

2. **Read-only commands silently write to disk.** `cachedGet`
   persists every 200-OK ETag response under `~/.gale/cache/`
   from `info`, `sbom`, `outdated`, `doctor` first-run paths;
   `--dry-run`, `--no-refresh`, and `GALE_OFFLINE=1` all fail
   to suppress (read-only-invariant 0002–0005). Worse, `gale
   audit` calls `Installer.InstallBuildDeps` and materialises
   build-dep packages into `~/.gale/pkg/` while "auditing" —
   the act of verifying mutates the surface being verified
   (read-only-invariant 0001, the only **critical**).

3. **Offline behaviour is pathological.** ETag cache only
   serves on `304`; any network failure bypasses cache and
   propagates as error (network-perf 0002). `outdated` then
   loops serially with a 30s timeout per package, prints
   `Skipping <pkg>: <err>` for each, then exits 0 with
   `==> Everything is up to date.` (empty-state 0002,
   exit-codes 0002, network-perf 0003). A 3-package
   `outdated` against an unreachable registry takes ~90s
   and lies.

4. **Read-only commands have no scope flags.** Mutation
   commands got `-g`/`-p` via behaviour cluster 0001–0003,
   but `list`, `info`, `sbom`, `outdated`, `env`, `which`,
   `verify`, `audit`, `generations` were all left without
   any way to inspect the global scope from inside a project
   (scope-behaviour 0001 + 0003 + 0004 + 0005). Pairs with
   the `env` global-vars bug above.

5. **Help output is wrong stream + missing flags.** Every
   `--help` writes to stderr (cobra default is stdout —
   stream-discipline 0001). Custom help template prints only
   `cmd.LocalFlags()`, so six persistent globals
   (`--no-color`, `--plain`, `--verbose`, `--quiet`,
   `--dry-run`, `--error-format`) are invisible in every
   subcommand's help (help-text 0001). Manpage also drifted
   (help-text 0004).

6. **`gale info` is the most user-hostile command.**
   Doesn't parse `@version` so `info jq@1.7` hits HTTP 404
   (bad-input 0001); interpolates the recipe name directly
   into a `raw.githubusercontent.com` URL with zero validation
   (bad-input 0002 — low-grade SSRF / arbitrary-URL fetch);
   bypasses TTY/color discipline (tty-vs-nontty 0002);
   makes a redundant second HTTP request per call
   (network-perf 0005).

7. **`gale doctor` doesn't diagnose what it exists to
   diagnose.** Reports a dangling `current` symlink as a
   healthy generation (empty-state 0001) — the one
   condition the command was written to catch. Plus: hits
   network during diagnosis (network-perf 0004), writes
   zero stdout in every scenario (stream-discipline 0003),
   populates the registry cache as a side effect
   (read-only-invariant 0002).

## By severity

### Critical (1)

- `read-only-invariant/0001` — `gale audit` mutates the store
  by installing build deps. **commands:** audit.

### High (12)

- `bad-input/0001` — `info <pkg>@<ver>` doesn't parse @version.
- `bad-input/0002` — recipe name injected into registry URL.
- `empty-state/0001` — `doctor` reports dangling `current` as
  healthy.
- `empty-state/0002` — `outdated` reports up-to-date when all
  resolves fail.
- `exit-codes/0001` — `env` swallows malformed config, exits 0.
- `exit-codes/0002` — `outdated` exits 0 on resolver failure.
- `network-perf/0002` — ETag cache useless when offline.
- `network-perf/0003` — `outdated` serial 30s/pkg timeout.
- `scope-behaviour/0001` — no scope flags on any read-only
  command.
- `scope-behaviour/0002` — `env` drops global `[vars]`.
- `stream-discipline/0001` — `--help` writes to stderr.

### Medium (22)

- `bad-input/0003` — `verify <pkg>@<ver>` doesn't parse @ver.
- `bad-input/0004` — `outdated <pkg>` rejected as unknown subcmd.
- `bad-input/0005` — `sbom --json` emits `null` on empty.
- `empty-state/0003` — `env` drops vars on malformed gale.toml.
- `empty-state/0004` — `list` reports declared as installed.
- `empty-state/0005` — `sbom` inconsistent empty-state + null JSON.
- `exit-codes/0003` — `list` vs `sbom` empty-state mismatch.
- `exit-codes/0005` — `generations` subcmds disagree on empty.
- `help-text/0001` — help template hides global flags.
- `help-text/0003` — `outdated --recipes` default misleading.
- `help-text/0004` — manpage missing flags/commands.
- `network-perf/0001` — `search` bypasses ETag cache.
- `network-perf/0004` — `doctor` hits network.
- `output-format/0001` — `sbom --json` null on empty.
- `output-format/0002` — `NO_COLOR` env ignored.
- `output-format/0003` — `list` schema swaps on host overlays.
- `read-only-invariant/0002` — `doctor` populates registry cache.
- `read-only-invariant/0003` — `sbom`/`outdated`/`info` write cache.
- `read-only-invariant/0004` — `--dry-run` doesn't suppress cache.
- `read-only-invariant/0005` — `GALE_OFFLINE` still writes cache.
- `scope-behaviour/0003` — `info` shadows global, no override.
- `scope-behaviour/0004` — `verify`/`audit` scope locked to cwd.
- `scope-behaviour/0005` — `list` has no cross-scope view.
- `stream-discipline/0002` — `outdated` empty-state on stderr.
- `stream-discipline/0003` — `doctor` writes zero stdout.

### Low (11)

- `exit-codes/0004` — `search` no-match exits 0.
- `help-text/0002` — `hook --help` says `<shell>`, accepts only direnv.
- `help-text/0005` — `audit` short help overpromises vs long.
- `network-perf/0005` — `info` extra HTTP roundtrip.
- `output-format/0004` — `outdated` nondeterministic order.
- `output-format/0005` — `lint` warnings use info prefix.
- `read-only-invariant/0003` — repeats above.
- `stream-discipline/0004` — `audit` bypasses output helper.
- `tty-vs-nontty/0001` — color keyed only on stderr TTY.
- `tty-vs-nontty/0002` — `info`/`which` bypass TTY discipline.
- `tty-vs-nontty/0003` — no prompt guard for future interactive use.

## Suggested fix clustering

If you fix in clusters the way the mutation audit did, the
natural groupings are:

- **Cluster RO-A — `env` correctness.** exit-codes/0001,
  empty-state/0003, scope-behaviour/0002. Same lenient
  `return nil //nolint:nilerr` handler at `cmd/gale/env.go:33-48`.

- **Cluster RO-B — registry cache is a write surface.**
  read-only-invariant/0002–0005 + network-perf/0001. Decide:
  is the cache part of the read-only contract or not? Then
  apply consistently (offline-suppress, dry-run-suppress,
  document, or move out of `~/.gale/cache/` into a documented
  side-channel).

- **Cluster RO-C — offline / `outdated` correctness.**
  empty-state/0002, exit-codes/0002, network-perf/0002,
  network-perf/0003. Stale-on-error in `cachedGet` plus an
  exit-code fix in `outdated`.

- **Cluster RO-D — `info` hardening.** bad-input/0001,
  bad-input/0002, tty-vs-nontty/0002, network-perf/0005.
  Add `@version` parsing, validate the recipe-name → URL
  segment, route through `internal/output/`.

- **Cluster RO-E — read-only scope flags.** All five
  scope-behaviour findings. Add `-g`/`-p` to read-only
  commands; settle the "which scope wins" rule consistently.

- **Cluster RO-F — help / docs.** stream-discipline/0001
  (stderr → stdout), help-text/0001 (show globals),
  help-text/0002–0005 + output-format/0002 (`NO_COLOR`).
  All small. Plus README/manpage updates.

- **Cluster RO-G — `doctor` actually diagnoses.**
  empty-state/0001, stream-discipline/0003, network-perf/0004,
  read-only-invariant/0002.

- **Cluster RO-H — empty/null JSON.** bad-input/0005,
  output-format/0001, empty-state/0005. Trivial.

- **Cluster RO-I — `list` consistency.** empty-state/0004,
  exit-codes/0003, output-format/0003, scope-behaviour/0005.

- **Cluster RO-J — `generations` empty-state + `lint`
  prefix + audit output helper + `outdated` ordering.**
  The remaining small stuff.

## Scope notes flagged by subagents

- README listed `gale diff` as a target; that command doesn't
  exist as a top-level — it's `gale generations diff`.
- README referenced `gale generations list`/`show` subcommands
  that don't exist; bare `gale generations` is the list.

Update README and re-run if these are real gaps; otherwise just
note them as out-of-scope.
