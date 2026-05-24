# output-format audit state

Dimension: output formatting consistency across the read-only
commands listed in `audit/readonly/README.md`.

Build used: `go build -o /tmp/gale-audit-binary ./cmd/gale` at
HEAD (no local mods on tracked Go files).

Scratch HOME: `/tmp/gale-ro-audit-output-format/`.

## Coverage matrix

| Command       | --json | --no-color flag | NO_COLOR env | TTY auto-detect | Sort order | Trailing \n |
| ------------- | ------ | --------------- | ------------ | --------------- | ---------- | ----------- |
| list          | none (rejects) | n/a (no color used) | n/a | n/a | sorted | yes |
| info          | none (rejects) | n/a (plain fmt) | n/a | n/a | n/a (single pkg) | yes |
| search        | none (rejects) | (no color used) | n/a | n/a | server-side | yes |
| doctor        | none (rejects) | works | IGNORED (F2) | works | n/a | yes |
| env           | none (rejects) | n/a (shell out) | n/a | n/a | sorted | yes |
| hook          | none (rejects) | n/a | n/a | n/a | n/a | yes |
| lint          | none (rejects) | works | IGNORED (F2) | works | input order | yes |
| verify        | none (rejects) | works | IGNORED (F2) | works | n/a | yes |
| sbom          | YES | n/a (plain text+tab) | n/a | n/a | sorted | yes / JSON null bug (F1) |
| inspect       | YES (--all only meaningfully) | works | IGNORED (F2) | works | sorted | yes |
| which         | none (rejects) | n/a (plain) | n/a | n/a | n/a | yes |
| outdated      | none (rejects) | works (out.Warn/Success) | IGNORED (F2) | works | NON-DET (F4) | yes |
| generations   | none (rejects) | n/a (plain) | n/a | n/a | by number | yes |
| generations diff | none | n/a | n/a | n/a | added/removed order? | yes |
| audit         | none (rejects) | works | IGNORED (F2) | works | n/a | yes |

## Findings filed

1. `0001-sbom-json-null-empty.md` — confirmed, medium. sbom
   --json emits `null` for empty package set; inspect emits
   `[]`.
2. `0002-no-color-env-ignored.md` — confirmed, medium.
   `NO_COLOR` env not honored; only `--no-color`/`--plain`
   flags suppress ANSI under TTY.
3. `0003-list-format-changes-with-overlays.md` — confirmed,
   medium. `gale list` flips between flat and grouped schemas
   based on whether host overlays are configured.
4. `0004-outdated-nondeterministic-order.md` — likely, low.
   `gale outdated` iterates `cfg.Packages` map without
   sorting; line order varies run-to-run.
5. `0005-lint-warnings-use-info-prefix.md` — confirmed, low.
   `gale lint` renders warning issues with the cyan `-->`
   info prefix instead of yellow `!!!`.

## Speculative / TODO (did not make the bar)

- T1: `cmd/gale/sbom.go:138-145` uses `text/tabwriter` with
  `padding=2`, but empty fields (`License`, `SOURCE` when
  recipe resolution fails) emit two-space gaps that align
  awkwardly. Not a bug, just ugly. Need >2 pkgs with mixed
  resolved/unresolved recipes to demo.
- T2: `cmd/gale/outdated.go:94` prints `pkg cur → latest`
  with a literal Unicode arrow `→`. Fine on UTF-8 terminals,
  but no fallback for `LANG=C` / `LC_ALL=C`. Speculative
  rendering issue; no concrete repro that breaks anything.
- T3: `gale env --vars-only` outside a project (no
  gale.toml) silently exits 0 with **no** output. Is that
  the intended UX? Could surprise direnv users invoking it
  defensively. Borderline empty-state issue, defer to
  `empty-state` dim if not picked up.
- T4: `gale hook` is documented with `ValidArgs:
  []string{"direnv"}` but never registers `cobra.OnlyValidArgs`
  so any string reaches `env.GenerateHook`, returning the
  generic "unsupported shell" error rather than a cobra
  validation. Borderline bad-input; not output-format.
- T5: prefix glyph inconsistency: `-->`, `==>`, `!!!`, `xxx`
  are gale-specific and decided per-call-site. No central
  policy doc. Worth a style note in `docs/dev/style-guide.md`
  but not a bug.
- T6: `cmd/gale/inspect.go:197-199` writes the issue tally
  to stderr (`Fprintf(os.Stderr, "\n%d issue(s)...")`) while
  the issues themselves go to stdout via `fmt.Println(k)`.
  Mixed stream discipline — likely belongs in
  `stream-discipline` dimension.
- T7: `--json` flag is missing from every read-only command
  that prints structured data except `sbom` and `inspect`.
  `list`, `outdated`, `info`, `generations`, `generations
  diff`, and `which` would all be natural JSON consumers.
  Cap reached; not filed.
- T8: `cmd/gale/root.go:43-44` flag is `--error-format
  text|json` (controls top-level errors only) — disjoint
  from per-command `--json` (controls happy-path output).
  No conflict, but the naming is confusing: a user might
  expect `--error-format json` to also imply `--json`. Not
  reproduced as a bug; UX note.

## What I did not check

- Heavy color-leak audit under all TTY combos (script(1)
  PTY only, not real terminal emulator behaviour).
- `gale generations diff` output structure with realistic
  added/removed sets (scratch store has zero generations).
- `gale verify` and `gale audit` output (require gh CLI and
  network respectively; out of scope for static read).
- `gale sbom <pkg>` single-package output formatting.
- Interaction of `--quiet` with each command's output.
