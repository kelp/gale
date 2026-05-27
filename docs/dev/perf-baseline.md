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
paste-ready Markdown.

The harness is destructive — it uninstalls and reinstalls each
package via both gale and brew. Don't run it on a workstation in the
middle of real work.

## Reference run

Fill these in once captured. Keep older runs in their own subsections
so trends are visible.

### Run: <YYYY-MM-DD> on <machine>

- Date: YYYY-MM-DD
- Machine: <model / chip / RAM>
- gale version: `gale --version` →
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
