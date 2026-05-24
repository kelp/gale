---
severity: medium
confidence: confirmed
commands: [list, sbom]
area: exit-codes
---
## Summary
With no `gale.toml` present, `gale list` exits 0 with "No packages
installed.", but `gale sbom` exits 1 with "reading config: ... no
such file or directory". The two commands cover the same conceptual
question ("what is installed?") with opposite exit-code semantics.

## Reproducer
```sh
export HOME=/tmp/gale-ro-audit-exit-codes-empty
mkdir -p "$HOME"
cd "$HOME"

/home/tcole/code/gale/gale list
# stdout: No packages installed.
# exit:   0

/home/tcole/code/gale/gale sbom
# stderr: Error: reading config: reading config: open
#         /tmp/.../.gale/gale.toml: no such file or directory
# exit:   1
```

After `mkdir -p $HOME/.gale && : > $HOME/.gale/gale.toml`:
- `list`  -> "No packages installed." exit 0
- `sbom`  -> empty table header only, exit 0

So the divergence is specifically the "no config file at all" case.
`sbom` also double-wraps the error: `reading config: reading config:`.

## Expected vs actual
Expected: `sbom` with no installed packages should be a clean
empty result, exit 0 (matches `list`), or both should exit
non-zero — pick one. The double-wrapped error string is a
separate cosmetic issue.

## Suggested investigation
`cmd/gale/sbom.go:160-178` (`resolveSbomConfig`) treats
"global config absent" as a hard failure and returns it through
`fmt.Errorf("reading config: %w", err)`, which `RunE` then wraps
*again* via `fmt.Errorf("reading config: %w", err)` at
`cmd/gale/sbom.go:49`. Compare with `cmd/gale/list.go:51-58`
which treats `os.ErrNotExist` as the empty-state and exits 0.
The two commands should share the same "no config = no packages"
short-circuit (no need to materialize `globalDir` at all when
absent).
