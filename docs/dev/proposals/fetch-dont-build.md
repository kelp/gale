# Fetch, Don't Build

Status: proposal (2026-08-15); accepted as the
plan the same day. Index lives in gale-recipes.
Phase 1 starts in a later session after this
merges. Appendix C is the review record.
Scope: gale CLI + gale-recipes. A product cut, not a
patch.
Verdict: stop being a distro. Keep the environment
manager. Fetch upstream artifacts. Pin them in the
lock. Do not compile or cache our own bottles.

This is the written form of the 2026-08-15 pivot
discussion. It is a plan, not an implementation.
Do not treat it as ready to code until §12's
prerequisites are decided in this file — they now
are.

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

This proposal **supersedes** three current
principles in `design.md`: "everything from source,"
"prebuilt binaries only for compiler bootstraps,"
and "one tool" as a full Homebrew replacement. The
new claim is narrower: a declarative environment
for developer CLI tools that already publish
relocatable binaries. The leftover policy in §8 is
the product decision, not a temporary gap. Those
three sentences in `design.md`, plus the matching
lines in `CLAUDE.md` and the README, are amended
when Phase 2 lands — not before, and not left
contradicting the shipped installer.

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
plausibly named binary for gale's primary platforms.
That is a candidate pool, not a proven catalog. It
is still almost every name a developer types. The other half
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
| `gale.toml` | declared pins and `[vars]` |
| `gale.lock` | enforced artifact identity |
| store (fetch namespace, §7f) | immutable trees |
| `gen/<N>` + `current` | atomic environment swap |
| direnv hook, `gale env` / `shell` / `run` | activation |
| global and project scopes | where the manifest lives |
| `gale sync --if-needed` + `sync-state.toml` | direnv must not stall |
| `gale gc`, one-step rollback | retention and undo |
| `internal/download` | fetch, hash, extract |

Host sections, `.tool-versions`, `[pinned]`, and
`[bin]` overlays are frozen or removed (§15).
They are not part of Phase 1.

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
- Sigstore *of our bottles*. Keep a generalized
  verifier for upstream attestations the lock
  requires (§7d). Delete the GHCR-only wiring.
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
latest = "1.56.0"

[versions."1.56.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"
sha256 = "…"
tree_digest = "…"
hash_source = "upstream-sha256sums"  # or "computed"
strip = 1
bin = ["just"]

[versions."1.56.0".artifacts."linux/amd64"]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-x86_64-unknown-linux-musl.tar.gz"
sha256 = "…"
tree_digest = "…"
hash_source = "upstream-sha256sums"
strip = 1
bin = ["just"]
```

Version blocks are append-only. A published
`[versions."1.56.0"]` is immutable under the linter.
`latest` is one pointer. It must name a version
block that exists. The resolver and the index
linter share that rule. A dangling `latest` is
invalid. `gale update` appends a new block and
moves `latest`. `gale install just@1.56.0` reads
that block, not whatever the URL string happens
to contain.

Templates (`{{version}}`, `{{os}}`, `{{arch}}`) are
authoring sugar only. The lock stores the resolved
URL, hash, strip, bin, and any attestation
requirement — never the template.

`strip` and `bin` tell the extractor how to land a
tree that `generation.Build` can link. Some upstreams
ship a bare binary (`jq-macos-arm64`). Some ship a
tarball with `bin/`. The installer does not guess.

**Permitted transformations:** download, hash, safe
extract, strip-components, place named bins. Nothing
else. No package-specific hooks, patches, wrappers,
repacking, rpath changes, or re-signing. An artifact
that needs one is omitted.

**Admission, per platform, recorded in the entry:**
correct arch, darwin code signature valid if the
file is Mach-O, and `otool -L` / `ldd` shows only
system libraries (`/usr/lib`, `/System`, linux
loader + libc). Admission runs the canonical
extractor and records the **tree digest** in
the index entry. Lock writers copy it; the
installer recomputes and checks it. That is
how a lock can name every platform without
extracting them on this machine.

Filename classification (appendix A) is a
candidate list.

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

**Start curated, in gale-recipes.** The index
stays a second repo so catalog edits are not
CLI releases. After the cut it is TOML
pointers only: no `[build]` steps, no GHCR
ledgers, no promote CI. The first catalog is
Phase 1's ten packages, after the admission
gate in §5. Appendix A's ~90 names are
candidates, not a launch list. Do not import
aqua-registry. Its first-fetch TOFU without a
reviewed hash contradicts this section. If
aqua returns, it is its own proposal.

**Hashes are required, and their origin is
recorded.** Prefer an upstream-published checksum
file, and verify that file's signature where one
exists (Go `.sha256`, HashiCorp GPG-signed
`SHA256SUMS`, Zig's signed index, GitHub
attestations). Fall back to a hash we computed
only when upstream publishes none, and set
`hash_source = "computed"`. An entry without a
per-platform `sha256` is invalid. First install
must not be "download whatever GitHub latest
returns."

**Who writes new hashes.** Deleting
`auto-update.yml` does not delete the job. A
narrow bot (or a human) opens a PR that appends a
version block. Constraints: PR-only, no write
token on main, verifies upstream checksums when
present, human-reviewed diff. The bot is a new
trust root. Name it when it exists. Do not
pretend the toil vanished.

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

**Index fetch errors are errors.** Do not inherit
today's stale-on-error / one-hour negative-404
cache for resolve. `install` and `update` hard-fail
when the index cannot be fetched and no lock
already names the package. A cached index body may
serve `outdated` as a hint, labeled stale, never as
a silent "nothing newer." The lock still wins for
sync.

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

5. **No post-extract mutation.** Hash the archive
   (or the bare binary) *before* extract. Extract
   into the store. Do not rewrite rpaths,
   pkg-config, or codesign. The honest claim is
   not "the bytes on PATH hash to the archive
   SHA-256" — extraction, strip, and mode bits
   intervene. The claim is: the archive matched
   the lock, Gale performed no mutation, and a
   **tree digest** (sorted `path + mode +
   sha256` of each regular file) binds the
   store to the lock. Admission computes it
   once per platform and writes it on the
   index entry. Lock writers copy it.
   Installers recompute and check it. That is
   how one lock names every platform without
   extracting them here. Provenance, `verify`,
   doctor, and staleness compare that digest.
   It occupies the slot `graph_digest`
   vacates. Activation still reads provenance
   and the lock; it does not rehash the tree
   on every `cd`. `gale verify` and doctor do.

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

> The archive matched the lock's SHA-256. Gale
> extracted it without mutation. The store
> directory's tree digest matches the lock. The
> lock named the URL, layout, and hash the index
> named at resolve time.

That is weaker origin authentication than today's
Sigstore-on-our-CI. A compromised index that
supplies both URL and hash is a single factor.
Curation, PRs, and an allowlist are process, not
a second mechanism. The mechanisms are: required
hashes, `hash_source` preferring upstream-signed
checksums, host allowlist, lock-only sync, sticky
attestation when one was locked (§7d), and the
index commit recorded in the lock so a later
index rewrite is visible.

It is stronger on "Gale did not mutate the
artifact" and on "a network blip cannot flip us
into an unattested source build."

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

That is **weaker** than today. Substituting an
artifact on a new resolve currently needs the
recipes-repo content *and* the Sigstore identity
of gale-recipes CI. After the cut, the index file
alone suffices for most of the catalog. "Minus
Sigstore" is the second factor, not a footnote.

Mitigations, in order:

- curated index, not a scrape of every GitHub
  release
- required hashes; no "latest asset" heuristic as
  the default
- host allowlist
- index changes are git PRs, same as recipes
- after the first lock, **the index cannot change
  those bytes**. Sync is lock-only.
- the lock records the index commit (or bundle
  digest) used at resolve, so a later rewrite is
  a visible diff, not a silent reread.

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

### 7d. Attestation is sticky and gale-owned

Some upstreams publish provenance we can verify.
Most do not. Do not require an attestation for
every package.

The index may *declare* an attestation. The
installer then verifies it and fails closed. The
expected issuer, SAN, and source repo live in
**gale**, not in the index. Verifying a bundle
with no identity policy is not a control.

The lock records whether that artifact was
attested, and under which identity. An update
that drops attestation for a package that had it
is a **refusal**, not a silent downgrade. The
index linter treats removing an attestation
field as a breaking change.

The index cannot switch the verifier off. That
is the regression §7d had in the first draft: a
compromised index deletes one line and
verification disappears. The lock is the switch.
`gale update --allow-attestation-drop` is the
explicit escape, and it warns.

**Required declarations:** the `gale` package
itself (we control that release), and
python-build-standalone if it is ever indexed.
Those cost nothing and close the worst entries.

Keep a generalized attestation verifier. Delete
only the "this is our GHCR bottle" wiring.

`gale verify` means: store tree digest matches
the lock, and any locked attestation still
verifies against the retained archive or the
re-fetched URL. It never mutates.

### 7e. URL and extract safety

- Allowlisted hosts only. The list includes known
  redirect targets per host, hop-validated
  (`github.com` → `objects.githubusercontent.com`;
  `dl.k8s.io` is a redirector). A naive "no
  off-list redirects" rule either rejects GitHub
  or gets wildcarded back into BUG-3.
- **No ambient credential on an artifact fetch.**
  `GALE_GITHUB_TOKEN` is unavailable on this path.
  If a later bottle entry uses `ghcr.io`, it uses
  the anonymous token only.
- Delete `download.Fetch`'s GNU `mirrors` map
  with the source builds. It substitutes a
  different host on HTTP error.
- Reject `url` values with credentials.
- Version / tag fields are `[A-Za-z0-9._+-]` or
  the URL is fully written in the index.
- Extract stages atomically. Write provenance
  only after the tree digest is computed.
- Reject archive entries that would write Gale
  metadata (`.gale-provenance.toml`,
  `.gale-deps.toml`), device/FIFO nodes, or
  escaping symlinks/hardlinks.
- Mask setuid/setgid (`07000`). Cap entry count
  and decompressed size.
- Hold `ExtractZip` to `extractTar`'s rules
  before any zip-distributed package enters the
  index. Zip is a primary format here
  (`ninja-mac.zip`, terraform, protoc,
  1password-cli).

### 7f. Store identity

Drop recipe revisions. The user-facing identity
is `name@version`. The on-disk path is **not**
bare `<name>/<version>/`.

Two reasons. Old gale's `resolveVersion` still
falls back from `<v>-1` to a bare `<v>`
directory (`internal/store/store.go`). During
coexistence, an old gale with a v1 lock and no
activation gate in the global scope can
cache-hit a fetched tree under a gale-built
identity. And two locks can pin the same
upstream version to different hashes; one bare
path cannot hold both.

Use a fetch namespace or a hash-qualified
internal path (`pkg/fetch/<name>/<version>-<sha12>/`
or similar). User-facing commands still print
`just@1.56.0`.

A re-tagged upstream that changes bytes fails
the hash check. It does not overwrite a
referenced directory.

`--path` local builds are out of scope for the
first cut. If they return, they keep the
working-tree digest so they cannot collide with
a fetched identity.

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
- **A lock is installable only while upstream
  keeps the artifact.** Deleting GHCR removes
  gale's only immutable copy. A deleted GitHub
  release is a permanent sync failure, with no
  attacker.   Accept that, as aqua and mise do.
  A local
  `~/.gale/cache/artifacts/<sha256>` of
  verified archives is Milestone 6 or never:
  own proposal, off by default, keyed by
  sha256, re-hash on read. Long-term
  reproducibility is a user-side mirror, not
  a return of our bottle farm.

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

The first catalog is Phase 1's ten packages,
after the §5 admission gate. The 91 are
candidates. Several appendix A names are
misclassified or contradictory (`rust` vs
`rustup`, `awscli` macOS `.pkg`,
`google-cloud-sdk` self-updates, `llvm` exists
for the deleted toolchain, `gale` is us). Do
not treat 91 as a launch number.

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
| `eza` | linux only on current release | Omit on Darwin |
| `dust` | no `darwin-arm64` | Omit on Darwin arm |
| `btop` | linux musl only | Omit on Darwin |
| `gitui` | `gitui-mac.tar.gz`, arch unclear | Verify; else omit on arm64 |
| `fish` | linux tarball; mac is `.pkg` | Omit on Darwin |
| `podman`, `renode`, `mosh` | installer / dmg / pkg | Omit |
| `patchelf` | linux only, by design | Fetch on Linux; skip on mac |
| `awscli` | macOS is a `.pkg` | Omit on Darwin |

Do not write "or bottle" here. That grows §8c
before Phase 4 exists.

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

**Bottle allowlist size is zero in v1.** The
names people will miss (`tmux`, `htop`, `wget`,
`nmap`, `socat`, a newer `git`) dynamically
link other Homebrew kegs. A bottle poured into
`~/.gale/pkg` without rewrite either breaks, or
resolves dylibs against the user's mutable
`/opt/homebrew` — unlocked bytes inside a
"locked" closure. Vendoring those kegs and
rewriting rpaths is the farm.

Admission if this ever returns (Phase 4, own
proposal): `otool -L` / `ldd` shows only system
paths, no Cellar assumptions, no post-install
scripts, no rewrite, and the binary runs from a
deep `GALE_HOME` through a generation symlink.
Expected result: still zero. Do not resolve
against `/opt/homebrew`.

**Omit until upstream ships a binary.** `gopls`,
`httpstat`, `flarectl`, `tokei` (v12 had assets,
v14 does not), `deadnix`, `statix`, `lua`,
`openocd`, `poppler-utils`, `dtc`, `tig`, `tio`,
`picocom`, `mandoc`, `mtr`, `lsof`, `pigz`,
`autossh`, `sqlite`. Do not `go install` them.

### 8e. Want git? Use Homebrew. Do not wrap it.

A newer `git` is the usual leftover people will
not give up. Upstream does not publish a
relocatable `darwin-arm64` tarball. A Homebrew
bottle is not a Gale package: it links other
kegs and is built for `/opt/homebrew`.

So yes: `brew install git`. The same for a
short, honest list that fails §5 admission —
today that is `git`, and likely `tmux`,
`htop`, `wget` if you want them. The OS copy
is also fine (`xcode-select` git, macOS zsh).

Rules, so this does not grow back into a
wrapper:

- Homebrew is for the leftovers only. Do not
  `brew install jq` and also `gale install jq`.
- `~/.gale/current/bin` stays ahead of
  `/opt/homebrew/bin` on PATH. Gale wins on
  name collision.
- Do not run `brew` from gale. No `brew
  bundle` frontend, no bottle fetch, no
  Cellar symlink into a generation.
- A project `gale.toml` does not mention
  brew packages. If you need the leftover
  documented, put it in a Brewfile or in
  prose. Optional later: `git = "system"`
  meaning "assume PATH; do not fetch." That
  is a note, not an installer.
- `brew upgrade` can still break those few
  tools. That is the old Homebrew complaint,
  confined to the set Gale refused.

This is coexistence, not a second backend.
The moment gale shells out to brew, the cut
has failed.

### 8d. Runtimes that are awkward

**`python` and `ruby` are out of the first
catalog.** python-build-standalone is a
third-party builder with its own trust story,
not "upstream." Add it later with declared
attestations (§7d) and tests for SSL, venvs,
and relocatability. Ruby has no named
maintained relocatable tarball. Omitting ruby
drops `cocoapods`, `tmuxinator`, and `colorls`
users. That is a non-goal, not an accident
(§13).

**The store is never written after finalize.**
`pip install`, `gem install`, and gcloud
self-update into a store prefix contradict
§7f. Interpreter user-site and self-updating
SDKs live outside the store, or the package
is omitted. `google-cloud-sdk` is omitted
until that policy has a home.

**`rust`.** Index `rustup` only. Do not also
index `rust`.

**Interpreted packages** (`httpie`, `glances`,
`meson`, the gems): not in the core catalog.

## 9. Lock, store, generations

### Lock schema

Keep enforcement. **Require lock `version = 2`.**
A v1 subset is undefined in every released gale
(`method` is `binary | source`). The moment a
scope contains one fetch node, the file is v2
plus a new downgrade guard. Mixed v1/v2 locks
do not exist. Mixed source/fetch locks are
refused.

A v2 node carries every field sync needs. The
index is not reread:

```toml
[packages."just@1.56.0".artifacts."darwin/arm64"]
url = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"
sha256 = "…"
tree_digest = "…"
method = "fetch"
strip = 1
bin = ["just"]
hash_source = "upstream-sha256sums"
index_commit = "…"
# attestation = { issuer = "…", san = "…", repo = "…" }  # if locked
```

Writers record **every platform** present in the
index entry, not only the running one. One
committed lock still serves every host.

Gone: `revision`, `runtime_deps`, `build_deps`,
`graph_digest` as a dep-closure hash, GHCR
`manifest_digest`, `method = "source"`.
`provenance.Record.graph_digest` becomes
optional; existing records remain readable.
`tree_digest` is the new bind.

Fetch packages are leaves. Admission (§5) must
show every dynamic dependency is inside the
artifact or on the OS-library allowlist. An
artifact that needs another store directory is
out of v1.

Sync still never writes the lock. Activation
still checks roots and provenance.

### Store

See §7f. Occupied directory + different
`tree_digest` = refuse. Same digest = cache hit.
The comparison is the tree digest, not a
directory hash the codebase cannot compute.

Generation rebuild does not walk
`.gale-deps.toml`. It links `bin/` (and `man/`
if present) from each locked root. No farm
populate.

### Staleness

A package is stale when the lock root disagrees
with `gale.toml`, or the store directory's
provenance `tree_digest` disagrees with the
lock. That is the whole check. Doctor's
"store-hash drift" means the same comparison,
recomputed.

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

Keep **current + one previous** generation.
That is enough for "I just broke PATH."
Older undo is the lockfile in git. Today's
keep-10 window is what made gc retain the
wrong set (gh#247).

Retention is every store directory linked by
those two generations, in every registered
scope. No farm claimants. No revision
orphans. Rollback still refuses an
incomplete generation.

## 10. Commands

Keep: `install`, `remove`, `sync`, `update`,
`list`, `info`, `outdated`, `which`, `doctor`,
`gc`, `init`, `env`, `shell`, `run`, `lock`,
`completion`, `hook`. `generations` shrinks
to `list` and `rollback` (one step).

`pin` / `unpin`, `search`, `switch`, `add`,
`repo *`, `sbom`, and `inspect` are mothballed
or deleted. Each is another finalize-adjacent
path. `[pinned]` goes with `pin`.

`install` resolves the index (or the lock, when
present and matching), fetches, finalizes.

`outdated` / `update` talk to the index, not to
GHCR ledgers.

`doctor` is read-only. Delete `--repair` and
`--force`. Four checks: PATH, lock readable,
generation matches lock roots, tree digest
matches. Farm, deps-meta, and legacy-lock
novels go when those packages die. The remedy
it prints is a command the user runs.

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
`just@1.56.0-3`. Ignoring a revision is
replacement, not equivalence.

`gale migrate` already means "refetch
unprovenanced binary-method dirs"
(`lockfile.md`). Use a new verb
(`gale migrate --to-fetch` is still a
collision). Pick `gale fetch-adopt` or
similar before Phase 1.

Plan:

1. Ship the v2 reader before any v2 writer.
   An old gale handed a v2 lock fails loud.
2. The adopt command plans every root first,
   prints the lock diff, requires
   confirmation, and refuses when frozen / in
   CI. Fetch and verify everything, then
   atomically write the v2 lock and a new
   generation. Any failure leaves the old
   lock, generation, and store usable.
3. Write fetch-namespace store paths only
   after every machine that shares that store
   has a gale new enough not to resolve them
   as bare `<version>` (§7f). Global scope
   has no activation gate; the namespace is
   what closes that window.
4. Old store dirs become gc candidates once
   no retained generation links them.
5. A version with no index entry is reported,
   not built from source.

Projects without a lock install fresh.

## 12. Phases

Phase 0 is this document. No code in this
change. Implementation is a later session.

**Prerequisites, before any fetch lock is
written:** lock v2 reader and guard; collision-safe
store paths (§7f); tree digest; `ExtractZip`
hardened; redirect allowlist; no ambient
credential on fetch. These are not Phase 3
cleanup.

**Phase 1 — fetch installer, ten packages.**
`jq`, `ripgrep`, `fd`, `just`, `gh`, `go`,
`gofumpt`, `golangci-lint`, `direnv`, `uv`.
Each passes the §5 admission gate. **Index
all ten first.** Then one switch PR. A
jq-only switch is a dual backend: every
other name still builds. Not-in-index is an
error, not a build. **One installer.** Build
fetch on a branch or as dead `internal/fetch`
until that switch. Do not ship
`backend = "fetch"` next to source install.
Mixed source/fetch locks are refused. Tests
at `cmd/gale` and `integration/`: hash
mismatch refuses; sync does not write the
lock; activation gate still holds; tree
digest drift is doctor-visible; rollback
then sync returns to the lock.

Phase 1 is fetch + lock v2 + global/project.
Host sections, `.tool-versions`, pins, and a
dual backend are out of that session (§15).
`[bin]` overlays stay until fetch is
default. 193 source recipes still collide.

**Phase 2 — default fetch, grow the catalog
by admission, not by 91.** `gale install`
fetches. Source build is unreachable from the
CLI. Index linter: required hashes,
`hash_source`, `tree_digest`, allowlisted
hosts, `bin` / `strip`, version blocks
immutable, `latest` names an existing
block. Amend
`design.md`, `CLAUDE.md`, and the README in
the same change.

**Phase 3 — delete the distro.** Remove
`internal/build`, farm, GHCR bottle wiring,
recipe build steps, recipes CI
promote/ledger. Keep a generalized attestation
verifier. `gale-recipes` becomes the index
repo. §11 adopt is required before this
delete.

**Phase 4 — leftovers, if any.** Each is its
own proposal: python-build-standalone,
`system` pins, bottle allowlist (expected
empty). None of this blocks 1–3.

Each phase keeps the environment invariants
in §7a. A phase that needs a rpath rewrite,
a package-specific hook, or a runtime dep
on another store dir has taken a wrong turn.

`docs/dev/change-discipline.md` still governs
this work. Version identity, finalize, and
staleness all change. That document is not
distro overhead.

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
  gems, crates), including cocoapods /
  tmuxinator / colorls once ruby is omitted.
- System packages, daemons, GUI apps.
- aqua-registry as a resolver.
- A Homebrew bottle backend in v1.
- Gale invoking `brew`. Coexistence is
  documented in §8e: brew for leftovers,
  gale for the fetch catalog, gale first
  on PATH.
- Two live installers in one binary.
- `.tool-versions` as a manifest.
- `[pinned]` / `gale pin`.
- `[bin]` collision overlays.

## 14. Open questions

Closed in this revision:

- Lock schema is **v2**. No v1 subset.
- `rustup` only. Not `rust`.
- Bottle allowlist is **zero** in v1.
- aqua-registry is **out**. Own proposal if
  ever.
- Phase 1 uses an experimental command or
  flag; mixed locks are refused.

Closed after owner review:

- The cut is the plan, including the
  narrower product promise.
- Index lives in **gale-recipes** (pointers
  only).
- Phase 1 starts in a later session, after
  this document merges. Not in the same
  change.
- Leftovers you still want (`git`, …) are
  Homebrew or the OS. Gale does not wrap
  brew (§8e).
- Control-plane cuts in §15 are in scope
  for the same program. Host sections are
  frozen, not extended. `.tool-versions`,
  pins, and `[bin]` overlays are out of
  Phase 1. Generations keep current + one
  previous. One installer, never two.

Still open, and not merge-blocking:

1. **Adopt command name.** Must not collide
   with documented `gale migrate`.
2. **Exact fetch store path spelling.**
   Namespace vs hash-qualified. Must not be
   resolvable by old `resolveVersion`.
3. **`darwin-amd64`.** Appendix A did not
   survey it. Admit per package or drop the
   platform from the first catalog.
4. **Who runs the index-update PR bot,**
   and under which app token. Wait until
   there is an index to update.
5. **Host sections: freeze or delete.**
   Freeze is the default. Delete if unused
   after fetch is default.

## 15. Control plane

The distro is not the only extra control
plane. These cuts stop the remaining
tier-3 shape: three scopes, two manifests,
ten generations, pin vs lock vs disk, and
a second installer.

**Do not simplify away:** the lock as a
control, fail-closed reads, the project
activation gate, no post-extract mutation,
`sync` never writing the lock,
`sync-state.toml`. Global vs project
stays.

**Before Phase 1 lands on main**

1. **One installer.** No `backend = "fetch"`
   flag beside source install. Fetch is
   unused code or a branch until the switch
   PR. The switch is global, after the first
   ten are in the index. A jq-only cutover
   is a dual backend. Mixed-method locks
   are refused.
2. **Freeze host sections.** No new
   `[hosts.*]` or `--host` semantics. Cross-
   scope bugs are a named class. Chezmoi
   or a second file already does
   multi-machine. Delete later if unused.
3. **Drop `.tool-versions`.** Gale reads
   `gale.toml` only.
4. **Keep current + one previous
   generation.** `generations` is `list`
   and one-step `rollback`.
5. **Drop `[pinned]` and `gale pin`.**
   `gale update` updates what you name.
   The lock is the pin.
6. **No `--recipes` override.** `--index
   <dir>` pointing at a gale-recipes
   checkout is the only local escape.
7. **Doctor never mutates.** Delete
   `--repair` and `--force`. The store has
   two writers: fetch-finalize and gc. The
   remedy doctor prints is a command the
   user runs.
8. **Freeze `config.toml`.** Add no keys.
   Port none to fetch. At the switch:
   compiled-in index URL, `--index <dir>`
   the only override, retention the
   constant 2, no `[sync] parallelism`,
   no `[generation] keep` / `-1` sentinel.
   A config file must not repoint
   resolution or disable gc.
9. **Project publication is registered.**
   A project generation does not swap
   `current` until its canonical root is
   durably registered. Registration
   failure aborts the swap. Read-only
   commands do not register.
10. **Bound automatic sync.**
    `sync --if-needed` has a fixed
    deadline. Timeout records incomplete,
    cancels work, leaves `current`
    unchanged. Typed `gale sync` is
    unbounded.
11. **Rollback is temporary.** It moves
    `current` only. The lock still names
    the new roots; any sync returns to
    them. Durable undo is reverting the
    lock in git. Rollback prints this.
    Integration-test the direnv-after-
    rollback sequence.
12. **Serial installer.** Delete
    `internal/parallel`, `internal/prewarm`,
    and `GALE_JOBS`. Deterministic order;
    every error surfaces. Lands with the
    `config.toml` freeze, before fetch is
    default.

**After fetch is default**

13. **One finalize function** for install,
    update, remove, and lock. One test
    family. `context.go` is the smell.
14. **Bin collisions are a hard error.**
    No `[bin]` overlay, no per-host winner.
    Safe once fetch is default and the
    catalog is the first ten. 193 source
    recipes still collide.
15. **Doctor is four checks:** PATH, lock
    readable, generation matches lock
    roots, tree digest matches.
16. **Index fetch errors are errors.**
    Do not inherit stale-on-error or the
    one-hour negative 404 for resolve.
17. **Every scope rebuilds only from the
    lock.** Global, project, and `--host`.
    None rebuild from the manifest or
    store alone when a lock is present.
18. **Mothball the long tail:** `sbom`,
    `inspect`, `search`, `switch`, `add`,
    `repo *`.
19. **GC does not repair.** No resolver,
    network, generation rebuild, or
    `--force`. Mark symlink targets of
    the two kept generations; sweep the
    rest. Fail closed if retention is
    incomplete. Milestone 2 is keep=2
    only. This rewrite is Milestone 5.

**Do not do**

- A `system` or brew backend in the
  installer. Coexistence is §8e.
- Content-addressed store paths in v1.
- Index signing, required attestations,
  aqua-registry.
- Parallel fetch as a reliability project.
  Serial is fine for ten packages.
- Linux as a Phase 1 admission platform.
  macOS-first until the ten are boring.
- A local artifact cache (§7g) before
  Milestone 6. Own proposal; keyed by
  sha256; re-hash on read.

PR-sized steps live in [`TODO.md`](../../../TODO.md).

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
`internal/farm` 4.1k, `internal/ghcr` 1.9k,
`internal/ai` 1.5k, `internal/lint` 1.9k,
plus `cmd/gale` build / create-recipe /
audit / GHCR-verify. `internal/attestation`
shrinks to a generalized verifier, not a
delete. `internal/provenance` shrinks
(`graph_digest` optional, `tree_digest`
added).

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

## Appendix C — Review (2026-08-15)

Three models reviewed the first draft:
fable (claude-fable-5), opus 5, and sol
(gpt-5.6). All three verdicts were **revise
before treating as the plan.** None rejected
the cut.

Consensus, now folded into the body:

1. **§7d was fail-open.** An index-declared
   attestation is attacker-controlled. The
   lock now pins the requirement; dropping
   it is a refusal. Keep a verifier. Require
   it for `gale` itself.
2. **"Verify equals run" overclaimed.** The
   archive hash is not the store tree. The
   bind is a `tree_digest` computed at
   index admission, copied into the lock,
   and recomputed at extract. Activation
   still does not rehash on `cd`.
3. **The lock omitted layout.** `strip`,
   `bin`, attestation, and `hash_source`
   must live in the lock or sync rereads
   the index.
4. **Bare `<name>/<version>/` collides**
   with old `resolveVersion` and cannot hold
   two hashes of one version. Fetch
   namespace required. Global scope has no
   activation gate.
5. **Bottle allowlist is the farm.** v1
   size is zero. Admission is `otool -L`
   system-only, not "relocatable enough."
6. **91 is a candidate list.** First
   catalog is Phase 1's ten, after a
   mechanical gate. Filename scrape is not
   a compatibility audit.
7. **Lock is v2.** No v1 subset. Writers
   record every platform. Schema reader
   ships before any writer.
8. **GC "at or above current" breaks
   rollback.** Retain every generation we
   keep, and every store dir it links.
9. **Redirects and credentials.** Allowlist
   includes CDN hops. No
   `GALE_GITHUB_TOKEN` on artifact fetch.
   Harden `ExtractZip`.
10. **Hashes need an origin.** Prefer
    upstream-signed checksum files. Name
    the update bot. Index fetch errors are
    errors.
11. **Store is immutable after finalize.**
    pip / gem / gcloud self-update are out
    of the store or out of scope.
12. **This supersedes `design.md`
    principles.** Amend those docs when
    Phase 2 lands. change-discipline still
    applies; it is not distro overhead.

Disagreements were of emphasis, not
direction. Sol wanted the 91 reframed more
aggressively and `darwin-amd64` called out
(open question 4). Opus wanted a local
artifact cache for durability (§7g). Fable
wanted aqua cut entirely rather than
"default off" — done.

The through-line: the first draft correctly
said today's Sigstore attests bytes we then
mutate, then swung to a model where one
TOML file is the whole trust root for new
resolves and put the remaining second
factor under that file's control. The
revisions above are what make §7b's
narrower claim hold.

A second pass (fable, sol) asked for more
simplification. Consensus folded into
§15 and `TODO.md`: doctor is read-only;
`config.toml` loses behavioral knobs;
`tree_digest` is computed at admission;
the installer switch is global, not
per-package; rollback vs sync is
specified; gc is a sweeper, not a
repairer; project registration is part
of publication.
