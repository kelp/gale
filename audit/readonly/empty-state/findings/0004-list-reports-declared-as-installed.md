---
severity: medium
confidence: confirmed
commands: [list]
area: empty-state
---
## Summary
`gale list` is documented and self-described as "List
installed packages" (and prints "No packages installed."
when empty), but it actually reads gale.toml and prints
declared packages without consulting the store. In states
where packages are declared but never installed — or
declared, installed, then the store directory deleted by
hand — `gale list` happily shows the package as if it
were installed.

## Reproducer
```sh
mkdir -p $HOME/.gale
cat > $HOME/.gale/gale.toml <<EOF
[packages]
jq = "1.7.1"
EOF
# Nothing actually installed in $HOME/.gale/pkg/
gale list
# jq@1.7.1
echo "exit=$?"   # 0
gale doctor 2>&1 | grep -i missing
# xxx Missing packages: jq@1.7.1
```

`list` and `doctor` disagree about reality. Same
divergence reproduces in the `declared-uninstalled`,
`store-no-current`, `dangling-current`, and
`gens-no-current` harness states — `list` always prints
`jq@1.7.1` with exit 0.

## Expected vs actual
Expected: either
  (a) command short-help and the empty-state message
      say "declared in gale.toml" / "No packages
      declared.", or
  (b) the implementation cross-checks the store and
      either marks missing entries (e.g.
      `jq@1.7.1 (not installed)`) or drops them.

Actual: `cmd/gale/list.go`:

```go
var listCmd = &cobra.Command{
    Use:   "list",
    Short: "List installed packages",
    ...
}
// ...
if len(cfg.Packages) == 0 {
    fmt.Fprintln(w, "No packages installed.")
    return nil
}
for _, name := range sortedKeys(cfg.Packages) {
    fmt.Fprintf(w, "%s@%s\n", name, cfg.Packages[name])
}
```

There is no `store.IsInstalled` call. The output is
purely a pretty-print of `cfg.Packages`. The "No
packages installed." string is a particularly
misleading empty-state response because it implies
the store was consulted.

The bug is mostly cosmetic in healthy environments,
but in the "I deleted my .gale/pkg by hand" or
"I bootstrapped a fresh machine but haven't run gale
sync yet" cases — both well within the empty-state
audit's remit — `list` is the first command users
reach for, and it confidently lies.

## Suggested investigation
- Decide whether `list` is a config view or a store
  view. Either is defensible; the current mix is not.
- If keeping config-view semantics, change the
  empty-state string to "No packages declared."
  and the command help to "List packages declared
  in gale.toml". Consider adding a `(not installed)`
  suffix gated on a cheap `store.IsInstalled` check
  — same path doctor already uses.
- `info` has the same shape (printConfigInfo in
  cmd/gale/info.go), but mitigates by printing
  `Store: <path>` only when actually installed. The
  asymmetry between the two commands is worth a look.
