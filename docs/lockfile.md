# The Lockfile

`gale.lock` is a v2 lock. It names the exact upstream
artifacts gale fetched: URL, `sha256`, `tree_digest`,
and platform. Commit it alongside `gale.toml`.

It is an enforced lock, not a report. Once a scope has
one, gale lands what the lock names and refuses
anything else. `gale sync` does not rewrite it. Every
failure it can produce carries its own exit code, so
a pipeline can tell "someone replaced an artifact"
from "the fetch failed" — see [ci-cd.md](ci-cd.md).

## Schema (v2)

```toml
version = 2

[packages."!gale-lock-v2"]
version = 2

[targets.default]
roots = ["just@1.56.0"]

[packages."just@1.56.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"
format = "tar.gz"
sha256 = "..."
tree_digest = "sha256:..."
method = "fetch"
hash_source = "upstream-sha256sums"
index_commit = "deadbeef"
```

`version` names the schema. gale refuses a version it
does not model rather than parsing it leniently.

`[targets.default]` holds the **declared** roots —
what `gale.toml` asks for, keyed `name@version` with
no revision. `[packages.*]` holds one artifact map
per root, keyed by platform. Leftover
`[targets.host.*]` refuses live verbs. There is no
`--host` flag.

`method` is `fetch`. Source and bottle methods are
gone. A mixed lock is refused.

The v2 guard (`[packages."!gale-lock-v2"]`) stops an
older gale from rewriting the file as v1.

A v1 lock migrates with `gale migrate`.

## The v1 downgrade guard

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

## Enforcement model

**Writers.** `gale install`, `gale update`, `gale
remove` and `gale lock` write `gale.lock`. Each resolves
its complete closure first, then replaces the file in
one atomic write. A partial or failed resolution leaves
the previous lockfile byte-identical.

**`gale sync` never writes the lock.** It is a pure
consumer: it lands the fetch trees the lock names and
fails if it cannot. Before enforcement, sync rewrote
the lock to match whatever it had just installed, so
a changed upstream artifact was recorded rather than
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

**There is no escape hatch.** `gale sync --no-frozen`
was removed with the fetch cutover. A lock gale cannot
honor is refused, and the refusal names the writer that
replaces it — `gale lock` for an absent or unparseable
one, `gale migrate` for a legacy or v1 one. Nothing
downgrades to unlocked mode, by flag or on its own.

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

When the pin or lock target is leftover
`[hosts.*]` / `[targets.host.*]`, gale names
that leftover and the fix: move leftover
`[hosts.*]` pins into `[packages]`, then
`gale lock`. It does not name `--host`.

**The lock cannot be read at all** — legacy schema,
unknown version, malformed TOML, unknown field, missing
or malformed guard. A legacy lock names
`gale migrate`. An unreadable file reports the
load error. `gale doctor` reports this state in
either scope. `gale gc` does not rebuild
a generation.

**A store directory attests nothing.** Every package
installed before enforcement is unprovenanced, so the
activation gate refuses it. `gale migrate`
refetches, verifies, writes v2, and swaps the
generation.

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

## Portability

A v2 lock names fetch artifacts. The same file works
on every machine that has that platform's archive in
the index. There is no source node whose output hash
can drift.

`gale verify` checks tree digests against the lock.

## Upgrading from a pre-enforcement lock

Every project that predates enforcement has a flat
or v1 `gale.lock`. gale refuses it, in every scope,
on the first run after the upgrade — including
inside direnv.

1. Upgrade gale everywhere first.
2. Run `gale migrate` in each scope. It reads
   the old lock, fetches, verifies, writes v2, and
   swaps the generation. Plain `gale lock` cannot
   finish the job on upgrade day: it does not fetch.

`gale doctor` reports each of these states, and names
the same commands.
