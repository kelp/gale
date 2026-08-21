# Index-update PR bot

Milestone 6 leftover. Not implemented. Humans
open index PRs today.

Related: `docs/dev/proposals/fetch-dont-build.md`
§4 ("Who writes new hashes"), open question 4.

## Status

gale-recipes `index/` is updated by human
PRs. `auto-update.yml` is gone. Workflows
use `contents: read`. Admit uploads
fragments and does not commit. No bot
opens version-block PRs. This file is the
own proposal Milestone 6 asked for. It is
not permission to add a workflow or mint
a token. A later gale-recipes PR is its
own change.

The bot, if ever built, lives in
gale-recipes. This document lives in gale.

## Gates

If a bot ever ships, all of these hold:

**PR-only.** It opens a pull request that
appends a version block. It never pushes
`main`.

**No write token on main.** The token or
app cannot push `main`. `contents: write`
on the default branch is a refusal.
Open question 4 (who runs it, which app
token) stays unanswered until that later
PR names the trust root.

**Upstream checksums.** Prefer an
upstream-published checksum file, and
verify that file's signature where one
exists. Fall back to a computed hash only
when upstream publishes none, and set
`hash_source = "computed"`. An entry
without a per-platform `sha256` is
invalid.

**Human-reviewed diff.** Every bot PR
waits for a human. The bot is a new trust
root. Name it when it exists.

**No farm.** Does not resurrect
`auto-update.yml`, promote, or GHCR.
Does not grant `GALE_GITHUB_TOKEN` on
artifact fetch.

## Out of scope here

A workflow file. A GitHub App or PAT.
Naming the bot. Changing `gale admit` or
index lint. Appending a version to any
index document. Linux admission.
