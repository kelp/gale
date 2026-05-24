# network-perf state

## Coverage matrix

| Command       | Network? | Cache? | Cold reqs (real reg) | Notes |
|---------------|----------|--------|-----------------------|-------|
| `search`      | yes      | NO     | 1 (`/index.tsv`)      | Bypasses `cachedGet` entirely. Plain `http.Get`. |
| `info` (uninstalled) | yes      | yes (ETag) | 2 (`.toml` + `.binaries.toml`) | Both go through `cachedGet`. |
| `info` (installed) | no | – | 0 | Reads config only. |
| `outdated`    | yes      | yes (ETag) | 2×N (N = packages in config) | Serial loop; 30s timeout per request. |
| `outdated --no-refresh` | yes | yes | 2×N | Skip flag only suppresses tap-refresh, not recipe fetch. |
| `sbom`        | yes      | yes | 2×N or 3×N | Calls `ResolveVersionedRecipe` per package — extra `.versions` fetch when pin ≠ latest. No `--no-refresh`. |
| `verify`      | yes      | no | shells out to `gh attestation verify` | Long timeout owned by `gh`. |
| `doctor`      | yes      | yes | 2×N | `checkOrphans` → `expandRuntimeDeps` → `Resolver` for every config entry. Behaviour is undocumented in `--help`. |
| `list`        | no | – | 0 | Reads gale.toml. |
| `env`         | no | – | 0 | |
| `which`       | no | – | 0 | |
| `lint`        | no | – | 0 | Local recipe only. |
| `hook`        | no | – | 0 | Static output. |
| `generations list/show` | no | – | 0 | |
| `audit`       | yes (out of scope — builds from source) | – | – | Network via build/download, not recipe fetch. |

(N = number of packages in the effective gale.toml.)

## Reproduction setup

- Scratch HOME: `/tmp/gale-ro-audit-network-perf`
- Local logging HTTP server: `/tmp/log_server3.py` (logs to `/tmp/server.log`),
  binds `127.0.0.1:9877`. Returns canned recipe + binaries with ETags so the
  cache path can be exercised.
- Offline simulations:
  - `[registry] url = "http://127.0.0.1:1"` — RST, immediate failure.
  - `[registry] url = "http://192.0.2.1"` — unrouted, 30 s timeout per call
    (`http.Client.Timeout` in `internal/registry/registry.go:107,147,198`).

## TODOs / speculative (not promoted to findings)

- The 30 s default `http.Client.Timeout` is set in four places
  (`registry.go:107,147,198`, `search.go:24`). Centralising would make a
  future `[network] timeout` config knob a one-line change.
- `gale verify` shells out to `gh attestation verify` — its timeout/retry
  behaviour is owned by `gh`, not gale. Not verified here.
- `cachedGet` writes to `~/.gale/cache/registry/<hash>/` even when the
  CLI is invoked read-only. Not strictly a write-violation finding (this
  audit lane is perf-only), but flagged for the read-only-invariant lane.
- `gale outdated --no-refresh` only skips git tap refresh, not the
  per-package HTTP recipe fetch. Misleading flag name; documented in the
  coverage matrix above but not promoted (it's a UX bug, not a perf bug).
- HTTP requests in `outdated`, `sbom`, and `doctor` are serial. Parallelising
  with a bounded errgroup would cut wall time roughly N× on the warm path.
  Not promoted because measured cold wall time on a real connection
  (1.24 s for 8 packages) is already tolerable; only the offline-timeout
  case (90 s for 3 packages) makes it pathological — and that is the
  finding logged below.
- `parseIndex` (search) doesn't reject malformed lines past the first
  tab; not a network/perf issue, possibly bad-input.
- `fetchBinaries` matches `"HTTP 404"` via `strings.Contains` on the
  error text. Already documented inline. Brittle but out of scope for
  perf.

## Findings

1. `0001-search-no-cache.md`
2. `0002-cache-useless-offline.md`
3. `0003-outdated-serial-timeout.md`
4. `0004-doctor-hits-network.md`
5. `0005-info-binaries-extra-roundtrip.md`
