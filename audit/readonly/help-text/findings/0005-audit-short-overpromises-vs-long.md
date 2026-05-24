---
severity: low
confidence: confirmed
commands: [audit]
area: help-text
---
## Summary
The one-line summary on `gale audit` claims it "Verify[s] a
package builds reproducibly", but the `Long` description on
the same command explicitly states that most builds are *not*
reproducible and a mismatch "does not indicate tampering".
A user scanning `gale --help` will draw the opposite
conclusion from what `gale audit --help` then explains.

## Reproducer

```
$ gale --help | grep '^  audit'
  audit          Verify a package builds reproducibly
```

```
$ gale audit --help
Rebuild a package from source and compare the SHA256 against the
installed binary. Most builds are not yet deterministic — mismatches
are expected due to timestamps, embedded paths, and build IDs. A match
confirms the build is reproducible. A mismatch does not indicate
tampering.
```

`cmd/gale/audit.go:14-19`:

```go
Short: "Verify a package builds reproducibly",
Long: `Rebuild a package from source and compare the SHA256 against the
installed binary. Most builds are not yet deterministic — mismatches
are expected due to timestamps, embedded paths, and build IDs. A match
confirms the build is reproducible. A mismatch does not indicate
tampering.`,
```

The `Short` reads as a guarantee; the `Long` admits the
guarantee almost never holds.

The manpage (`gale.1`) repeats the same framing — "Verifies
reproducibility." — under the Supply Chain Security section,
where it sits next to `verify` (Sigstore attestation, which
genuinely is a verification step), inviting the wrong mental
model.

## Expected vs actual
- Expected: `Short` describes what the command *does*
  ("Rebuild and compare SHA256"), not the aspirational
  semantics most builds don't satisfy yet.
- Actual: `Short` over-claims; `Long` walks it back.

## Suggested investigation
`cmd/gale/audit.go` Short and `gale.1` Supply Chain Security
section. A description like "Rebuild a package and compare
its hash" would align with the Long.
