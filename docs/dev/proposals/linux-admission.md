# Linux admission

Milestone 6 leftover. Not implemented. The
catalog has no linux artifact keys.

Related: `docs/dev/proposals/fetch-dont-build.md`
§5, Appendix A (`linux-amd64`), Appendix B
(macOS-first until the ten are boring).

## Status

The ten have darwin/arm64 entries from
`gale admit`. Admit CI is Darwin-only and
does not commit. Farm CI is gone. That is
"macOS is boring." It does not admit
linux.

The ten: jq, ripgrep, fd, just, gh, go,
gofumpt, golangci-lint, direnv, uv. Later
darwin names already used
grow-by-admission. This leftover is the
ten first for linux.

This file is the own proposal Milestone 6
asked for. It is not permission to admit
linux. A later gale-recipes PR is its own
change.

## Gates

If a linux index PR ever ships, all of
these hold:

**The ten first.** Index key
`linux/amd64`. Later names use
grow-by-admission.

**Pass §5.** Correct arch. Linkage from
ELF headers (`PT_INTERP`, `DT_NEEDED`),
not `ldd` (`ldd` can execute an untrusted
ELF). Loader plus libc only. Omit an
artifact that needs an rpath rewrite. No
symlinks or hardlinks. No extra
transforms. `gale admit` printed every
`tree_digest`. Do not invent hashes.

**New version blocks.** Do not add linux
to a published `[versions."X"]`. Every
platform that version will carry is
admitted before commit.

**Upstream checksums.** Prefer an
upstream-published checksum file, and
verify that file's signature where one
exists. `hash_source` stays honest.

`gale admit --os linux --arch amd64` can
run on the Darwin admit host. A Linux
runner is a choice, not a requirement.

## Out of scope here

Any `index/**.toml` linux artifact.
`admit-linux.yml`. Extending
`admit_manifest.py`. `darwin/amd64`.
Packages beyond the ten. `linux/arm64`.
`gale admit` in the sandbox.
