# Build System Improvements

Improvements to gale's build system identified while
building 40+ recipes in gale-recipes. Ordered from
simplest to most complex.

## 1. Lint: Detect Missing Build Deps from Build Steps

**Problem**: Recipes for direnv and lazygit used
`go build` but didn't declare `build = ["go"]`. CI
failed with `go: command not found`. We only caught
this when CI ran, not at lint time.

**Fix**: `gale lint` should scan build step strings
for known tool invocations and warn if the corresponding
build dep is missing:

| Build step pattern | Expected dep |
|---|---|
| `go build` | `"go"` |
| `cargo install` | `"rust"` |
| `cmake` | `"cmake"` |
| `./configure` + `make` | (no dep — uses system cc) |
| `pkg-config` or `pkgconf` | `"pkgconf"` |

This is pure string matching on the steps array.
Warning level, not error — the tool might be provided
by a different dep name or be on the system PATH.

## 2. Runtime Deps Available at Build Time

**Problem**: The `[dependencies]` section has `build`
and `runtime` lists, but only `build` deps are installed
during `gale build`. If a recipe declares
`runtime = ["pcre2"]` and the build links against
`-lpcre2`, it fails because pcre2 isn't installed.

The current workaround is listing deps in both `build`
and `runtime`, which is redundant and error-prone.

**Proposed behavior**: Build deps are tools needed only
at compile time (compilers, code generators). Runtime
deps are libraries needed by the built binary. Both
should be installed during builds, because you can't
link against a library that isn't present.

The model: `runtime` deps are always available during
builds. `build` deps are additional tools that aren't
needed at runtime. This matches how Homebrew and Nix
work — build deps are `nativeBuildInputs`, runtime
deps are `buildInputs`, and both are present during
the build.

**Implementation**: In `InstallBuildDeps`, merge
`recipe.Dependencies.Build` and
`recipe.Dependencies.Runtime` before resolving and
installing. Both sets get their `bin/`, `lib/`,
`include/`, and `lib/pkgconfig/` paths added to the
build environment.

## 3. Transitive Build Dep Resolution

**Problem**: If recipe A has `build = ["openssl"]` and
openssl has `build = ["pkgconf"]`, does gale install
pkgconf too? If not, the openssl source build fails
because it can't find `pkg-config`.

Currently, recipes must redeclare their deps' deps:
```toml
build = ["pkgconf", "openssl"]
```

This is fragile — the recipe author has to know the
full transitive dependency tree. If openssl adds a new
build dep, every recipe that depends on openssl breaks.

**Fix**: When resolving build deps, recursively resolve
each dep's own build and runtime deps. Install the
full transitive closure before running build steps.

**Cycle detection**: A depends on B which depends on A.
This shouldn't happen in practice (it would mean
circular build deps), but gale should detect it and
error clearly rather than infinite-looping.

**Ordering**: Deps should be installed leaf-first
(topological sort). If A needs B which needs C, install
C, then B, then build A. This ensures each dep's deps
are available when it builds.

## 4. Dynamic Linker Paths in Build Environment

**Problem**: `LIBRARY_PATH` and `C_INCLUDE_PATH` are
now set from build deps (the fix being released). But
some build systems (autotools `./configure`, cmake
`try_compile`) compile and execute small test programs
during configuration. These test programs need the
dynamic linker to find shared libraries at runtime.

`LIBRARY_PATH` only affects the static linker (`ld`).
The dynamic linker uses `LD_LIBRARY_PATH` (Linux) or
`DYLD_FALLBACK_LIBRARY_PATH` (macOS).

**Fix**: In the same place that sets `LIBRARY_PATH`,
also set:
- `LD_LIBRARY_PATH` on Linux
- `DYLD_FALLBACK_LIBRARY_PATH` on macOS

These are set from the same dep store paths as
`LIBRARY_PATH`. Platform detection can use
`runtime.GOOS`.

**Caveat**: `DYLD_LIBRARY_PATH` (without `FALLBACK`)
overrides system library paths and can break the
build tools themselves. Use `DYLD_FALLBACK_LIBRARY_PATH`
which only kicks in when the normal search fails.

## 5. Lint: Detect Cargo Workspace Issues

**Problem**: Recipes for statix, trippy, and
tree-sitter used `cargo install --path .` but the
source is a Cargo workspace with a virtual manifest.
Cargo fails with "found a virtual manifest instead of
a package manifest". We only discovered this from CI
failures.

**Fix**: When linting a Rust recipe that uses
`cargo install --path .`, check if the source tarball
contains a virtual manifest. This requires downloading
the tarball (or at least the root Cargo.toml), so it
should be an optional deep-lint mode:

```
gale lint --deep recipes/s/statix.toml
```

Deep lint would:
1. Download the source tarball
2. Check root Cargo.toml for `[workspace]` without
   `[package]`
3. If virtual manifest, warn that `--path .` won't
   work and suggest specific crate paths found in the
   workspace members

Normal `gale lint` (without `--deep`) would skip this
check to stay fast and offline.

## 6. Recipe Platform Field

**Problem**: Some packages are platform-specific (e.g.
xclip is Linux-only, lsof's tirpc dep is Linux-only).
Currently CI builds them on all platforms and they
fail. The failure is expected but noisy.

**Fix**: Add an optional `platforms` field to
`[package]`:

```toml
[package]
name = "xclip"
platforms = ["linux-amd64"]
```

If `platforms` is set, `gale build` skips the build
with a message on non-matching platforms. CI matrix
could also use this to exclude jobs.

If `platforms` is absent, the recipe builds on all
platforms (current behavior, no change needed).

## 7. Auto-Detect --local from Context

**Problem**: Running `gale build recipes/j/jq.toml`
in the gale-recipes directory fails to resolve build
deps because gale doesn't know to look at the sibling
recipes. You have to remember `--local`.

**Proposed behavior**: If the recipe file path is
inside a directory that looks like a recipes repo
(has `recipes/<letter>/<name>.toml` structure), gale
automatically resolves deps locally. The `--local`
flag becomes a way to force it when auto-detection
doesn't apply.

Detection heuristic: walk up from the recipe file
path, look for a parent directory containing
`recipes/` with letter-bucketed subdirectories.

## 8. Binary Verification Override in Recipe

**Problem**: The CI verify step tries `--version`,
`version`, `--help`, `-V`, `-v` to check that a built
binary runs. Lua needed `-v` (which we added). Other
tools may use different patterns — `lua` prints help
to stderr and exits non-zero for `--help`.

Rather than expanding the flag list indefinitely in
the workflow, let recipes declare how to verify.

**Fix**: Add an optional `verify` field to `[package]`:

```toml
[package]
name = "lua"
verify = "lua -v"
```

CI reads this field and runs it instead of the default
chain. If absent, falls back to the existing
`--version || version || --help || -V || -v` chain.

This is both a gale format change (new optional field)
and a CI workflow change (read the field before
running verify).

## 9. Platform Variables in Build Steps

**Problem**: The rust and go recipes duplicate nearly
identical build steps across `[build.darwin-arm64]`
and `[build.linux-amd64]`, differing only in a
platform target string or bootstrap URL. For example,
rust's configure line is identical except for
`darwin64-arm64-cc` vs `linux-x86_64`.

**Proposed**: Add platform variables available in
build steps alongside `${PREFIX}` and `${JOBS}`:

| Variable | darwin-arm64 | linux-amd64 |
|---|---|---|
| `${OS}` | `darwin` | `linux` |
| `${ARCH}` | `arm64` | `amd64` |
| `${PLATFORM}` | `darwin-arm64` | `linux-amd64` |

This would let many recipes collapse platform-specific
sections into a single `[build]` section with
conditional logic in shell:

```toml
[build]
steps = [
  "if [ ${OS} = darwin ]; then TARGET=darwin64-arm64-cc; else TARGET=linux-x86_64; fi",
  "perl ./Configure --prefix=${PREFIX} $TARGET",
]
```

Whether this is actually cleaner than separate
`[build.<platform>]` sections is a design question.
The variables are useful even without collapsing —
they let build steps reference the current platform
without hardcoding or running `uname`.

## 10. Binary Index Separation

See `docs/dev/binary-index-design.md` for the full
proposal. Summary: move `[binary.<platform>]` sections
out of recipe TOMLs into separate `.binaries` files
managed by CI.
