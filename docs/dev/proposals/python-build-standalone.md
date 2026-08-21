# python-build-standalone

Milestone 6 leftover. Not implemented. Python is
not in the catalog.

Related: `docs/dev/proposals/fetch-dont-build.md`
§7d, §8d, Phase 4.

## Status

gale-recipes `index/` has no python document.
`gale install python` is not-in-index. This
file is the own proposal Phase 4 asked for. It
is not permission to admit python. An index PR
is a later change.

## Gates

If an index PR ever ships, all of these hold:

**Declared attestation.** §7d requires a
declaration for python-build-standalone.
Identity policy (issuer, SAN, source repo)
lives in gale, not the index. The lock records
whether the artifact was attested and under
which identity. An update that drops
attestation refuses. The index cannot switch
the verifier off.

**Store immutability.** The prefix is never
written after finalize. `pip install` into
the store prefix is a refusal. User-site and
venv writes live outside the store, or the
package is omitted.

**Admission tests.** SSL, venvs, and
relocatability. §8d. Fail any of those and
the entry does not merge.

## Out of scope here

Admitting python. Changing the attestation
verifier. A pip or venv installer. Ruby.
Interpreted packages. `google-cloud-sdk`.
