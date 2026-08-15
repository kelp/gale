# Fetch, Don't Build

Status: proposal (2026-08-15)
Scope: gale CLI + gale-recipes. A product cut, not a
patch.
Verdict: stop being a distro. Keep the environment
manager. Fetch upstream artifacts. Pin them in the
lock. Do not compile, attest, or cache our own
bottles.

This is the written form of the 2026-08-15 pivot
discussion. It is a plan, not an implementation.

Related: [`design.md`](../design.md),
[`lockfile.md`](../../lockfile.md),
[`relocatable-binaries.md`](../relocatable-binaries.md),
[`revisions.md`](../../revisions.md),
[`docs/audit/internal-supply-chain.md`](../../audit/internal-supply-chain.md).

## 1. Decision

Gale's product is a declarative environment:

- `gale.toml` names packages and versions
- `gale.lock` names exact artifacts
- the store holds immutable trees
- a generation is a symlink snapshot
- `current` swaps atomically
- direnv activates a project on `cd`

Gale's cost center is a second product we grew around
that: a two-repo distro that compiles ~193 recipes,
pushes GHCR bottles, attests them, tracks revisions,
farms dylibs, and rewrites Mach-O/ELF after verify.

The cut: keep the first product. Delete the second.
Acquire packages the way aqua and mise already do —
upstream's own release, hashed, extracted into the
store.

User-facing commands stay. `gale install jq` still
works. What changes is where the bytes come from, and
what we stop operating.

## 2. Why

The March 2026 design listed "building from source by
default" as a non-goal. Recipes were TOML data. "No
build system, no CI required."

What shipped is the opposite. The current identity is
"everything from source; GHCR is a cache." That
identity created:

- a promote / ledger / attestation pipeline
- revision bumps that cascade
- a shared dylib farm
- Darwin and Linux binary fixup
- sync staleness that can loop direnv
- a change-discipline document because the pipelines
  keep violating each other's invariants

The last stretch of changelog is lock enforcement, gc
retention, rollback, attestation, ledger coherence.
That is real work. It is almost no new user-visible
value.

A 2026-08-15 audit of all 193 recipes (appendix A)
found 91 packages whose upstream already publishes a
relocatable binary for gale's primary platforms. That
is almost every name a developer types. The other half
of the catalog is libraries we compile against, or
source-only C tools, or gems.

Wrapping `brew` was considered and rejected. A shared
Cellar re-inherits the README's opening complaint. A
brew-bottle *fetch* is a last-resort backend for a
short allowlist, not the product.

## 3. What stays

Unchanged in shape, and still the product:

| Piece | Role |
|---|---|
| `gale.toml` | declared pins, `[vars]`, host sections |
| `gale.lock` | enforced artifact identity |
| `~/.gale/pkg/<name>/<version>/` | immutable store |
| `gen/<N>` + `current` | atomic environment swap |
| direnv hook, `gale env` / `shell` / `run` | activation |
| global / project / host scopes | where the manifest lives |
| `gale sync --if-needed` + `sync-state.toml` | direnv must not stall |
| `gale gc`, `generations rollback` | retention and undo |
| `internal/download` | fetch, hash, extract |

The pipeline collapses to:

```
resolve name@version → URL + sha256
  → download + verify hash
  → extract into store
  → write gale.toml + gale.lock
  → rebuild generation
  → atomic current swap
```

`gale sync` still never writes the lock.

## 4. What goes

Stop funding these. Do not ifdef them. Delete or
mothball once the fetch path is the only installer.

**In gale**

- `internal/build` — compile, `install_name_tool`,
  `patchelf`, pkg-config rewrite
- `internal/farm` — shared dylibs
- `internal/ghcr` — our bottle host
- `internal/attestation` — Sigstore of *our* bottles
- `internal/ai` / `create-recipe` — recipe authorship
- recipe `[build] steps`, revisions, `.gale-deps.toml`
- `gale build`, `gale audit` (rebuild-and-compare),
  `gale lint` as a recipe linter

**In gale-recipes**

- `build.yml`, `verify.yml`, `promote.yml`,
  `ledger-check.yml`, `auto-update.yml`,
  `reproducibility.yml`, and the Python around them
- `.binaries.toml` ledgers
- every `[build]` block

**Operational load that leaves with them**

- two-repo bottle coordination
- merge-commit-only because `[[history]].commit` pins
- "verify green ≠ mergeable"
- held `GITHUB_TOKEN` runs
- revision cascade on dependents
- Darwin-invisible fixup as the riskiest code

Rough size, measured 2026-08-15: about 39k lines of
Go delete outright (build, farm, attestation, GHCR,
AI, and their commands). Another ~63k (installer,
registry, recipe, lock graph, doctor, gc, context)
shrinks to a much smaller fetch-and-lock core. The
environment packages are ~20k and stay. A finished
tree is estimated at 40–55k Go, versus 123k today.
The recipes repo's CI job drops by most of its
surface.

Those numbers are a consequence, not a target. The
target is fewer coupled pipelines.

## 5. Acquisition

An index entry is a pointer, not a build recipe.

```toml
[package]
name = "just"
description = "Save and run project-specific commands"
license = "CC0-1.0"
homepage = "https://github.com/casey/just"
repo = "casey/just"

[artifacts.darwin-arm64]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"
sha256 = "…"
strip = 1
bin = ["just"]

[artifacts.linux-amd64]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-x86_64-unknown-linux-musl.tar.gz"
sha256 = "…"
strip = 1
bin = ["just"]
```

Templates (`{{version}}`, `{{os}}`, `{{arch}}`) are
allowed as authoring sugar. The lock stores the
resolved URL and hash, never the template.

`strip` and `bin` tell the extractor how to land a
tree that `generation.Build` can link. Some upstreams
ship a bare binary (`jq-macos-arm64`). Some ship a
tarball with `bin/`. The index names the layout. The
installer does not guess.

Official non-GitHub hosts use the same shape:

| Package | Host |
|---|---|
| `go` | `go.dev/dl` |
| `nodejs` | `nodejs.org/dist` |
| `zig` | `ziglang.org/download` |
| `rustup` | `static.rust-lang.org` |
| `terraform` | `releases.hashicorp.com` |
| `helm` | `get.helm.sh` |
| `kubectl` | `dl.k8s.io` |
| `docker` | `download.docker.com` |
| `1password-cli` | `cache.agilebits.com` |
| `google-cloud-sdk` | `dl.google.com` |
| `arm-none-eabi-gcc` | `developer.arm.com` |
| `awscli` | `awscli.amazonaws.com` |

Four current recipes already fetch vendor tarballs
and call it a build (`1password-cli`,
`arm-none-eabi-gcc`, `google-cloud-sdk`, `probe-rs`).
The thin installer is that path, generalized.

**No source fallback.** If the asset is missing, the
hash mismatches, or the host is not allowed, install
fails. A network error is an error. That closes the
class in BUG-2 of the supply-chain audit: treating a
failed `.binaries.toml` fetch as "not found" and
falling through to a source build.

**No `go install` / `cargo install` backend.** Those
are compilers. They recreate the farm.

## 6. Index

The index replaces gale-recipes-as-a-build-farm.

**Start curated.** One entry per package we will
actually fetch, ~90 names from appendix A. Do not
import aqua-registry as the default resolver. Aqua is
an escape hatch later, not the trust root.

**Hashes are required.** An entry without a
per-platform `sha256` is invalid. A URL template
without resolved hashes is invalid. First install
must not be "download whatever GitHub latest
returns."

**Host allowlist.** Every `url` host must be on a
list the installer and the index linter share.
Unknown hosts are a hard error. This is the
replacement for the loose `isGHCR` check that sent a
bearer token to any `/v2/…/blobs/` URL (BUG-3).

**No unsanitized path splice.** Version, tag, and
commit-like fields are validated before they enter a
URL (BUG-1). Prefer fully written URLs in the index
over concatenation.

**Updating a package** is "new tag, new hashes,"
reviewed as a lock-diff on the user side and as an
index PR on ours. It is not "rebuild two platforms
and promote a ledger."

**`gale update`** re-resolves through the index, then
writes a new lock entry. The lock diff is the review
surface. The index cannot change bytes of an already
locked version: sync reads the lock, not the index.

## 7. Security

The security goal does not change: a machine that
has a lock installs those bytes and refuses anything
else. What changes is who produced the bytes, and
what we can still prove.

### 7a. Invariants that survive

These are already paid for. Keep them.

1. **The lock is a control, not a report.**
   `gale sync` never writes `gale.lock`. Writers are
   `install`, `update`, `remove`, and `lock`. A
   failed resolve leaves the previous lock
   byte-identical.

2. **Readers fail closed.** A lock that is present
   and cannot be fully modeled is an error, never
   treated as absent. A missing symlink target is
   unreadable, not unlocked.

3. **Hash mismatch has no automatic remedy.**
   Something on disk is not what the lock says. A
   human decides. `gale sync` does not "fix" it by
   rewriting the lock.

4. **Project activation is gated.** Before
   `PATH_add`, the active generation must match the
   lock roots, and every reached store directory
   must carry provenance that matches the locked
   identity, SHA-256, and method. Failure leaves
   the system PATH untouched. `gale shell` and
   `gale run` treat it as fatal. Global scope still
   has no activation gate — `~/.gale/current/bin`
   is on PATH from shell rc — and still forbids
   carry-forward of unlocked versions.

5. **Verify equals run.** Hash the archive (or the
   bare binary) *before* extract. Extract into the
   store. Do not rewrite rpaths, pkg-config, or
   codesign. The bytes on PATH are the bytes that
   hashed. This is the relocatable-binaries
   proposal, and the fetch model makes it the
   default instead of a rebuild of our bottles.

6. **Provenance is all-or-nothing.** Each store
   directory writes `.gale-provenance.toml` only
   when the record is complete. Incomplete records
   are not written. Invalid records are treated as
   absent.

7. **No silent downgrade.** `--no-frozen` remains
   the explicit escape, and it warns. Nothing
   unlocks on its own.

### 7b. What we stop claiming

We will no longer claim:

- "we compiled this from this source on our CI"
- "Sigstore proves gale-recipes built this bottle"
- "a revision bump means we rebuilt the world"

Those claims were expensive, and they were already
weaker than they sounded. Install mutates the
attested bottle today (`relocatable-binaries.md`):
we verify artifact X and run X′. `gale verify`
checks the remote GHCR blob, not the file on PATH.
`gale audit` compares archive hashes of a rebuild
and says a mismatch is not tampering.

The fetch model is a narrower claim, and we can
keep it:

> This store directory is the bytes the lock named.
> The lock named the bytes the index named at
> resolve time. The index named an upstream URL
> and a SHA-256. The installer checked the hash
> and did not mutate the result.

That is the cargo / aqua / go module model. It is
weaker on "we built it." It is stronger on "the
thing we run is the thing we hashed," and on "a
network blip cannot flip us into an unattested
source build."

### 7c. Trust on first resolve

A scope with no lock is unlocked mode, as today,
with one warning.

`gale install` without a lock:

1. Fetch the index entry (HTTPS, ETag cache as
   today).
2. Require a platform artifact with `url` +
   `sha256`.
3. Check the host against the allowlist.
4. Download. Hash. Refuse on mismatch.
5. Extract. Write provenance. Write the lock.

The index author plus HTTPS is the trust-on-first-use
source. A compromised index can point a *new*
install at malware that matches the (also
compromised) hash.

That is the same class as a compromised recipe +
`.binaries.toml` today, minus Sigstore on our
rebuild. Mitigations, in order:

- curated index, not a scrape of every GitHub
  release
- required hashes; no "latest asset" heuristic as
  the default
- host allowlist
- index changes are git PRs, same as recipes
- after the first lock, **the index cannot change
  those bytes**. Sync is lock-only.

`gale update` is a new resolve. It is supposed to
be. The review surface is the lock diff, the same
way a `go.sum` or `Cargo.lock` diff is the review
surface today.

Do not revive recipe ed25519 signing. It was
removed in v0.13.0. The lock is the user-facing
control. Signing the index would authenticate the
TOFU source; it would not replace the lock. If we
add index integrity later, sign a bundled index
snapshot, not per-file ad-hoc keys.

### 7d. Optional upstream attestations

Some upstreams (Go, a few GitHub-attested
releases) publish provenance we can verify.

Rule: if the index *declares* an attestation, the
installer verifies it and fails closed. If the
index does not declare one, the SHA-256 is the
control. Do not require attestations. That would
drop most of the catalog.

Do not call this `gale verify` of *our* GHCR
bottle. If the command stays, it verifies the
lock's hash against the store, and any declared
upstream attestation against the archive. It
never mutates.

### 7e. URL and extract safety

- Allowlisted hosts only. No GHCR anonymous token
  dance unless a bottle-allowlist entry uses
  `ghcr.io/homebrew`, and then the token is scoped
  to that host.
- Reject `url` values with credentials, redirects
  off the allowlist, or path traversal in the
  archive (already in `extractTar`).
- Version / tag fields are `[A-Za-z0-9._+-]` or
  the URL is fully written in the index.
- Checksums are hex SHA-256, compared in
  constant time with the existing helper.

### 7f. Store identity

Drop recipe revisions. Identity is
`<name>/<version>/`.

A version's bytes are the lock's SHA-256. A
re-tagged upstream that changes bytes fails the
hash check. It does not overwrite the store
directory. That is the content-addressed-store
concern (gh#191) without content-addressed paths:
the path stays human, the hash refuses the swap.

`--path` local builds are out of scope for the
first cut. If they return, they keep the
working-tree digest in the version so they cannot
collide with a fetched identity.

### 7g. What this does not buy

- We do not detect a malicious *upstream* release
  that we have not yet locked. Neither does
  Homebrew. Neither did our Sigstore story, which
  attested *our* rebuild of that source.
- We do not sandbox the downloaded binary.
- We do not replace OS package signing
  (Apple, Debian).
- A global `PATH` entry is still unhooked. The
  lock still cannot police a binary the user
  copied by hand into `~/.gale/pkg`.

## 8. Leftovers

A 2026-08-15 pass over all 193 recipes:

| Bucket | Count | Action |
|---|---:|---|
| GitHub release, both primary platforms | 77 | Index them |
| Official vendor host | 14 | Index them |
| Third-party well-known builds | 2 | `python`, `ruby` — see below |
| Release exists but incomplete | 11 | Per-package, §8b |
| Interpreted (gem / pip) | 7 | Not first-class packages |
| Source-only CLI / server | 38 | §8c |
| Library / build-dep | 44 | Drop |

The first thin catalog is the 91 fetchable
packages. That is the product.

### 8a. Drop libraries and servers

`openssl`, `pcre2`, `libgit2`, `autoconf`, and the
rest exist so we can compile. Stop compiling and
they have no users.

`postgresql`, `mariadb`, `redis`, `qemu` are
services. Out of scope. The original design said
developer CLI tools, not Homebrew.

### 8b. Partial releases

Decide per package. Do not invent a general
"best-effort binary" path.

| Package | Situation | Action |
|---|---|---|
| `cmake` | `macos-universal` + linux tarballs | Fetch |
| `ninja` | `ninja-mac.zip` / `ninja-linux.zip`, arch implicit | Fetch; verify arch once |
| `ccache` | `darwin.tar.gz` arch unspecified | Fetch; verify arch |
| `procs` | `aarch64-mac` + linux | Fetch |
| `eza` | linux only on current release | Omit on Darwin, or bottle |
| `dust` | no `darwin-arm64` | Omit on Darwin arm, or bottle |
| `btop` | linux musl only | Omit on Darwin, or bottle |
| `gitui` | `gitui-mac.tar.gz`, arch unclear | Verify; else omit on arm64 |
| `fish` | linux tarball; mac is `.pkg` | Omit on Darwin, or bottle |
| `podman`, `renode`, `mosh` | installer / dmg / pkg | Omit |
| `patchelf` | linux only, by design | Fetch on Linux; skip on mac |

"Verify arch" means a one-time `file` / Mach-O
check when adding the index entry, recorded in the
entry, not a runtime rewriter.

### 8c. Source-only CLIs

Do not keep a general source-build path. Split
the 38 by need:

**OS already has it.** macOS ships `git`, `zsh`,
`bash`, `less`, `curl`, `unzip`. Optional later:
`git = "system"` meaning "do not install; assume
PATH." No store entry, no lock hash. Not required
for the first cut.

**Short bottle allowlist.** The names people will
actually miss: `tmux`, `htop`, `tree`, `wget`,
maybe a newer `git`, `nmap`, `socat`. Fetch a
Homebrew bottle as a tarball into the store. Do
not run `brew`. Pin the bottle's SHA-256. Host
allowlist `ghcr.io` for that path only.

If the bottle is not relocatable enough to run
from `~/.gale/pkg/…` without `install_name_tool`,
**skip the package**. Rewriting rpaths reopens
verify-equals-run and `fixup_darwin.go`. The
allowlist should stay short and embarrassing.
Each entry is debt.

**Omit until upstream ships a binary.** `gopls`,
`httpstat`, `flarectl`, `tokei` (v12 had assets,
v14 does not), `deadnix`, `statix`, `lua`,
`openocd`, `poppler-utils`, `dtc`, `tig`, `tio`,
`picocom`, `mandoc`, `mtr`, `lsof`, `pigz`,
`autossh`, `sqlite`. Do not `go install` them.

### 8d. Runtimes that are awkward

**`python`.** Official macOS installers are not a
store prefix. Use python-build-standalone
(indygreg / astral), which is what aqua and mise
use. Hash-pin those artifacts. Treat the builder
as an upstream, not as "our rebuild."

**`ruby`.** No good official relocatable tarball.
Either omit from the first catalog or use a
known ruby-build standalone, same rule as
Python. Gems (`cocoapods`, `colorls`,
`tmuxinator`, `ruby-lsp`) are not Gale packages.
They install into a Ruby prefix if we have one.

**`rust`.** Do not ship a gale-built rustc. Ship
`rustup`. Toolchain versions are rustup's job.

**Interpreted packages** (`httpie`, `glances`,
`meson`, the gems): not in the core catalog. A
runtime plus that language's installer is enough.

## 9. Lock, store, generations

### Lock schema

Keep enforcement. Simplify the node.

A v2 (or a v1 subset) node is:

```toml
[packages."just@1.56.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"
sha256 = "…"
method = "fetch"
```

Gone: `revision`, `runtime_deps`, `build_deps`,
`graph_digest` as a dep-closure hash, GHCR
`manifest_digest`, `method = "source"`.

Keep: schema version, the downgrade guard
pattern so an old gale cannot silently rewrite
the file, `[targets.*]` roots, platform as an
artifact dimension, fail-closed unknown fields.

`graph_digest` today binds a node to its
dependency closure. Fetch packages are leaves.
If a future package needs a runtime file from
another store dir, add it explicitly. Do not
keep the farm's implicit closure.

Sync still never writes the lock. Activation
still checks roots and provenance.

### Store

`~/.gale/pkg/<name>/<version>/`. No `-N` suffix.

Occupied directory + different hash = refuse.
Occupied directory + same hash = cache hit.

Generation rebuild does not walk
`.gale-deps.toml`. It links `bin/` (and `man/`
if present) from each locked root. No farm
populate.

### Staleness

A package is stale when the lock root disagrees
with `gale.toml`, or the store directory's
provenance hash disagrees with the lock. That is
the whole check.

Missing vs empty `.gale-deps.toml` goes away.
"Highest revision on disk" goes away. The
direnv-loop class that came from those
comparisons goes away.

`sync-state.toml` stays. A failed fetch must
not retry for minutes on every `cd`. The stamp
still records `complete` / `incomplete`, the
failed names, and a fingerprint of manifest +
lock + host + platform.

### gc and rollback

Retention keys are lock roots plus generations
at or above `current`, in every registered
scope. No farm claimants. No revision orphans.

Rollback still refuses an incomplete
generation.

## 10. Commands

Keep: `install`, `remove`, `sync`, `update`,
`list`, `info`, `outdated`, `search`, `which`,
`doctor`, `gc`, `generations`, `init`, `env`,
`shell`, `run`, `pin`, `unpin`, `lock`,
`completion`, `hook`.

`install` resolves the index (or the lock, when
present and matching), fetches, finalizes.

`outdated` / `update` talk to the index, not to
GHCR ledgers.

`doctor` loses farm, deps-meta, and
legacy-provenance migration checks. It keeps
PATH, scope, lock readability, sync-state, and
store-hash drift.

Drop or mothball: `build`, `create-recipe`,
`audit` (rebuild), `lint` (recipe), `recipes`,
`verify` as GHCR-attestation. Reuse `verify`
only if it means "store matches lock."

`gale import homebrew` is not a build-recipe
importer. If it stays, it writes index pointers
or a bottle-allowlist entry.

## 11. Migration

Existing users have store dirs keyed
`<name>/<version>-<revision>/`, GHCR
provenance, and v1 locks with dep graphs.

Do not silently reuse those directories. A
fetched `just@1.56.0` is not the gale-built
`just@1.56.0-3`.

Plan:

1. Ship a gale that can fetch and that still
   *reads* old locks enough to print a
   migration command. It does not treat an old
   lock as unlocked.
2. `gale migrate --fetch` walks each locked
   root, resolves the same *version* (ignore
   revision) through the new index, fetches,
   writes new store paths, writes a new lock,
   rebuilds the generation.
3. Old store dirs become gc candidates.
4. A version with no index entry is reported,
   not built from source.

Projects without a lock install fresh.

Do not require a flag day across every
machine on day one. Require it before the old
installer is deleted. The downgrade-guard
lesson still applies: an old gale handed a new
lock must fail loud, not rewrite.

## 12. Phases

Phase 0 is this document. No code.

**Phase 1 — fetch installer, small index.**
Ten packages we already use (`jq`, `ripgrep`,
`fd`, `just`, `gh`, `go`, `gofumpt`,
`golangci-lint`, `direnv`, `zoxide` or `uv`).
New installer path next to the old one, behind
a clear config or a new command, until it is
the default. Lock nodes for those packages use
the fetch schema. Tests at `cmd/gale` and
`integration/`: hash mismatch refuses; sync
does not write the lock; activation gate
still holds.

**Phase 2 — default fetch, index of the 91.**
`gale install` fetches. Source build is
unreachable from the CLI. Index linter:
required hashes, allowlisted hosts, `bin` /
`strip` present. Doctor and gc ignore farm
and revisions for new installs.

**Phase 3 — delete the distro.** Remove
`internal/build`, farm, GHCR, Sigstore of our
bottles, recipe build steps, recipes CI
promote/ledger. `gale-recipes` becomes the
index repo, or folds into a directory in
gale. Migration path from §11 is required
before this delete.

**Phase 4 — leftovers, if any.** Bottle
allowlist. Python standalone. `system` pins.
None of this blocks 1–3.

Each phase keeps the environment invariants
in §7a. A phase that needs a rpath rewrite
has taken a wrong turn.

## 13. Non-goals

- Replacing Homebrew's catalog.
- Compiling from source "just in case."
- A general `brew` wrapper or `brew bundle`
  frontend.
- Reimplementing aqua's template language in
  v1.
- Content-addressed store paths (gh#191).
  Hash refusal is enough.
- Index signing in v1.
- Attestation required for every package.
- Language package management (npm, pip,
  gems, crates).
- System packages, daemons, GUI apps.

## 14. Open questions

1. **Lock schema version.** New `version = 2`
   versus a v1 subset that drops unused
   fields. v2 is cleaner. v1-with-omissions
   is less migration code. Old gale must fail
   on the new file either way.
2. **Where the index lives.** A slim
   gale-recipes, or `index/` inside gale.
   Two repos still have a coordination cost.
   One repo couples CLI releases to catalog
   edits. Lean: keep a second repo, but it
   is only TOML pointers.
3. **aqua-registry as a fallback.** Useful
   for one-offs. It is not hashed the way
   §6 requires unless we snapshot hashes
   into *our* lock on first fetch (TOFU
   without a reviewed index hash). Default
   off.
4. **`rust` vs `rustup`.** Index `rustup`
   only, unless someone has a concrete need
   for a pinned rustc tarball.
5. **Bottle allowlist size.** Zero for
   phases 1–3 is acceptable. Non-zero needs
   a relocatable-bottle test that does not
   call `install_name_tool`.
6. **Command compatibility.** Keeping the
   same verbs is the point. A
   `~/.gale/config.toml` `backend = "fetch"`
   during phase 1, then delete the flag.

## Appendix A — Recipe audit (2026-08-15)

Method: parse all 193 recipe TOMLs. For each
GitHub `source.repo`, fetch
`/releases/latest` then
`/releases/expanded_assets/<tag>`. Classify
assets by platform in the filename. Add
known official hosts (`go.dev`,
`nodejs.org`, `ziglang.org`, HashiCorp,
`dl.k8s.io`, ARM, 1Password, Google, AWS).

Primary platforms: `darwin-arm64`,
`linux-amd64`.

### Fetch from GitHub releases (77)

`actionlint`, `age`, `atuin`, `bandwhich`,
`bat`, `bottom`, `bun`, `chezmoi`,
`cloudflared`, `cmake`, `croc`,
`difftastic`, `direnv`, `dive`, `doctl`,
`doggo`, `duckdb`, `duf`, `fastfetch`,
`fd`, `flyctl`, `fzf`, `gale`, `gdu`, `gh`,
`git-delta`, `git-lfs`, `glow`, `gofumpt`,
`golangci-lint`, `gping`, `grpcurl`,
`hcloud`, `helix`, `herdr`, `hyperfine`,
`jless`, `jq`, `just`, `k9s`,
`lazydocker`, `lazygit`, `llvm`, `lsd`,
`mdbook`, `micro`, `mise`, `mongosh`,
`neovim`, `nushell`, `ouch`, `pnpm`,
`prismacat`, `probe-rs`, `procs`,
`protobuf`, `pscale`, `ripgrep`, `ruff`,
`scc`, `sd`, `shellcheck`, `shfmt`,
`starship`, `stylua`, `tealdeer`,
`tree-sitter`, `trippy`, `uv`, `vibeutils`,
`xh`, `yq`, `yt-dlp`, `zellij`, `zls`,
`zmx`, `zoxide`

### Official vendor hosts (14)

`1password-cli`, `arm-none-eabi-gcc`,
`awscli`, `docker`, `go`,
`google-cloud-sdk`, `helm`, `kubectl`,
`nodejs`, `rust`, `rustup`, `terraform`,
`zig`, `zig15`

### Third-party builds (2)

`python` — python-build-standalone.
`ruby` — no official relocatable tarball.

### Partial / awkward (11)

`btop`, `ccache`, `dust`, `eza`, `fish`,
`gitui`, `mosh`, `ninja`, `patchelf`,
`podman`, `renode`

### Interpreted (7)

`cocoapods`, `colorls`, `glances`,
`httpie`, `meson`, `ruby-lsp`,
`tmuxinator`

### Source-only CLI / server / runtime (38)

`autossh`, `bash`, `coreutils`, `curl`,
`deadnix`, `dtc`, `flarectl`, `git`,
`gopls`, `htop`, `httpstat`, `less`,
`lsof`, `lua`, `mandoc`, `mariadb`, `mtr`,
`nmap`, `openocd`, `picocom`, `pigz`,
`poppler-utils`, `postgresql`, `qemu`,
`redis`, `rsync`, `socat`, `sqlite`,
`statix`, `tig`, `tio`, `tmux`, `tokei`,
`traceroute`, `tree`, `unzip`, `wget`,
`zsh`

### Libraries / build-deps (44)

`autoconf`, `automake`, `bison`, `bzip2`,
`cairo`, `dbus`, `expat`, `flac`, `flex`,
`fontconfig`, `freetype`, `gettext`,
`glib`, `gmp`, `gnumake`, `gperf`,
`lcms2`, `libevent`, `libffi`, `libgit2`,
`libidn2`, `libjpeg-turbo`, `libogg`,
`libpng`, `libpsl`, `libssh2`, `libtiff`,
`libtool`, `libusb`, `libyaml`, `lz4`,
`m4`, `ncurses`, `oniguruma`, `openssl`,
`openssl4`, `openjpeg`, `pcre2`, `pixman`,
`pkgconf`, `readline`, `xz`, `zlib`,
`zstd`

## Appendix B — Code surface (indicative)

Measured on gale `main` at the time of
writing. Tests included.

Delete-shaped: `internal/build` 11.1k,
`internal/farm` 4.1k, `internal/attestation`
2.3k, `internal/ghcr` 1.9k, `internal/ai`
1.5k, `internal/provenance` (shrink, not
wholesale delete), `internal/lint` 1.9k,
plus `cmd/gale` build / create-recipe /
audit / verify / migrate-from-old-provenance.

Shrink-shaped: `internal/installer` 10.8k,
`internal/registry` 5.2k, `internal/recipe`
4.0k, lockwrite / lockplan / lockgraph,
`cmd/gale` doctor / context / gc / install /
sync.

Keep-shaped: `internal/store`, `download`,
`config`, `activation`, `projects`, `env`,
generation core, direnv hook.

gale-recipes: 10 workflows, ~11k lines of
CI script, 193 ledgers. Almost all of that
is the distro.
