# Test audit P1a — snapshot workspace identity

Date: 2026-10-30. Status: proposed; not executed.
Base: `5841079fc6e1c18352dc2cf7589c9193dc1a9f8e`.
Requirements: [snapshot identity and recorder findings](../audit.md#baseline-failures-and-flake-candidates)
and [P1 roadmap](../roadmap.md#p1--snapshot-identity-and-cancellation-safety).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** the service and manager agree on the active workspace store, while
previously written snapshots remain accessible without an implicit destructive
migration.

**Scope:** this is the identity and recorder portion of P1. The separate
process-group EPERM failure remains open for a subsequent P1b plan; its cause
is not established and this plan must not weaken the fail-closed process-tree
guarantee. P2's shell admission and prompt budgets also remain open.

**Non-goals:** merge snapshot stores, delete or rename historical stores,
change the persisted hash algorithm, alter snapshot retention policy, or
modify process-tree termination. Keep `WorkspaceHashFor`'s existing raw-path
hash behavior for external callers and historical identifiers.

**Identity and compatibility decisions:**

- The active v2 catalog is `Manager.CatalogForRoot(s.workTree)`'s canonical
  workspace. `Service.StoreDir`, `Workspace`, `hasStore`, capture, status and
  maintenance must refer to that same workspace. `WorkTree()` continues to
  return the supplied path. Canonicalization uses the existing
  `canonicalWorkspaceRoot`: absolute/clean plus symlink resolution when
  possible, and absolute/clean for an absent path. Preserve the current
  constructor's no-store-creation behavior. If canonicalization fails,
  `store()` continues to return its error; the existing no-error getters may
  use the raw hash as a diagnostic fallback and must never create a store.
- The original supplied path also supplies one historical raw workspace hash.
  If it differs from the canonical hash, exact-hash lookup checks the
  canonical catalog first and then the raw catalog/legacy path under the
  existing root-wide store lock. Check both v2 and legacy lookup through
  `lookupSnapshotOwned`, preserving its exact-object validation, typed errors
  and `SnapshotLocation`. `LookupSnapshot`, `Diff` and `Restore` all use the
  same lookup; capture always writes canonical. Never move or merge stores.
- For historical v2 data, trust an alternate catalog only if its readable
  manifest's `Root`, canonicalized now, matches the requested canonical root.
  Reject a malformed or mismatched manifest with a typed error; never use it
  to serve or reclaim another workspace's snapshots. A known raw-path legacy
  bare repo has no root provenance, so consult only the direct raw hash of
  the supplied path, never a guessed hash or unrelated discovered store; it
  remains read-only under existing legacy rules. When a path has been removed,
  use its absolute/clean spelling and do not guess its former symlink target.
- Lookup tries the second known identity only when the first returns
  `ReasonSnapshotNotFound` or `ReasonSnapshotExpired`. An unreadable, corrupt,
  canceled or otherwise unexpected canonical result is returned immediately;
  a corrupt historical candidate is also surfaced, not converted to missing.
  If both are absent/expired, preserve the existing distinction: return
  expired if either candidate had a store but lacked the hash; otherwise
  return not-found. An exact canonical hit wins if both contain the hash.
- `Prune` and `Status` consider the canonical and validated historical v2
  catalogs, deduplicated by hash, within one manager ownership interval.
  Legacy bare repos are not reclaimed. Never bootstrap a store for a
  workspace with neither v2 catalog. Propagate unreadable/mismatched catalog
  errors; do not silently report healthy. Prune each validated v2 catalog
  with the existing retention method and existing protected-generation rules.
  With two stores, process canonical first; on error stop and return it, so a
  subsequent call can retry. Document that this is not an atomic multi-store
  transaction; each existing reclaim already commits its own progress.
- A legacy bare repository's hash alone cannot reveal a different, unknown
  symlink spelling. When launched by a canonical path, this phase cannot
  discover arbitrary historical legacy aliases; report that limitation in
  the implementation record. Do not scan and attribute unidentified bare
  repositories to a workspace. Previously known raw paths remain discoverable
  when that same path is supplied. V2 manifests with explicit matching roots
  that do not correspond to either known hash are left untouched and visible
  to existing manager-wide discovery; this phase does not automatically
  associate or reclaim them.
- Use temporary owned stores, Git and symlink fixtures; no actual user
  snapshots, credentials or external services. Preserve unrelated untracked
  work. Sequential tasks must be verified before committing named files.

## Contracts

Add these private helpers in package `snapshot` for Tasks 1–3:

```go
// serviceWorkspaceIDs returns canonical and original-path hashes in that
// order, deduplicated. canonicalRoot is the validated canonical workspace.
func (s *Service) serviceWorkspaceIDs() (canonicalRoot string, ids []string, err error)

// serviceCatalogs returns the canonical catalog and any existing, validated
// historical v2 catalog, in that order. It must be called with manager store
// ownership held; it performs no migration or deletion.
func (s *Service) serviceCatalogs(m *Manager, canonical *Catalog) ([]*Catalog, error)

// lookupServiceSnapshotOwned tries the known identities under the store lock.
func (s *Service) lookupServiceSnapshotOwned(ctx context.Context, m *Manager, canonical *Catalog, hash string) (*SnapshotLocation, error)
```

The lookup helper also checks the raw legacy path even when no raw v2 catalog
exists, as specified above. These are private; no public wire or API changes.

## Task 1: Align service identity with the canonical catalog

**Goal:** service-reported identity and maintenance prechecks identify the
same v2 catalog used by captures.

**Files:** `internal/snapshot/service.go`,
`internal/snapshot/manager_test.go`, `internal/snapshot/service_test.go`.

**Steps:**

1. Implement `serviceWorkspaceIDs` from Contracts using
   `canonicalWorkspaceRoot` and `WorkspaceHashFor` on the canonical and
   original roots. In `Workspace()` return the canonical ID; in `StoreDir()`
   use it. On canonicalization error, getters fall back to the original hash;
   `store()` still propagates its `CatalogForRoot` error. Keep `WorkTree()`
   and the lazy `New` construction behavior.
2. Update `TestServiceStoreDirMatchesManagerCatalog` to compare both
   `StoreDir` and `Workspace` to `CatalogForRoot` rather than requiring the
   uncanonicalized `WorkspaceHashFor(project)` result. Add
   `TestServiceCanonicalAndSymlinkIdentityAgree` with explicit owned symlink
   and real paths, an absent path, and no store creation from the getters.
   Verify captures from both existing aliases land in the same active store.
   Tests of `WorkspaceHashFor` itself retain raw-hash expectations.
3. Keep legacy compatibility in Tasks 2–3; do not delete old fixtures or
   change their expected behavior to conceal lookup failures.

**Verify:**

```bash
go test ./internal/snapshot -run '^Test(ServiceStoreDirMatchesManagerCatalog|ServiceCanonicalAndSymlinkIdentityAgree|WorkspaceHashForMatchesProjectHash)$' -count=1 -timeout 90s
gofmt -l internal/snapshot/service.go internal/snapshot/manager_test.go internal/snapshot/service_test.go
```

Expected: tests pass with the default temp root; formatting prints no paths.
Task 1 is independently committable, but does not by itself complete P1a.

## Task 2: Preserve exact lookup and restoration across known identities

**Goal:** canonical and known historical stores can serve existing snapshot
hashes without moving or mutating either store.

**Files:** `internal/snapshot/service.go`, `internal/snapshot/lookup.go`,
`internal/snapshot/lookup_test.go`.

**Steps:**

1. Implement `serviceCatalogs` and `lookupServiceSnapshotOwned` from
   Contracts and Decisions. Use `m.Catalog(id)` for the alternate, validate
   the existing v2 manifest's stored root against `canonicalRoot`, and do not
   create a manifest while merely inspecting. Check a raw legacy path using
   the raw catalog even when no raw v2 manifest exists. Distinguish not-found,
   expired and unreadable results as specified; hold the existing manager
   lock throughout and do not follow a symlink as a store directory.
2. In `LookupSnapshot`, `Diff`, and `Restore`, replace their direct
   `lookupSnapshotOwned` calls with the shared helper. Leave hash validation,
   store locking, operation target and read-only legacy treatment unchanged.
3. Add `TestServiceLookupHistoricalWorkspaceIdentity` with canonical and
   symlink paths and independent canonical v2, raw v2 and raw legacy
   fixtures. Exercise exact lookup, diff and restore of a hash unique to each
   store, canonical-first duplicate hash, and both stores present. Assert
   historical stores and protected flags remain unchanged by lookup. Add
   `TestServiceLookupRejectsMismatchedHistoricalManifest` for corrupt,
   unreadable and wrong-root v2 manifests; assert errors are not masked by a
   hit elsewhere. Add `TestServiceLookupMissingRootDoesNotGuessAlias` and
   `TestServiceLookupHistoricalErrorPrecedence` for the specified outcomes.
   Keep existing legacy lookup and restore tests; make their identities
   explicit instead of assuming a raw path is canonical on all platforms.

**Verify:**

```bash
go test ./internal/snapshot -run 'Test(ServiceLookup|Lookup)' -count=1 -timeout 2m
gofmt -l internal/snapshot/service.go internal/snapshot/lookup.go internal/snapshot/lookup_test.go
```

Expected: tests pass on the default temp root, including raw legacy fixtures;
formatting prints no paths. No user data is used or mutated.

## Task 3: Maintain validated v2 catalogs consistently

**Goal:** status and retention cover both known v2 identities without
creating unrelated stores or reclaiming unverified data.

**Files:** `internal/snapshot/service.go`, `internal/snapshot/service_test.go`,
`internal/snapshot/manager_test.go`, `internal/snapshot/rooted_test.go`,
`internal/snapshot/reconcile_test.go`, `internal/snapshot/recovery_test.go`,
`internal/snapshot/git_test.go`.

**Steps:**

1. Refactor `hasStore`, `Prune` and `Status` to recognize either known v2
   directory before bootstrap, then resolve and validate catalogs with
   `serviceCatalogs` while holding the existing root-wide ownership lock.
   If neither exists, return without creating the store. Apply the existing
   `ReclaimExpiredWorkspace` and `StatusWorkspace` to each validated
   catalog in canonical-first order; return the first error. Preserve legacy
   read-only and protected-generation guarantees.
2. Add `TestServiceMaintenanceKnownWorkspaceIdentities` with canonical only,
   raw only, both and neither, asserting each real catalog is visited and
   unrelated siblings and legacy bare repos remain byte-identical. Include
   protected generations, expired/unexpired entries, an invalid historical
   manifest, and a storeless project beside an over-budget unrelated store.
   Add `TestServiceMaintenanceSerializesAcrossAliases` using two services and
   the existing manager lock fixture to show maintenance cannot race capture.
   Update versioned test fixtures that unintentionally derive their expected
   active ID from a raw symlinked temp path: in `rooted_test.go` use the
   canonical service/catalog workspace ID; in the deleted-checkout case in
   `reconcile_test.go`, save the canonical root before removing the checkout
   and compare against that recorded root. In `recovery_test.go` compare
   reconciled IDs with the canonical catalog's ID. Keep explicit historical
   fixtures and assertions in `git_test.go` and `service_test.go`; their
   existing `Service` getters should now locate the canonical manifest.

**Verify:**

```bash
go test ./internal/snapshot -run 'Test(ServiceMaintenance|RootedMaintains|RootedMaintenance|Service_Prune|Service_Excludes|DiscoverFinds)' -count=1 -timeout 2m
gofmt -l internal/snapshot/service.go internal/snapshot/service_test.go internal/snapshot/manager_test.go internal/snapshot/rooted_test.go internal/snapshot/reconcile_test.go internal/snapshot/recovery_test.go internal/snapshot/git_test.go
```

Expected: focused tests pass on the default temp root; formatting prints no
paths. The independently known process-group EPERM flake is outside this task.

## Task 4: Correct the Git recorder assertion

**Goal:** the shutdown test detects real mutating Git operations without
mistaking an empty log for a command.

**Files:** `internal/app/snapshot_warning_test.go`.

**Steps:**

1. In `TestRuntimeCloseRunsNoGitGC`, normalize the recorded log into zero
   entries when empty and nonempty lines otherwise, both before and after
   `rt.Close`. Assert the recorder saw at least one expected Git operation
   during `Track` before counting the baseline; fail with the fixture's log
   path if not. Compare only appended entries with the existing read-only
   allowlist and heavy-command checks. Do not turn a missing recorder into a
   success and do not relax allowed commands.
2. Run the existing startup-maintenance warning test with the default temp
   root to verify the preceding identity fix reaches the app path.

**Verify:**

```bash
go test ./internal/app -run '^Test(RuntimeCloseRunsNoGitGC|StartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces)$' -count=1 -timeout 90s
gofmt -l internal/app/snapshot_warning_test.go
```

Expected: tests pass with default temp root; formatting prints no paths.

## Final verification and integration

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-test-p1a ./cmd/marshal
go test ./internal/snapshot -count=1 -timeout 3m
go test ./internal/app -run '^Test(RuntimeCloseRunsNoGitGC|StartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces)$' -count=1 -timeout 90s
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -count=1 -timeout 5m
go vet ./...
gofmt -l internal/snapshot/service.go internal/snapshot/lookup.go internal/snapshot/lookup_test.go internal/snapshot/manager_test.go internal/snapshot/service_test.go internal/snapshot/rooted_test.go internal/snapshot/reconcile_test.go internal/snapshot/recovery_test.go internal/snapshot/git_test.go internal/app/snapshot_warning_test.go
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-test-p1a go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Formatting must print no paths. Compare actual failures to the P0 baseline
in [the roadmap](../roadmap.md#p0--restore-trustworthy-test-signals); record
any process-group EPERM separately, without treating it as a passed test. P1a
does not close P1 until the separate process-cancellation work is complete.
Document the unknown historical-legacy-alias limitation, tested compatibility
cases and any OS/terminal environments not exercised. After verification,
update the identity and recorder audit findings and P1 roadmap status; commit
the named plan and docs with the work. Do not claim the full suite is clean
while P1b/P2 remain open.

## Plan self-review

- Service identity, lookup, restore and maintenance use the same canonical
  catalog with a constrained, validated known-alias fallback.
- No automatic store move, merge, deletion or unsafe guessing is proposed;
  error precedence and partial maintenance behavior are explicit.
- The process-group failure is deliberately left for a separately investigated
  plan. No implementation occurred in planning.
