---
severity: critical
confidence: confirmed
commands: [audit]
area: read-only-invariant
---
## Summary
`gale audit <pkg>` calls `Installer.InstallBuildDeps` before
rebuilding, which writes new package directories into
`~/.gale/pkg/` even though the audit README and `audit`'s own
help text both state that audit does not mutate the store.

## Reproducer
Scratch HOME `/tmp/gale-ro-audit-readonly-invariant`:

```
.gale/pkg/jq/1.7.1-1/bin/jq            # fabricated
.gale/pkg/ripgrep/14.1.0-1/bin/rg
.gale/gen/1/bin/{jq,rg}                # symlinks
.gale/current -> gen/1
.gale/gale.toml                        # jq + ripgrep
gale.lock                              # jq@1.7.1, dummy sha
```

Then:

```sh
HOME=$PWD /home/tcole/code/gale/gale audit jq
```

Store contents grow from 2 dirs to 6:

```
before:  jq/1.7.1-1  ripgrep/14.1.0-1
after:   jq/1.7.1-1  ripgrep/14.1.0-1
         autoconf/2.73-2  automake/1.18.1-2
         libtool/...      m4/1.4.19-2
```

Command also created `~/.gale/cache/registry/...` cache
entries (a separate concern — see finding 0002).

## Expected vs actual
Expected, per `audit/readonly/README.md` line 18 (`audit`
"rebuilds from source but does not mutate
store/config/lockfile — treat as read-only of state") and the
command's own Long description ("Rebuild a package from source
and compare the SHA256"): the user-visible store, config, and
lockfile are unchanged after `gale audit <pkg>`.

Actual: `cmd/gale/audit.go:57` calls
`ctx.Installer.InstallBuildDeps(r)`. That function
(`internal/installer/installer.go:732`) walks the dep closure
and runs the full installer for any missing dep — writing
into `~/.gale/pkg/<dep>/<ver>-<rev>/`. Build deps like m4 →
autoconf → automake → libtool aren't typically present on the
target system already, so a real audit run materially expands
the store.

The user's mental model "audit is read-only" is wrong in
practice, and the README's audit carve-out ("rebuilds from
source") was clearly intended to license only `~/.gale/tmp/`
churn, not store growth.

## Suggested investigation
- Look at whether `audit` could shell out to the same
  hermetic-temp build path that `build` uses without touching
  the global store. The rebuild only needs the bytes to come
  out the same — it does not require the deps to be in the
  user-visible store.
- Worst case, document that `audit` will install missing
  build deps (and surface that in the Long help). Better:
  detect missing deps and refuse with a "run `gale sync` first"
  rather than silently installing them.
- The README's audit carve-out should be reworded to match
  whatever fix lands. Today it understates the side effects by
  a lot.
