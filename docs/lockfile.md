# The Lockfile

`gale.lock` records the exact closure gale installed:
every package, every dependency, and the checksum of
every artifact, per platform. Commit it alongside
`gale.toml`.

It is an enforced lock, not a report. Once a scope has
one, gale installs what the lock names and refuses
anything else. Every failure it can produce carries its
own exit code, so a pipeline can tell "someone replaced
an artifact" from "the build broke" — see
[ci-cd.md](ci-cd.md).

## Schema

```toml
version = 1

[packages."!gale-lock-v1"]
version = 1

[targets.default]
roots = ["jq@1.8.1-2", "ripgrep@14.1.1-1"]

[targets.host."ci-*,build-*"]
roots = ["jq@1.8.1-2", "zig@0.14.1-1"]

[packages."jq@1.8.1-2".artifacts."darwin/arm64"]
sha256 = "..."
manifest_digest = "sha256:..."
method = "binary"                 # binary | source
runtime_deps = ["oniguruma@6.9.10-1"]
build_deps = ["autoconf@2.72-1"]
graph_digest = "sha256:..."
```

`version` names the schema. gale refuses a version it
does not model rather than parsing it leniently: TOML
decoding drops unknown fields silently, so a partial
read followed by a write would destroy them.

`[targets.*]` holds the **declared** roots — what
`gale.toml` asks for. `[packages.*]` holds the whole
closure, roots and transitive dependencies alike. The
split is what makes staleness answerable: a lock is
stale when its roots disagree with the manifest, and
transitive entries never enter that comparison.

Host targets are keyed by `gale.toml`'s selector string
verbatim, wildcards and comma lists included. They are
not resolved against the current machine, so one
committed lock serves every host the manifest names.

Package nodes are keyed `name@version-revision`, so a
lock can name several versions of one package across
targets. Platform is an artifact dimension, not a
separate file: one lock covers every platform its
writers have seen.

`runtime_deps` and `build_deps` are recorded apart. A
binary install validates runtime deps; a source install
validates both. `graph_digest` binds a node's identity
and its dependencies into one value, so a dependency
substituted anywhere below a package changes the digest
above it.

## The downgrade guard

```toml
[packages."!gale-lock-v1"]
version = 1
```

Every v1 lockfile carries this entry. It is mandatory
and fixed, not an example.

It exists because a gale released before enforcement
reads `gale.lock` as a flat table of package names and
ignores keys it does not know. Handed a v1 file, such a
build would decode near-empty packages, call the lock
stale, and rewrite it in its own schema — destroying an
enforced lock without a word. The guard's `version` is
an integer where the old schema's is a string, so the
old decoder fails on type and stops.

The guard turns silent data loss into a loud failure. It
does not remove it. **Upgrade gale everywhere before
committing a v1 lock.**

The key cannot collide with a real node, which is always
`name@version-revision`. gale strips the guard on read
and injects it on write, so it is never visible as a
package. A lockfile claiming version 1 without a
well-formed guard is refused, not repaired: accepting it
would leave a nominally-enforced lock that an old build
still destroys.

## Schema v2 (written unused, not loaded)

`WriteV2` writes the fetch schema. `ReadV2` reads it.
`Load` and `ReadV1` still refuse a v2 file as an
unknown schema (exit 4). That is what stops this gale
from rewriting a v2 lock as v1. Live install still
writes v1.

A v2 file carries its own guard:

```toml
[packages."!gale-lock-v2"]
version = 2
```

Already-shipped gale fails loud on those bytes: a
v1-enforcement build rejects top-level `version = 2`;
a pre-enforcement build fails the integer guard the
same way it fails the v1 guard. Package keys and
target roots are `name@version`, with no revision.

## Enforcement model

**Writers.** `gale install`, `gale update`, `gale
remove` and `gale lock` write `gale.lock`. Each resolves
its complete closure first, then replaces the file in
one atomic write. A partial or failed resolution leaves
the previous lockfile byte-identical. `gale add` writes
the manifest only.

**`gale sync` never writes the lock.** It is a pure
consumer: it installs the closure the lock names and
fails if it cannot. This is the change that makes the
lock a control. Before enforcement, sync rewrote the
lock to match whatever it had just installed, so a
changed upstream artifact was recorded rather than
refused.

**Readers fail closed.** A lock that is present and
cannot be fully modeled is an error, never treated as
absent. Absence means nothing exists at the path and
nothing else — a `gale.lock` symlink whose target is
missing is a lock gale cannot read, not a project
without one.

**No lockfile is unlocked mode**, with one warning. That
is how a new project starts.

**Activation is gated.** Before adding a project to
`PATH`, gale checks that the active generation links
exactly what the lock roots, and that every runtime
store directory it reaches carries provenance matching
the locked identity, artifact SHA, method and
`graph_digest`. The gate reads provenance files and the
lock; it hashes nothing. A failure refuses the project's
`PATH_add` and leaves the system `PATH` untouched.
`gale shell` and `gale run` treat it as fatal.

**Global scope has no activation gate, deliberately.**
`~/.gale/current/bin` reaches `PATH` from your shell rc
with no gale invocation, so there is nothing to hook.
Global relies on enforcement at write time instead, and
a locked global plan forbids carry-forward: a version
carried into the generation that the lock does not name
is exactly what no later check would catch. `gale gc`
takes its versions from the scope's lock for the same
reason.

**The escape hatch is explicit.** `gale sync
--no-frozen` ignores `gale.lock`, installs from recipes
without integrity enforcement, and warns. Nothing
downgrades to unlocked mode on its own.

## Remedies

Every refusal names the command that ends it. The
wording below is what gale prints.

**The lock does not match `gale.toml`.** You edited a
pin, added a package, or removed one. gale names the
disagreement — `gale.toml declares X with no locked
root`, `the lock roots X which gale.toml no longer
declares`, or `X is declared 1.8 but locked at 1.7-1` —
and then names the fix:

- For a package the lock has never seen: `'gale
  install' to install and lock the new package(s), or
  'gale lock' to lock what is already installed`.
- For one it has: `'gale lock' to regenerate the
  affected target(s)`.

When the package's pin comes from a host section, the
`gale lock` in those sentences is spelled `gale lock
--host "<selector>"`, naming every target that has to be
rewritten. Running one and not the others leaves the
next sync failing on the rest. When gale cannot tell
which section owns the pin, it says `'gale lock' (or
'gale lock --host <selector>' when the package belongs
to a host section)`.

**The lock cannot be read at all** — legacy schema,
unknown version, malformed TOML, unknown field, missing
or malformed guard. Run `gale lock --refresh`. `gale
doctor` reports this state in either scope. `gale gc
--force` rebuilds a scope whose lock is beyond repair.

**A store directory attests nothing.** Every package
installed before enforcement is unprovenanced, so the
activation gate refuses it. `gale lock --refresh <pkg>`
refetches, verifies and replaces one directory; `gale
migrate` does the same in bulk for every binary-method
package in the closure. Source-method packages cannot be
migrated this way — `migrate` lists them and what
rebuilding costs.

**The active generation does not match the lock.** Run
`gale sync`. This is drift, not tampering: it is what a
carried-forward version or an interrupted rebuild leaves
behind, and it is kept a separate class so a
carried-forward package never reads as a substituted
artifact.

**A store directory's bytes disagree with the lock.**
This one has no automatic remedy by design. Something on
disk is not what the lock says, and a human decides
whether upstream moved legitimately or not.

## Source builds and portability

A locked source build is enforced strictly, never
warn-only. `gale audit` mismatches are normal for most
packages — Mach-O `LC_UUID`, absolute paths baked into
`.la` and `.pc` files, `ar` timestamps — and the same
non-determinism means a locked source build may
legitimately fail to reproduce on another machine. The
remedy is to re-lock on that machine, or to use a binary
artifact.

The consequence for a committed lock follows from
`graph_digest`: a source node's output hash feeds every
digest above it. **A committed lock is portable across
machines with certainty only when its whole closure is
binary.** A closure containing a source build is
portable exactly as far as that build reproduces, which
today is the exception rather than the rule.

## Upgrading from a pre-enforcement lock

Every project that predates enforcement has a flat
`gale.lock` written by sync. gale refuses it, in every
scope, on the first run after the upgrade — including
inside direnv.

1. Upgrade gale everywhere first. A v1 lock committed
   ahead of an old build stops that build with the
   guard's error rather than being destroyed by it, but
   stopping is still a broken machine.
2. Run `gale lock --refresh` in each scope. Plain `gale
   lock` cannot finish the job on upgrade day: it reads
   provenance, and pre-upgrade store directories have
   none.
3. Or run `gale migrate`, which refetches and replaces
   every unprovenanced binary-method directory in one
   pass, and reports the source-method packages it
   cannot.

`gale doctor` reports each of these states, and names
the same commands.
