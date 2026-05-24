---
severity: high
confidence: confirmed
commands: [outdated]
area: exit-codes
---
## Summary
`gale outdated` exits 0 and prints "Everything is up to date." even
when every declared package failed to resolve against the registry —
mirrors the pre-cluster-0009 sync/update/gc bug for read-only state.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-exit-codes
mkdir -p "$HOME/.gale"
cat > "$HOME/.gale/gale.toml" <<EOF
[packages]
nosuch = "9.9.9"
EOF
cd "$HOME"
/home/tcole/code/gale/gale outdated
# stderr: !!! Skipping nosuch: fetch recipe nosuch: HTTP 404
# stdout: ==> Everything is up to date.
# exit:   0
```

The same shape applies if the registry is unreachable, the recipe
was renamed/deleted, or the user has a typo in `gale.toml` — the
warn-and-continue path eats the error and the final "all clear"
message is printed unconditionally when `len(items) == 0`.

## Expected vs actual
Expected: a CI job running `gale outdated` to gate releases on
"nothing needs updating" must not falsely succeed when the
registry could not be consulted. Either the command should exit
non-zero when any package failed to resolve, or it should
distinguish "checked, nothing outdated" from "could not check".
Actual: indistinguishable from the genuine all-clear case.

## Suggested investigation
`cmd/gale/outdated.go:53-80`. The loop tracks no `failedPkgs`
counter; the empty-`items` branch then unconditionally calls
`out.Success("Everything is up to date.")` and returns nil. The
pattern fixed in `d5ca2ad` (cluster 0009–0011) for `sync`/`update`/`gc`
applies here too — a fresh "failed > 0 => error" mirror would
close the gap. `audit/behaviour/findings/0009-0011` were
mutation-command framings; this is the read-only counterpart and
was not in scope for that pass.
