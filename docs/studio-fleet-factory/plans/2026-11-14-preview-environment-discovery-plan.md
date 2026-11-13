# Preview and test-environment capability discovery implementation plan

Date: 2026-11-14
Status: approved for autonomous execution by the user's implementation request.
Base: `f8e6e2f7` (`docs(discovery): address the preview spec review findings`).
Specification: [preview-environment-discovery-spec.md](../preview-environment-discovery-spec.md).

**Goal:** deliver the provider-capability discovery the narration slice left
open: a read-only, authenticated bridge endpoint that reports what a registered
forge instance and credential expose about a repository's preview deployments
and test environments — which environments are configured, which deployments
exist for a requested revision, and each deployment's provider-reported status,
preview link and revision — without creating a deployment, touching a
checkout, or changing anything anywhere.

## Scope and boundaries

This slice adds:

- `GET /api/repos/{id}/preview-environments` for an already-registered
  repository, with optional `?ref=` and `?sha=` scope parameters;
- a per-instance, per-credential capability report with three frozen keys —
  `preview_environments`, `preview_deployments`, `preview_statuses` — using the
  discovery contract's five states and its contextual HTTP classifier;
- read-only GitHub and Gitea/Forgejo preview adapters, which ask the instance
  rather than consulting a table of forge family names;
- the configured deployment environments, the deployments for the resolved
  scope, and each deployment's newest status, carrying the provider's own
  values verbatim.

It does not add a studio surface, a pull-request-description section, or any
rendering of preview links — that is the deferred **proposal** in the
specification §1, not the agreed part. It does not build, trigger, or manage a
preview environment; it reads what the forge already has. It does not extend
the discovery report's ten frozen capability keys, and it widens neither the
`Forge` interface nor `DiscoveryForge`.

All new Go code lives under `web/bridge/` and imports only the standard
library. `web/ui/` is untouched.

## Reconciled implementation decisions

The specification settles these; they are recorded here so an implementer does
not re-litigate them. Two independent Minimax planning briefs agreed on the
route, the read order, the five-state vocabulary, the partial-failure model
and the one-line route registration. The points below resolve their remaining
differences, and each names the code it builds on.

1. **A third forge interface, not a widening.** The preview read is a new
   optional interface `PreviewForge`, declared in the new
   `web/bridge/preview_environment_forge.go`, with the three methods the specification D4
   gives it: `Environments`, `Deployments` and `DeploymentStatuses`. `Forge`
   (`web/bridge/forge.go:96`) and `DiscoveryForge`
   (`web/bridge/discovery_forge.go:29`) are **not** touched. A repository
   whose forge family has no preview adapter is reported as `unsupported`,
   never as an error.

2. **`PreviewForgeFor(repo, client)` mirrors `DiscoveryForgeFor`.** It lives
   beside `DiscoveryForgeFor` (`web/bridge/discovery_forge.go:50`) in
   `web/bridge/preview_environment_forge.go`, returns `&previewGitHubForge{client: client}`
   for `repo.Forge == "github"` and `&previewGiteaForge{client: client}` for
   `"gitea"` (which also serves Forgejo), and returns an error wrapping
   `errDiscoveryUnsupported` for anything else. A nil client falls back to
   `forgeHTTPClient` (`web/bridge/forge.go:133`), exactly as
   `DiscoveryForgeFor` does.

3. **The capability and gap shapes are the discovery ones, reused unchanged.**
   `DiscoveryCapability` (`web/bridge/discovery.go:27`) and `DiscoveryGap`
   (`web/bridge/discovery.go:58`) are reused verbatim, and
   `DiscoveryCapabilityState` (`web/bridge/discovery.go:16`) is reused for the
   five states. This is what the specification D3 asks for — one vocabulary
   across the bridge, so the studio renders both reports with one set of
   rules — and it keeps the preview surface free of a near-duplicate struct.
   What stays **separate** is the *key list*: the preview surface has its own
   frozen `previewCapabilityOrder` of three keys, so the discovery report's ten
   keys and their order cannot be touched.

4. **The four wire types are new and live in one file.** `PreviewFilter`,
   `PreviewEnvironment`, `PreviewDeployment` and `PreviewDeploymentStatus` are
   declared in `web/bridge/preview_environment.go` beside the report types, and their JSON
   tags are exactly the field names in the specification §6.4. No discovery
   type is reused for a preview value, and no discovery type is modified.

5. **The reads happen in this fixed order.** The order is what makes each
   failure distinguishable, so it is fixed rather than left to the
   implementer:

   1. **No declared forge** → the Git-only path, **no request at all**.
   2. **Credential resolution** — `f.discoveryCredential(ctx, repo)`
      (`web/bridge/discovery_report.go:456`), exactly as the discovery report
      resolves it. An unusable credential is `unconfigured` and **no request
      is made**; a non-PAT credential is refused before any host is touched.
      The credential failure is degraded to `unconfigured` across all three
      keys **before** any classification call, exactly as `probeForge` does
      (`web/bridge/discovery_report.go:272`–`277`). It cannot go through the
      shared classifier: `discoveryCredential` returns plain `errors.New`
      values, and the classifier maps a plain non-sentinel, non-`forgeAPIError`
      error to `temporarily_unavailable`, which would contradict the
      specification D15 row.
   3. **Metadata probe** — `DiscoveryForge.RepositoryMetadata`, for the
      reason the specification D15 gives: it establishes whether a later
      `404` means "this instance does not serve this endpoint" or "this
      credential cannot see this repository", and it supplies the forge's
      default branch for the scope fallback.
      - `temporarily_unavailable` → the whole report is `unavailable` with a
        non-empty top-level `error` and all three capabilities
        `temporarily_unavailable`; the report returns here (D16).
      - `unconfigured` → all three capabilities `unconfigured`; the report
        returns here, because the setting is instance-wide.
      - any other failure → the reads continue with `metadataReadable=false`,
        so a later `404` classifies as `insufficient_permission`.
   4. **Environments read.**
   5. **Deployments read** for the resolved scope.
   6. **Statuses read**, one per named deployment.

   The metadata probe is **not** a fourth capability: the three keys are
   `preview_environments`, `preview_deployments` and `preview_statuses`. A
   metadata failure that is neither an outage nor an unconfigured setting is
   visible only through the `metadataReadable` flag that changes the `404`
   classification.

6. **Failure classification is the shared contextual classifier, reused
   verbatim.** `discoveryCapabilityState` (`web/bridge/discovery.go:171`) is
   called as-is; no new classifier, no new HTTP-status table.
   `discoveryProbeOutcome(err, metadataReadable)`
   (`web/bridge/discovery_report.go:476`) is reused too, because it is the
   existing seam that lets an adapter's own message win for the sentinels it
   raises (`errDiscoveryUnconfigured`, `errDiscoveryUnsupported`). The preview
   surface adds **no** new sentinel error.

7. **The preview surface has its own fix rules, and only its own.** The
   discovery surface's `discoveryFix` (`web/bridge/discovery_report.go:497`)
   reasons about checkout targets and about declaring a forge, neither of which
   is the remedy here, so the preview surface gets its own
   `previewFix(state DiscoveryCapabilityState, forgeDeclared bool) string` and
   `previewGapDetail(detail string, state DiscoveryCapabilityState, forgeDeclared bool) string`
   in `web/bridge/preview_environment.go`, with the four texts the specification D19
   fixes. Its `unsupported` text names the **instance** not exposing the
   resource and never says "adapter support would be needed" (D20). The gap
   shape is the reused `DiscoveryGap`: `Key`, `Section`, `State`, `Detail`.

8. **Scope resolution follows the specification D6 exactly.** (a) `?ref=` and
   `?sha=` are applied conjunctively when both are given. (b) When neither is
   given: the registered branch `Repo.Branch` if it has one, otherwise the
   forge's default branch from the metadata probe. (c) The resolved scope is
   echoed in the report's `ref` and `sha`. (d) **Terminal case:** when
   neither query parameter, nor the registered branch, nor the forge's default
   branch resolves, the deployments read is **unfiltered** — `ref` and `sha`
   are both empty, the capability stays `supported`, and its detail is exactly
   `"no revision scope was resolvable; every deployment the forge reports is
   listed"`. A `sha` scope never borrows a ref, and a ref scope never borrows
   a sha. A filter dimension that is empty is **omitted** from the request
   query rather than sent as an empty value.

9. **The bounds pipeline runs in the fixed order of the specification D12.**
   (1) Read deployments for the resolved scope, bounded by
   `discoveryForgePageSize` / `discoveryForgeMaxPages`
   (`web/bridge/discovery_forge.go:20`). (2) Group them by environment name,
   keeping the deployment with the greatest id per group. (3) Order the
   survivors by id **descending** and keep the first 20. (4) Read statuses
   only for those 20 deployments. The cap applies to the newest deployment
   **per environment**, not to the newest 20 deployments: a branch with 40
   environments yields one row per environment, capped at the 20 newest
   environments, rather than 20 rows that might all belong to one environment.
   The 20 is a new named constant in `web/bridge/preview_environment.go`,
   `previewNamedDeploymentLimit`.

10. **Ordering is by the provider's own id, descending** (D14). Deployment
    ids are numeric; the report carries them as strings, because the
    specification §6.4 requires the native id to survive any provider, but
    the grouping and ordering compare the **numeric** value, so `"99"` sorts
    above `"9"`. A deployment whose id does not parse as a number **loses to
    any numeric id within its environment group**, so it survives grouping
    only as the sole member of that group; it is then ordered after every
    numeric survivor and before the keep-first-20 cut, and its status is not
    read, because the status URL needs a usable id.

11. **The newest status supplies the deployment row's status fields** (D4a).
    `state`, `environmentUrl`, `targetUrl`, `logUrl` and `description` come
    from the deployment's newest status only. A field the newest status does
    not carry is left empty and is **not** back-filled from an older status,
    because an older status describes an older moment of the environment and
    reporting it under the newest deployment's row would misstate the current
    state. A deployment with no statuses at all keeps its row with empty
    status fields — a completed read of an empty collection, not a failure.

12. **`preview_statuses` is `supported` with the detail
    `"not exercised: no deployment was reported for this scope"`** when there
    were no deployments to read statuses for (D10). This mirrors the
    discovery report's not-exercised convention
    (`web/bridge/discovery_report.go:380`).

13. **A truncated or capped read keeps its capability `supported`** and
    records a gap naming the bound hit. The gap's **key** is the capability
    whose collection was bounded — `preview_deployments` for the deployments
    bounds, `preview_environments` for the environments bound, and
    `preview_statuses` for a statuses bound — and its **state** is
    `temporarily_unavailable`, mirroring the discovery report's truncation
    gaps: the forge did answer, so the capability stays `supported`, and the
    gap says the answer was not exhaustive. The exact texts are in the
    **Bound-hit details** table below. No bound detail mentions adapter
    support: the adapter exists and the instance answered; what was not read
    is a matter of this report's own bounds.

14. **The status capability carries the most actionable state among its
    failures** (D18). When several per-deployment status reads fail with
    different causes, the capability and its single gap carry the state with
    the highest `discoveryStateSeverity`
    (`web/bridge/discovery_report.go:533`): `unconfigured` >
    `insufficient_permission` > `temporarily_unavailable` > `unsupported`. The
    helper `discoveryWorstState([]DiscoveryGap)`
    (`web/bridge/discovery_report.go:548`) is reused verbatim. A deployment
    whose status read failed keeps its row with empty status fields; only the
    capability state and the gap reflect the failure.

15. **One failure degrades one capability — unless another read depends on
    it** (D18). A refused environments read leaves `preview_deployments` and
    `preview_statuses` `supported`, and `gaps` names only the refused key.
    Deployment statuses are a sub-resource of deployments, so the dependency
    propagates: a failed deployments read degrades `preview_statuses` with
    the **same state and cause**, in its own gap, because reporting
    `preview_statuses` as `supported` would claim a capability the instance
    does not have. Each key still carries exactly one gap.

16. **Each degraded capability contributes exactly one gap.** `gaps` holds at
    most one entry per capability key, so the Git-only path — three keys
    degraded — carries three gaps, each with the same cause text, exactly as
    the discovery report's Git-only path carries one gap per degraded forge
    key. Every gap carries the cause; the cause is what the operator reads.
    `gaps` is ordered by the frozen preview key order.

17. **The Git-only path makes no request at all** (D17). A repository with
    `Repo.Forge == ""` reports all three capabilities `unsupported`, with the
    detail "no forge is declared for this repository", `status: "degraded"`,
    and empty `environments` / `deployments` arrays. The report never implies
    the repository has no previews.

18. **A declared forge whose metadata probe fails as
    `temporarily_unavailable` is a visible outage** (D16): `status:
    "unavailable"`, a non-empty top-level `error`, and all three preview
    capabilities `temporarily_unavailable` with gaps. It is never a silent
    downgrade to "this repository has no previews".

19. **Every array is always present and never null.** `capabilities`,
    `environments`, `deployments` and `gaps` are non-nil in the Go value, so
    they serialise as `[]` and never `null`. This matches the discovery
    report's discipline (`web/bridge/discovery_report.go:679`).

20. **The report carries provider values verbatim** (D22): native ids as
    strings, native URLs, the provider's own status string, the provider's
    own revision, the provider's own timestamps as reported. Marshal
    normalises nothing and rewrites no URL.

21. **Read-only guarantees are structural** (D21). Every forge request is a
    `GET`; no credential, request header or raw forge error body appears in
    the report, a gap or an error string. `doJSON`
    (`web/bridge/forge_github.go:77`) already reports only the status and the
    forge's own message, and `forgeAPIError.Error()`
    (`web/bridge/forge_github.go:49`) already omits headers, so this holds by
    construction. No credential value is ever copied into a report field.

22. **The route is a `GET`**, registered by a new `previewRoutes()` method in
    `web/bridge/preview_environment_http.go`, called by **exactly one** added line in
    `web/bridge/http.go` next to the existing `s.discoveryRoutes()` call
    (`web/bridge/http.go:254`). The bearer wrapper is inherited: every `/api`
    route is wrapped when the server has a token (`web/bridge/http.go:90`). The
    handler requires fleet mode through the existing `s.requireFleet(w)`
    (`web/bridge/disk_http.go:39`) and resolves the repository through the
    existing `s.fleet.ws.Repo(id)` (`web/bridge/workspace.go:757`).

23. **The route's status codes** are the specification §6.1 table: 200, 401,
    404, 405, 503, and 500 for an unexpected in-process failure — written
    directly with `writeJSON` (`web/bridge/http.go:273`), **not** through
    `writeErr`, whose 502 default denotes a child-process failure
    (`web/bridge/http.go:279`).

24. **The assembler returns an error only for a request the bridge cannot
    assemble at all.** It mirrors `Fleet.DiscoverRepo`
    (`web/bridge/discovery_report.go:77`–`85`), whose returned error "is
    reserved for a request the bridge cannot assemble at all": concretely, a
    registered repository whose `Repo.ID` is empty. Everything else is
    classified into the report and returned with a nil error, so the 500
    branch is reachable without a forge being contacted. The route test
    reaches it the way the discovery route test does
    (`web/bridge/discovery_http_test.go:238`–`270`): by seeding a workspace
    record whose map key exists but whose `Repo.ID` field is empty.

25. **Every file in this slice is named `preview_environment*.go`.** The plan's
    first draft named `web/bridge/preview.go` and
    `web/bridge/preview_test.go`, but **both already exist** at this base
    commit and hold the unrelated, already-shipped **web-studio live preview
    proxy** — `previewToken`, `previewStore`, `IssuePreview`,
    `PreviewHandler`, and about 28 `TestPreview*` tests. The slice's files are
    therefore named `preview_environment.go`, `preview_environment_test.go`,
    `preview_environment_forge.go`,
    `preview_environment_forge_github.go`,
    `preview_environment_forge_gitea.go`,
    `preview_environment_forge_test.go`,
    `preview_environment_report.go`,
    `preview_environment_report_test.go`,
    `preview_environment_http.go` and
    `preview_environment_http_test.go`. The existing `preview.go` and
    `preview_test.go` are **not** modified by this slice.

    No symbol collides: none of `PreviewReport`, `PreviewFilter`,
    `PreviewEnvironment`, `PreviewDeployment`, `PreviewDeploymentStatus`,
    `previewCapabilityOrder`, `previewCapabilityKeys`, `previewFix`,
    `previewGapDetail`, `previewNamedDeploymentLimit` or the `previewSection*`
    constants exists in the package today, and none of this slice's test names
    collides with the live-preview tests. Because the live-preview tests also
    begin with `TestPreview`, this slice's run gates **name this slice's own
    tests explicitly** instead of matching the bare `^TestPreview` prefix, so a
    gate failure means this slice failed and never that an unrelated feature
    broke.

## HTTP and JSON contract

### Request

```http
GET /api/repos/{id}/preview-environments[?ref=<ref>][&sha=<sha>]
Authorization: Bearer <token>   # when the server has a token
```

The body is not read. The repository always comes from its registered record,
never from the caller. Both query parameters are optional; see decision 8 for
how the scope resolves.

### Response

```json
{
  "repoId": "repo-1",
  "forge": "github",
  "status": "ok",
  "ref": "feature/x",
  "sha": "",
  "capabilities": [
    {"key": "preview_environments", "state": "supported", "detail": ""},
    {"key": "preview_deployments", "state": "supported", "detail": ""},
    {"key": "preview_statuses", "state": "supported", "detail": ""}
  ],
  "environments": [
    {"name": "production", "id": "1234567",
     "htmlUrl": "https://github.com/o/r/deployments/activity_log?environments_filter=production",
     "branchPolicy": "protected_branches", "protectionRuleCount": 2}
  ],
  "deployments": [
    {"id": "99", "environment": "production", "originalEnvironment": "",
     "ref": "feature/x", "sha": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
     "task": "deploy", "state": "success",
     "environmentUrl": "https://preview.example.internal",
     "targetUrl": "https://ci.example/run/42", "logUrl": "https://ci.example/run/42/log",
     "description": "deployed 8e4c1b2", "transient": false, "production": true,
     "createdAt": "2026-11-12T09:00:00Z", "updatedAt": "2026-11-12T09:01:12Z"}
  ],
  "gaps": [
    {"key": "preview_environments", "section": "environments",
     "state": "insufficient_permission",
     "detail": "the credential lacks permission to read this resource (HTTP 403); grant the credential read access to the repository's deployments and environments, and retry"}
  ]
}
```

`error` is present only when `status` is `unavailable`. `environments`,
`deployments` and `gaps` are always present as arrays and never `null`.

### Capability keys, sections and order

| # | Key | Reads | Gap `section` |
|---|---|---|---|
| 1 | `preview_environments` | The repository's configured deployment environments | `environments` |
| 2 | `preview_deployments` | The deployments recorded for the resolved scope | `deployments` |
| 3 | `preview_statuses` | The provider-reported status of each reported deployment | `statuses` |

The list is frozen for this surface exactly as the discovery list is, and
`gaps` is ordered by it.

### Report status boundary

- `ok` — every capability is `supported` and `gaps` is empty.
- `degraded` — any capability is not `supported`, or any gap exists.
- `unavailable` — the declared forge's metadata probe failed as
  `temporarily_unavailable`. The top-level `error` is non-empty.

### Shared Go types

Task 1 owns these names verbatim. Later tasks consume them without renaming:
`PreviewFilter`, `PreviewEnvironment`, `PreviewDeployment`,
`PreviewDeploymentStatus`, `PreviewReport`, `previewCapabilityOrder`,
`previewCapabilityKeys()`, `previewFix(state, forgeDeclared)`,
`previewGapDetail(detail, state, forgeDeclared)`,
`previewNamedDeploymentLimit`, `previewSectionEnvironments`,
`previewSectionDeployments`, `previewSectionStatuses`. The classifier, the
states, the capability shape, the gap shape, the status constants, the
severity order and the truncation sentinel are **reused** from the discovery
contract and are not redefined.

### Fix texts (specification D19)

| State | Fix |
|---|---|
| `unconfigured` | this repository needs a personal access token credential to read preview environments; register or repair one and retry |
| `insufficient_permission` | grant the credential read access to the repository's deployments and environments, and retry |
| `temporarily_unavailable` | the forge could not answer right now; retry later |
| `unsupported` | this forge instance does not expose this resource; no adapter can read what the instance does not serve |

The Git-only path has a different cause — **no forge is declared** — so its
`unsupported` fix is `declare github or gitea for this repository`, not the
instance text above. The instance text would tell the operator to look for a
resource their instance was never asked about. `previewFix` therefore takes a
`forgeDeclared` flag, exactly as the discovery surface's `discoveryFix` does.

### Bound-hit details (specification D13)

| Bound hit | Gap key | Detail |
|---|---|---|
| The page bound on deployments | `preview_deployments` | the forge reported more deployments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest |
| The name cap | `preview_deployments` | the forge reported deployments for more environments than this report names; the newest deployment for each of the most recently updated environments is listed |
| The page bound on environments | `preview_environments` | the forge reported more environments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest |
| The page bound on a deployment's statuses | `preview_statuses` | the forge reported more statuses for at least one deployment than this report reads; the listed status may not be the newest |

### Status codes

| Status | Meaning |
|---|---|
| `200` | The report |
| `401` | The server has a token and the request did not carry it |
| `404` | No repository is registered under that id |
| `405` | A method other than `GET` |
| `503` | The server runs without a fleet, so there is no credential store |
| `500` | An unexpected in-process failure |

## File and dependency map

| File | Task | Contents |
|---|---|---|
| `web/bridge/preview_environment.go` | 1 | The report type, the four wire types, the frozen key list, `previewFix`, `previewGapDetail`, the section and limit constants |
| `web/bridge/preview_environment_test.go` | 1 | The contract tests: frozen key order, fix texts, gap detail, JSON shape |
| `web/bridge/preview_environment_forge.go` | 2 | The `PreviewForge` interface and `PreviewForgeFor` |
| `web/bridge/preview_environment_forge_github.go` | 2 | The GitHub adapter |
| `web/bridge/preview_environment_forge_gitea.go` | 2 | The Gitea/Forgejo adapter |
| `web/bridge/preview_environment_forge_test.go` | 2 | The adapter tests against `httptest` loopback servers |
| `web/bridge/preview_environment_report.go` | 3 | The assembler: read order, scope resolution, the bounds pipeline, classification, gap writing |
| `web/bridge/preview_environment_report_test.go` | 3 | The report tests: every failure, bound and edge case |
| `web/bridge/preview_environment_http.go` | 4 | `previewRoutes()` and the handler |
| `web/bridge/preview_environment_http_test.go` | 4 | The route tests |
| `web/bridge/http.go` | 4 | **One** added route-registration line |
| `docs/studio-fleet-factory/preview-environment-discovery-spec.md` | 5 | The implementation record and the branch review record |
| `docs/studio-fleet-factory/README.md` | 5 | The active-specification entry |

No task edits `discovery.go`, `discovery_forge.go`, `discovery_forge_github.go`,
`discovery_forge_gitea.go`, `discovery_report.go`, `discovery_checkout.go`,
`discovery_http.go`, `forge.go`, `preview.go` or `preview_test.go`.

## Task 1: Freeze the preview report contract and its own fix rules

**Files:** create `web/bridge/preview_environment.go` and `web/bridge/preview_environment_test.go`.

- [ ] Write `TestPreviewCapabilityOrderIsStable`: the three keys are
  `preview_environments`, `preview_deployments`, `preview_statuses`, in that
  order, and the accessor hands back a copy so a caller cannot reorder the
  contract for everyone else. Assert the discovery report's own key list is
  untouched by this slice.
- [ ] Write `TestPreviewContractJSONUsesStableShape`: the report marshals with
  the field names of the HTTP and JSON contract above; every array is present
  and `[]`, never `null`; `error` is omitted unless `status` is
  `unavailable`.
- [ ] Write `TestPreviewFixRulesMatchTheContract`: each of the five states
  maps to the fix text in the table above, and the `unsupported` text names
  the instance and never says adapter support would be needed.
- [ ] Write `TestPreviewGapDetailJoinsCauseAndFix`: a gap's detail is the
  cause and the fix, and the fix is not duplicated when the cause already
  contains it.
- [ ] Run `go test ./web/bridge -run '^(TestPreviewCapabilityOrderIsStable|TestPreviewContractJSONUsesStableShape|TestPreviewFixRulesMatchTheContract|TestPreviewGapDetailJoinsCauseAndFix)$' -count=1`; confirm
  failure.
- [ ] Implement `web/bridge/preview_environment.go`: the `PreviewReport` type, the four
  wire types with the JSON tags of the specification §6.4, the frozen
  `previewCapabilityOrder` with `previewCapabilityKeys()` returning a copy,
  `previewFix(state, forgeDeclared)`, `previewGapDetail(detail, state,
  forgeDeclared)`, and the named constants `previewNamedDeploymentLimit` (20)
  and the three section names. Reuse `DiscoveryCapability`, `DiscoveryGap` and
  `DiscoveryCapabilityState` unchanged.
- [ ] Run `go test ./web/bridge -run '^(TestPreviewCapabilityOrderIsStable|TestPreviewContractJSONUsesStableShape|TestPreviewFixRulesMatchTheContract|TestPreviewGapDetailJoinsCauseAndFix)$' -count=1`; require PASS.
- [ ] Commit only this task: `feat(preview): freeze the preview report contract`.

## Task 2: Add the read-only preview forge adapters

**Files:** create `web/bridge/preview_environment_forge.go`,
`web/bridge/preview_environment_forge_github.go`, `web/bridge/preview_environment_forge_gitea.go` and
`web/bridge/preview_environment_forge_test.go`.

- [ ] Write `TestPreviewForgeGitHubReadsEnvironments`: the adapter issues
  `GET /repos/{owner}/{repo}/environments`, decodes the **object** shape
  `{total_count, environments: [...]}`, and maps each environment's `name`,
  `id`, `html_url`, `protection_rules` count and `deployment_branch_policy`
  onto `PreviewEnvironment`. A branch policy of `protected_branches` or
  `custom_branch_policies` is carried verbatim; a policy the forge does not
  report is empty.
- [ ] Write `TestPreviewForgeGitHubReadsDeployments`: the adapter issues
  `GET /repos/{owner}/{repo}/deployments` with the resolved filters, decodes
  the bare array, and maps each record onto `PreviewDeployment`. An empty
  filter dimension is **omitted** from the query, never sent as an empty
  value.
- [ ] Write `TestPreviewForgeGitHubReadsDeploymentStatuses`: the adapter
  issues `GET /repos/{owner}/{repo}/deployments/{id}/statuses` and returns
  the records as `PreviewDeploymentStatus`, carrying the provider's `state`,
  `environment_url`, `target_url`, `log_url`, `description` and timestamps.
- [ ] Write `TestPreviewForgeGitHubTruncatesAtThePageBound`: a full final
  page yields the items read **and** `errDiscoveryTruncated`, so the caller
  can keep the items and record the bound.
- [ ] Write `TestPreviewForgeGiteaUsesTokenAuthAndVersionedBase`: the Gitea
  adapter sends `Authorization: token <pat>` against the instance's
  `apiBase`, and asks the instance's own paths: `/repos/{owner}/{repo}/environments`,
  `/repos/{owner}/{repo}/deployments` and
  `/repos/{owner}/{repo}/deployments/{id}/statuses` — the natural mirror of
  the GitHub paths under the instance's versioned API root. Gitea serves no
  such resource, so the instance answers `404`; the adapter reports what it
  answered and the assembler classifies it.
- [ ] Write `TestPreviewForgeGiteaWithoutAPIBaseIsUnconfigured`: with no
  `apiBase`, the adapter reports `errDiscoveryUnconfigured` **without
  building a request**.
- [ ] Write `TestPreviewForgeIssuesOnlyGetRequests`: every request the
  adapters issue is a `GET`.
- [ ] Write `TestPreviewForgeNeverEchoesTheCredential`: the credential
  literal appears in no request the adapter builds outside the
  `Authorization` header, and in no returned error string.
- [ ] Run `go test ./web/bridge -run '^TestPreviewForge' -count=1`; confirm
  failure.
- [ ] Implement the three files. The interface and `PreviewForgeFor` mirror
  `DiscoveryForge` and `DiscoveryForgeFor` (`web/bridge/discovery_forge.go:29`
  and `:50`). The GitHub adapter mirrors
  `discoveryGitHubForge` (`web/bridge/discovery_forge_github.go:20`,
  `:42`): the same `apiBase` default of `https://api.github.com`, the same
  `Accept: application/vnd.github+json`, the same `repoPath`, and the same
  `doJSON` (`web/bridge/forge_github.go:77`) so the `forgeAPIError` mapping
  is unchanged. The Gitea adapter mirrors `discoveryGiteaForge`
  (`web/bridge/discovery_forge_gitea.go:28`, `:53`): `apiRoot` requires
  `apiBase` and reports `unconfigured` without building a request, and the
  auth header is `token <pat>`. Both adapters page with
  `discoveryForgePageSize` / `discoveryForgeMaxPages` and return
  `errDiscoveryTruncated` on a full final page.
- [ ] Run `go test ./web/bridge -run '^TestPreviewForge' -count=1`; require
  PASS.
- [ ] Commit only this task: `feat(preview): add read-only preview forge adapters`.

## Task 3: Assemble the preview environment report

**Files:** create `web/bridge/preview_environment_report.go` and
`web/bridge/preview_environment_report_test.go`.

- [ ] Write `TestPreviewReportGitOnlyMakesNoRequest`: with no declared
  forge, assert **no** request is made, all three capabilities are
  `unsupported` with the detail "no forge is declared for this repository",
  `status` is `degraded`, and the arrays are empty.
- [ ] Write `TestPreviewReportUnconfiguredWithoutAPATCredential`: with a
  missing or non-PAT credential, all three capabilities are `unconfigured`
  and **no** request is made.
- [ ] Write `TestPreviewReportUnavailableWhenTheForgeIsUnreachable`: make
  the metadata read fail at the transport layer; assert `status` is
  `unavailable`, the top-level `error` is non-empty, and all three
  capabilities are `temporarily_unavailable`.
- [ ] Write `TestPreviewReportClassifies404AfterReadableMetadataAsUnsupported`:
  a `404` from an instance that served its metadata but not the preview
  resource is `unsupported`, with a gap naming the resource.
- [ ] Write `TestPreviewReportClassifies404BeforeReadableMetadataAsInsufficientPermission`:
  the same `404` from an instance whose metadata was **not** readable is
  `insufficient_permission`.
- [ ] Write `TestPreviewReportGiteaInstanceAnswering404IsUnsupported`: point a
  Gitea `apiBase` at an `httptest` server that answers `404` on the three
  preview paths, and assert that **all three** capabilities are `unsupported`
  with the D20 instance wording, that the report does not claim the
  repository has no previews, and that the status is `degraded`.
- [ ] Write `TestPreviewReportDegradesOnlyTheDeniedEnvironmentsRead`: a `403`
  on the environments read leaves `preview_deployments` and
  `preview_statuses` `supported` with their facts intact, and `gaps` names
  only `preview_environments`.
- [ ] Write `TestPreviewReportReportsSupportedGitHubWithPAT`: a supported
  instance reports the provider's ids, URLs, status strings and revisions
  verbatim, and the report's `ref`/`sha` echo the resolved scope.
- [ ] Write `TestPreviewReportResolvesScopeFromRefAndSHA`: both query
  parameters are applied conjunctively and both are echoed.
- [ ] Write `TestPreviewReportFallsBackToTheRegisteredBranch` and
  `TestPreviewReportFallsBackToTheForgeDefaultBranch`: with neither query
  parameter, the scope is the registered branch, then the forge's default
  branch.
- [ ] Write `TestPreviewReportWithoutAnyScopeIsUnfiltered`: with no scope
  resolvable at all, the deployments read carries no filter, `ref` and `sha`
  are both empty, and `preview_deployments` carries the exact detail
  "no revision scope was resolvable; every deployment the forge reports is
  listed".
- [ ] Write `TestPreviewReportNamesTheNewestDeploymentPerEnvironment`: two
  deployments of one environment name yield one row, the one with the
  greatest id; the status is read only for the rows named.
- [ ] Write `TestPreviewReportRecordsTheNameCapGap`: more than 20 distinct
  environments yield exactly 20 rows and a gap on `preview_deployments` with
  the name-cap detail.
- [ ] Write `TestPreviewReportRecordsThePageBoundGap`: a truncated
  deployments read keeps `preview_deployments` `supported`, keeps the items
  read, and records a gap with the page-bound detail; neither bound detail
  mentions adapter support.
- [ ] Write `TestPreviewReportFoldsTheNewestStatusOnly`: a deployment's
  status fields come from its newest status; a field the newest status
  lacks stays empty and is not back-filled from an older status; the
  deployment's own `description` is never used for the row's description.
- [ ] Write `TestPreviewReportDeploymentWithNoStatusesKeepsEmptyState`: a
  deployment with an empty status collection keeps its row, `state` is
  empty, `preview_statuses` stays `supported`, and no gap is recorded.
- [ ] Write `TestPreviewReportStatusNotExercisedWithoutDeployments`: with no
  deployments, `preview_statuses` is `supported` with the detail "not
  exercised: no deployment was reported for this scope".
- [ ] Write `TestPreviewReportStatusFailureDegradesOnlyStatuses`: one failed
  status read degrades `preview_statuses` alone and leaves the other rows'
  statuses intact.
- [ ] Write `TestPreviewReportStatusFailureTakesTheMostActionableState`:
  two status reads failing differently leave the capability and its single
  gap carrying the most actionable state.
- [ ] Write `TestPreviewReportReportsEmptyCollectionsAsSupported`: an empty
  repository reports empty arrays, every capability `supported`, no gaps,
  and `status` `ok` — textually distinct from an unreadable section.
- [ ] Write `TestPreviewReportRecordsTheEnvironmentsPageBoundGap`: a
  truncated environments read keeps the environments read, keeps
  `preview_environments` `supported`, and records a gap on
  `preview_environments` with the environments page-bound detail.
- [ ] Write `TestPreviewReportRecordsTheStatusesPageBoundGap`: a truncated
  statuses read keeps its items, keeps `preview_statuses` `supported`, and
  records a gap on `preview_statuses` with the statuses page-bound detail —
  a listed status may not be the newest.
- [ ] Write `TestPreviewReportReadsNoLocalPath`: the report never reads a
  checkout, a workspace or any local file.
- [ ] Write `TestPreviewReportDoesNotChangeTheDiscoveryContract`: the
  discovery report's ten keys, their order and its route are unchanged, and
  `TestDiscoveryCapabilityOrderIsStable` still passes.
- [ ] Run
  `go test ./web/bridge -run '^TestPreviewReport' -count=1`; confirm
  failure.
- [ ] Implement `web/bridge/preview_environment_report.go`: the read order of decision
  5, the scope resolution of decision 8, the bounds pipeline of decision 9,
  the classification of decision 6, the fix and gap text of decision 7, the
  severity fold of decision 14, the dependency propagation of decision 15, and
  the always-present arrays of decision 19. A truncated statuses read records
  its gap **after** the capability's failure gap, so the more actionable
  failure keeps the key and the bound detail is added only when the key has no
  gap yet.
  Reuse `discoveryCapabilityState`, `discoveryProbeOutcome`,
  `discoveryWorstState`, `discoveryStateSeverity` and
  `f.discoveryCredential` unchanged.
- [ ] Run `go test ./web/bridge -run '^TestPreviewReport' -count=1`; require
  PASS. Then run `go test ./web/bridge -count=1`; require PASS.
- [ ] Commit only this task: `feat(preview): assemble the preview environment report`.

## Task 4: Expose the authenticated preview-environments route

**Files:** create `web/bridge/preview_environment_http.go` and
`web/bridge/preview_environment_http_test.go`; modify `web/bridge/http.go` by adding one
route-registration line.

- [ ] Write `TestPreviewRouteReturnsTheReport`: GET returns 200, a JSON
  content type, the repository id, the three capabilities, the environments,
  the deployments and the gaps.
- [ ] Write `TestPreviewRouteForwardsRefAndSHA`: the `?ref=` and `?sha=`
  parameters reach the report's scope.
- [ ] Write `TestPreviewRouteRequiresFleet`: a registry-only server returns
  503 and does not panic.
- [ ] Write `TestPreviewRouteUnknownRepo`: an unknown id returns 404.
- [ ] Write `TestPreviewRouteMethodMismatch`: POST returns 405.
- [ ] Write `TestPreviewRouteHonoursBearerAuth`: with a configured token, an
  unauthenticated request returns 401 and an authenticated request succeeds.
- [ ] Write `TestPreviewRouteDoesNotPersistState`: the workspace file bytes
  and version are unchanged before and after a successful call.
- [ ] Write `TestPreviewRouteInternalFailureIs500`: force an in-process
  preview failure and assert 500, not the child-failure 502 convention.
- [ ] Run `go test ./web/bridge -run '^TestPreviewRoute' -count=1`; confirm
  failure.
- [ ] Implement `previewRoutes()` and the handler in the new file. The
  handler requires fleet mode, resolves the registered repo, calls the report,
  and writes JSON. It reads no body. Add exactly one `s.previewRoutes()`
  call at the end of the flat registration list in `routes()`, next to the
  existing `s.discoveryRoutes()` call; do not reorder existing lines. Return
  500 directly for an unexpected in-process error rather than using
  `writeErr`, whose 502 convention denotes a child-process failure.
- [ ] Run
  `go test ./web/bridge -run '^(TestPreviewRoute|TestWebIsStdlibOnly)' -count=1`;
  require PASS. Inspect this task's commit with
  `git show --format= -- web/bridge/http.go` and confirm it contains exactly
  one added route-registration line, independent of any later base-branch
  movement.
- [ ] Commit only this task: `feat(preview): expose read-only preview discovery`.

## Task 5: Finalize the specification and prove acceptance

**Files:** modify
`docs/studio-fleet-factory/preview-environment-discovery-spec.md` and
`docs/studio-fleet-factory/README.md`.

- [ ] Run `go build ./...`; require exit 0.
- [ ] Run `go vet ./web/bridge/...`; require exit 0.
- [ ] Run `gofmt -l web/bridge`; require no output.
- [ ] Run `go test ./web/bridge -count=1`; require PASS.
- [ ] Run `go test ./web/bridge -run '(^(TestPreviewCapabilityOrderIsStable|TestPreviewContractJSONUsesStableShape|TestPreviewFixRulesMatchTheContract|TestPreviewGapDetailJoinsCauseAndFix)$|TestPreviewForge|TestPreviewReport|TestPreviewRoute|TestWebIsStdlibOnly)$' -count=1`;
  require PASS.
- [ ] Fill the specification's **Implementation record** with the commits and
  the acceptance gates, and its **Branch review record** with the
  implementation-review findings once that review is done.
- [ ] Update the archive README's active-specification entry to describe the
  served contract.
- [ ] Commit only this task: `docs(discovery): record the implemented preview discovery`.

## Implementation record

Delivered on branch `studio/preview-environment-discovery-implementation` as
Tasks 1–4, test-first, one commit per task, plus two commits that close the
gaps the implementation review found.

| Task | Deliverable | Commit |
|---|---|---|
| 1 | Frozen contract: the report type, the four wire types, the three ordered capability keys, and this surface's own fix and gap rules | `c21f16f2` |
| 2 | Read-only GitHub and Gitea/Forgejo preview adapters behind an optional `PreviewForge` interface | `2631d70b` |
| 3 | The report assembler: the fixed read order, scope resolution, the bounds pipeline, classification and gap writing | `960e0498` |
| 4 | The authenticated `GET /api/repos/{id}/preview-environments` route | `22dfbf33` |
| — | Bound-gap completion: a truncated read of a deployment's statuses records the gap the contract fixes | `1ab68d88` |
| — | Review fixes: the gaps name what happened, and three untested branches are covered | `6d970434` |
| 5 | Specification, README and plan outcome record | this documentation commit |

Files: `web/bridge/preview_environment.go`, `preview_environment_forge.go`,
`preview_environment_forge_github.go`, `preview_environment_forge_gitea.go`,
`preview_environment_report.go`, `preview_environment_http.go`, their tests, and
one route-registration line in `web/bridge/http.go`. All new code is standard
library only; `web/ui/` is untouched, and neither the `Forge` interface nor the
`DiscoveryForge` interface is widened. The unrelated shipped live-preview proxy
in `web/bridge/preview.go` is not touched.

Acceptance gates, run from the repository root on this branch:

| Gate | Result |
|---|---|
| `go test ./web/bridge -run '^(TestPreviewCapabilityOrderIsStable\|TestPreviewContractJSONUsesStableShape\|TestPreviewFixRulesMatchTheContract\|TestPreviewGapDetailJoinsCauseAndFix\|TestPreviewForge\|TestPreviewReport\|TestPreviewRoute\|TestWebIsStdlibOnly\|TestDiscoveryCapabilityOrderIsStable)$' -count=1` | PASS — `ok marshal/web/bridge 0.596s` |
| `go test ./web/bridge -count=1` | PASS — `ok marshal/web/bridge 128.309s` |
| `gofmt -l web/bridge` | no output |
| `go vet ./web/bridge/...` | no output, exit 0 |
| `CGO_ENABLED=1 go build ./...` | exit 0 |

The forge reads are exercised only against `httptest` loopback servers, and no
test reads developer credentials, calls a live forge, or contacts a model
provider.

## Acceptance matrix

| Specification acceptance criterion | Proved by |
|---|---|
| A GitHub-wire-compatible endpoint with a `pat` returns a report carrying the provider's own ids, URLs, status strings and revisions, with all three capabilities `supported` | Task 3 `TestPreviewReportReportsSupportedGitHubWithPAT`; Task 4 `TestPreviewRouteReturnsTheReport` |
| A credential refused with `403` on the environments read yields `insufficient_permission` for `preview_environments` with a gap naming cause and fix, while the other two stay `supported` with their facts intact | Task 3 `TestPreviewReportDegradesOnlyTheDeniedEnvironmentsRead`; Task 1 `TestPreviewFixRulesMatchTheContract` |
| A `404` after readable metadata is `unsupported`; the same `404` before it is `insufficient_permission` | Task 3 `TestPreviewReportClassifies404AfterReadableMetadataAsUnsupported`, `TestPreviewReportClassifies404BeforeReadableMetadataAsInsufficientPermission` |
| An empty repository reports empty arrays, every capability `supported`, no gaps, `status` `ok`, and an unreadable section stays textually distinct from an empty one | Task 3 `TestPreviewReportReportsEmptyCollectionsAsSupported` (empty), `TestPreviewReportDegradesOnlyTheDeniedEnvironmentsRead` (unreadable) |
| A Gitea/Forgejo instance answering `404` reports all three capabilities `unsupported` with a gap naming the resource, and does not claim the repository has no previews | Task 3 `TestPreviewReportGiteaInstanceAnswering404IsUnsupported`; Task 2 `TestPreviewForgeGiteaUsesTokenAuthAndVersionedBase` |
| A repository with no declared forge makes no forge request, reports every capability `unsupported` with a gap saying no forge is declared, and reports `degraded` | Task 3 `TestPreviewReportGitOnlyMakesNoRequest` |
| More deployments than the page bound holds: the deployments read are reported, `preview_deployments` stays `supported`, and a gap says the list is not exhaustive | Task 2 `TestPreviewForgeGitHubTruncatesAtThePageBound`; Task 3 `TestPreviewReportRecordsThePageBoundGap` |
| At most 20 deployments, one per environment, statuses read only for those named; more environments records a gap on `preview_deployments` naming the bound | Task 3 `TestPreviewReportNamesTheNewestDeploymentPerEnvironment`, `TestPreviewReportRecordsTheNameCapGap` |
| With no scope resolvable, the read is unfiltered, `ref` and `sha` are empty, and the capability carries the exact detail | Task 3 `TestPreviewReportWithoutAnyScopeIsUnfiltered` |
| A truncated environments read keeps the items, keeps `preview_environments` `supported`, and records a gap on that key | Task 3 `TestPreviewReportRecordsTheEnvironmentsPageBoundGap` |
| A truncated statuses read keeps its items, keeps `preview_statuses` `supported`, and records a gap on that key | Task 3 `TestPreviewReportRecordsTheStatusesPageBoundGap` |
| The probe issues only `GET` requests, reads no local path, and writes nothing anywhere | Task 2 `TestPreviewForgeIssuesOnlyGetRequests`; Task 3 `TestPreviewReportReadsNoLocalPath`; Task 4 `TestPreviewRouteDoesNotPersistState` |
| The credential, request headers and the forge's raw error body appear nowhere in the report, a gap or an error string | Task 2 `TestPreviewForgeNeverEchoesTheCredential` |
| `TestWebIsStdlibOnly` passes and no new import from `marshal/internal/...` appears under `web/` | Task 4 `go test ./web/bridge -run '^(TestPreviewRoute|TestWebIsStdlibOnly)' -count=1` |
| The discovery report's ten keys, their order, its route and its body discipline are unchanged | Task 3 `TestPreviewReportDoesNotChangeTheDiscoveryContract`; Task 1 `TestPreviewCapabilityOrderIsStable` |

## Execution, commits, and PR policy

- Tasks run in order. Each task is committed on its own, with the commit
  message the task names, and no task's commit contains another task's
  files.
- The implementation branch is stacked on this specification branch, so the
  contract can be reviewed independently of the code that serves it.
- Every task ends with a green narrow test run; a task that cannot be
  verified is not committed.
- The implementation PR is opened as a **draft**, reviewed independently with
  `glm-5.3-flash`, and only then marked ready. The branch review record is
  posted on the PR and recorded in the specification's branch review record.
- No forge is contacted by any test: every forge read is against an
  `httptest` loopback server, and no test reads real credentials or needs
  the network.

## Plan self-review

- [x] Every file path and symbol named in this plan exists in the tree, or
      is created by a task here. The `web/bridge/` anchors were checked
      against the worktree at `f8e6e2f7`, and the file-name claim was
      re-checked against `caa8f64c` — which is how the live-preview collision
      behind decision 25 was found.
- [x] Every acceptance criterion in the specification §9 maps to a named
      test in the matrix above.
- [x] The specification's decisions D1–D23 are each reflected in the
      reconciled decisions, and none is re-litigated.
- [x] No task widens `Forge` or `DiscoveryForge`, and no task edits a
      discovery file.
- [x] Every task is independently verifiable with a narrow
      `go test ./web/bridge -run '...' -count=1` command.
- [x] The bounds are stated and sufficient: at most 5 pages of 100 per
      collection, at most 20 named deployments, at most 20 status reads.
- [x] The two planning briefs agree on the route, the read order, the
      five-state vocabulary, the partial-failure model and the one-line
      route registration; the reconciled decisions resolve the rest —
      the capability and gap shapes are reused rather than duplicated
      (decision 3), the read order is fixed in one place (decision 5), and
      each degraded capability contributes exactly one gap (decision 16).

## Branch review record

This plan received an independent `glm-5.3-flash` review through
`ollama-cloud` on 2026-11-13, against three questions: correctness against
the code, fidelity to the specification, and executability task by task. Five
findings were raised; all five are resolved.

| Severity | Finding | Resolution |
|---|---|---|
| Important | The status table and `TestPreviewRouteInternalFailureIs500` require a 500, but no decision defined any failure the assembler can *return* as an error — everything in the read order is classified into the report — so the test had nothing to trigger | Decision 24 states the error contract explicitly: the assembler returns an error only for a request the bridge cannot assemble at all, mirroring `Fleet.DiscoverRepo` (`web/bridge/discovery_report.go:77`–`85`), concretely a registered repository whose `Repo.ID` is empty. The route test reaches it the way the discovery route test does, by seeding a workspace record whose map key exists but whose `Repo.ID` is empty |
| Important | The Gitea/Forgejo `404` acceptance criterion mapped to tests that do not prove it: one asserted only the auth header and API base, the other was a GitHub-shaped `404` on one resource | Task 3 now writes `TestPreviewReportGiteaInstanceAnswering404IsUnsupported`, which points a Gitea `apiBase` at an `httptest` server answering `404` on the three preview paths and asserts that **all three** capabilities are `unsupported` with the D20 wording, that the report does not claim the repository has no previews, and that the status is `degraded`. The acceptance matrix points at it |
| Minor | The reuse list sent the implementer into a classification trap: `discoveryCredential` returns plain `errors.New` values, and the shared classifier maps a plain error to `temporarily_unavailable`, contradicting the specification D15 row | Decision 5 step 2 now states that the credential failure is degraded to `unconfigured` across all three keys **before** any classification call, exactly as `probeForge` does (`web/bridge/discovery_report.go:272`–`277`), and explains why it cannot go through the classifier |
| Minor | The Gitea adapter's request paths were never named, so the adapter was not fully specified | Task 2 now names the three paths — `/repos/{owner}/{repo}/environments`, `/repos/{owner}/{repo}/deployments` and `/repos/{owner}/{repo}/deployments/{id}/statuses` — the natural mirror of the GitHub paths under the instance's versioned API root, and notes that the instance answers `404` |
| Minor | The plan's file map named `web/bridge/preview.go` and `web/bridge/preview_test.go`, which already exist at the base commit holding the unrelated, shipped web-studio live preview proxy | Found by the implementer before it wrote any code, not by the reviewer. Decision 25 renames every file in this slice to `preview_environment*.go`, keeps every type, helper, constant and test name the plan fixes, and narrows the run gates to this slice's own test names so the live-preview tests cannot mask a failure here. The existing files are not modified |
| Minor | A non-numeric deployment id was under-defined: it did not say how it competes within its environment group, nor whether "ordered after the numeric ones" applies before or after the keep-first-20 cut | Decision 10 now says a non-numeric id **loses to any numeric id within its environment group**, so it survives grouping only as the sole member of that group; it is ordered after every numeric survivor and before the keep-first-20 cut, and its status is not read |

The reviewer confirmed that every anchor, helper signature and claimed
behaviour in the plan resolves against the worktree at `f8e6e2f7`; that the
specification's decisions D1–D23 are each carried by a reconciled decision
with none re-litigated; that the reconciled decisions — reusing the discovery
capability and gap shapes, the fixed read order, numeric id comparison
against string wire ids, and one gap per degraded capability — are sound and
consistent with the specification; and that the task order is a valid
dependency order in which no commit contains another task's files.

The reviewer also noted, against the **specification** rather than this
plan, that D6.2's claim that the registered-branch-first order "matches"
`branchHead` (`web/bridge/discovery_report.go:576`) is off: that helper
prefers the forge's default branch first. The plan follows the
specification's own order, so the plan is unaffected; the specification's
wording is corrected in the same commit as this record.

### Implementation review (PR #68)

The implementation was reviewed independently with `glm-5.3-flash` against
this plan's decisions and the specification. Four findings were raised, all
resolved: the missing Task 5 documentation, two gap-detail defects (a false
"not exercised" detail, and a near-empty cause for an unknown forge family),
and three untested branches. The resolutions and their commits are recorded in
the specification's branch review record; the two code fixes are `6d970434`.
