# Cross-device factory working drafts implementation plan

**Goal:** Autosave incomplete factory handoffs across devices, reconcile offline/concurrent edits field by field, and safely convert a confirmed version into one existing factory draft.

**Spec:** [Working-drafts specification](../factory-working-drafts-spec.md), based on the approved [design](../factory-working-drafts-design.md). The user approved this implementation plan on 2026-11-15.

**Base:** authored at `16bc3952` on `codex/mcp-factory-handoff-plan`, with these planning documents uncommitted. Start from current `main`, bring the approved documents to the factory handoff, and reconcile in-flight project-factory-settings, MCP, Claude planner, preview and P5 changes before editing shared files. Do not merge unrelated work solely to reproduce the historical base.

**Non-goals:** MCP/Claude integration, production factory execution changes beyond idempotent creation, multi-user permissions, live collaboration, automatic text merging or automatic deletion. `web/` remains Go standard library only. Tests use isolated state, fake clocks, owned endpoints and disposable repositories; no ambient credentials or live providers.

## Contracts

Add these bridge-local types in `web/bridge/working_draft_types.go` and equivalent TypeScript types in `web/ui/src/lib/working-drafts.ts`. JSON names are stable. `content` contains raw editor inputs, not the effective factory configuration.

```go
type WorkingDraftContent struct {
    Title string `json:"title"`; Plan string `json:"plan"`; Spec string `json:"spec"`; RepoID string `json:"repoId"`
    TargetBranchOverride string `json:"targetBranchOverride"`; WorkspaceOverride string `json:"workspaceOverride"`
    ImplementerOverride string `json:"implementerOverride"`; ReviewerOverride string `json:"reviewerOverride"`
    ReviewerInstructions string `json:"reviewerInstructions"`; MaxRoundsOverride string `json:"maxRoundsOverride"`
    CIPollOverride string `json:"ciPollOverride"`; CIWaitOverride string `json:"ciWaitOverride"`
    ChecksMode string `json:"checksMode"`; ExpectedChecksText string `json:"expectedChecksText"`
    Native bool `json:"native"`; NativeChosen bool `json:"nativeChosen"`; ImplementerAuto bool `json:"implementerAuto"`
    NoRequiredCIAcknowledged bool `json:"noRequiredCiAcknowledged"`
}
type WorkingDraft struct {
    ID string `json:"id"`; ProjectRoot string `json:"projectRoot"`; Version uint64 `json:"version"`
    State string `json:"state"`; Content WorkingDraftContent `json:"content"`; UpdatedAt time.Time `json:"updatedAt"`
    FactoryItemID string `json:"factoryItemId,omitempty"`; Error string `json:"error,omitempty"`
}
```

`checksMode` is `inherit`, `none` or `custom`; blank raw number/profile/branch overrides mean follow. Empty/invalid values are accepted while editing; conversion performs existing factory validation. Project root is never derived from `content`. Limit encoded content to 2 MiB and combined plan/spec to `factoryImportLimit` (1 MiB). On local persistence failure preserve text in memory and show error; no claim of offline durability. Project draft store retains tombstones and conversion records indefinitely until a separately designed retention feature.

`WorkingDraftStore` owns single-writer storage and locking. Expose `OpenWorkingDraftStore(dir string, now func() time.Time) (*WorkingDraftStore, error)`, `Close() error`, `Create(projectRoot, requestID string, content WorkingDraftContent) (WorkingDraft,error)`, `Get(id string) (WorkingDraft,error)`, `List(projectRoot string) ([]WorkingDraft,error)`, `Save(id, requestID string, expectedVersion uint64, content WorkingDraftContent) (WorkingDraft,error)`, and `Delete(id, requestID string, expectedVersion uint64) (WorkingDraft,error)`. Add `BeginConversion(id, requestID string, expectedVersion uint64, request FactoryCreate, settingsVersion string) (WorkingDraft,error)` and `CompleteConversion(id, requestID, itemID string) (WorkingDraft,error)`: first durably reserves a single exact frozen conversion; second associates the item. Internal store records include canonical operation digest and full frozen request; they are not exposed in `WorkingDraft`. Same ID/operation/request/payload replays original result before version comparison; different payload conflicts. `Get` can return tombstone to its authorized user for recovery; `List` excludes deleted. Store uses a versioned, checksummed append-only journal, serialized lock and synced append; reject corruption/unavailable store rather than replacing it. Versioned records preserve identity and replay across restart. Add `ErrWorkingDraftConflict`, `ErrWorkingDraftNotFound`, `ErrWorkingDraftUnavailable` and `ErrWorkingDraftUncertain`.

Conversion is mediated by an instance-scoped `workingDraftConverter` with `Convert(ctx context.Context, id, requestID string, expectedVersion uint64, request FactoryCreate, settingsVersion string) (WorkingDraft, *FactoryItem, error)` and `Reconcile(ctx context.Context) error`. Freeze and persist the factory request after verifying that title, plan, spec, project and config correspond to the current working-draft content, current project settings version and user-confirmed resolved values. Resolve effective defaults and run preflight **before** reserving conversion; the exact normalized configuration and existing factory request ID are persisted with the intent. Use existing factory validation and exact stored request. Avoid holding the working-draft lock over network preflight or factory calls: perform preflight, then compare version/settings again atomically under the bridge's project-settings synchronization and reserve; run create after releasing. Implement and document a bounded lock order. Recheck settings at the linearization point; if conditional settings version cannot be obtained from the existing settings API, extend it without changing legacy clients. Add an internal `factoryAPI.CreateFrozen` entry point that first checks the factory journal for replay and otherwise validates and preflights the frozen config and calls `FactoryStore.CreateResolved` with that exact config, without resolving defaults again. This is necessary even for empty string or nil fields: normal `FactoryAPI.Create` would re-inherit them. A bridge crash after reserve replays only the frozen request with its original factory request ID through this entry point. If missing evidence/journal unavailable, retain `converting` and return uncertain. A known validation failure with no journaled item durably restores editing, with a new working-draft version; a subsequent conversion has a new ID. Serialize by draft ID, including simultaneous callers and startup reconciliation. The browser cannot supply an arbitrary factory request ID already used elsewhere; derive a stable namespaced factory request ID from draft ID and conversion request ID, and bind payload in both stores.

The internal factory seam is `func (a *factoryAPI) CreateFrozen(req, resolved FactoryCreate) (FactoryItem, error)`; it is only callable by the converter after it has validated and durably reserved its intent. It checks replay before preflight and never reads mutable defaults. The server, not the browser, constructs `resolved`.

For conflict UI, treat the following as atomic fields: title, plan, spec, repo, target branch, workspace, implementer profile, reviewer profile, reviewer instructions, each numeric limit, CI mode+text, native+chosen, implementer auto, and no-CI acknowledgement. Project is immutable. The combined result starts as local input; explicit per-field choices select local or server values, and every selected field remains editable. Save against the latest server version. Retain base, local, server and in-progress combined values in local storage until the backend confirms; a second conflict updates server and preserves the combined result.

## Task 1: Durable working-draft store

**Goal:** Persist incomplete working drafts, idempotent versioned edits and tombstones independently of factory items.

**Files:** Create `web/bridge/working_draft_types.go`, `web/bridge/working_draft_store.go`, `web/bridge/working_draft_store_test.go`.

**Steps:** Implement the contracts, checksum journal, private permissions and exclusive ownership as described above; follow existing `OpenFactoryStore`, `factoryCanonical`, `factoryDigest` and `FactoryStore.Update` patterns in `factory_store.go` without sharing mutable global factories. Validate type/size and registered project association at the service boundary; do not validate a complete factory plan while saving. Test `TestWorkingDraftIncompleteAndMultiple`, `TestWorkingDraftConditionalSave`, `TestWorkingDraftIdempotentReplayAndMismatch`, `TestWorkingDraftDeleteDoesNotResurrect`, `TestWorkingDraftProjectIsolation`, `TestWorkingDraftRestartAndPermissions`, `TestWorkingDraftCorruptionFailsClosed`, `TestWorkingDraftWriteFailureDoesNotClaimSaved`, and `TestWorkingDraftConcurrentSaves` using isolated temp dirs and an injected clock.

**Verify:** `go test ./web/bridge -run '^TestWorkingDraft' -count=1` and `go test -race ./web/bridge -run '^TestWorkingDraftConcurrentSaves$' -count=1` pass. Commit `working-drafts: task 1 — durable store`.

## Task 2: Idempotent conversion and reconciliation

**Goal:** Convert one exact reviewed working-draft version into at most one existing factory draft, including across a crash.

**Files:** Create `web/bridge/working_draft_convert.go`, `web/bridge/working_draft_convert_test.go`; modify `web/bridge/working_draft_store.go`, `web/bridge/working_draft_types.go`, `web/bridge/factory_http.go`, `web/bridge/projects.go` and their package tests where necessary.

**Steps:** Implement `BeginConversion`, `CompleteConversion`, `workingDraftConverter.Convert` and `Reconcile` from Contracts. At `factoryAPI.Create`, retain the existing `priorRequest` replay-before-resolution path; add a narrow internal preflight/resolve seam returning a frozen normalized request and stable project settings version without changing legacy callers. Add `CreateFrozen` as specified in Contracts so empty fields cannot inherit newly changed defaults on replay. Verify the server-derived factory request matches the working content and the user-confirmed display; never trust a mismatching `factoryRequest` supplied by a browser. Reserve intent atomically against the exact draft/settings version, use the stable namespaced factory request ID, and associate the returned item durably. On a confirmed preflight error store editing plus error without a factory item; on an indeterminate journal failure keep uncertain. Reconcile every persisted converting intent on startup before admitting new conversions for it. Never hold a working-draft mutex across factory preflight or an external network call.

Test `TestWorkingDraftConvertCreatesUnapprovedFactoryDraft`, `TestWorkingDraftConvertConcurrentOneItem`, `TestWorkingDraftConvertLostResponse`, `TestWorkingDraftConvertRestartAfterIntent`, `TestWorkingDraftConvertRestartAfterFactoryAppend`, `TestWorkingDraftConvertRestartAfterAssociation`, `TestWorkingDraftConvertPreflightFailure`, `TestWorkingDraftConvertFactoryUnavailable`, `TestWorkingDraftConvertChangedSettingsRequiresReview`, `TestWorkingDraftConvertFrozenEmptyFieldsDoNotReinherit`, `TestWorkingDraftConvertRejectsTamperedFactoryRequest`, and `TestWorkingDraftConvertLegacyFactoryReplay`. Inject failures at each persistence boundary; verify no duplicate factory item and no approval or run.

**Verify:** `go test ./web/bridge -run '^(TestWorkingDraftConvert|TestFactoryHTTPCreateApprovalAndReplay)' -count=1` and `go test -race ./web/bridge -run '^TestWorkingDraftConvertConcurrentOneItem$' -count=1` pass. Commit `working-drafts: task 2 — conversion reconciliation`.

## Task 3: Authenticated bridge API

**Goal:** Expose project-scoped working-draft operations and conversion to Studio through the spec's routes.

**Files:** Create `web/bridge/working_draft_http.go`, `web/bridge/working_draft_http_test.go`; modify `web/bridge/http.go` only for store lifecycle, converter construction and route wiring; modify `web/bridge/working_draft_types.go` only for exact wire requests.

**Steps:** Register POST/list/get/PUT/DELETE/convert routes from spec §3 behind the existing `/api` authentication. Add an authenticated `GET /api/factory/working-drafts/installation` returning a random stable installation ID persisted under the state directory (no filesystem path, token or user data); create and sync it once and keep it across restarts. Construct the store under the fleet state directory and close it at server shutdown; fail unavailable if ownership or replay fails. Validate registered project with the existing `registeredProject`, root-to-item identity and request limits; reject unknown fields and trailing JSON using the factory decoder's pattern. Return 409 with current accessible draft on version conflict, not another project's data. Implement and test the request/response contracts exactly as spec §3. Tests: `TestWorkingDraftHTTPAuthAndProjectScope`, `TestWorkingDraftHTTPInstallationIdentity`, `TestWorkingDraftHTTPIncompleteRoundTrip`, `TestWorkingDraftHTTPConflictAndReplay`, `TestWorkingDraftHTTPDeleteTombstone`, `TestWorkingDraftHTTPConvertAndUncertain`, `TestWorkingDraftHTTPMalformedAndLimits`, `TestWorkingDraftHTTPUnavailableStore`.

**Verify:** `go test ./web/bridge -run '^TestWorkingDraftHTTP' -count=1` and `go test ./web/bridge -run TestWebIsStdlibOnly -count=1` pass. Commit `working-drafts: task 3 — Studio API`.

## Task 4: Browser autosave and conflict state

**Goal:** Make saved, pending and conflicting working-draft state recoverable per device without silently losing newer edits.

**Files:** Create `web/ui/src/lib/working-drafts.ts`, `web/ui/src/lib/working-drafts.test.ts`; modify `web/ui/src/lib/api.ts`, `web/ui/src/lib/api.test.ts`.

**Steps:** Implement typed API methods matching Task 3. Build an instance-owned draft controller with injected local-storage adapter, network functions, clock/timer, and unique per-tab editor ID. Store last acknowledged base, local content, pending operation ID and exact bytes, and conflict/combined state in IndexedDB transactions, keyed by bridge installation identity, project, draft and editor; obtain a stable installation identity from the authenticated bridge's Task 3 endpoint rather than deriving it from a shared URL. On reopening after a tab or browser closes, enumerate recoverable local records for that installation/project/draft and let the user choose among orphaned editor copies; do not depend on retaining a tab ID across browser restarts or overwrite another tab's record. Keep an unassigned locally-created draft until a project is selected. Serialize writes, debounce 500 ms, persist locally before send, retry the same pending request ID/body on reconnect, and advance saved state only for the matching request. Once backend ID is known, migrate the local key atomically. On version conflict expose base/local/server and preserve local pending content. Do not discard data on quota errors, offline status, out-of-order replies, browser restart or another tab's saves. Explicitly report local persistence failure.

Tests: `autosavesAndReopensAcrossContexts`, `doesNotMarkNewerEditSaved`, `retriesIdenticalPendingRequest`, `offlineEditsRemainLocal`, `conflictKeepsAllVersions`, `quotaFailureIsVisible`, `localOnlyBeforeProjectSelection`, `localKeyMigrationIsAtomic`, `staleResponseAfterDeleteIsNotResurrected`. Use a fake IndexedDB implementation or injected storage double and fake timers; don't depend on a real browser's storage in unit tests.

**Verify:** `cd web/ui && npm test -- src/lib/working-drafts.test.ts src/lib/api.test.ts` passes; `npm run check` has no new errors against current-main baseline. Commit `working-drafts: task 4 — autosave client`.

## Task 5: Studio list, editor and conflict resolution

**Goal:** Give users a phone-friendly multi-draft workflow with transparent autosave and an editable field-level merge.

**Files:** Create `web/ui/src/views/WorkingDrafts.svelte`, `web/ui/src/views/WorkingDrafts.test.ts`, `web/ui/src/lib/WorkingDraftConflict.svelte`, `web/ui/src/lib/WorkingDraftConflict.test.ts`; modify `web/ui/src/views/FactoryNew.svelte`, `web/ui/src/views/FactoryNew.test.ts`, `web/ui/src/lib/routes.ts`, `web/ui/src/App.svelte` and route tests.

**Steps:** Add project-scoped list/new/open routes and factory/project links. Refactor `FactoryNew.svelte` input state into `WorkingDraftContent` without changing `buildConfig` inheritance semantics, checks-mode distinction or existing factory approval behavior. Show saved/pending/offline/conflict/errors and unavailable saved options. Implement the atomic field groups in Contracts as local/server selection controls plus an editable combined result; display both original versions until a conditional save succeeds. If another conflict arrives while editing, retain the combined result and show the newest server version. On deleted/converted state retain local unsent content and offer create-new; do not autosave into a factory item. Warn on unsaved navigation when possible, preserve draft across settings navigation and support keyboard and 390px layouts. Do not expose secrets in routes/logging.

Tests: `listsMultipleProjectDrafts`, `newDraftIsLocalUntilProjectSelected`, `restoresIncompleteFieldsAndInheritance`, `showsSavingAndOffline`, `fieldChoicesProduceEditableCombinedResult`, `secondConflictRetainsCombinedResult`, `deletedRemoteOffersCopy`, `unavailableOptionsRemainVisible`, `manualFactoryFlowStillWorks`. Use injected API/storage, not real credentials.

**Verify:** `cd web/ui && npm test -- src/views/WorkingDrafts.test.ts src/lib/WorkingDraftConflict.test.ts src/views/FactoryNew.test.ts` passes; `npm run check` has no new errors against baseline. Commit `working-drafts: task 5 — draft editing and conflicts`.

## Task 6: Conversion review, end-to-end proof and docs

**Goal:** Let users create one reviewed factory draft from a working draft and verify the complete cross-device flow.

**Files:** `web/ui/src/views/FactoryNew.svelte`, `web/ui/src/views/FactoryNew.test.ts`, `web/ui/src/lib/working-drafts.ts`, its tests; create `web/bridge/working_draft_integration_test.go`; update this plan, the spec/design and `docs/studio-fleet-factory/README.md`.

**Steps:** Present exact current working-draft content, effective settings and settings version before explicit convert. Disable conversion while local edits are pending; retain the stable conversion request ID and exact body locally and server-side until resolved. On response loss or reload, reconcile; never automatically generate a new conversion request ID for the same frozen request. On converted state show a link to the unapproved factory item; offer new working draft for any subsequent local edits. Tests: `conversionRequiresSavedReview`, `settingsChangeRequiresNewReview`, `lostConversionResponseReconciles`, `simultaneousConvertReturnsOneItem`, `validationErrorKeepsWorkingDraft`, `convertedDraftCannotBeAutosaved`.

Add `TestWorkingDraftEndToEnd` with real local HTTP service, temporary workspace and fake factory preflight; test two contexts, conflict, field merge, conversion and reload. Add `TestWorkingDraftEndToEndRecovery` for process restart at each intent/journal/association boundary; assert one factory item and zero approvals. Record exact implementation state, evidence and known limitations without rewriting historical records. Verify actual desktop and 390px browser flows using two contexts, offline and local-storage failure; use isolated endpoints and no external provider. The SPA build updates embedded assets; follow tracked asset convention.

**Verify:** `go test ./web/bridge -run '^(TestWorkingDraft|TestFactoryHTTPCreateApprovalAndReplay|TestWebIsStdlibOnly)' -count=1`; `cd web/ui && npm test && npm run check && npm run build`; `CGO_ENABLED=1 go build ./cmd/marshal`; `go build ./cmd/webbridge`; `go test ./...`; `go vet ./...`; `git diff --check`; `gofmt -l` on changed Go files prints nothing. Compare failures with current-main baseline and report them. Commit `working-drafts: task 6 — conversion and verification`.

## Integration notes

The user approved the implementation specification and plan on 2026-11-15. Autosaved working drafts are not factory items; conversion records a durable intent and reuses the factory's existing idempotent journal rather than promising a transaction across stores. Browser data can be evicted; only backend-confirmed edits are cross-device durable. Never report an ambiguous response as saved, approved, or completed. Recheck the settings and MCP implementation's landed interfaces before starting, especially project-settings versioning and `FactoryCreate` fields.

## Implementation record

Delivered on branch `factory-working-drafts`, one commit per task. The approved
documents came in first, by merging [PR #85](https://github.com/alecpullen/marshal/pull/85)
(`4fb99598`); the plan's historical base `16bc3952` was not reproduced, and no
unrelated branch was merged. Tasks 1–2 and 4–5 ran two at a time — the Go store
and conversion chain sequential inside package `web/bridge`, the browser client
and Studio views sequential inside `web/ui` — because those two halves have
disjoint files and separate build/test units. Tasks 3 and 6 each waited for the
task whose surface they consume.

| Task | Deliverable | Commit |
|---|---|---|
| 1 | `WorkingDraft`/`WorkingDraftContent`, checksummed append-only journal, tombstones, replay identities, conversion reservation records | `8701ab4e` |
| 4 | Typed API methods, `WorkingDraftController` with injected storage/requests/clock/timer, IndexedDB and in-memory adapters, conflict/offline/pending state | `8285e57d` |
| 2 | `workingDraftConverter.Convert`/`Reconcile`, `factoryAPI.PrepareFrozen`/`CreateFrozen`, `FailConversion`, derived namespaced factory and association IDs | `790b279a` |
| 5 | `#factory/drafts` routes, `WorkingDrafts.svelte`, `WorkingDraftConflict.svelte`, `FactoryNew.svelte` moved onto one `WorkingDraftContent` | `095e9b2b` |
| 3 | Authenticated `/api/factory/working-drafts` routes, installation identity, store lifecycle on the fleet, startup reconciliation | `8bfb148b` |
| 6 (Go) | `TestWorkingDraftEndToEnd`, `TestWorkingDraftEndToEndRecovery` over a real local HTTP service | `08528110` |
| 6 (UI) | Conversion review, frozen conversion request across reload, uncertain/retry card, converted links | `f9a8035b` |
| 6 (docs) | This record, the spec status and the README entry | this documentation commit |

Files: `web/bridge/working_draft_types.go`, `working_draft_store.go`,
`working_draft_convert.go`, `working_draft_http.go`, their tests and
`working_draft_integration_test.go` are new; `web/bridge/factory_http.go`,
`http.go`, `fleet.go` and `working_draft_types.go` (wire bodies) are modified.
In `web/ui`: `src/lib/working-drafts.ts`, `src/views/WorkingDrafts.svelte` and
`src/lib/WorkingDraftConflict.svelte` are new; `src/lib/api.ts`,
`src/lib/factory.ts`, `src/lib/routes.ts`, `src/App.svelte` and
`src/views/FactoryNew.svelte` are modified. `web/` stays Go standard library
only, nothing under `internal/...` is touched, no route outside `/api` is added,
and the existing factory approval, preflight and journal paths are unchanged.

### Deviations from the plan, and why

| Planned | Delivered | Reason |
|---|---|---|
| Store constructed in `http.go`'s `factoryRoutes` | Constructed by `NewServer`'s FactoryAPI loop via `workingDraftSetup` | `NewServer` is that loop's caller in `http.go`; the plan limited `http.go` to lifecycle, and `factory_http.go` was another task's file |
| Extend the settings API for a conditional version | Reused the existing `(*Workspace).ProjectSettingsETag` and the settings `ETag` header | The conditional token already existed; no legacy client changed |
| One task-6 commit | Two commits (end-to-end proof, then conversion review) plus this docs commit | The Go tests and the UI review are independently verifiable and have disjoint files |
| Draft editor inside `FactoryNew.svelte` | Editor host in `WorkingDrafts.svelte`, `FactoryNew.svelte` reduced to the shared form | Task 5's list/editor split; the manual flow and `buildConfig` semantics are unchanged and pinned by `manualFactoryFlowStillWorks` |
| `Convert(id, requestID, expectedVersion, request, settingsVersion)` only | Added store-level `FailConversion` and a package-private conversion interface | The spec requires returning a draft to `editing` on a confirmed preflight refusal, which Task 1's contract list did not name |
| Task 4's nine camelCase test names; `TestWorkingDraftConvertFrozenEmptyFieldsDoNotReinherit` | Behaviour delivered under sentence-case `it()` titles; the two Go test names are used, with the typo corrected | Every named behaviour is covered by title and assertion; only the spelling differs |

### Review round

Three independent reviewers audited the finished branch — the Go bridge, the
browser client and views, and spec/wiring conformance — and every finding below
was either fixed on the branch or recorded as accepted. Fixes carry their own
regression test, and the gates in the next section were re-run afterwards.

| Severity | Finding | Resolution |
|---|---|---|
| blocker | A retried conversion request ID whose factory attempt had been refused could create an item no association could reach (`convertReserved` never rechecked the draft's state) | `convertReserved` reads the draft under the per-draft lock and refuses unless it is `converting`; `TestWorkingDraftConvertRefusedRetryCreatesNothing` |
| major | A settings write between the durable reserve and the recheck left the draft stuck `converting` until the process restarted | Nothing has been created at that point, so the reservation is released back to `editing` with the reason (`abandonReservation`), and the next conversion is reviewed against the current settings; the retried request is refused rather than dead-ended |
| major | A draft left `converting` by a crash or a lost answer only advanced at the next restart | `Convert` now resumes a reserved draft through the same path reconciliation takes (`resume`), so the next request finishes the one handoff; `TestWorkingDraftConvertResumesReservedConversion` |
| major | An `owner.lock` left by an unclean exit permanently disabled every working-draft route | The lock records its writer's PID and is reclaimed only when that process is provably gone; a live, half-written or older opaque lock is still refused (`working_draft_lock_{unix,other}.go`, both behaviours tested) |
| major | A confirmed 4xx refusal of a save wedged the pending request forever and was reported as "offline" | The save path mirrors conversion: a confirmed refusal frees the request id, surfaces the error and re-schedules, so corrected text goes out under a new id |
| major | Content typed with no project became unreachable after a reload (fresh editor key per instance, project-scoped lists) | `restore()` offers every record in the same `(installation, projectRoot)` scope, whatever editor wrote it; adoption is unchanged |
| major | `DELETE` was implemented end to end but unreachable from the UI, so tombstones and deleted-draft recovery shipped dead | The editor gains a confirmed "Remove this working draft" control driven by a controller `remove()` that freezes its request id and version, retries an unknown outcome identically, and keeps the local copy recoverable |
| minor | Concurrent creates could both answer `201` (a peek raced the append) | `Store.create` reports the replay from inside the lock, and the peek is gone |
| minor | Error text in a `503` body and in a draft's failure message could carry absolute journal paths | Journal write failures are reduced to their operation and cause; the converter's failure text strips `*os.PathError` down the same way |
| minor | Conversion records were retained forever | A record is dropped when its draft leaves `converting`, bounding the map; accepted request IDs still replay by contract |
| minor | A `409` that carries no current draft dead-ended the editor | A "Reload from the bridge" card refetches and clears the conflict once the bridge no longer has the draft |
| minor | The UI never said where plan and spec text is kept | One muted line under the editor header, as the spec requires |
| nit | Status fallback claimed "Pending — saved on this device" for `deleted` drafts | `deleted` has its own accurate line |
| nit | `non_reactive_update` on the editor's controller binding | Declared `$state`, read through `untrack` in the effect so it stays keyed on props |
| nit | Reconciliation ordering and a schema-history comment contradicted the code | Comments corrected; the conversion record is documented as part of the reservation, not a schema change |
| nit | A store test name was misspelled | Renamed to `TestWorkingDraftConvertFrozenEmptyFieldsDoNotReinherit` |

Accepted, with the reason recorded rather than fixed:

- **The production IndexedDB adapter has no dedicated test.** Every suite injects
  the in-memory adapter, which is the behavioural reference; the IndexedDB
  adapter is lazy, feature-detected and reviewed by reading. Covering it needs
  `fake-indexeddb`, a new test dependency, which is out of scope for a fix
  round. It is listed under Known limitations.
- **The conflict panel's flag group is one click, not three.** The plan listed
  native+chosen, implementer auto and the no-CI acknowledgement as separate
  atomic choices. Spec §5 is still met — every field stays individually
  editable in the combined result — and the coarser grouping is friendlier than
  three controls that always move together.
- **The `requests` replay map is unbounded.** Retaining every accepted request
  ID's result is the replay contract itself, not an oversight; bounding it is a
  retention feature, which the spec explicitly defers.

### Known limitations

- The backend store is single-user and unexpired, as specified: tombstones,
  rejected conversion attempts and superseded versions are retained until a
  separately designed retention feature exists.
- Cross-device durability covers only backend-confirmed saves. A browser that
  evicts IndexedDB loses unsent edits, and the UI says so rather than implying
  otherwise; local persistence failure is surfaced, never hidden.
- Conversion is reconciliation across two durable stores, not a transaction.
  An outcome that cannot be established stays `converting` and is reported as
  uncertain. A retried request finishes it, and a restart reconciles it first;
  the UI retries the frozen request and never mints a second one.
- A store abandoned by an unclean exit is reopened on the next start, but only
  where the platform can prove the previous owner is gone (Linux and macOS). On
  other platforms the ownership lock still has to be removed by hand.
- The browser's IndexedDB adapter has no dedicated test; the in-memory adapter
  is what the suite exercises. Covering the real adapter needs a fake IndexedDB
  dependency this change does not add.
- The manual desktop and 390px two-context walkthrough in Task 6's last step
  was not performed in this environment (no browser session was driven). The
  390px and keyboard requirements are covered only by the component tests and
  the layout conventions the new views follow, so a real-device pass is still
  owed before release.

### Acceptance gates

All gates below ran from the worktree root at the final commit; the SPA build
was run after the last UI change so the embedded assets match the source.

| Gate | Result |
|---|---|
| `go test ./web/bridge -run '^(TestWorkingDraft\|TestFactoryHTTPCreateApprovalAndReplay\|TestWebIsStdlibOnly)' -count=1` | PASS — `ok marshal/web/bridge`; every `TestWorkingDraft*`, including the two end-to-end tests and all 12 conversion tests, plus the pre-existing factory create/replay and stdlib-boundary tests |
| `go test -race ./web/bridge -run '^TestWorkingDraftConcurrentSaves$' -count=1` | PASS |
| `go test -race ./web/bridge -run '^TestWorkingDraftConvertConcurrentOneItem$' -count=1` | PASS |
| `cd web/ui && npm test` | PASS — 91 files, 1015 tests |
| `cd web/ui && npm run check` | 6 errors, all pre-existing and outside this work: `views/Chat.svelte`, `views/Run.svelte`, `lib/Sidebar.test.ts`, `lib/fleet.test.ts`, `views/Review.test.ts`, `views/Runs.test.ts`; no new error in any new or changed file |
| `cd web/ui && npm run build` | PASS — rebuilt `web/bridge/static/`; the regenerated assets are committed with this record |
| `CGO_ENABLED=1 go build ./cmd/marshal` | PASS |
| `go build ./cmd/webbridge` | PASS |
| `go vet ./...` | PASS — no output |
| `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 25m` | PASS — exit 0 under `set -o pipefail`; every package `ok`, `web/bridge` 135.7s |
| `git diff --check` | PASS — no output |
| `gofmt -l` on every changed Go file | no output |
