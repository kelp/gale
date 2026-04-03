# Audit Report: internal/download

## Summary

Four bugs in HTTP fetch, hashing, and archive extraction. The
most serious is a symlink path-traversal bypass in `extractTar`
that lets a malicious archive write files outside the destination
directory despite the existing path-validation guard.

## Bugs Found

### BUG-1: Symlink destination not validated -- path-traversal bypass

- **File:** `internal/download/download.go:419-430`
- **Severity:** High
- **Category:** security
- **Description:** When a tar entry has type `TypeSymlink`, the
  code validates `hdr.Name` (the symlink's location) against the
  dest directory, but does not validate `hdr.Linkname` (what the
  symlink points to). A malicious archive can create a symlink at
  `destDir/escape -> ../../etc/`. A subsequent `TypeReg` entry
  named `escape/passwd` passes the prefix check because
  `filepath.Clean` doesn't resolve symlinks, but the actual
  `os.OpenFile` follows the symlink and writes outside dest.
  Hard links ARE validated (lines 432-434).
- **Code path:** `extractTar` -> `case tar.TypeSymlink` ->
  `os.Symlink(hdr.Linkname, target)` with no Linkname check.
- **Impact:** Arbitrary file write outside the extraction
  directory. Requires a malicious upstream package.

### BUG-2: HTTP clients have no timeout

- **File:** `internal/download/download.go:36, 72`
- **Severity:** Medium
- **Category:** resource-leak
- **Description:** `FetchNamed` uses `http.Get` and
  `FetchWithAuthNamed` uses `http.DefaultClient.Do`. Neither
  sets a timeout. A slow or stalled server causes the goroutine
  to hang forever.
- **Code path:** `Fetch` -> `http.Get(url)`.
- **Impact:** `gale install` hangs without timeout on
  unresponsive servers. No recovery path.

### BUG-3: Bearer token sent over plain HTTP

- **File:** `internal/download/download.go:61-87`
- **Severity:** Medium
- **Category:** security
- **Description:** `FetchWithAuthNamed` unconditionally adds
  `Authorization: Bearer <token>` regardless of URL scheme.
  Token can be sent over plain HTTP.
- **Code path:** `FetchWithAuth` -> `req.Header.Set(
  "Authorization", "Bearer "+bearerToken)` with no scheme check.
- **Impact:** Token leakage on HTTP redirects or misconfigured
  call sites. The test `TestFetchWithAuthSendsAuthHeader` uses
  plain HTTP, demonstrating the bug.

### BUG-4: File descriptor leak in CreateTarZstd Walk callback

- **File:** `internal/download/download.go:556-560`
- **Severity:** Medium
- **Category:** resource-leak
- **Description:** Inside the `filepath.Walk` callback, each
  file is opened and `defer src.Close()` runs when the
  enclosing function (`CreateTarZstd`) returns, not the
  callback. Every file opened during the walk stays open until
  `CreateTarZstd` returns.
- **Code path:** `CreateTarZstd` -> `filepath.Walk` callback ->
  `os.Open(path)` -> `defer src.Close()`.
- **Impact:** `gale build` on packages with 250+ source files
  fails with "too many open files" (default macOS ulimit is
  256).

## Test Coverage Gaps

- No test for symlink-based path traversal (BUG-1). Tests cover
  `../escape.txt`-style traversal and hard link validation, but
  not symlink-through-write.
- No test for HTTP timeout behavior.
- No test that `FetchWithAuth` rejects plain HTTP.
- No test for `CreateTarZstd` with a large file tree.

## Files Reviewed

- `internal/download/download.go`
- `internal/download/download_test.go`
