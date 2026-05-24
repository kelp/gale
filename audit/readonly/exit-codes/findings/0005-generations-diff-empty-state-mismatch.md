---
severity: medium
confidence: confirmed
commands: [generations]
area: exit-codes
---
## Summary
Inside the `generations` command group, peer subcommands disagree on
exit code for the same empty state. `gale generations` exits 0 with
"No generations found."; `gale generations diff` exits 1 with
"no current generation" — even when given explicit `from to`
arguments that don't logically need the current generation.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-exit-codes
rm -rf "$HOME/.gale"
mkdir -p "$HOME" && cd "$HOME"

/home/tcole/code/gale/gale generations
# stdout: No generations found.
# exit:   0

/home/tcole/code/gale/gale generations diff
# stderr: Error: no current generation
# exit:   1

/home/tcole/code/gale/gale generations diff 1 2
# stderr: Error: no current generation
# exit:   1   (even though caller asked for an explicit pair)
```

## Expected vs actual
Expected: peer subcommands of the same group should agree on
empty-state semantics. The most defensible behaviour is that
`generations diff` with no generations should exit 0 with
"No generations found." or "no changes" — matching the parent.
With explicit `from to` args, the precondition is "those two
generations exist", not "a current generation exists".

## Suggested investigation
`cmd/gale/generations.go:59-94`. The `cur, err := generation.Current(galeDir)`
+ `if cur == 0` guard runs unconditionally before the args switch.
Move the guard into `case 0` and `case 1` (the branches that
actually consume `cur`); for `case 2`, validate that the two
explicit generations exist instead. Also worth aligning the
exit-code for "no work to do" with the `generations` listing
command — pick one, document, hold the line.
