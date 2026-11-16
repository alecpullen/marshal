# Marshal agent Studio integration implementation plan

> **For agentic workers:** Execute the approved plan with the repository's `marshal-executing-plans` workflow. Do not begin implementation before plan approval.

**Goal:** Give Marshal sessions and factory workers a built-in, project-scoped connection to Factory, with individual user-controlled permissions and truthful provenance.

**Architecture:** Prototype the real MCP path, including a local Unix-socket transport and session-bound enrollment. Reuse `web/bridge` factory operations, grants and journal; keep the session identity and user-action evidence outside model-controlled tool arguments. Studio and the TUI expose the same effective access and connection state through their existing ACP seams.

**Tech stack:** Go, existing Marshal MCP client and daemon/ACP seams, `web/bridge` factory and MCP server, Svelte 5 Studio UI and Bubble Tea TUI.

**Spec:** [Marshal agent Studio integration](../marshal-agent-studio-integration-spec.md); [approved design](../marshal-agent-studio-integration-design.md).

**Base:** `0fb2bbbd` on `origin/main`, plus no dependency on the docs-only branch containing these documents. Rebase on current `main` before execution and reconcile the in-flight daemon conversion and factory/worker changes. Tasks execute sequentially; commit each as `marshal-studio-integration: task N — <title>` after its verification.

**Non-goals:** exposing new factory operations, changing existing external MCP permissions, automatically starting a daemon, silently granting write permissions, granting cross-project access, or reimplementing the factory controller on the agent side. No live credentials or developer services in tests.

## Global constraints

- Current-project read is automatic only after a trusted instance/session/project binding is established; no default write or standing authority.
- Settings are project-default plus per-session/worker overrides; unset inherits and explicit false denies. Existing MCP clients keep their existing grant semantics.
- Bind operations to authenticated session and project, not model arguments or a shared bearer token alone. Recheck grants at call time and retain the existing revocation/admission lock.
- Reuse authenticated user-action receipts for exact operations where they exist. A user-directed MCP argument alone is a client assertion; it is not proof of a user request. Do not add a second click when an existing trusted receipt covers the action.
- Do not expose credentials to prompts, tool results, logs, project files or model-controlled child environments. `web/` stays standard-library-only and cannot import `internal/`.
- Local access must use the existing owner-only Unix socket without an unauthenticated TCP listener or global relaxation of MCP URL validation. Startup must succeed without Studio.

## Review focus

- Two sessions in one project have different effective grants: tasks 2 and 5 pin isolation and revocation.
- A worker's checkout path differs from the host project root: task 3 uses the authenticated owner mapping and tests spoofed paths.
- A model asserts user direction without a matching receipt: task 4 records an assertion or requests approval, never claims verification.
- A local daemon restarts, an instance changes, or the MCP reply is lost: tasks 1, 3 and 5 verify rebinding and no duplicate mutation.
- An external MCP client or a subagent tries to obtain built-in session privileges: tasks 2 and 4 preserve separate identity and authority ceilings.

## Shared contracts

`web/bridge` and agent-side packages own separate declarations, serialized with identical JSON property names. Do not import `internal/` from `web/`. Credentials are held by a transport/token-source instance and never serialized in these status objects.

```go
// package bridge: persisted overrides and server-owned binding
type MarshalFactoryAccess struct {
    Read            *bool   `json:"read,omitempty"`
    CreateDraft     *bool   `json:"createDraft,omitempty"`
    EditDraft       *bool   `json:"editDraft,omitempty"`
    AnswerQuestion  *bool   `json:"answerQuestion,omitempty"`
    ApproveAndQueue *bool   `json:"approveAndQueue,omitempty"`
    ApprovalAuthority *string `json:"approvalAuthority,omitempty"` // none, user_directed, standing
}
type MarshalSessionBinding struct {
    InstanceID string `json:"instanceId"`
    ProjectID  string `json:"projectId"`
    SessionID  string `json:"sessionId"`
    WorkerID   string `json:"workerId,omitempty"`
    ItemID     string `json:"itemId,omitempty"`
    Role       string `json:"role,omitempty"`
    Generation string `json:"generation"`
}
type MarshalConnectionStatus struct {
    State      string `json:"state"` // connected, unavailable, ambiguous, unsupported, stale
    InstanceID string `json:"instanceId,omitempty"`
    ProjectID  string `json:"projectId,omitempty"`
    SessionID  string `json:"sessionId,omitempty"`
    VerifiedAt string `json:"verifiedAt,omitempty"` // RFC3339 UTC
    Access     MarshalFactoryAccess `json:"access"`
    Source     map[string]string `json:"source,omitempty"` // project or session
}
```

The effective default has `Read=true`, all mutation fields `false`, and `ApprovalAuthority="none"`; a project override replaces each present field, then a session/worker override replaces each present field. The control plane resolves all pointers before returning effective access. `MarshalSessionBinding` is established by a trusted owner-controlled session association, never by a tool-call parameter. A binding generation changes on restart or owner change; credentials and verified receipts cannot cross generations. Requests retain existing factory request IDs and version semantics. Action receipt evidence is server-verifiable and binds session, instance, project, exact operation/target and user action; if no such proof exists the session requests a normal user confirmation rather than fabricating it.

## Task 1: MCP transport and identity prototype

**Goal:** Establish which MCP path can actually connect a Marshal session to its own local and configured remote factory.

**Files:** `internal/tools/mcp/http_client.go`, `internal/tools/mcp/http_client_test.go`, `internal/daemon/client.go`, `web/bridge/mcp_factory_integration_test.go`, new `docs/studio-fleet-factory/marshal-agent-studio-mcp-prototype.md`.

**Steps:**

1. Write a test using an owner-only temporary Unix listener serving the actual MCP handler and `internal/tools/mcp.HTTPClient` with an injected HTTP transport that dials that socket. Add a narrowly scoped constructor/option for an injected `http.Client`; preserve the existing `ValidateRemoteServer` rules for configured remote servers. Prove initialize, tool discovery and an authorized read. Never create a loopback TCP proxy.
2. Test a configured TLS `httptest` remote with an explicit token source and certificate trust; verify endpoint identity and failure with the wrong instance. Do not use environment credentials. Prove token source values do not appear in registered tool descriptions or logs.
3. Write `marshal-agent-studio-mcp-prototype.md` with the exact test results and the remaining session enrollment/receipt gaps. The decision is MCP-first if the socket and remote client can carry authenticated per-session requests; otherwise use a thin client to the same existing service behind the same Contracts and tests. Do not claim an MCP path works based only on mock requests.

**Verify:** `go test ./internal/tools/mcp ./internal/daemon ./web/bridge -run 'Test.*(MCP|StudioIntegration)'` passes; the prototype report identifies the tested transport and unresolved gaps.

## Task 2: Session-scoped factory policy

**Goal:** An authenticated built-in principal receives project defaults and an optional individual override, while external clients remain unchanged.

**Files:** `web/bridge/workspace.go`, `web/bridge/client.go`, `web/bridge/mcp_factory_auth.go`, `web/bridge/factory_types.go`, new `web/bridge/marshal_factory_access_test.go`.

**Steps:**

1. Add the Contracts' access and binding types. Persist per-project access defaults and session/worker overrides in workspace state with versioned updates; initialize absent values to read-only effective access for verified built-in sessions. Keep unset fields distinct from false and preserve unknown existing workspace fields and existing MCP client records.
2. Extend `factoryMCPAuthorizer` with built-in principal authorization that resolves defaults and overrides while holding its existing admission lock. Require a server-verified binding before applying read default, enforce only its bound project and worker's authority ceiling, and recheck revocation immediately before durable mutation. Do not apply the built-in default to external MCP clients.
3. Extend `FactoryProvenance` additively to preserve authenticated instance/session/worker/role and authority origin, leaving legacy records and client provenance readable. Bind request idempotency scope to the authenticated principal so two sessions using the same request ID cannot collide.
4. Add `TestMarshalFactoryAccessDefaultRead`, `TestMarshalFactoryAccessOverridesAndReset`, `TestMarshalFactoryAccessTwoSessions`, `TestMarshalFactoryAccessRevocationRace`, `TestMarshalFactoryAccessExternalMCPUnchanged`, and `TestMarshalFactoryAccessLegacyReplay`.

**Verify:** `go test ./web/bridge -run 'TestMarshalFactoryAccess|TestFactoryMCPAuthorize|TestMCPFactory'` passes.

## Task 3: Trusted enrollment and discovery

**Goal:** An actual Marshal session can bind to the right Studio instance and project without exposing its credential to the model.

**Files:** `internal/daemon/client.go`, `internal/app/runtime.go`, `web/bridge/http.go`, `web/bridge/mcp.go`, `web/bridge/fleet.go`, `web/bridge/factory_workers.go`, new `web/bridge/marshal_enrollment.go`, new `web/bridge/marshal_enrollment_test.go`, and corresponding `internal/daemon` tests.

**Steps:**

1. Add a local enrollment operation on the owner-only daemon socket. Resolve the real session through the daemon's tracked session association; for workers use the server-owned `FactoryItemID`, `FactoryRole` and session association, not a path or identity supplied by the model. Bind to an opaque project ID from the workspace registration and issue a short-lived, scoped credential to the trusted client transport. Never expose it to session content or environment.
2. Add `internal/daemon.Client` support for discovery and enrollment using its existing socket transport. An explicitly configured remote endpoint uses the existing authenticated TLS/credential path and an equivalent server-verified session binding; if unavailable, report unsupported rather than broadening access. Reject ambiguous instances/project mappings until the user selects one. On daemon generation change, revoke old bindings and revalidate; absent/offline daemon must not fail agent startup.
3. Add `TestMarshalEnrollmentCanonicalProject`, `TestMarshalEnrollmentWorkerOwnedMapping`, `TestMarshalEnrollmentRejectsSpoofedSession`, `TestMarshalEnrollmentRemoteRequiresAuth`, `TestMarshalEnrollmentRestartInvalidates`, and `TestMarshalEnrollmentAmbiguousOrOffline`. Include two projects, two sessions, subdirectory launch and worker checkout paths.

**Verify:** `go test ./internal/daemon ./web/bridge -run 'TestMarshalEnrollment'` passes without ambient services.

## Task 4: User direction and session provenance

**Goal:** Approval-and-queue and question answers distinguish trusted user direction from a model or external-client assertion.

**Files:** `internal/app/session/steering.go`, `internal/app/runtime.go`, `internal/acpwire/requests.go`, `web/bridge/mcp_factory_tools.go`, `web/bridge/mcp_factory_auth.go`, `web/bridge/factory_types.go`, new `web/bridge/marshal_user_direction_test.go` and corresponding session/ACP tests.

**Steps:**

1. Reuse existing authenticated interaction receipts where available. Add a session-owned, opaque, expiring operation receipt binding session generation, project, action, target and user action. Only the trusted owner creates it after the exact user action/confirmation; the model cannot mint or alter it. A receipt can be replayed only for the same request ID and payload.
2. At the built-in transport, attach a matching receipt outside the model-provided MCP arguments. If no receipt covers the exact action, use the existing session interaction approval path; after approval send the same operation with its receipt. A model's `authority=user_directed` is not itself evidence. Do not prompt again if a valid existing receipt is present; standing authority remains the separately configured policy.
3. Extend built-in authorization and journal provenance to distinguish `verified_user_directed`, `standing`, and unverified external-client assertion; keep existing external MCP behavior intact. Reject expired, mismatched or revoked receipts, and do not record a success for a refused operation.
4. Add `TestMarshalUserDirectionReceipt`, `TestMarshalUserDirectionModelAssertion`, `TestMarshalUserDirectionNoDuplicateConfirmation`, `TestMarshalUserDirectionWrongTarget`, `TestMarshalUserDirectionExternalClientAssertion`, and `TestMarshalUserDirectionRevoked`.

**Verify:** `go test ./internal/app/session ./internal/acp/... ./web/bridge -run 'TestMarshalUserDirection|TestMCPFactory'` passes.

## Task 5: Built-in session MCP client

**Goal:** Eligible Marshal sessions discover and use the existing Factory tool surface with their own scoped identity.

**Files:** `internal/tools/mcp/manager.go`, `internal/tools/mcp/http_client.go`, `internal/app/app.go`, `internal/app/runtime.go`, `internal/tools/registry/registry.go`, new `internal/tools/mcp/marshal_studio_test.go`, `web/bridge/mcp_factory_integration_test.go`.

**Steps:**

1. At session construction, use task 3 discovery/enrollment to attach an instance-scoped built-in MCP caller through the task 1 transport. Register the existing factory tools with their existing schemas and bounded results; keep connection credentials in a private per-session token source. Do not write a user MCP config entry or share a token/client identity with other sessions. Unavailable connection yields a status and does not abort `buildAgentRunnerWithLock`.
2. Supply project, session and worker identity only from the trusted bound connection. Revalidate on reconnect; dynamically show only verified capabilities and return clear unauthorized/unavailable/unknown errors. Apply inherited actor authority when tools are present in a child registry. Preserve request IDs on retries; query/reconcile an uncertain outcome with the factory's existing idempotency instead of issuing a new action.
3. Add `TestMarshalStudioToolsReadDefault`, `TestMarshalStudioToolsPermissions`, `TestMarshalStudioToolsSessionIsolation`, `TestMarshalStudioToolsOfflineStartup`, `TestMarshalStudioToolsReconnect`, `TestMarshalStudioToolsChildCeiling`, and `TestMarshalStudioToolsLostResponse` using actual endpoint, isolated daemon and deterministic credentials.

**Verify:** `go test ./internal/tools/mcp ./internal/app/... ./web/bridge -run 'TestMarshalStudioTools' -timeout 5m` passes.

## Task 6: Connection and access surfaces

**Goal:** Users can see connection identity and change project defaults or individual session/worker permissions in Studio and the TUI.

**Files:** `web/bridge/http.go`, `web/bridge/workspace.go`, `web/ui/src/views/Project.svelte`, `web/ui/src/lib/session/AgentConversation.svelte`, `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/lib/api.ts`, `internal/acpwire/settings.go`, `internal/acp/service/`, `internal/acpclient/`, `internal/app/tui/settings/`, and their corresponding existing/new tests.

**Steps:**

1. Expose versioned authenticated user-side settings operations for project defaults, session/worker overrides and selected instance. Never expose grant administration to the model-facing MCP tools. Return connection status and effective field sources via the existing session/ACP status seam. Concurrent stale saves conflict; resetting a field restores inheritance.
2. In project settings show default read and separate write/approval capabilities; allow user-directed or standing approval authority explicitly. In the session/worker surface show current instance/project, connected/stale/unavailable state, effective values, sources and individual overrides. Provide instance selection when ambiguous, and a clear revoke/reset action. Reuse the same controls for a factory worker and an ordinary Fleet session.
3. Add an equivalent TUI settings/access view using the `internal/acpclient` seam, not direct imports of bridge or policy internals. Both UIs must expose ambiguous, unauthorized and offline states without preventing ordinary agent work.
4. Add UI and seam tests for inheritance, per-session differences, disabled read, stale save conflict, standing authority, instance selection, offline status and mobile/keyboard interaction.

**Verify:** `go test ./internal/acp/... ./internal/acpclient/... ./internal/app/tui/... ./web/bridge -run 'Test.*(MarshalStudio|IntegrationAccess)'`; `cd web/ui && npm run check && npm test -- --run`. All pass.

## Task 7: Real-session integration and recovery

**Goal:** Demonstrate the intended workflow through real Marshal sessions and the real factory MCP endpoint.

**Files:** new `web/bridge/marshal_studio_integration_test.go`, `internal/launch/launch_test.go`, `docs/studio-fleet-factory/marshal-agent-studio-mcp-prototype.md`.

**Steps:**

1. Run isolated real Marshal ACP/session processes with a scripted deterministic provider against a disposable repository, local daemon socket and real factory MCP/controller. Exercise a TUI-origin session, a Studio-origin session and a factory worker; do not replace the agent runtime or MCP endpoint with a fake factory adapter. Verify current-project read, no default writes, enabling individual write access and recording actual actor provenance.
2. Test one user-directed approval with a matching trusted receipt and no duplicate confirmation; test a model assertion without a receipt and one user-approved standing-authority case. Confirm external MCP clients and a reviewer retain their existing permissions/role constraints.
3. Exercise two concurrent sessions with different grants, cross-project denial, revoke during admission, daemon disconnect/restart and lost response followed by replay. Assert no duplicate operation, stale status and no leak of tokens or host paths.
4. Update the prototype report with the actual MCP/adapter decision, test evidence and any remote/TUI/mobile scenarios not exercised. If the selected MCP route cannot meet the requirements, implement the thin same-service adapter from the spec and rerun these same tests; do not claim completion with a missing identity or policy capability.

**Verify:** `go test ./web/bridge ./internal/launch -run 'TestMarshalStudioRealSession' -v -timeout 5m` passes with isolated fixtures and no live services.

## Final verification

Run `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, and `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent`. Run `cd web/ui && npm run check && npm test -- --run && npm run build`, then `go build ./cmd/webbridge` from the root. Check changed Go files with `gofmt -l`; run `go test ./web/bridge -run TestWebIsStdlibOnly` and `go test ./internal/app/tui -run TestTUIStaysOnTheClientSeam`. Repeat the local real-session proof after daemon restart. Record actual live remote, TUI and phone walkthroughs separately and explicitly mark untested scenarios.
