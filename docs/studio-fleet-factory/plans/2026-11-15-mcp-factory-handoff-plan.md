# MCP factory handoff implementation plan

**Goal:** Expose project-scoped, permissioned factory proposal, review, approval/queue and question-answering tools to MCP clients, using the same durable factory state as Studio.

**Spec:** [MCP factory handoff specification](../mcp-factory-handoff-spec.md), derived from the approved [product design](../mcp-factory-handoff-design.md). The user approved this implementation plan on 2026-11-15.

**Base:** authored at `0dc4967ff1db081122f89dadc2a40fccfe11804d` with the three MCP planning documents uncommitted. Start from the latest `main` and recheck current P5, Studio Claude planner, factory-defaults and preview branches. Never overwrite parallel work; include the approved spec and this plan in the factory handoff.

**Non-goals:** External file/edit transfer, MCP permission administration, merge/push/delete/shell tools, factory stop/retry controls, implementing OAuth, waking an external agent, or modifying the production Claude ACP backend. MCP approval is an existing approval-and-queue operation, not proof of work completion.

**Global constraints:** `web/` remains Go standard library only. Existing clients receive no new factory rights. Retain the bridge's existing client bearer-token identity and repository allowlist, legacy fleet tools, factory CI/review/ownership gates, `FactoryPlanHash` and replay compatibility. Do not use real credentials or services in automated tests. Execute tasks sequentially; no task is parallel-safe by default.

## Shared contracts

All new types below live under `web/bridge`; names may be unexported where not used outside the package, but cross-task signatures are fixed.

```go
// client.go; persisted as an additive, optional field on MCPClient.
type MCPFactoryProjectGrant struct {
    ProjectID string `json:"projectId"`
    Read bool `json:"read"`
    CreateDraft bool `json:"createDraft"`
    EditDraft bool `json:"editDraft"`
    AnswerQuestion bool `json:"answerQuestion"`
    ApproveAndQueue bool `json:"approveAndQueue"`
    ApprovalAuthority string `json:"approvalAuthority"` // none, user_directed, standing
}
// client field: FactoryGrants []MCPFactoryProjectGrant `json:"factoryGrants,omitempty"`
// Project identity record in the Workspace persistence model:
type MCPProjectIdentity struct {
    ID string `json:"id"`             // 128-bit random hex, assigned on registration
    Root string `json:"root"`         // server-only canonical registered root
}
// Grant management response; Version is a SHA-256 of the canonical grant
// JSON under the policy lock. It is opaque to the UI.
type FactoryGrantState struct {
    ClientID string `json:"clientId"`
    ProjectID string `json:"projectId"`
    Grant MCPFactoryProjectGrant `json:"grant"`
    Version string `json:"version"`
}
// factory_types.go; added to FactoryEvent as an optional field. Older records
// without it remain valid and retain byte-compatible approval hashes.
type FactoryProvenance struct {
    ClientID string `json:"clientId"`
    ProjectID string `json:"projectId"`
    Action string `json:"action"`
    RequestID string `json:"requestId"`
    Authority string `json:"authority,omitempty"`
    DecisionID string `json:"decisionId,omitempty"`
}
```

Project IDs are generated and durably saved for every registered project on first upgrade/startup under the workspace lock. An existing project keeps its ID across restarts and equivalent canonical paths. Removing a registration deletes the identity and all its grants in the same workspace save; re-registering creates a fresh ID. A persistence failure does not expose an unsaved ID. No project ID maps to a browser-provided path. Persist a project's `plannerDelegationEnabled` bool and `designatedMCPClientId` string in its existing settings with false/empty defaults. Removing or revoking the client makes it ineligible without altering past history.

Management endpoints, protected by the existing Studio API authentication, are `GET /api/clients/{clientId}/factory-grants/{projectId}` and `PUT` at the same path. GET returns `FactoryGrantState`; PUT takes `{grant,version}` and atomically rejects stale `version` with 409, returning the current version only to the authorized Studio operator. Add an optional `mcpProjectId` field to each entry of the existing Studio `GET /api/projects` response, retaining existing fields and consumers; it contains the stable opaque ID from Task 1. Save uses workspace persistence; validate both identities, known enum values and matching project ID. Return a zero grant and stable version for a valid client/project pair with no stored grant. Deleting/revoking a client and changing the project's delegation fields share the policy lock. The UI edits one project/client pair and never sees a token hash.

An instance-scoped `factoryMCPAuthorizer` owns a mutex that serializes grant updates, client deletion and authorization plus durable factory mutations. The lock order is policy -> workspace -> factory store; no callback reenters policy, and no forge/network/model I/O under the policy mutex. Implement:

```go
func (a *factoryMCPAuthorizer) WithAuthorizedMutation(clientID, projectID, repoID, capability string, fn func() (FactoryItem, error)) (FactoryItem, error)
func (a *factoryMCPAuthorizer) AuthorizeRead(clientID, projectID, repoID string) error
```

`WithAuthorizedMutation` reloads the client and grant while locked, checks repoAllowed, calls `fn` only for local validation and the journal append, and releases before scheduler wake or external work. All management grant changes and client deletion take the same mutex. If existing `factoryAPI.Create` preflight performs external work, split it into preflight outside the lock and a final authority, revision and project/repo identity recheck inside; never invoke the current networked preflight while holding the mutex. The final journal commit is the linearization point. Factory request IDs are mapped to `mcp:` + lowercase SHA-256 of `clientID + NUL + originalRequestID` before existing store operations; the original ID is kept only in provenance. Hash identity binds full normalized operation and payload, including authority, as existing create/update replay logic does. Reauthorize before both new operations and replay.

MCP tool schemas and exact semantics are the spec §3. New factory calls return `{"content":[{"type":"text","text":"<JSON object>"}],"isError":false}`; domain errors use `isError:true` and a JSON object with `code` and `message`. Unauthorized or nonexistent indirect IDs use identical `not_found`. Strip host-root paths, token hashes and secret fields from all projected responses, including nested `FactoryConfig`, provenance and errors. Preserve existing fleet tools' observable results. Raise MCP body envelope from 1 MiB to 2 MiB, matching `factoryRequestLimit`; still enforce `factoryImportLimit` on combined plan/spec and reject oversized/trailing/unknown inputs.

User-directed approval/answer is a client assertion, recorded as `user_directed`, not verified human approval. Standing approval requires both `approveAndQueue` and grant authority `standing`; user-directed approval is allowed under `user_directed` or `standing`. Delegated answer requires `answerQuestion`, project toggle true and matching designated client ID; user-directed answer requires only `answerQuestion`. Invalid/missing authority denies without a journal mutation. Explicitly distinguish these cases in the Studio history.

## Task 1: Project identity and client grant persistence

**Goal:** Persist explicit client/project grants and stable registered project IDs without changing legacy MCP client rights.

**Files:** `web/bridge/client.go`, `web/bridge/workspace.go`, `web/bridge/workspace_test.go`, `web/bridge/client_test.go`.

**Steps:** Add the contracts above. Add a server-only project identity collection to the workspace file, mapping canonical registered project roots to random IDs; atomically populate missing identities for existing registrations and remove the identity and grants on project removal. Keep old files readable, preserve all existing fields and never log token hashes. Add workspace methods for lookup by ID and copying/updating one grant with a conditional version; don't return live mutable slices. Add persisted `plannerDelegationEnabled` and `designatedMCPClientId` to `ProjectSettings` using existing conditional settings behavior. Test `TestMCPFactoryGrantLegacyDefaultsDeny`, `TestMCPProjectIdentityStableAcrossRestart`, `TestMCPProjectRemoveRevokesAndReregisterGetsNewID`, `TestMCPFactoryGrantConditionalConflict`, `TestMCPFactoryGrantRoundTripPreservesClientTokenAndFleetPolicy`, `TestMCPFactoryDelegationDefaultsOff`. Reuse isolated temp workspaces and `testenv` patterns.

**Verify:** `go test ./web/bridge -run '^(TestMCPFactoryGrant|TestMCPProject|TestMCPFactoryDelegation)' -count=1` passes. Commit `mcp-factory: task 1 — identity and grants`.

## Task 2: Linearized authorization and management APIs

**Goal:** Let the Studio operator grant and revoke factory capabilities while preventing post-revocation mutations.

**Files:** Create `web/bridge/mcp_factory_auth.go`, `web/bridge/mcp_factory_auth_test.go`; modify `web/bridge/http.go`, `web/bridge/client.go`, `web/bridge/workspace.go`, `web/bridge/projects.go` only for the contracts and registration.

**Steps:** Implement `factoryMCPAuthorizer` and both methods from Contracts, the management GET/PUT and shared policy lock for client deletion, project removal and delegation-setting writes. Add `mcpProjectId` to the Studio project-list response as specified in Contracts. Reload the client/grant and, for delegated answers, project delegation settings on each call; check canonical project mapping and `repoAllowed`. Do not expose management APIs via `/mcp`. No-lock validation may run first; recheck identities and authority under lock before the journal append. Write `TestMCPFactoryAuthorizeCapabilities`, `TestMCPFactoryUnauthorizedIDDoesNotLeak`, `TestMCPFactoryGrantRevokeRacesMutation` (channel-controlled barrier, not sleeps), `TestMCPFactoryDelegationRevokeRacesAnswer`, `TestMCPFactoryDeletedClientDeniesReplay`, `TestMCPFactoryGrantManagementConflict`, `TestMCPFactoryGrantExistingFleetAutonomyDoesNotGrantAccess`. Include persistence failure and unsupported authority checks.

**Verify:** `go test ./web/bridge -run '^(TestMCPFactoryAuthorize|TestMCPFactoryUnauthorized|TestMCPFactoryGrant|TestMCPFactoryDelegation|TestMCPFactoryDeleted)' -count=1` and `go test -race ./web/bridge -run '^TestMCPFactoryGrantRevokeRacesMutation$' -count=1` pass. Commit `mcp-factory: task 2 — authorize project grants`.

## Task 3: Atomic provenance and draft replacement

**Goal:** Add a versioned draft-replacement operation and journal MCP provenance atomically while preserving legacy factory behavior.

**Files:** `web/bridge/factory_types.go`, `web/bridge/factory_store.go`, `web/bridge/factory_controller.go`, `web/bridge/factory_http.go`, `web/bridge/factory_store_test.go`, `web/bridge/factory_http_test.go`, `web/bridge/factory_controller_test.go`.

**Steps:** Add optional provenance to `FactoryEvent` and an internal principal argument, not a client-settable field on the ordinary REST `FactoryCommand`. Add a service-level draft replacement command with `expectedVersion`, request ID and full candidate create config. Normalize and preflight outside the store lock, then in `FactoryStore.Update` require `State == "draft"`, the same item/project/repo and matching expected version; update only title, plan, spec and configuration. Preserve every previously appended event/approval and immutable identity; append provenance to the same journal mutation as draft create, replacement, approval and answer. Extend create mutation so initial provenance is included in the same append, not a second audit call. Extend controller service entry point to accept server-authenticated principal and audit authority; ordinary Studio calls keep their current behavior and legacy JSON. Never trust a principal from an MCP argument or legacy REST payload. Preserve namespaced request identity on create, replacement and commands; replays return the originally journaled item.

Tests: `TestFactoryMCPCreateProvenanceAtomic`, `TestFactoryMCPDraftReplaceVersionAndIdentity`, `TestFactoryMCPApprovedDraftImmutable`, `TestFactoryMCPApproveProvenanceAndQueue`, `TestFactoryMCPAnswerDecisionRace`, `TestFactoryMCPJournalFailureNoSuccess`, `TestFactoryMCPLegacyJournalReplay`, `TestFactoryMCPScopedRequestReplay`. Test lost response by retrying the same request after commit; verify one item/event/approval.

**Verify:** `go test ./web/bridge -run '^(TestFactoryMCP|TestFactoryHTTPCreateApprovalAndReplay)' -count=1` passes. Commit `mcp-factory: task 3 — journal factory provenance`.

## Task 4: MCP protocol and factory tools

**Goal:** Make the documents-only factory flow accessible through a protocol-correct, capability-checked MCP endpoint.

**Files:** `web/bridge/mcp.go`, `web/bridge/mcptools.go`, `web/bridge/mcp_test.go`, `web/bridge/mcptools_test.go`; create `web/bridge/mcp_factory_tools.go`, `web/bridge/mcp_factory_tools_test.go`.

**Steps:** Register the six tools and schemas in spec §3. Introduce an injectable instance-scoped reference to the same `FactoryAPI` used by `factoryRoutes`; if factory is disabled, return an explicit unavailable result without bypassing authorization. Validate JSON strictly, scope all list/get and mutations through Task 2, invoke the Task 3 principal-aware service operations, and project bounded redacted outputs. Match authority and delegation semantics exactly; do not expose grant tools. Preserve current six fleet tools and their legacy result shapes. Implement `2025-06-18` Streamable HTTP initialization/version headers, POST/GET, `Accept`/content-type, notification handling and Origin checks per spec §5; authenticate all `/mcp` operations. Return a standard MCP result envelope for new factory tools with structured JSON inside text; sanitize unknown/unauthorized error detail and enforce envelope and artifact limits.

Tests: `TestMCPFactoryToolsListedWithSchemas`, `TestMCPFactoryProjectAndRepoScope`, `TestMCPFactoryReadCreateUpdate`, `TestMCPFactoryUserDirectedAndStandingApproval`, `TestMCPFactoryDelegatedAnswerRequiresToggleAndIdentity`, `TestMCPFactoryUserDirectedAnswer`, `TestMCPFactoryAuthBeforeReplay`, `TestMCPFactoryStaleAndDuplicateRequests`, `TestMCPFactoryToolResultsProtocolShape`, `TestMCPStreamableHTTPVersionAndOrigin`, `TestMCPFactoryLegacyFleetToolsUnchanged`. Use `testServerWithClient` and `testAddClient`; all endpoints and credentials are local fixtures.

**Verify:** `go test ./web/bridge -run '^(TestMCPFactory|TestMCPStreamable|TestMCPListsTools|TestListIsScoped)' -count=1` and `go test -race ./web/bridge -run '^(TestMCPFactoryGrantRevokeRacesMutation|TestMCPFactoryStaleAndDuplicateRequests)' -count=1` pass. Commit `mcp-factory: task 4 — MCP factory tools`.

## Task 5: Studio permission controls and provenance

**Goal:** Let the user manage per-client/project factory authority and see the origin of factory actions.

**Files:** `web/ui/src/lib/ClientsPanel.svelte`, `web/ui/src/lib/ClientsPanel.test.ts`, `web/ui/src/views/Project.svelte`, `web/ui/src/views/Project.test.ts`, `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`, `web/ui/src/lib/api.ts`, `web/ui/src/lib/api.test.ts`, `web/ui/src/lib/factory.ts`.

**Steps:** In ClientsPanel add an existing-project chooser and grant editor for the five independent permissions and three-state approval authority; default to none. Use GET/PUT from Contracts and preserve local changes on stale-version 409; reload only on explicit user action. The screen must explain that user-directed is a client assertion, standing is optional, and revocation affects future actions. In Project settings expose the delegation toggle and designated MCP client selection with the existing conditional settings API; default off/unselected. Reuse existing `ProjectSettings` and client list, preserve unrelated fields and avoid persisting token values. Add provenance to factory history with accurate mode and client ID/name fallback; treat missing historical provenance normally. Add tests: `editsProjectScopedFactoryGrant`, `grantConflictRetainsInput`, `existingClientStartsWithNoGrant`, `delegationDefaultsOffAndDesignatesClient`, `provenanceDistinguishesAuthority`, `legacyEventsRender`, `unauthorizedGrantIsNotEditable`. Handle project-client removal, loading and API errors.

**Verify:** `cd web/ui && npm test -- src/lib/ClientsPanel.test.ts src/views/Project.test.ts src/views/FactoryItem.test.ts src/lib/api.test.ts` passes. `npm run check` has no new errors compared with current-main baseline. Commit `mcp-factory: task 5 — Studio access controls`.

## Task 6: End-to-end proof and documentation

**Goal:** Demonstrate a complete authorized MCP-to-factory handoff and document tested client compatibility.

**Files:** Create `web/bridge/mcp_factory_integration_test.go`, `docs/studio-fleet-factory/mcp-factory-handoff-compatibility.md`; update `docs/studio-fleet-factory/mcp-factory-handoff-spec.md`, `docs/studio-fleet-factory/README.md` and this plan.

**Steps:** Use a real local HTTP MCP server, isolated client/project stores, disposable Git repositories and fake factory effects. Drive initialize, tools/list, create, read, edit, approve-and-queue, question and answer; stop the external client and prove the factory continues. Retry a lost response and race Studio/MCP writes and revocation. Assert one durable operation, correct exact approval hash, question attribution and no unauthorized leakage. Implement tests `TestMCPFactoryEndToEnd`, `TestMCPFactoryEndToEndLostResponse`, `TestMCPFactoryEndToEndRevokedAndOffline`; do not invoke live credentials. Add a compatibility report recording protocol version, transport, Claude Code and one independent MCP client, exact versions, tested auth setup and pass/fail/untested. Live client runs require explicitly configured credentials and a disposable project; if unavailable mark untested and do not claim interoperability. Document no OAuth enrollment and no selected-edit transfer in v1.

**Verify:** `go test ./web/bridge -run '^(TestMCPFactory|TestMCPStreamable|TestFactoryMCP|TestWebIsStdlibOnly)' -count=1`; `cd web/ui && npm test && npm run check && npm run build`; `CGO_ENABLED=1 go build ./cmd/marshal`; `go build ./cmd/webbridge`; `go test ./...`; `go vet ./...`; `git diff --check`; `gofmt -l` on changed Go files outputs nothing. Record current-main baseline failures separately. Verify the real permissions editor and provenance at desktop and 390px with isolated fixtures, including refresh and revoked access. The UI build updates embedded assets; follow the repository's tracked-build convention. Commit `mcp-factory: task 6 — verify MCP handoff`.

## Integration notes

The user approved this plan on 2026-11-15; execution follows the factory handoff. The external client can truthfully report an instruction but cannot prove the human gave it; authorization, attribution and UI copy must retain that distinction. Keep factory dispatch and remote forge I/O outside the policy mutex; define the commit linearization point exactly as Contracts. An existing client with unrestricted `AllowedRepos` still has no factory project grant until the user creates one. P5 and Claude ACP may change shared routes or ownership before this plan runs: reconcile against the actual base rather than assuming historical line numbers or introducing a second factory store.
