# Race audit findings

Worktree branch: `worktree-races-audit` (off main).
Reproducers: `cmd/gale/race_repro_test.go` and
`internal/generation/race_repro_test.go`.

## Findings (5)

| ID  | Sev      | Conf      | Class | Commands                                            | State item            | Slug |
|-----|----------|-----------|-------|------------------------------------------------------|-----------------------|------|
| 0001 | high    | confirmed | C     | remove, install, add, update, sync, switch         | gale.lock             | [removelockentry-unlocked](findings/0001-removelockentry-unlocked.md) |
| 0002 | high    | confirmed | B     | generations rollback, install, sync, update, ...   | ~/.gale/current       | [rollback-bypasses-gen-lock](findings/0002-rollback-bypasses-gen-lock.md) |
| 0003 | critical| confirmed | C     | gc, install, sync, update, switch, add, remove     | ~/.gale/gen/<N>/      | [gc-cleanoldgenerations-unlocked](findings/0003-gc-cleanoldgenerations-unlocked.md) |
| 0004 | high    | confirmed | C     | gc, remove, install, sync, update, switch          | ~/.gale/pkg/          | [store-remove-no-package-lock](findings/0004-store-remove-no-package-lock.md) |
| 0005 | medium  | confirmed | C     | sync, install, update, switch                       | store + farm (scope)  | [project-gen-lock-not-shared-with-store-gen-lock](findings/0005-project-gen-lock-not-shared-with-store-gen-lock.md) |

## By class

- **Class A** (Go data races): 0 confirmed.
  `CGO_ENABLED=1 go test -race ./...` ran clean on the
  pre-existing test suite (the new TestAudit_* tests are
  expected to "fail" — they assert the bugs).
  No goroutines are spawned in mutation paths (verified
  by `grep -rn 'go func' cmd/ internal/`).
- **Class B** (FS TOCTOU / atomicity): 0002 confirmed
  (Rollback bypassing the gen lock is a classic
  unlocked-write-vs-locked-write).
- **Class C** (multi-process): 0001, 0003, 0004, 0005
  confirmed. Each races a Build/Install against another
  command operating on shared state.

## By shared-state item

| inventory item              | scenario tested | finding |
|------------------------------|------------------|---------|
| gale.toml                    | Class A/C: gc + install indirect; switch read-decide-mutate observed statically (parked) | static only |
| gale.lock                    | Class C deterministic                                 | 0001 |
| ~/.gale/current              | Class B deterministic                                 | 0002 |
| ~/.gale/gen/<N>/             | Class C deterministic                                 | 0003 |
| ~/.gale/pkg/                 | Class C deterministic                                 | 0004 |
| ~/.gale/lib/ (farm)          | Class C deterministic (scope mismatch)                | 0005 |
| ~/.gale/tmp/                 | MkdirTemp randomness; no shared mutation              | clean |
| in-memory mutation state     | -race sweep                                            | clean |

## Reproducers

- `cmd/gale/race_repro_test.go`:
  - TestAudit_RemoveLockEntryRace (→ 0001)
  - TestAudit_GcVsBuildRace (→ 0003)
  - TestAudit_GcVsInstall_WindowBetweenStoreWriteAndConfigWrite (→ 0004)
  - TestAudit_ProjectGenLockNotSharedWithStoreGenLock (→ 0005)
  - TestAudit_GaleTomlReadModifyWriteAcrossLockBoundary (static; parked in state.md)
- `internal/generation/race_repro_test.go`:
  - TestAudit_RollbackVsBuildRace_Deterministic (→ 0002)
  - TestAudit_RollbackBypassesGenLock (→ 0002)
