## Summary

<!-- What changed and why. One short paragraph. -->

## Change discipline

Tier guide: [`docs/dev/change-discipline.md`](docs/dev/change-discipline.md)

Mark **one** tier (or round up if unsure):

- [ ] **Tier 0–1** — docs, output text, or single-package change with no version/config/generation semantics
- [ ] **Tier 2–3** — version identity, finalize path, generation/farm, gc/sync staleness, or `cmd/gale/context.go`

If **tier 2–3**, confirm before merge:

- [ ] Pre-change trace written (invariant, pipeline, grep, commands, test anchors, blast radius)
- [ ] Repro test at `cmd/gale/` or `integration/` layer when behavior changes (not only `internal/`)

CI enforces the test-layer rule for pipeline-sensitive paths when this PR adds or
modifies tests — see `scripts/check-pipeline-tests.sh`.

## Testing

<!-- e.g. `just test`, `just test-pkg generation`, integration if touched -->
