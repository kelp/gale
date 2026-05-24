---
severity: high
confidence: confirmed
commands: [doctor]
area: empty-state
---
## Summary
`gale doctor`'s generation check follows the `current`
symlink with `os.Readlink` only and never verifies that
the target generation directory exists, so a dangling
`current` symlink is reported as a healthy generation
(green checkmark) instead of a broken one.

## Reproducer
Setup (fresh `$HOME`, no real gale):

```sh
mkdir -p $HOME/.gale/pkg/jq/1.7.1/bin
: > $HOME/.gale/pkg/jq/1.7.1/bin/jq
chmod +x $HOME/.gale/pkg/jq/1.7.1/bin/jq
ln -s $HOME/.gale/gen/42 $HOME/.gale/current   # target does NOT exist
cat > $HOME/.gale/gale.toml <<EOF
[packages]
jq = "1.7.1"
EOF
```

Then `gale doctor` produces:

```
==> Gale home (~/.gale/)
==> Global config (1 packages)
==> No host-override shadows
==> Store (1 versions in /.../.gale/pkg)
==> All packages installed
==> Generation (current -> /.../.gale/gen/42)   <-- ✓ but target missing
==> Lib farm (/.../.gale/lib)
!!! Stale installs (1) — deps changed since built:
xxx PATH missing /.../.gale/current/bin
```

The generation check prints success and a target path
that does not exist on disk. The exit-1 in this run is
driven by unrelated checks (PATH missing, stale installs);
delete those signals and doctor would happily exit 0 with
no mention of the dangling symlink.

## Expected vs actual
Expected: a dangling `current` symlink is flagged with
the same severity as "No active generation" — it actively
breaks all binary lookups through `~/.gale/current/bin`.

Actual: `checkGeneration` in `cmd/gale/doctor.go:296`:

```go
gen, err := generation.Current(ctx.galeDir)
if err != nil || gen == 0 {
    ctx.out.Error("No active generation\n  Run: gale sync")
    return false
}
currentLink := filepath.Join(ctx.galeDir, "current")
target, err := os.Readlink(currentLink)
if err != nil {
    ctx.out.Error("current symlink broken")
    return false
}
ctx.out.Success(fmt.Sprintf(
    "Generation (current -> %s)", target))
return true
```

`generation.Current` (internal/generation/generation.go:385)
only `os.Readlink`s the symlink and parses the number from
`filepath.Base(target)` — it returns the integer 42 without
ever stat'ing the target directory. The follow-up
`os.Readlink` in doctor likewise succeeds because the
symlink itself is present. Nothing in this code path calls
`os.Stat` / `filepath.EvalSymlinks` on the target.

The `checkSymlinks` follow-on check would notice broken
symlinks under `current/bin`, but it returns early via
`return true // no bin dir is handled by other checks`
when `os.ReadDir(binDir)` fails — and a dangling parent
symlink makes the directory read fail, so it silently
passes too.

Net effect: a corrupt active-generation state that breaks
the user's PATH bin lookups is hidden behind a green check
in the very command designed to surface broken state.

## Suggested investigation
- Add `os.Stat` of the resolved target (or
  `filepath.EvalSymlinks`) in `checkGeneration` before
  printing success; on `ErrNotExist`, surface "current
  symlink dangles -> <target>; Run: gale sync".
- Race-audit 0005 fixed a different bug (lock-sharing
  between project and store gen locks). The detection
  gap here is independent.
- Worth checking whether other read-only paths
  (`generations`, `which`, `env`) similarly trust the
  symlink without validating the target.
