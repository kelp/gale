# bad-input — state

Dimension: bad-input on read-only commands.
Scratch HOME: `/tmp/gale-ro-audit-bad-input`.
Binary: `/home/tcole/code/gale/gale` (v0.16.2-dev.21+39c9485).

## Coverage matrix

| Command            | nonexistent | @version | unknown flag | no-arg | extra-arg | injection / paths |
|--------------------|-------------|----------|--------------|--------|-----------|-------------------|
| `list`             |   n/a       |  n/a     |     OK       | OK     | bad: F-T1 |        n/a        |
| `info`             |  weak: F1   |  F1      |     OK       | OK     | OK        | weak: F2          |
| `search`           |   T2        |  n/a     |     OK       | OK (T3)| OK        | T2                |
| `doctor`           |   n/a       |  n/a     |     OK       | OK     | bad: F-T1 |        n/a        |
| `env`              |   n/a       |  n/a     |     OK       | OK     | n/a       |        n/a        |
| `hook`             |   n/a       |  n/a     |     OK       | OK     | OK        | weak: T4          |
| `lint`             |   n/a       |  n/a     |     OK       | OK     | n/a       | OK (sym loop, dir)|
| `verify`           |   OK        |  F3      |     OK       | OK     | OK        | OK                |
| `sbom`             |  OK         |  weak: T5|     OK       | OK     | OK        | OK                |
| `which`            |   OK        |  n/a     |     OK       | OK     | OK        | OK (Rel HasPrefix)|
| `outdated`         |   F4        |  F4      |     OK       | OK     | F4        |        n/a        |
| `generations`      |   T6        |  n/a     |     OK       | OK     | bad: F-T1 |        n/a        |
| `generations diff` |   weak: T6  |  n/a     |     OK       | OK     | OK        | weak: T6          |
| `audit`            |   OK*       |  T7      |  not tested  | OK     | not tested| not tested        |

F1–F5 → findings/000{1..5}-*.md.
"OK" = behaves reasonably and exits 1 with a useful message.
"weak" = exits 1 but the message is misleading or generic.
"bad" = TODO below.

## Speculative TODOs (didn't make the bar)

- **T1 (`list`/`doctor`/`generations` reject positional args via
  generic cobra error)**: `gale list nosuchpkg` → `unknown
  command "nosuchpkg" for "gale list"` (exit 1). For
  `outdated` this is documented-but-broken (finding 0004); for
  `list`/`doctor`/`generations` it's just cobra default and is
  probably fine, but a custom error would be friendlier
  ("`list` takes no arguments — try `gale info <pkg>`").

- **T2 (`gale search ''` and `gale search 'jq?inj'`)**: empty
  query returns "No packages found matching \"\"" and exits 0.
  Probably correct, but worth confirming the search backend
  doesn't make an unbounded request when the query is empty
  or contains URL-meaningful chars. Search the registry to
  see if `?inj` reached the network.

- **T3 (`gale search` with no arg)**: not tested for what it
  returns — might dump all recipes (could be slow). Worth a
  separate pass.

- **T4 (`gale hook` shell name validation)**: `gale hook bash`
  says "unsupported shell". OK. But the command takes any
  string — would `gale hook '$(touch /tmp/pwn)'` ever be
  eval'd by anything? (Probably not — output is meant for
  `eval "$(gale hook direnv)"` in user's shell config — but
  worth a defensive check.)

- **T5 (`gale sbom 'jq@1.7'`)**: like `verify`, sbom doesn't
  parse `@` — message is "jq@1.7 not found in gale.toml".
  Same misleading-error shape as findings 0001/0003. Could
  be rolled into a single sweep across all version-aware
  read-only commands.

- **T6 (`gale generations diff` with non-numeric args)**: with
  zero generations, the command short-circuits with "no
  current generation" before validating arg syntax. If we
  set up a fake generation, the strconv.Atoi error path
  (`cmd/gale/generations.go:77-93`) is reachable but produces
  a fine error. Worth a test for `diff -1 -2` (cobra eats
  `-1` as a flag — actual user might quote).

- **T7 (`gale audit jq@1.7`)**: didn't test. Likely same shape
  as `verify` (finding 0003) since it follows the same
  lockfile-lookup-by-bare-name pattern.

- **`info` panic risk on multi-byte first rune**: `bucket :=
  string(name[0])` in `internal/registry/registry.go:103`
  takes the first *byte*, not rune. A name like `"éclat"`
  becomes bucket `string(0xc3)` and would land in a URL
  bucket that doesn't exist. Not a panic (string conversion
  of a byte is safe), but odd. Mentioned briefly in finding
  0002.

- **`gale audit --json`?**: audit has no `--json` flag, but
  `outdated` doesn't either despite the audit covering JSON
  output as a separate dimension. Cross-reference with
  `output-format` findings to avoid double-reporting.

## Cap hit

5 findings filed. Stopping here per dimension cap.
