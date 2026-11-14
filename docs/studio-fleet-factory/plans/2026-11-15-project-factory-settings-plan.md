# Project factory settings implementation plan

**Goal:** Give each project a combined settings experience where factory workspace and role profiles follow agent defaults until independently overridden, with accurate handoff overrides and stable draft snapshots.

**Spec:** [Combined project and factory settings design](../project-factory-settings-ui-design.md), approved 2026-11-15; [existing factory defaults contract](../project-factory-defaults-spec.md).

**Base:** `89eb2463cab17ad521c7e40b42bbd3892e7b0657` on `codex/studio-worker-integration`; the approved design and this plan are uncommitted at authoring time and must accompany the factory handoff. Recheck anchors and merge-base against current `main` at execution; do not reset or overwrite parallel work.

**Non-goals:** P5 fleet inbox, attachment and viewer changes; preview/test-environment presentation; external agents and workers; planner delegation; workflow editing; changing retry resolution; making native, auto permission or the no-CI acknowledgement inheritable.

**Assumptions:** Go bridge is stdlib-only; UI is Svelte 5 / TypeScript. The factory owns its implementation branch and commits; the task commit messages below apply only where compatible with the factory's delivery policy. Tasks are sequential; none is marked parallel-safe.

## Contracts and decisions

1. Factory create resolution is `handoff > factory defaults > project agent defaults > existing normalization/validation`. The last two sources apply only to workspace, implementer profile and reviewer profile. Preserve explicit factory routes and workspace unchanged. For fallback model routing construct `{"profile": <project routing profile>}` from `ProjectSettings.Routing.profile`, not the project's other role overrides; an absent, empty, null or non-string profile contributes no fallback. Existing raw factory and handoff routing objects remain intact.
2. An explicit handoff `native: true` wins over a *project agent* workspace fallback and suppresses that fallback. A separately configured factory workspace with native remains the existing explicit conflict, with both sources in the error. An explicit handoff workspace combined with native remains invalid. When no workspace is configured anywhere the UI may choose native; the backend does not infer `native: true` from an empty project workspace. Existing project settings with no factory override now follow the project agent value for future creates; prior drafts, approvals, retry replacements, and request replays remain unchanged.
3. Existing string and number zero-value semantics remain: an empty factory field follows lower precedence; existing API cannot express an empty string or zero as an explicit override. For expected checks `nil` inherits; `[]` explicitly selects none. The no-CI acknowledgement remains per handoff. A project factory default cannot persist an empty check list with the existing `omitempty` contract.
4. Project settings writes from the new UI must be conditional. Extend existing GET/PUT with optional HTTP `ETag` / `If-Match`: GET returns a quoted SHA-256 of the JSON encoding of the current typed `ProjectSettings`; PUT with `If-Match` atomically compares it with the current value while holding `Workspace.mu` and returns 412 on mismatch. PUT without `If-Match` retains legacy behavior. Conditional comparison, map update and persistence occur under the same lock; all ordinary workspace settings writes already take that lock. Validate and retain `Fleet.PutProjectSettings`'s intake side effects and audit on successful writes; on a conflict perform neither. UI fetches a fresh ETag, sends it with a full settings record formed by spreading the fetched record, and on 412 retains edits and asks the user to reload before saving; no silent retry. The API's typed server model does not preserve fields unknown to that server; preserve all fields and nested routing members returned by the server and do not claim forward compatibility with an older binary that discards unknown fields. `ETag` is opaque to clients.
5. The create form uses a separate request type for an unresolved `config`: fields the backend treats as unset are optional; `FactoryConfig` remains the resolved response shape. Explicitly send `expectedChecks: []` for the deliberate no-checks override; omit it for inheritance. For optional strings and numeric fields use omission for follow; show their current source and effective value, and submit an explicit nonempty/nonzero value only after override selection. Keep `repoId`, `projectRoot`, `native`, `implementerAuto`, and `noRequiredCiAcknowledged` explicit. Raw routing follows the same omit-or-explicit rule. Backend response remains authoritative; show its resolved draft before approval.

Shared TypeScript contracts, added to `web/ui/src/lib/factory.ts` and referenced by Tasks 3–4:

```ts
export interface FactoryDefaults {
  targetBranch?: string
  workspace?: string
  hostId?: string
  implementerRouting?: Record<string, unknown>
  reviewerRouting?: Record<string, unknown>
  reviewerInstructions?: string
  expectedChecks?: FactoryCheckKey[]
  maxRounds?: number
  ciPollSeconds?: number
  ciWaitSeconds?: number
}
export type FactoryCreateConfig = Pick<FactoryConfig, 'repoId' | 'projectRoot' | 'native' | 'implementerAuto' | 'noRequiredCiAcknowledged'> & Partial<Omit<FactoryConfig, 'repoId' | 'projectRoot' | 'native' | 'implementerAuto' | 'noRequiredCiAcknowledged'>>
```

`FactoryCreate.config` uses `FactoryCreateConfig`; stored `FactoryItem.draft` must instead use `Omit<FactoryCreate, 'config'> & { config: FactoryConfig }`, since returned drafts are resolved. Add `factoryDefaults?: FactoryDefaults` to `ProjectSettings` in `api.ts`.

Settings HTTP client contract added in Task 3:

```ts
export interface VersionedProjectSettings { settings: ProjectSettings; etag: string }
export function getVersionedProjectSettings(root: string): Promise<VersionedProjectSettings>
export function putVersionedProjectSettings(root: string, settings: ProjectSettings, etag: string): Promise<VersionedProjectSettings>
```

Use the existing request plumbing or a narrowly scoped response-header helper; do not change the behavior of existing callers. A missing ETag is an error for this editor, not permission to make an unconditional write.

## Task 1: Resolve factory inheritance in the bridge

**Goal:** Resolve project agent workspace and role profile fallbacks on the server while preserving explicit overrides, native selection and durable snapshots.

**Files:** `web/bridge/factory_defaults.go`, `web/bridge/factory_defaults_test.go`, `web/bridge/factory_defaults_http_test.go`.

**Steps:**

1. At `factoryResolveDefaults`, read `ProjectSettingsFor(root)` once; validate non-nil `FactoryDefaults` as now, then merge factory defaults with `factoryMergeDefaults`. Apply workspace fallback only when the original handoff is not native and merged workspace is empty. Apply each absent role route from the project profile as described in Contracts 1–2. Retain the existing factory workspace/native conflict before fallback, including its error. Do not mutate settings, request replay logic, or routing JSON.
2. Add `TestFactoryAgentDefaultsFollowProject` covering changing workspace/profile between two new creates; both roles follow the new profile, and first draft stays unchanged. Add `TestFactoryExplicitOverridesAndReset` for independent implementer, reviewer and workspace precedence and removing each override. Add `TestFactoryNativeSuppressesAgentWorkspace` and preserve `TestFactoryDefaultsConflictNamesBothSources`. Add `TestFactoryAgentRoutingProfileOnly` for missing, null, malformed and extra routing fields. Add `TestFactoryReplayAfterAgentDefaultsChange` proving same request ID and payload returns original draft; existing retry replacement remains literal. Use existing fixture `newFactoryDefaultsFixture` and unit-test `factoryMergeDefaults` where appropriate.
3. Update comments in `factory_defaults.go` to describe both precedence levels and native behavior. Do not alter other field inheritance.

**Verify:** `go test ./web/bridge -run 'TestFactory(Agent|ExplicitOverrides|Native|ReplayAfter|Defaults)' -count=1` — pass. `go test ./web/bridge -run TestWebIsStdlibOnly -count=1` — pass. Commit `project-factory-settings: task 1 — resolve inheritance`.

## Task 2: Guard project settings against stale writes

**Goal:** Provide opt-in atomic conditional settings updates without changing legacy PUT clients.

**Files:** `web/bridge/workspace.go`, `web/bridge/projects.go`, `web/bridge/projects_test.go` (or the existing project settings HTTP test file discovered by the executor within this package).

**Steps:**

1. Add `func projectSettingsETag(ps ProjectSettings) (string, error)` computing a quoted lowercase SHA-256 hex digest of `json.Marshal(ps)`; add `func (w *Workspace) PutProjectSettingsIfMatch(root string, ps ProjectSettings, expected string) (bool, error)` comparing and updating under `w.mu`. Reuse the existing `w.save()` path and restore the prior in-memory settings if persistence fails. An empty expected value is not accepted by the conditional method. Keep existing unconditional `PutProjectSettings` behavior intact.
2. Refactor `Fleet.PutProjectSettings` validation and side effects minimally so the conditional route calls the same validation and performs the atomic write before intake mirroring or audit. Preserve the existing exported method and unconditional route behavior. Supply the previous stored value from the atomic operation for correct intake comparisons; extend the helper return signature above if needed **within this task only**, and keep it unexported if changed. Map mismatch to HTTP 412 without side effects. Return the ETag corresponding to the exact saved or read settings, not a later racing read. Parse a single exact `If-Match` value; reject `*`, multiple tags and malformed tags with 400.
3. Add `TestProjectSettingsETagRoundTrip`, `TestProjectSettingsConditionalConflict`, `TestProjectSettingsConcurrentConditionalWrites` (two writers with the same ETag: exactly one succeeds), `TestProjectSettingsConditionalFailureNoIntakeSideEffect`, and `TestProjectSettingsLegacyPut` (no header still works). Test persistence error rollback using the existing workspace fixture or a disposable inaccessible state path.

**Verify:** `go test ./web/bridge -run 'TestProjectSettings' -count=1` and `go test -race ./web/bridge -run 'TestProjectSettingsConcurrentConditionalWrites' -count=1` — pass. Commit `project-factory-settings: task 2 — guard settings writes`.

## Task 3: Project settings screen

**Goal:** Expose project agent and factory defaults together with explicit follow and independent override controls, preserving saved data and preventing stale writes.

**Files:** `web/ui/src/lib/api.ts`, `web/ui/src/lib/api.test.ts`, `web/ui/src/lib/factory.ts`, `web/ui/src/views/Project.svelte`, `web/ui/src/views/Project.test.ts`; optional new `web/ui/src/lib/project/FactoryDefaults.svelte` and its test for the factory-only form.

**Steps:**

1. Add the shared TypeScript contracts. Implement the versioned API helpers with actual response headers and `If-Match`; do not alter existing helpers' signatures. Add API tests for ETag read/write, missing ETag error, and 412 propagation.
2. At `Project.svelte`'s `adopt`, `saveDefaults`, and `saveIntake`, use a single versioned saved record for all project settings writes, preserving unrelated fields via spreads. A missing/failed settings read disables save. On 412 show that settings changed elsewhere, keep local edits, and offer explicit reload; on other save errors retain edits. Don't reinitialize drafts on a background fetch. Preserve complete routing objects when changing just a profile, including unknown nested members.
3. Replace the separate conceptual defaults layout with grouped workspace and model choices. Workspace has agent default plus factory `Use project default` or explicit workspace; model has agent profile plus independently inheriting/overridden implementer and reviewer. Display actual inherited values or 'Not configured', retain unavailable stored choices with an explanatory label. Use factory settings for target branch, expected checks, review instructions and max rounds; advanced controls for CI poll/wait. Validate checks in the same `check_run:name:appId` / `commit_status:context` syntax as the current create form; no project-level 'no CI' acknowledgement. Unset fields are deleted, not materialized as fallbacks. Show existing factory host value if unexpectedly unavailable rather than discarding it. Include a factory-to-project-settings link using the existing route mechanism.
4. Keep navigation and responsive labels usable at 390px and desktop widths. Warn on unsaved changes before in-app navigation and browser unload, and keep edits after failed writes. Add Project tests: `followsAgentDefaultsUntilOverridden`, `roleOverridesIndependentAndReset`, `preservesNestedRoutingAndOtherSettings`, `unavailableChoicesRemainVisible`, `failedLoadCannotSave`, `staleSaveRetainsDraft`, `factoryFieldsRoundTrip`, and `unsavedNavigationRequiresConfirmation`. Add API tests described above.

**Verify:** `cd web/ui && npm test -- src/lib/api.test.ts src/views/Project.test.ts` — pass. `npm run check` — no new errors versus recorded base; report any baseline failures by file. Commit `project-factory-settings: task 3 — edit project defaults`.

## Task 4: Handoff inheritance and override UI

**Goal:** Show source and effective settings in factory creation while submitting inheritance accurately and preserving handoff input.

**Files:** `web/ui/src/views/FactoryNew.svelte`, `web/ui/src/views/FactoryNew.test.ts`, `web/ui/src/lib/factory.ts` only if needed to finish the Task 3 create/response type separation; `web/ui/src/lib/routes.ts` only if the existing project settings navigation cannot express the link.

**Steps:**

1. At `onMount` and `payload`, stop hard-coding values that mask defaults. Load project settings and present each effective source: handoff override, factory default, project agent fallback, or built-in for numeric limits. Show missing profile/workspace clearly and retain unavailable saved values. Use local selection state separate from source values; default to follow, let users set/reset each override, and update the displayed effective values when project settings reload. Reuse the existing repository branch only as the displayed fallback when no project or handoff branch is selected; submit it explicitly if required for creation.
2. For workspace, a handoff native choice suppresses only the project-agent fallback; an explicit factory workspace paired with native shows the conflict and requires choosing a non-native environment or changing project settings. If no effective workspace exists, native may be selected explicitly. Don't silently send a workspace when native is selected. Submit `expectedChecks` omitted for inherit or `[]` for explicit none; require the no-CI acknowledgement only when effective checks are empty. Keep acknowledgements handoff-scoped. Submit configured CI timing overrides only if the user explicitly overrides; expose advanced values.
3. Preserve plan, spec and title when opening settings: open the project settings route in a separate tab/window with an accessible label, so the in-progress form remains mounted; additionally retain input after load/save errors and do not regenerate the request ID for a byte-identical retry. The browser's draft warning covers reload/close; do not claim durable cross-reload persistence. Draft preview shows the server-returned resolved settings and their complete expected checks before approval.
4. Add `FactoryNew.test.ts` cases: `showsInheritedSources`, `omitsFollowingFields`, `independentRoleOverrideAndReset`, `nativeIgnoresAgentWorkspace`, `nativeConflictsWithFactoryWorkspace`, `distinguishesInheritedAndExplicitEmptyChecks`, `usesResolvedServerDraft`, `preservesInputOnErrors`, and `reusesRequestIdOnIdenticalRetry`. Use fixture responses with actual API shapes.

**Verify:** `cd web/ui && npm test -- src/views/FactoryNew.test.ts` — pass. `npm run check` — no new errors versus base. Commit `project-factory-settings: task 4 — expose handoff overrides`.

## Task 5: End-to-end verification and documentation

**Goal:** Verify project defaults, UI handoff and factory snapshots work together, and record shipped behavior.

**Files:** `web/bridge/factory_defaults_http_test.go`, `docs/studio-fleet-factory/project-factory-settings-ui-design.md`, `docs/studio-fleet-factory/project-factory-defaults-spec.md`, `docs/studio-fleet-factory/README.md`, this plan. Modify other tests only if necessary to preserve existing test contracts and name them in the task report.

**Steps:**

1. Add an HTTP integration test using the existing factory fixture: set project agent defaults, create a draft with omitted following fields, change defaults, create another, and assert both drafts' actual snapshots differ as expected. Exercise explicit role override, native choice and explicit empty checks plus acknowledgement through the served API.
2. Record implementation status and the chosen native conflict, inheritance and conditional-write semantics in the design and earlier defaults spec. Update the index to identify the completed slice; do not rewrite historical claims as if they were current. Capture the exact accepted base, CI/test results and any known baseline failures in this plan.
3. Run the final checks below and perform desktop and 390px browser checks of project settings, reset-to-follow, handoff review, stale-save warning and settings navigation. Verify no horizontal overflow, readable labels and retained form input. The SPA build is needed before testing the embedded bridge; `npm run build` writes `web/bridge/static/`, which must be included in the final commit only if the repository tracks those generated assets. Do not touch the parallel preview branch.

**Verify:** `go test ./web/bridge -run 'TestFactoryDefaults|TestFactoryAgent|TestProjectSettings|TestWebIsStdlibOnly' -count=1`; `cd web/ui && npm test && npm run check && npm run build`; `CGO_ENABLED=1 go build ./cmd/marshal`; `go test ./...`; `go vet ./...`; `git diff --check`; `gofmt -l` on changed Go files (empty output). Record existing baseline failures rather than labeling them passing. Browser checks must observe the real UI and saved/reloaded behavior, with no external credentials. Commit `project-factory-settings: task 5 — verify and document`.

## Integration notes

- The review should compare the code's actual settings precedence and conflict behavior to this plan, and confirm whole-record saves cannot overwrite concurrent changes from either this screen or a legacy writer when the new screen sends its ETag.
- CI, permissions, worktree and daemon lifecycle are unchanged. The project setting affects future creation only; verify retries, approvals and existing drafts do not re-resolve.
- P5 plan 1/2 is already represented on this base; check the active `p5-3-fleet-screen` diff at execution and avoid its changed files or reconcile deliberately. Check the currently running preview implementation branch independently before touching PR-view code.
- Factory handoff must include the approved design and this plan as durable documents. The source planning branch's uncommitted artifacts must not be lost when seeding the factory's checkout.
