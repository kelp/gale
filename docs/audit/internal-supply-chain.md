# Audit Report: internal/supply-chain

## Summary

Five bugs across the registry, trust, installer, and GHCR
subsystems. Three are security issues: an SSRF/URL-injection
path through the version index, a bearer-token leak to arbitrary
HTTP hosts via a loose `isGHCR` check, and silent suppression of
network errors that allows unsigned binaries through.

## Bugs Found

### BUG-1: Unvalidated commit hash from .versions file used in URL

- **File:** `internal/registry/registry.go:146`
- **Severity:** High
- **Category:** security / url-injection
- **Description:** `FetchRecipeVersion` fetches the `.versions`
  index (not signature-verified), then splices the raw
  commit-hash string directly into the recipe fetch URL. A
  value like `../../../../etc` or one containing `?` or `#`
  would produce a malformed or redirected URL. The fetched
  recipe IS signature-verified, so exploitation also requires
  forging a signature, but the URL-injection itself is a logic
  bug independent of the trust layer.
- **Code path:** `FetchRecipeVersion` -> `parseVersionIndex` ->
  `commit` used verbatim in `fmt.Sprintf(...)` at line 146.
- **Impact:** Attacker who controls `.versions` file can redirect
  recipe fetches to arbitrary path segments on the same host.

### BUG-2: Network errors on .binaries.toml silently treated as "not found"

- **File:** `internal/registry/registry.go:191`
- **Severity:** Medium
- **Category:** security / error-handling
- **Description:** `fetchBinaries` returns `(nil, nil)` when the
  HTTP client returns an error (connection refused, timeout,
  DNS failure). The recipe falls through with no binary section,
  causing silent fallback to source build. A network-layer
  suppression of the file bypasses the `.binaries.toml` signature
  check without any warning.
- **Code path:** `FetchRecipe` -> `fetchBinaries` -> `client.Get`
  error -> `return nil, nil`.
- **Impact:** No log output, no warning. In a targeted attack,
  the attacker forces a source build by disrupting one HTTP
  request.

### BUG-3: GHCR bearer token sent to non-GHCR hosts matching OCI path pattern

- **File:** `internal/installer/installer.go:254-256`
- **Severity:** High
- **Category:** security / credential-leak
- **Description:** `isGHCR` returns true for any URL whose path
  starts with `/v2/` and contains `/blobs/`, regardless of host
  or scheme. A recipe with a binary URL like
  `http://evil.example/v2/x/blobs/sha256:abc` satisfies this.
  The code then calls `ghcr.Token(repo)` and sends the bearer
  token (potentially `GALE_GITHUB_TOKEN`) to the attacker host.
- **Code path:** `installBinary` -> `isGHCR(bin.URL)` true ->
  `ghcr.Token` -> `download.FetchWithAuthNamed` with crafted URL.
- **Impact:** GitHub token exfiltrated to attacker-controlled
  server via a malicious recipe's binary URL.

### BUG-4: trust.Verify API ambiguity -- (false, nil) for malformed signature

- **File:** `internal/trust/trust.go:69`
- **Severity:** Medium
- **Category:** error-handling / API contract
- **Description:** `trust.Verify` returns `(false, nil)` when
  base64 decode of the signature fails. Callers must check the
  bool, not just the error. `verifyRecipe` in `registry.go` does
  this correctly, but the test `TestVerifyCorruptedSignature`
  accepts either `ok == false` or `err != nil`, not pinning the
  contract.
- **Code path:** `trust.Verify` with non-base64 signature ->
  `return false, nil`.
- **Impact:** Future refactors that change the error behavior
  pass the test while breaking callers.

### BUG-5: Direct Registry struct construction bypasses signature verification

- **File:** `internal/registry/registry.go:21-24`
- **Severity:** Medium
- **Category:** security / verification-bypass
- **Description:** `Registry` is an exported struct with an
  exported `PublicKey` field. Setting `PublicKey` to `""` causes
  `verifyRecipe` to return nil without checking signatures. All
  tests construct `&Registry{BaseURL: srv.URL}` directly,
  leaving `PublicKey` empty. No test exercises `verifyRecipe`
  with an actual key.
- **Code path:** `&Registry{BaseURL: ...}` -> `FetchRecipe` ->
  `verifyRecipe` -> `r.PublicKey == ""` -> `return nil`.
- **Impact:** Code that constructs `Registry` directly (not via
  `New()` or `NewWithURL()`) silently disables verification.

## Test Coverage Gaps

- No test exercises `verifyRecipe` with an actual key.
- No test verifies `FetchRecipeVersion` rejects invalid commit
  hashes.
- No test exercises the GHCR token-leak path for non-GHCR hosts.
- `trust.Verify` contract tests accept either result.
- Network error path for `.binaries.toml` is not tested.

## Files Reviewed

- `internal/registry/registry.go`
- `internal/registry/search.go`
- `internal/trust/trust.go`
- `internal/attestation/attestation.go`
- `internal/ghcr/ghcr.go`
- `internal/registry/registry_test.go`
- `internal/trust/trust_test.go`
- `internal/attestation/attestation_test.go`
- `internal/ghcr/ghcr_test.go`
- `internal/installer/installer.go`
- `internal/download/download.go`
