# Project factory defaults — implementation plan

Date: 2026-11-13
Status: approved for autonomous execution by the user's implementation request.
Base: `1432a477` (the reviewed gap-2 specification).
Specification: [project-factory-defaults-spec.md](../project-factory-defaults-spec.md).

**Goal:** let a project record the factory configuration its work items normally
use, so a handoff inherits every field it does not set itself and overrides
every field it does — without changing the shipped handoff contract, the
bootstrap factory's acceptance contract, or the workspace file format.

## Scope and boundaries

This slice adds:

- a `FactoryDefaults` record on `ProjectSettings`;
- a pure merge that applies a project's defaults to a handoff's configuration;
- write-time and resolve-time validation of the defaults;
- resolution in the create path, before normalisation and preflight;
- the resolved configuration snapshotted into the work item's draft.

It does not add a route, change `FactoryConfig`, change `factoryNormalize`,
change the factory loop, its CI evidence or its review/repair semantics, add an
SPA surface, or implement adoption of independent work.

All new Go code stays under `web/bridge/` and imports only the standard
library. `web/ui/` is untouched.

## Reconciled implementation decisions

The two Minimax planning briefs agreed on the type, the merge semantics, the
validation subset, the create-path ordering and the test surface. The points
below resolve their differences and record the discrepancies they found
between the specification and the real code.

1. **The type is the rule.** `FactoryDefaults` carries exactly the ten
   inheritable fields. `RepoID`, `ProjectRoot`, `Native`, `ImplementerAuto` and
   `NoRequiredCIAcknowledged` are absent from it, so they are structurally
   uninheritable rather than merely excluded at runtime.
2. **Unset is per type.** `string` unset is `""`; `int` unset is `0`;
   `json.RawMessage` unset is nil or zero length; `[]FactoryCheckKey` unset is
   `nil`. A non-nil empty check list is an explicit choice and is preserved.
   A zero-length `json.RawMessage` is treated as unset, matching the existing
   `len(route) == 0` check in the factory preflight.
3. **Resolution runs before normalisation.** `factoryNormalize` fills the
   built-in limits (five rounds, 15 s CI poll, 1800 s CI wait) into unset
   fields, so resolving afterwards would make a project default for those
   limits unreachable.
4. **The validator never fills anything.** It checks the defaults; it does not
   apply the built-in defaults to them, because a zero limit in a default means
   "unset" and must stay unset for a handoff to inherit.
5. **The create-replay identity is the operator's request.** `FactoryStore.Create`
   hashes the marshalled request to recognise a replay. If the resolved request
   were hashed, a client retrying an unchanged POST after the project's defaults
   had moved on would get 409 instead of the original item. The store
   therefore keeps identity and content apart: the hash is computed over the
   request the operator sent, and the item is built from the resolved
   configuration.
6. **The named project root is the only live resolution path.** A valid
   handoff always names a registered, trusted `ProjectRoot`, and the named
   root wins over the repository's intake project. The intake mapping is a
   defensive fallback that only chooses which defaults are consulted; it never
   supplies a root and never creates a work item.
7. **The retry path is un-resolved by construction.** Resolution is introduced
   at exactly one call site, the create path, so there is no flag to get wrong.
8. **Status codes.** A default rejected when it is written is a bad request
   (400, through the existing `errInvalidProjectSettings`). A default that is
   invalid when it is resolved is an unsupported stored configuration (422,
   through the existing `errFactoryConfig`). A conflict between a default and a
   handoff is a bad request (400, through `errFactoryInput`).
9. **The only cross-source conflict is workspace against native.** `Native` is
   the only non-inheritable field that can collide with an inheritable one. A
   handoff that sets both `native` and `workspace` itself is a handoff-internal
   conflict and stays with the unchanged `factoryNormalize`.
10. **The defaults are validated on their own, always.** A default that is
    invalid on its own is rejected even when the handoff would have overridden
    the offending field. That is the "not a silent fallback" rule.

### Discrepancies the planners found, and how they are resolved

| Discrepancy | Resolution |
|---|---|
| The specification's §5 sketch showed a routing shape the preflight rejects (`{"preset": …}`) | The sketch is corrected to `{"profile": …}`; the preflight requires a routing profile |
| The sketch showed the item's fields at the top level of the create response | The sketch is corrected to the real `{"item": {…}}` envelope, with 201 on create and 200 on replay |
| The sketch showed empty optional settings fields | The sketch is corrected: `omitempty` fields are absent, not empty |
| `omitempty` on `FactoryDefaults.ExpectedChecks` means a default cannot express "no checks" on its own | Recorded as a known limitation. The handoff's explicitly empty list — the case the acceptance criteria cover — is preserved |
| The specification never mentioned the create-replay identity | The specification now states that the identity is the operator's request and the resolved configuration is content |
| `ProjectSettings.Workspace` and `FactoryDefaults.Workspace` are different things under one name | The specification now says so |
| An older bridge binary silently drops the new field on its next save | The specification now records it as a reason the resolve-time check exists |

## HTTP and JSON contract

No new route. The defaults ride the existing project-settings surface.

```
PUT /api/projects/settings?root=/path/to/project
  -> 200 {
       "factoryDefaults": {
         "targetBranch": "main",
         "workspace": "ws://published/example",
         "implementerRouting": {"profile": "frontier"},
         "reviewerRouting": {"profile": "frontier"},
         "reviewerInstructions": "be terse",
         "expectedChecks": [{"kind": "check_run", "name": "build", "appId": 7}],
         "maxRounds": 5,
         "ciPollSeconds": 15,
         "ciWaitSeconds": 1800
       },
       "intake": {"repoId": "repo-1"}
     }

POST /api/factory/items
  -> 201 {
       "item": {
         "id": "item-1",
         "draft": {
           "title": "Add the thing",
           "config": {
             "repoId": "repo-1",
             "projectRoot": "/path/to/project",
             "targetBranch": "main",
             "workspace": "ws://published/example",
             "implementerRouting": {"profile": "frontier"},
             "maxRounds": 5
           }
         }
       }
     }
```

The handoff in that request set only `repoId`, `projectRoot`, `title` and the
two routings; the rest came from the project's defaults, and the stored draft
shows the resolved values.

## Shared Go types

Task 1 owns these names verbatim. Later tasks consume them without renaming.

```go
// FactoryDefaults are a project's default factory configuration. A handoff
// inherits every field it leaves unset.
//
// The type is the rule: a field that is not here can never be inherited.
// RepoID and ProjectRoot are absent because they say what the work item is
// about and which project it belongs to. Native, ImplementerAuto and
// NoRequiredCIAcknowledged are absent because their false is a meaningful
// choice, and the handoff's plain FactoryConfig cannot tell "unset" from
// "explicitly false".
type FactoryDefaults struct {
	TargetBranch         string            `json:"targetBranch,omitempty"`
	Workspace            string            `json:"workspace,omitempty"`
	HostID               string            `json:"hostId,omitempty"`
	ImplementerRouting   json.RawMessage   `json:"implementerRouting,omitempty"`
	ReviewerRouting      json.RawMessage   `json:"reviewerRouting,omitempty"`
	ReviewerInstructions string            `json:"reviewerInstructions,omitempty"`
	ExpectedChecks       []FactoryCheckKey `json:"expectedChecks,omitempty"`
	MaxRounds            int               `json:"maxRounds,omitempty"`
	CIPollSeconds        int               `json:"ciPollSeconds,omitempty"`
	CIWaitSeconds        int               `json:"ciWaitSeconds,omitempty"`
}
```

`ProjectSettings` gains one optional field, after `Automations`:

```go
	// FactoryDefaults are the project's default factory configuration. A
	// handoff inherits every field it leaves unset. It is optional, so the
	// workspace file format does not change and workspaceVersion stays 11 —
	// the same reasoning the Automations field already follows.
	FactoryDefaults *FactoryDefaults `json:"factoryDefaults,omitempty"`
```

## File and dependency map

All paths are relative to the repository root.

| Task | Files | Depends on |
|---|---|---|
| 1 | `web/bridge/factory_types.go`, `web/bridge/workspace.go`, `web/bridge/factory_defaults.go` (new), `web/bridge/factory_defaults_test.go` (new) | — |
| 2 | `web/bridge/projects.go`, `web/bridge/projects_test.go` | 1 |
| 3 | `web/bridge/factory_http.go`, `web/bridge/factory_store.go`, `web/bridge/factory_defaults_http_test.go` (new) | 1, 2 |
| 4 | `web/bridge/factory_defaults.go`, `web/bridge/factory_http.go`, `web/bridge/factory_defaults_test.go`, `web/bridge/factory_defaults_http_test.go` | 3 |
| 5 | `web/bridge/factory_defaults_test.go`, `web/bridge/factory_defaults_http_test.go` | 4 |
| 6 | `docs/studio-fleet-factory/project-factory-defaults-spec.md`, this plan | 1–5 |

The implementation tasks are sequential: they compile as one Go package and
share the frozen contract. Do not run two implementers against `web/bridge` at
the same time.

## Task 1: The defaults type, the merge, and the validator

**Files:** modify `web/bridge/factory_types.go` and `web/bridge/workspace.go`;
create `web/bridge/factory_defaults.go` and `web/bridge/factory_defaults_test.go`.

- [ ] Write `TestFactoryDefaultsApplyPerTypeUnsetRules`: a table over every
  inheritable field and every type. A string default applies to an empty
  handoff string and loses to a set one; an int default applies to a zero
  handoff int and loses to a set one; a `json.RawMessage` default applies to
  a nil handoff value and loses to `{}` and to `null` (compare the raw bytes,
  not a decoded struct); a check-list default applies to a nil handoff list and
  loses to a non-nil empty one. Assert nil-ness, not only length.
- [ ] Write `TestFactoryDefaultsNeverSupplyNonInheritableFields`: assert by
  reflection that `FactoryDefaults` has no `RepoID`, `ProjectRoot`, `Native`,
  `ImplementerAuto` or `NoRequiredCIAcknowledged` field, and behaviourally that
  a merge leaves those five exactly as the handoff set them.
- [ ] Write `TestFactoryValidateDefaults`: nil defaults are valid; a negative
  `maxRounds`, `ciPollSeconds` or `ciWaitSeconds` is rejected naming the
  field; a `hostId` other than `local` is rejected; a check identity with no
  name, an unknown kind, a `check_run` without an app id, or a
  `commit_status` with one is rejected; a valid default passes. The validator
  must not fill the built-in defaults — a zero limit stays zero.
- [ ] Run
  `go test ./web/bridge -run '^TestFactoryDefaults' -count=1 -v`; confirm the
  new tests fail because the contract does not exist.
- [ ] Implement the shared types above, plus in `factory_defaults.go`:
  - `func factoryMergeDefaults(c FactoryConfig, d *FactoryDefaults) FactoryConfig`
    — the merge, one branch per inheritable field, copying the check list
    rather than aliasing it, and never collapsing a non-nil empty list to nil;
  - `func factoryValidateDefaults(d *FactoryDefaults) error` — the validation
    subset, in a fixed order so the error is deterministic, reusing the same
    check-identity predicate `factoryNormalize` uses.
- [ ] Run `go test ./web/bridge -run '^TestFactoryDefaults' -count=1 -v` and
  `gofmt -l` on the four task files; require PASS and no output.
- [ ] Commit only this task: `feat(factory): add project factory defaults type and resolver`.

## Task 2: Validate the defaults when they are written

**Files:** modify `web/bridge/projects.go` and `web/bridge/projects_test.go`.

- [ ] Write `TestProjectSettingsRoundTripFactoryDefaults`: a `factoryDefaults`
  block survives `PUT` → `GET` through the existing project-settings surface,
  and the workspace file still reports `"version": 11`.
- [ ] Write `TestFactoryDefaultsRejectInvalidDefaultsAtWriteTime`: through
  `PUT /api/projects/settings?root=`, a negative limit, a non-`local` host id
  and each invalid check identity are rejected with 400 and an error naming
  the offending field; a zero limit, a `local` host id, an explicitly empty
  check list and a nil check list are accepted; a rejected write leaves the
  previously stored settings untouched.
- [ ] Run
  `go test ./web/bridge -run '^(TestProjectSettingsRoundTripFactoryDefaults|TestFactoryDefaultsRejectInvalidDefaultsAtWriteTime)' -count=1 -v`;
  confirm the new tests fail.
- [ ] Implement: in `Fleet.PutProjectSettings`, after the existing automations
  validation, call `factoryValidateDefaults(ps.FactoryDefaults)` and wrap a
  failure in `errInvalidProjectSettings` so the status stays 400 and the
  message names the field. The raw `Workspace.PutProjectSettings` stays a dumb
  store, exactly as it is today for `Automations`.
- [ ] Run the named tests; require PASS. Run `gofmt -l` on the two task
  files; require no output.
- [ ] Commit only this task: `feat(factory): validate project factory defaults on write`.

## Task 3: Resolve the defaults in the create path

**Files:** modify `web/bridge/factory_http.go` and `web/bridge/factory_store.go`;
create `web/bridge/factory_defaults_http_test.go`.

- [ ] Write `TestFactoryCreateResolvesProjectDefaultsIntoStoredDraft`: a
  project with defaults creates a factory whose stored draft carries the
  default for every field the handoff left unset.
- [ ] Write `TestFactoryCreateHandoffOverridesDefault`: the handoff's own value
  wins for the field it sets.
- [ ] Write `TestFactoryCreatePreservesExplicitlyEmptyChecks`: the default has
  one check, the handoff sends an explicitly empty list, and the stored draft
  has an empty, non-nil list.
- [ ] Write `TestFactoryDefaultsResolveBeforeNormalisation`: a project default
  of `maxRounds: 9` reaches the stored draft as 9, not the built-in 5. This is
  the ordering proof.
- [ ] Write `TestFactoryDefaultsResolveFromNamedProjectRootNotIntake`: with two
  registered projects whose defaults differ in every observable field, a
  handoff naming the second project gets the second project's defaults, and a
  handoff naming the first gets the first's.
- [ ] Write `TestFactoryDefaultsRootlessHandoffIsStillRejected`: a handoff with
  no `ProjectRoot` is rejected and no work item is created.
- [ ] Run
  `go test ./web/bridge -run '^TestFactory(Create|Defaults)' -count=1 -v`;
  confirm the new tests fail.
- [ ] Implement:
  - in `factory_defaults.go`, `func (a *factoryAPI) factoryResolveDefaults(req FactoryCreate) (FactoryCreate, error)`
    — locate the project (named root first, intake mapping only as a defensive
    fallback that never writes a root), read its defaults, validate them at
    resolve time and report a failure naming the project's defaults as the
    source, then merge;
  - in `factoryAPI.Create`, resolve **before** `factoryNormalize`, and
    preflight the resolved configuration;
  - in `factory_store.go`, split `Create` into `Create` (identity and content
    both the request) and `CreateResolved(req, resolved, now)` (identity the
    request, content the resolved configuration), so the create-replay hash
    stays over the operator's request.
- [ ] Run the named tests; require PASS. Run `gofmt -l` on the three task
  files; require no output.
- [ ] Commit only this task: `feat(factory): resolve project factory defaults on handoff create`.

## Task 4: Name the sources of conflicts and keep retries un-resolved

**Files:** modify `web/bridge/factory_defaults.go`,
`web/bridge/factory_http.go`, `web/bridge/factory_defaults_test.go` and
`web/bridge/factory_defaults_http_test.go`.

- [ ] Write `TestFactoryDefaultsConflictNamesBothSources`: a project default
  that carries a workspace and a handoff that sets `native: true` are
  rejected with an error naming the conflict, both field names, the project
  root and the handoff.
- [ ] Write `TestFactoryDefaultsRejectInvalidDefaultsAtResolveTime`: a
  default that reached the workspace file another way is rejected when it is
  resolved, with an error naming the project's defaults as the source — and
  it is still rejected when the handoff would have overridden the offending
  field.
- [ ] Write `TestFactoryRetryReplacementIsNotReResolved`: a retry replacement
  that omits a field the project default sets keeps that field empty in the
  new approval. The replacement must be driven through the store's public
  mutation path so a decision exists; no forge, git or loop is involved.
- [ ] Run
  `go test ./web/bridge -run '^TestFactory(Defaults|Retry)' -count=1 -v`;
  confirm the new tests fail.
- [ ] Implement: detect the cross-source conflict during resolution and report
  it with both sources; report a resolve-time validation failure with the
  project's defaults as the source; leave the retry path untouched.
- [ ] Run the named tests; require PASS. Run `gofmt -l` on the four task
  files; require no output.
- [ ] Commit only this task: `feat(factory): name conflict sources and keep retry replacements un-resolved`.

## Task 5: Pin the untouched contract

**Files:** modify `web/bridge/factory_defaults_test.go` and
`web/bridge/factory_defaults_http_test.go`.

- [ ] Write `TestProjectWithoutDefaultsBehavesExactlyAsToday`: for a project
  with no settings record, and again for one whose `FactoryDefaults` is nil,
  the stored draft's configuration equals the pre-slice expected values field
  by field, including the built-in `local`, 5, 15 and 1800. A zero-valued
  defaults object must behave exactly like no defaults.
- [ ] Write `TestFactoryBootstrapAcceptanceContractStillHolds`: each element
  of the specification's §4.8 list, run twice — once against a defaults-free
  project and once against a project with a full set of defaults: the required
  request id, title, plan and repository; the plan and specification size
  limit; the local-host rule; the built-in limits; a project default setting
  the limits explicitly; and the rule that a handoff must select expected CI
  checks or acknowledge that none are required.
- [ ] Run `go test ./web/bridge -run '^(TestProjectWithoutDefaults|TestFactoryBootstrapAcceptance)' -count=1 -v`;
  confirm the new tests fail or pass as appropriate, then make them pass.
- [ ] Run `go test ./web/bridge -run 'Factory' -count=1`; the pre-existing
  factory tests must pass **unmodified** — they are the executable form of
  "the bootstrap factory's acceptance contract still holds".
- [ ] Run `gofmt -l` on the two task files; require no output.
- [ ] Commit only this task: `test(factory): pin the bootstrap acceptance contract under project defaults`.

## Task 6: Finalize the contract documentation and prove acceptance

**Files:** modify `docs/studio-fleet-factory/project-factory-defaults-spec.md`
and this plan.

- [ ] Add an "Implementation record" section to the specification: the branch,
  the task commits, the acceptance gates and the open questions from §8 that
  remain open.
- [ ] Record the plan's outcome here: the task/commit table and the gates
  actually run.
- [ ] Run the complete feature suite:
  `go test ./web/bridge -run 'FactoryDefaults|ProjectWithoutDefaults|FactoryCreate|FactoryRetry|FactoryBootstrapAcceptance|ProjectSettingsRoundTripFactoryDefaults|WorkspaceRoundTripsFactoryDefaults' -count=1 -v`.
- [ ] Run the pre-existing contract gate: `go test ./web/bridge -run 'Factory' -count=1`.
- [ ] Run the package gate: `go test ./web/bridge -count=1`.
- [ ] Run the boundary and build gates:
  `go test ./web/bridge -run '^TestWebIsStdlibOnly$' -count=1`,
  `gofmt -l web/bridge`, `go vet ./web/bridge/...`, and
  `CGO_ENABLED=1 go build ./...`.
- [ ] Run the repository suite: `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 20m`.
  Record unrelated baseline failures separately; do not claim an overall pass
  when any required gate remains red.
- [ ] Commit docs and any test-first defect fixes:
  `docs(factory): record implemented project factory defaults`.

## Acceptance matrix

| Specification criterion | Owning tests |
|---|---|
| A project with defaults creates a factory whose stored draft carries the default for every unset field | Task 3 `TestFactoryCreateResolvesProjectDefaultsIntoStoredDraft` |
| A handoff that sets a field explicitly wins over the project's default | Task 1 `TestFactoryDefaultsApplyPerTypeUnsetRules`; Task 3 `TestFactoryCreateHandoffOverridesDefault` |
| `RepoID`, `ProjectRoot`, `Native`, `ImplementerAuto` and `NoRequiredCIAcknowledged` are never taken from the defaults | Task 1 `TestFactoryDefaultsNeverSupplyNonInheritableFields` |
| An explicitly empty `expectedChecks` is preserved rather than replaced | Task 1 `TestFactoryDefaultsApplyPerTypeUnsetRules`; Task 3 `TestFactoryCreatePreservesExplicitlyEmptyChecks` |
| Changing a project's defaults after a work item exists does not change that work item's stored or approved configuration | Task 3 `TestFactoryCreateStoredDraftIsSnapshotted` |
| A project default that conflicts with the handoff is rejected with an error naming both sources | Task 4 `TestFactoryDefaultsConflictNamesBothSources` |
| A project default that is invalid on its own is rejected when it is written, and again when it is resolved | Task 2 `TestFactoryDefaultsRejectInvalidDefaultsAtWriteTime`; Task 4 `TestFactoryDefaultsRejectInvalidDefaultsAtResolveTime` |
| The defaults applied are the ones of the project named by `Config.ProjectRoot` | Task 3 `TestFactoryDefaultsResolveFromNamedProjectRootNotIntake` |
| A `retry` replacement is taken as written and is not re-resolved | Task 4 `TestFactoryRetryReplacementIsNotReResolved` |
| A project with no defaults produces exactly the behaviour it produces today | Task 5 `TestProjectWithoutDefaultsBehavesExactlyAsToday` |
| The defaults are read and written through the existing project-settings surface, with no new route | Task 2 `TestProjectSettingsRoundTripFactoryDefaults` |
| The bootstrap factory's existing acceptance contract still holds | Task 5 `TestFactoryBootstrapAcceptanceContractStillHolds`; the unmodified pre-existing factory tests |

Every test is offline and deterministic: no live forge, no real git
repository, no container runtime, no model provider, and no developer
credentials. The registered repository uses the RFC 2606 reserved
`https://example.invalid/...` URL, which the preflight only constructs a
client for and never calls. No test may call `t.Parallel()`, because the trust
fixture uses `t.Setenv`.

## Execution, commits, and PR policy

- Execute Tasks 1–6 sequentially with one DeepSeek
  `deepseek-v4.1-flash:1017` implementer per task through the `ollama-cloud`
  provider.
- Each implementer follows test-driven development: add the named failing
  test, capture the expected failure, implement the smallest owning change,
  run the named verification, and commit only that task's files.
- If the `ollama-cloud` provider is unavailable or materially slow, retry the
  same scoped task with `openrouter/deepseek-v4.1-flash:1017` or
  `opencode-go/deepseek-v4.1-flash:1017`; do not split a task across models.
- The specification/plan PR is the base of the stack. The implementation PR
  targets that base branch and contains Tasks 1–6.
- Every PR is created as draft. Before it can become ready, dispatch a
  `glm-5.3-flash` reviewer through `ollama-cloud` (fallback:
  `openrouter/glm-5.3-flash` or `opencode-go/glm-5.3-flash`) to review the
  branch against its base. Address every substantive finding, rerun the
  affected gates, and have the reviewer verify the fixes before changing the
  PR from draft to ready.
- Do not merge automatically. The user retains the merge decision.

## Plan self-review

- Coverage: the specification's §4 and §10 map to Tasks 1–5; §9 (adoption)
  stays deferred; §11's non-goals are respected.
- Contract consistency: the type, the merge semantics, the validation subset
  and the create-path ordering are fixed in Task 1 and consumed unchanged.
- Safety: no route, no `FactoryConfig` change, no `factoryNormalize` change,
  no workspace version bump, no SPA change, no change to the factory loop.
- Testability: every acceptance criterion has an offline deterministic test;
  the pre-existing factory tests are the regression net for the untouched
  contract.
- Collision control: all implementation tasks are sequential because they
  compile as one Go package and share the frozen contract.
