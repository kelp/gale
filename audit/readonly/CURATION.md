# Speculative-TODO Curation

Walk-through of the ~67 speculative items across the 10
`state.md` files. Categorised three ways:

1. **Closed incidentally** — already fixed by the main
   pipeline (RO-A…RO-J + followups). Listed for the record;
   no further action.
2. **RO-K cluster** — promoted to a fix cluster. Small,
   concrete, worth doing.
3. **Parked / deferred** — real but lower-leverage. Tracked
   here so they don't get re-discovered.

Date: 2026-05-24, after commit `56c9820`.

---

## Closed incidentally

These were flagged speculatively during the audit and were
fixed (incidentally) by the main pipeline. Confirmed by
inspecting current `main`.

| TODO | Source | Closed by |
|------|--------|-----------|
| `hook` shell `OnlyValidArgs` not registered | bad-input T4, output-format T4 | `537d53e` (RO-J hook fix) |
| `sbom <pkg>@<ver>` doesn't parse `@` | bad-input T5 | `e69868e` (F-1 parser strict) — `parsePackageArg` is now strict at the call site |
| Read-only commands lack `-g`/`-p` scope flags | empty-state cross-ref | `7769b00` (RO-E scope flags) |
| `outdated` mixed stderr/stdout | empty-state cross-ref, stream-discipline 0002 | `4a54c9e` (RO-F) and earlier waves |
| `doctor` mutation status unknown | empty-state cross-ref | `a237185` + `f420fe3` (RO-G — moved network behind `--check-registry`) |
| Dangling-current secondary surface for `which` | empty-state cross-ref | `f420fe3` (RO-G dangling symlink) |
| `cachedGet` writes during read-only | network-perf TODO | `2591722` (RO-B+C cache contract) |
| `outdated` serial 30s/pkg timeout | network-perf TODO | `06f6908` (RO-B+C early-skip) |
| `fetchBinaries` brittle `"HTTP 404"` substring match | network-perf TODO | partially — `69499b1` (F-4 negative cache) introduced `errHTTP404` sentinel; remaining substring match in `fetchBinaries` is now the only consumer |
| `info <pkg>@<ver>` doesn't parse `@` | scope-behaviour TODO | `74796d1` (RO-D), reinforced by `e69868e` (F-1) |
| `--help` lands on stderr | tty-vs-nontty TODO | `4a54c9e` (RO-F) |

---

## RO-K cluster — promote and fix

Six concrete items, all small, all in scope for one programmer
agent.

### K-1 — `outdated --no-refresh` should skip recipe fetch

**Source:** network-perf TODO.
**Problem:** `--no-refresh` only skips git tap refresh; the
per-package HTTP recipe fetch still happens. Misleading flag
name; users invoking it for offline expectations are confused.
**Fix:** Thread `--no-refresh` through to the resolver so it
uses cached recipes only (stale-on-error path from RO-B+C),
skipping live fetches.
**Files:** `cmd/gale/outdated.go`, possibly `internal/registry/`.
**Tests:** assert outdated under `--no-refresh` issues zero HTTP
requests when cache is warm.

### K-2 — `outdated` arrow fallback for `LANG=C`

**Source:** output-format T2.
**Problem:** `cmd/gale/outdated.go:94` prints `pkg cur → latest`
with a literal Unicode `→`. Renders as `?` or garbage under
`LANG=C` / `LC_ALL=C`.
**Fix:** Detect non-UTF-8 locale (`LC_ALL` / `LANG` ending in
non-UTF-8 charset) and fall back to ASCII `->`.
**Files:** `cmd/gale/outdated.go`.
**Tests:** unit test with `t.Setenv("LC_ALL", "C")` asserts
output contains `->` not `→`.

### K-3 — `inspect` mixed stream discipline

**Source:** output-format T6, stream-discipline cross-ref.
**Problem:** `cmd/gale/inspect.go` writes the issue tally to
stderr (`Fprintf(os.Stderr, "\n%d issue(s)...")`) while the
issues themselves go to stdout via `fmt.Println(k)`. Inconsistent
with the rest of gale's read-only stream discipline.
**Fix:** Route tally through `internal/output/` to stderr (it's
status, not data) OR route both to the same stream depending on
the contract. The data lines should stay on stdout; tally on
stderr is actually correct — but it bypasses the output helper
which makes the color/TTY decision. Route through the helper.
**Files:** `cmd/gale/inspect.go`.
**Tests:** assert tally line is written through the output
helper (color-suppressible, TTY-aware).

### K-4 — `sbom` doubled "reading config:" error wrap

**Source:** empty-state cross-ref, finding 0005.
**Problem:** Error wrapping double-counts the context phrase
when sbom's config path errors out, producing `reading config:
reading config: <inner>`.
**Fix:** Locate the inner wrap and drop one of the two layers.
RO-H may have already cleaned this — verify and fix if not.
**Files:** `cmd/gale/sbom.go`.
**Tests:** assert error message contains `reading config:`
exactly once.

### K-5 — `sbom` tabwriter alignment with empty fields

**Source:** output-format T1.
**Problem:** `sbom.go` tabwriter with `padding=2` produces
awkward two-space gaps when `License` or `SOURCE` is empty
(recipe resolution failed). Renders ragged.
**Fix:** Replace empty fields with `-` (or omit the column
when the entire column is empty). `-` is the simplest fix
that aligns.
**Files:** `cmd/gale/sbom.go`.
**Tests:** assert empty field renders as `-` (or chosen
placeholder).

### K-6 — Friendlier "no args allowed" message

**Source:** bad-input T1.
**Problem:** `gale list nosuchpkg`, `gale doctor nosuchpkg`,
`gale generations nosuchpkg` all return cobra's default
`unknown command "nosuchpkg" for "gale list"` which is
confusing — these commands accept zero args, not arbitrary
subcommands.
**Fix:** Set `Args: cobra.NoArgs` (or equivalent) on the
three commands so cobra emits the correct `accepts 0 arg(s),
received 1` instead.
**Files:** `cmd/gale/list.go`, `cmd/gale/doctor.go`,
`cmd/gale/generations.go`.
**Tests:** integration or unit test asserting the new error
shape.

---

## Parked / deferred

Real, but lower leverage or larger scope than RO-K. Tracked
here so they're discoverable.

- **Centralise the 30s HTTP timeout** (network-perf TODO).
  Set in four places (`registry.go:107,147,198`,
  `search.go:24`). Centralise behind a `[network] timeout`
  config knob. Refactor, not a bug; defer unless someone asks
  for per-network tuning.
- **Tap refresh writes during read-only invariant** (read-only-
  invariant TODO). `gale outdated` with a configured tap does
  `git fetch` against `~/.gale/repos/<tap>/.git/`. Same shape
  as the registry-cache invariant violation, but the tap
  refresh is more visibly "the user opted in" by configuring
  a tap. Defer pending a decision on whether taps should also
  honour `GALE_OFFLINE`.
- **`--json` on every read-only command** (output-format T7).
  Real feature, not a finding. Belongs in TODO.md proper.
  Natural follow-on to RO-H's `sbom --json` work. Punt to
  product planning.
- **`--error-format json` vs `--json` naming confusion**
  (output-format T8). Belongs alongside the `--json` rollout
  above — pick names that don't overlap conceptually.
- **`FORCE_COLOR` / `CLICOLOR_FORCE` env support** (tty-vs-
  nontty TODO). Mirror of NO_COLOR (closed by RO-F). Worth
  adding for symmetry but not user-blocking.
- **`doctor --repair` needs a `--yes` flag + prompt**
  (tty-vs-nontty TODO). Mutating command currently runs
  without confirmation. Track under mutation-command work,
  not the read-only audit.
- **`info` mixes "(latest)" / "(not installed)" status into
  the stdout data block** (stream-discipline TODO). Soft
  violation of "data only on stdout." Real but no consumer
  is hurt today.
- **`sbom` Method heuristic undocumented** (help-text TODO).
  The `Method` column is `binary` if `ArchiveSHA256` matches
  the recipe's binary hash, else `source`. Help doesn't
  explain the heuristic. Doc-only.
- **Prefix glyph inconsistency** (output-format T5). `-->`,
  `==>`, `!!!`, `xxx` are decided per call site. Belongs in
  `docs/dev/style-guide.md`.
- **`which ""` leading-space message** (exit-codes TODO).
  Cosmetic.
- **Bucket via `name[0]` byte vs rune** (bad-input TODO,
  partial — finding 0002). Multi-byte first rune lands in
  a bucket that won't resolve; not a panic. Already gated by
  `registry.ValidName` after F-1, which rejects non-ASCII —
  so this path is unreachable. Effectively closed; left here
  for documentation.
- **`gale --version` stream discipline not retested**
  (stream-discipline TODO). Cobra default routes to stdout;
  presumed OK. Verify once if curious.

---

## Audit closure

After RO-K lands, the read-only audit is closed:
- 46 findings filed → 46 commits on main.
- ~67 speculative TODOs → 11 closed incidentally, 6 promoted
  to RO-K, ~50 parked here for future reference.
- Integration suite green; lint clean.
