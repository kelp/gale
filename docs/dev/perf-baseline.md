# Performance baseline: gale vs Homebrew

A fixed reference for tracking gale install performance. Re-run the
harness after meaningful changes (parallelism, streaming, GHCR token
caching, etc.) and append a new column or sub-section so we can see
whether the change moved the numbers — not just whether tests still
pass.

The threat model is **user patience**: cumulative time spent on
multi-package `gale sync` and `gale outdated`, plus the visible cost
of a cold single-package install. Numbers below should reflect that
mix, not micro-benchmarks of any one phase.

## How to capture

```sh
./scripts/perf-baseline.sh --dry-run   # preview commands
./scripts/perf-baseline.sh --yes       # actually run + measure
```

The harness wipes per-package gale + brew state, runs each scenario
3 times, prints the median in whole seconds, and emits a Markdown
block. Redirect stdout to a file (or paste under "Results"):

```sh
./scripts/perf-baseline.sh --yes > /tmp/baseline.md
```

Status / progress output goes to stderr, so the captured stdout is
paste-ready Markdown. The emitted block now stamps the **gale version
and platform** (`OS/arch`) at the top, captured from the binary
actually measured — so you no longer fill those in by hand.

By default the harness **builds gale from HEAD** (`just build`) and
measures that binary, asserting its reported version matches the
working tree. Set `GALE=<path>` to skip the build and measure a
specific binary (e.g. a released gale for a release-vs-HEAD
comparison); it warns if that binary doesn't look like a HEAD build.

All gale work happens in a throwaway isolated `$HOME`, so your real
`~/.gale/` is untouched. With `--with-brew` it also runs `brew
reinstall` (never uninstall), leaving brew state as it found it.
Still, don't run it on a workstation in the middle of real work —
it saturates network and CPU.

## Reference run

Fill these in once captured. Keep older runs in their own subsections
so trends are visible.

### Run: 2026-05-30 on MacBook Pro 16" M3 Max, gale v0.16.3

- Date: 2026-05-30
- Machine: MacBook Pro 16" (Mac15,9), Apple M3 Max, 16 cores
  (12 performance + 4 efficiency), 48 GiB RAM, macOS 26.5 (25F71)
- gale version: `0.16.3-dev.5+a26e188` (built from HEAD by the harness)
- Platform: `Darwin/arm64`
- Homebrew: 5.1.14
- Network: Wi-Fi
- Notes: First macOS run, and the first with the honest brew cold/warm
  comparison (`--with-brew` after the harness reworked the brew passes).
  All 5 packages installed from prebuilt binaries (preflight passed;
  verified zero source/compile activity in the timed runs — confirmed a
  cold `bat` install streams its whole closure as `binary-stream` with
  zero build output). eza cold (35s) and bat cold (40s) pull their full
  binary dependency closure into the cold store (openssl, libgit2,
  libssh2, oniguruma, cmake, zlib, pkgconf), which dominates those two
  rows; the lighter tools (jq/fd/ripgrep) land in 5-10s. gale numbers
  track the 2026-05-29 Linux run within whole-second resolution despite
  different hardware, because the cost is GHCR fetch + attestation +
  extraction, not local CPU. gale cold is **slower than brew cold
  across the board** — jq/fd/ripgrep 5/5/10s vs brew's flat 2s, and
  widest on the heavy closures (40/35s vs 19/18s for bat/eza). brew's
  bottle-closure re-fetch beats gale's per-component attestation
  verification (`gh attestation verify` shellouts) plus per-blob GHCR
  fetch/extract; the GHCR token itself is cached per process (T1.0),
  so it is not the cost. That gap is the thing the perf loop is meant
  to close.
  This run also depends on the harness gh-token fix (commit 48f234f):
  the isolated `$HOME` left `gh` unable to reach its keychain-stored
  token, so attestation failed and prebuilts source-fell-back (eza
  cascaded into a full rustc-toolchain compile); the harness now
  resolves `gh auth token` under the real HOME and exports `GH_TOKEN`
  to every isolated gale call.

#### Per-package install (seconds, median of 3)

| package | gale cold | gale warm | brew cold | brew warm |
|---------|-----------|-----------|-----------|-----------|
| jq      |         5 |         0 |         2 |         1 |
| fd      |         5 |         0 |         2 |         1 |
| ripgrep |        10 |         0 |         2 |         1 |
| bat     |        40 |         0 |        19 |         2 |
| eza     |        35 |         0 |        18 |         2 |

`brew cold` clears the download cache for the package and its full
dependency closure, then reinstalls the whole closure — every bottle
re-fetched over the network and relinked, the honest analog to gale
cold (which pulls the closure into an empty store). `brew warm`
reinstalls the leaf with its bottle cached (relink only). gale warm is
an idempotent skip (~0s) whereas brew always relinks, so brew warm >
gale warm is expected. Neither brew path ever uninstalls.

#### Multi-package gale sync (seconds, single run, 5 packages)

| operation        | seconds |
|------------------|---------|
| gale sync (cold) |   39    |

Sync (39s) ≈ the slowest single package (bat, 40s) rather than the sum
of all five (~95s), confirming the parallel-install path (T1.2) holds
on macOS too.

#### Phase timing breakdown (jq cold install, `gale --verbose`)

```
[timing] recipe-fetch jq elapsed=46ms
[timing] ghcr-token kelp/gale-recipes/jq elapsed=330ms
[timing] binary-stream jq@1.8.1 elapsed=243ms
[timing] lockfile-write jq elapsed=5ms
```

### Run: 2026-05-29 on Linux cloud VM (gale-only), gale v0.16.3

- Date: 2026-05-29
- Machine: Debian 13 (trixie) cloud VM, x86_64, 4 cores, 15.3 GiB RAM
- gale version: `0.16.3-dev.1+ba697a9` (built from HEAD by the harness)
- Platform: `Linux/x86_64`
- Homebrew: n/a (Linux — brew comparison skipped, low signal)
- Network: cloud VM egress
- Notes: After the v0.16.3 macOS rpath fix and the rebuild of
  ripgrep/bat/eza to `-3` binaries (rpath fix). Re-run on Linux to
  confirm no regression; all 5 still install from prebuilt binaries.
  Timings are unchanged from the 2026-05-28 run within whole-second
  resolution — expected, since the rpath fix is macOS-only and doesn't
  touch the Linux install path.

#### Per-package install (seconds, median of 3)

| package | gale cold | gale warm |
|---------|-----------|-----------|
| jq      |         6 |       0   |
| fd      |         6 |       0   |
| ripgrep |        11 |       0   |
| bat     |        42 |       0   |
| eza     |        37 |       0   |

#### Multi-package gale sync (seconds, single run, 5 packages)

| operation        | seconds |
|------------------|---------|
| gale sync (cold) |   44    |

#### Phase timing breakdown (jq cold install, `gale --verbose`)

```
[timing] recipe-fetch jq elapsed=318ms
[timing] ghcr-token kelp/gale-recipes/jq elapsed=648ms
[timing] binary-stream jq@1.8.1 elapsed=624ms
[timing] lockfile-write jq elapsed=0s
```

### Run: 2026-05-28 on Linux cloud VM (gale-only)

- Date: 2026-05-28
- Machine: Debian 13 (trixie) cloud VM, x86_64, 4 cores, 15.3 GiB RAM
- gale version: `0.16.2-dev.94+92ee79e` (built from HEAD by the harness)
- Platform: `Linux/x86_64`
- Homebrew: n/a (Linux — brew comparison skipped, low signal)
- Network: cloud VM egress
- Notes: First valid run after the attestation fix (binary installs no
  longer source-fall-back) and the harness HEAD-build + sync-resolution
  fixes. All 5 packages installed from prebuilt binaries (preflight
  passed). bat/eza cold times include pulling their dependency-binary
  chains (rust, cmake, libgit2, openssl, …) into the cold store.

#### Per-package install (seconds, median of 3)

| package | gale cold | gale warm |
|---------|-----------|-----------|
| jq      |         6 |       0   |
| fd      |         6 |       0   |
| ripgrep |        11 |       0   |
| bat     |        42 |       0   |
| eza     |        37 |       0   |

#### Multi-package gale sync (seconds, single run, 5 packages)

| operation        | seconds |
|------------------|---------|
| gale sync (cold) |   41    |

Sync ≈ the slowest single package (bat, 42s) rather than the sum of all
five (~102s), confirming the parallel-install path (T1.2) is working.

#### Phase timing breakdown (jq cold install, `gale --verbose`)

```
[timing] recipe-fetch jq elapsed=326ms
[timing] ghcr-token kelp/gale-recipes/jq elapsed=598ms
[timing] binary-stream jq@1.8.1 elapsed=591ms
[timing] lockfile-write jq elapsed=0s
```

### Run: <YYYY-MM-DD> on <machine>

- Date: YYYY-MM-DD
- Machine: <model / chip / RAM>
- gale version / platform: auto-stamped at the top of the emitted block
- Homebrew version: `brew --version` →
- Network: <e.g. residential 200 Mbps wired, fresh DNS cache>
- Notes: <anything anomalous — VPN, throttled link, etc.>

#### Per-package install (seconds, median of 3)

| package | gale cold | brew cold | gale warm | brew warm |
|---------|-----------|-----------|-----------|-----------|
| jq      |           |           |           |           |
| fd      |           |           |           |           |
| ripgrep |           |           |           |           |
| bat     |           |           |           |           |
| eza     |           |           |           |           |

#### Multi-package install (5 packages, single run)

| operation               | seconds |
|-------------------------|---------|
| gale sync               |         |
| brew install (all pkgs) |         |

#### Phase timing breakdown (jq cold install, `gale --verbose`)

```
<paste [timing] lines here>
```

## Interpreting the numbers

- **gale cold > brew cold** is the expected starting point and the
  thing the perf loop is trying to close. Watch the trend, not the
  absolute gap.
- **gale sync vs brew install (all)** is the more important comparison
  for everyday use. Parallelising sync (Tier 1, T1.2) should move
  this number the most.
- **Phase timing**: the largest phase is the biggest target. Expect
  the ordering on a cold install to be roughly: binary-download >
  binary-extract > recipe-fetch > ghcr-token > everything else.
  Surprises here usually point at the next thing worth fixing.

## Caveats

- Whole-second precision. Sub-second noise (DNS jitter, page cache)
  is below the resolution; for installs of 5-60s that's fine.
- macOS-first. On Linux, Homebrew bottles behave differently (often
  source builds instead of bottles for some recipes); the brew
  column there is informational only.
- Single-machine baseline. Don't compare absolute numbers across
  machines — only deltas on the same machine before/after a change.
