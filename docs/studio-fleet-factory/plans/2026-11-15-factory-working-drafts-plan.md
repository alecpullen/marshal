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
