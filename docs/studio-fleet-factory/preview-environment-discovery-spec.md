# Preview and test-environment capability discovery — feature spec

Date: 2026-11-14
Status: spec for the fifth implementation slice of the studio-fleet-factory
archive. No product code is changed by this document. This specification
awaits an independent `glm-5.3-flash` review; see the branch review record.

## 1. Provenance

This is the design output for the **provider-capability discovery** that
[the narration spec §1](factory-narration-spec.md#1-provenance) left open.
That spec records the position precisely:

> Surfacing a repository's existing branch testing, preview deployments or
> test environments in the pull request experience is an **agreed** priority;
> only the refinement of it — showing the links with their provider-reported
> status and associated revision — is a proposal, and its discovery mechanism
> is explicitly open. That capability is therefore **deferred pending the
> provider-capability discovery the proposal leaves open**, not rejected.

Two things are therefore separated, and this slice takes only the first:

| | Status | Owner |
|---|---|---|
| The **discovery mechanism** — how Marshal learns, per forge instance and per credential, what preview and test-environment information exists | **Agreed**, and the open question | This slice |
| The **refined presentation** — showing those links, with their provider-reported status and associated revision, in the pull-request experience | **Proposal**, not approved | A later slice, once the mechanism is settled |

What is approved, from [design-notes §6](design-notes.md#6-forge-experience-and-narrated-engineering-reports)
("Agreed priorities"):

> Where a repository and its Git provider already have branch testing, preview
> deployments, or test environments configured, Marshal should surface those
> capabilities neatly in the PR experience. The user explicitly does not want
> Marshal to build its own separate preview/testing infrastructure. Availability
> depends on the existing repository/provider setup; this is integration and
> presentation of that setup, not a prerequisite Marshal must supply.

And the adapter rule from [design-notes §6](design-notes.md#6-forge-experience-and-narrated-engineering-reports)
("External forge integration — API-first direction approved"):

> Adapters should expose capabilities for each instance and credential set,
> rather than assume a provider name guarantees a complete feature set.
> Distinguish unsupported, unconfigured, insufficient permission and
> temporarily unavailable states. Preserve native provider IDs, URLs, states
> and commit revisions. … Existing preview/test infrastructure remains
> external; this proposal does not add a new preview service.

This slice therefore answers, concretely and in code: **for this forge
instance and this credential, what preview and test-environment information
exists, and how would Marshal read it?** It does not decide where the links
appear, and it does not build any preview infrastructure.

## 2. Summary

A registered repository gains a read-only **preview-environment report**. It
answers, per forge instance and credential, whether the forge exposes
deployment records, deployment statuses and deployment environments at all;
which preview and test environments the repository has configured; which
deployments exist for a requested ref or revision; and, for each reported
deployment, the provider's own status, the preview link, and the revision it
was built from.

Everything the report contains is a value the forge reported. Marshal adds no
inference: a preview link is a link the provider gave, a status is the
provider's own status string, and a revision is the provider's recorded SHA.

## 3. What exists today

Measured on the reviewed stack head `1771e4bb`.

| Piece | Where | State |
|---|---|---|
| Read-only capability report | `web/bridge/discovery_http.go:9` | `POST /api/repos/{id}/discover`; empty body; the repository comes from its registered record, never from the caller |
| Capability vocabulary | `web/bridge/discovery.go:16` | Five states, frozen: `supported`, `unconfigured`, `insufficient_permission`, `temporarily_unavailable`, `unsupported`. The studio switches on these exact strings |
| Capability key list | `web/bridge/discovery.go:133` | **Ten** keys, frozen in order, with `TestDiscoveryCapabilityOrderIsStable` (`web/bridge/discovery_test.go:54`) pinning them. The comment at `web/bridge/discovery.go:130` says the list "must not be reordered or extended" |
| Contextual classifier | `web/bridge/discovery.go:171` | `discoveryCapabilityState(err, metadataReadable)` — the same HTTP-status rules for every section, including the 404-after-readable-metadata rule |
| Per-section failure | `web/bridge/discovery_report.go:254` | `probeForge` degrades one section without touching the others; `discoveryProbeOutcome` (`:476`) prefers the adapter's own message for the sentinels it raises |
| Gap vocabulary | `web/bridge/discovery_report.go:497` | `discoveryFix` turns a state into the fix that would resolve it; `discoveryGapDetail` (`:518`) joins cause and fix |
| Read-only forge probes | `web/bridge/discovery_forge.go:29` | `DiscoveryForge` — metadata, branches, pull requests, checks, check logs. Deliberately a separate optional interface: "discovery must never gain a mutating call, and `Forge` must not widen to carry a probe that only the report needs" |
| Per-family plumbing | `web/bridge/discovery_forge_github.go:20`, `web/bridge/discovery_forge_gitea.go:28` | GitHub defaults to `https://api.github.com`; Gitea/Forgejo requires `apiBase` and reports `unconfigured` without building a request |
| Forge families | `web/bridge/forge.go:141` | `ForgeFor` knows `github` and `gitea` only; `registerRepo` (`web/bridge/repos_http.go:163`) rejects any other forge with "expected github or gitea" |
| Collection bounds | `web/bridge/discovery_forge.go:21` | 100 per page, at most 5 pages; a full final page is `errDiscoveryTruncated`, which keeps the items read and records a gap |
| Report assembly | `web/bridge/discovery_report.go:679` | `report()` orders every slice deterministically and derives `ok` / `degraded` / `unavailable` |
| Forge-side head resolution | `web/bridge/discovery_report.go:576` | `branchHead` picks the forge's default-branch head, falling back to the registered branch |
| Studio-side guess | `web/ui/src/lib/forge.ts` | `forgeCapabilities` guesses `richPRs` and `issueIntake` from the forge name and credential kind alone. It knows nothing about preview environments, and it cannot: it never asks an instance |
| Studio consumption of discovery | — | **None.** `web/ui/src` contains no reference to the discovery route; the report has no client yet |
| Studio pull-request surface | — | **None.** No route in `web/bridge/http.go` exposes forge pull requests; `Forge.ListPRs`/`GetPR` (`web/bridge/forge.go:107`) are used by `web/bridge/reviewbot.go` only |
| Newest-status ordering | `web/bridge/discovery_forge_github.go:287` | `newestGitHubStatusPerContext` keeps the newest status per context, ordering by the status id — the same id-ordering this slice relies on |

There is **no** preview or deployment read anywhere in the bridge. A search of
`web/bridge/*.go` for `deployments` or `environments` returns only prose
about Marshal's own agent deployment. The gap is that Marshal cannot yet say
whether a connected repository has preview environments.

## 4. Provider capability findings

The design record left the discovery mechanism open. This is what the provider
API surfaces actually say, read from the providers' own published descriptions
on 2026-11-13. These are documentation findings, not live-instance
observations: no forge was contacted.

### 4.1 GitHub

| Resource | Endpoint | Query | Response shape |
|---|---|---|---|
| Deployment environments | `GET /repos/{owner}/{repo}/environments` | `per_page`, `page` | An **object**: `{total_count, environments: [...]}` — not a bare array |
| Deployments | `GET /repos/{owner}/{repo}/deployments` | `sha`, `ref`, `task`, `environment`, `per_page`, `page` | A **bare array** of deployment records |
| Deployment statuses | `GET /repos/{owner}/{repo}/deployments/{deployment_id}/statuses` | `per_page`, `page` | A **bare array** of status records |

Fields that carry the preview information:

- A **deployment** records `id`, `sha` (the revision recorded at creation),
  `ref`, `task`, `environment`, `original_environment`, `description`,
  `creator`, `created_at`, `updated_at`, `statuses_url`,
  `transient_environment`, `production_environment`.
- A **deployment status** records `id`, `state`, `environment`,
  `environment_url` ("The URL for accessing your environment" — the preview
  link), `target_url`, `log_url`, `description`, `created_at`, `updated_at`.
  The documented states are `error`, `failure`, `pending`, `in_progress`,
  `queued`, `success`, plus `inactive` for a destroyed environment.
- A **deployment environment** records `id`, `name`, `url`, `html_url`,
  `protection_rules`, `deployment_branch_policy` — the last of which is
  `{protected_branches, custom_branch_policies}`, and the two are mutually
  exclusive.

Scopes, from the providers' own documentation:

- Deployments and deployment statuses are granted by the `repo_deployment`
  scope, "without granting access to repository code"; the `public_repo` and
  `repo` scopes grant it as well.
- The environments endpoint is usable by "anyone with read access to the
  repository"; OAuth app tokens and classic PATs need the `repo` scope for a
  **private** repository.

Two consequences follow, and both are the reason the report is per instance
and per credential rather than per forge family:

1. A credential can read deployments and be refused environments. The two
   reads have different scope requirements, so they can disagree.
2. A GitHub Enterprise Server instance on a version without one of these
   endpoints answers `404`. That is an instance fact, not a family fact.

### 4.2 Gitea and Forgejo

Neither family exposes a deployment or deployment-environment resource. The
Gitea OpenAPI description (`go-gitea/gitea`, `templates/swagger/v1-swagger.generated.json`)
lists 186 repository-scoped paths and none of them is a deployment,
deployment-status or environment path; the word "deployment" does not occur
in the description at all. The Forgejo description
(`forgejo/forgejo`, branch `forgejo`, `templates/swagger/v1_json.tmpl`) lists
159 repository-scoped paths with the same absence.

Gitea and Forgejo do publish a deployment-like link, but through commit
statuses: `CommitStatus` carries `target_url` ("the URL to link to for more
details"). That is the instance's own way of pointing at a deployed
environment — and it is already reported, because the existing Gitea
discovery probe maps a commit status's `target_url` onto `DiscoveryCheck.URL`
(`web/bridge/discovery_forge_gitea.go:207`) and the CI-checks fact carries
that URL in its detail (`web/bridge/discovery_report.go:610`).

So on Gitea and Forgejo the preview link exists but is not a deployment
resource. This is recorded here as a **finding for the later presentation
slice**, not as something this slice turns into a second preview concept:
promoting a commit-status target URL to a "preview environment" would be
Marshal inferring a preview from a generic link, which is the inference
[design-notes §6](design-notes.md#6-forge-experience-and-narrated-engineering-reports)
warns against ("Distinguish detected facts from recommendations").

### 4.3 GitLab

Not supported by this bridge. `ForgeFor` knows `github` and `gitea` only, and
`registerRepo` refuses to register any other forge. There is no GitLab
adapter, so there is nothing to probe. GitLab's own API does expose
environments and deployments; wiring a GitLab adapter is a separate slice
that the approved compatibility scope in
[design-notes §6](design-notes.md#6-forge-experience-and-narrated-engineering-reports)
already anticipates.

### 4.4 The matrix this slice implements

| Forge family | Deployment environments | Deployments | Deployment statuses |
|---|---|---|---|
| GitHub | Probed; `404` means this instance does not serve it | Probed; `404` means this instance does not serve it | Probed; `404` means this instance does not serve it |
| Gitea / Forgejo | Probed; `404` on this family | Probed; `404` on this family | Probed; `404` on this family |
| GitLab | No adapter — the bridge cannot register a GitLab repository | Same | Same |
| No forge declared | No request is made | No request is made | No request is made |

Every cell is determined by **the instance's answer**, not by a table of
family names. The table above records what is expected; the report records
what happened.

## 5. Design

Every decision below is **Decided** and can be challenged by a reviewer.

### 5.1 The surface

**Decided — D1.** The preview-environment report is its own read-only
endpoint:

```http
GET /api/repos/{id}/preview-environments?ref=<branch|tag|sha>&sha=<sha>
Authorization: Bearer <token>   # when the server has a token
```

Both query parameters are optional. The route is a `GET` because it changes
nothing, matching the bridge's other read routes
(`GET /api/repos/{id}/issues` at `web/bridge/http.go:230`). The bearer
wrapper is inherited: every `/api` route is wrapped when the server has a
token (`web/bridge/http.go:90`).

**Decided — D2.** The report is a **separate document** from the discovery
report, and the discovery report's ten frozen capability keys are **not**
extended. Three reasons:

1. The discovery key list is frozen by contract and by
   `TestDiscoveryCapabilityOrderIsStable`. Extending it would amend a
   reviewed, implemented slice for a capability that belongs to a different
   subject.
2. The discovery report is repository-scoped and revision-scoped to the
   *local checkout*'s HEAD. The preview report is scoped to a **forge-side**
   revision the caller names. Merging them would blur which revision a
   preview belongs to — the exact distinction the open proposal turns on.
3. The two reports cost different amounts and fail differently. A caller that
   wants to know "does this repository have preview environments?" should not
   pay for a checkout scan, and a caller that wants a checkout scan should not
   pay for a status fan-out.

The two surfaces are complementary and fetched independently.

**Decided — D3.** The report reuses the discovery contract's vocabulary
verbatim rather than inventing a second one: the same five
`DiscoveryCapabilityState` values, the same `DiscoveryCapability` and
`DiscoveryGap` shapes, the same contextual classifier
(`discoveryCapabilityState`), and the same `ok` / `degraded` / `unavailable`
status boundary. One vocabulary across the bridge; the studio renders both
reports with one set of rules.

The **fix text** is surface-specific, because the discovery contract's
`discoveryFix` reasons about checkout targets and about declaring a forge,
neither of which is the remedy here. The preview surface has its own fix
rules (§5.6).

### 5.2 The probe

**Decided — D4.** The preview read is a **separate optional forge
interface**, mirroring `DiscoveryForge`:

```go
// PreviewForge is the read-only forge surface the preview-environment
// probe uses. It is separate from DiscoveryForge so that adding a preview
// read never widens an existing interface, and so a forge that serves no
// such resource is discovered by asking the instance rather than by a
// family table.
type PreviewForge interface {
    Environments(ctx context.Context, repo Repo, cred Credential) ([]PreviewEnvironment, error)
    Deployments(ctx context.Context, repo Repo, filter PreviewFilter, cred Credential) ([]PreviewDeployment, error)
    DeploymentStatuses(ctx context.Context, repo Repo, deploymentID int64, cred Credential) ([]PreviewDeploymentStatus, error)
}
```

`PreviewForgeFor(repo, client)` builds the per-family adapter, exactly as
`DiscoveryForgeFor` does. Both families implement all three methods; the
Gitea/Forgejo adapter asks the instance, and the instance answers `404`.

The four Go types are named by their wire shape, so §6.4 is their
definition:

| Go type | Carries |
|---|---|
| `PreviewFilter` | The scope: `Ref` and `SHA`, either or both, empty meaning "do not filter on this dimension" |
| `PreviewEnvironment` | One configured environment: the fields of §6.4's `environments[]` row |
| `PreviewDeployment` | One deployment: the fields of §6.4's `deployments[]` row, with the newest status folded in |
| `PreviewDeploymentStatus` | One status record: the provider's `id`, `state`, `environment_url`, `target_url`, `log_url`, `description`, `created_at` and `updated_at` |

**Decided — D4a.** The **newest** status of a deployment supplies that
deployment row's status fields — `state`, `environmentUrl`, `targetUrl`,
`logUrl` and `description`. A field the newest status does not carry is left
empty; it is **not** back-filled from an older status, because an older
status describes an older moment of the environment and reporting it under
the newest deployment's row would misstate the current state.

**Decided — D5.** The preview surface **does not read the local checkout at
all**. It reports what the *forge* knows. The revision a preview belongs to
comes from the forge's own records, so the surface needs no project linkage
and works for a registered repository that has no workspace yet.

**Decided — D6.** The requested scope is resolved in this order:

1. `?ref=` and `?sha=` are both applied when both are given — GitHub
   applies its filters conjunctively, and so does this report.
2. When neither is given, the report uses the **registered branch** of the
   repository (`Repo.Branch`) if it has one, and otherwise the forge's
   **default branch**. This is the registered branch first, which is the
   reverse of `branchHead`
   (`web/bridge/discovery_report.go:576`): that helper resolves a *head
   revision*, preferring the forge's default branch, whereas this resolves a
   *scope to filter by*, and the registered branch is the one the
   repository was connected on.
3. The resolved scope is echoed in the report, so a reader can always see
   what was asked for.
4. **Terminal case.** When no scope resolves at all — neither query
   parameter, no registered branch, and the forge reported no default
   branch — the deployments read is **unfiltered**: the report lists every
   deployment the forge reports, `ref` and `sha` are both empty, and
   `preview_deployments` is `supported` with the detail "no revision scope
   was resolvable; every deployment the forge reports is listed". The
   detail is what distinguishes "unfiltered" from "an empty scope was
   applied", so the report never leaves a reader guessing which happened.

**Decided — D7.** `?ref=` is the primary filter, and each reported
deployment carries its **own** SHA. This is deliberate. The open proposal
wants links shown "with their provider-reported status and associated
revision … so users can tell which change they are trying". A link's
associated revision is only meaningful if more than one revision can appear
under one filter, which is true for a branch filter and false for a SHA
filter. So the report reports both: the filter that was applied, and each
deployment's recorded revision, and leaves the comparison to the reader.

### 5.3 What the report contains

**Decided — D8.** The report is one JSON document:

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
    {"name": "production", "id": "1234567", "htmlUrl": "https://github.com/o/r/deployments/activity_log?environments_filter=production",
     "branchPolicy": "protected_branches", "protectionRuleCount": 2}
  ],
  "deployments": [
    {"id": "99", "environment": "production", "originalEnvironment": "", "ref": "feature/x",
     "sha": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9", "task": "deploy",
     "state": "success", "environmentUrl": "https://preview.example.internal",
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

Field rules:

- `repoId` and `forge` echo the registered repository. `forge` is empty for
  the Git-only path.
- `status` is `ok`, `degraded` or `unavailable`, with the same meanings as
  the discovery report (`web/bridge/discovery_report.go:26`).
- `ref` and `sha` echo the scope that was resolved (§5.2, D6). `sha` is
  empty when the scope was a ref.
- `capabilities` always carries **three** keys, always in this order:
  `preview_environments`, `preview_deployments`, `preview_statuses`. The list
  is frozen for this surface exactly as the discovery list is.
- `environments` lists the **configured** deployment environments the forge
  reports. Each carries the native environment id, the browser URL, the
  branch-policy mode (`protected_branches`, `custom_branch_policies`, or
  empty when neither is reported) and the number of protection rules the
  forge reported.
- `deployments` lists the **deployments** the forge reports for the scope.
  Every field is a value the forge gave. `state` is the provider's own status
  string, verbatim; `environmentUrl` is the provider's "URL for accessing
  your environment"; `sha` is the revision the deployment recorded at
  creation.
- `gaps` carries at most one entry per capability key, as in the discovery
  report.
- `environments` and `deployments` are always present as arrays, never
  `null`, and are empty when the forge reported nothing.

**Decided — D9.** The capability keys are **three**, and each is
independently observable, which is the discovery contract's own rule for
choosing keys:

- `preview_environments` — `GET /repos/{owner}/{repo}/environments`.
- `preview_deployments` — `GET /repos/{owner}/{repo}/deployments`.
- `preview_statuses` — `GET /repos/{owner}/{repo}/deployments/{id}/statuses`.

The third is a separate key because it is a separate read that can fail on
its own: a credential may list deployments and be refused their statuses, and
a deployment may have no statuses at all. Collapsing it into
`preview_deployments` would make "there is no status for this deployment"
indistinguishable from "the status could not be read".

**Decided — D10.** `preview_statuses` is read **per deployment**, and the
report shows the **newest** status of each deployment. A deployment with no
statuses is reported with its `state` empty — that is a completed read of an
empty collection, not a failure. When there are no deployments at all to read
statuses for, `preview_statuses` is `supported` with the detail "not
exercised: no deployment was reported for this scope", which is how the
discovery report distinguishes a section it had nothing to exercise
(`web/bridge/discovery_report.go:378`).

### 5.4 Bounds

**Decided — D11.** The forge-collection bounds are the ones the discovery
surface already uses, unchanged: 100 per page, at most 5 pages
(`web/bridge/discovery_forge.go:21`). A full final page means the forge
reported more than is read. The items read are kept, the capability stays
`supported`, and a gap records that the list is not exhaustive.

**Decided — D12.** The report names **at most 20 deployments** and reads
statuses only for the deployments it names. The pipeline is fixed, in this
order:

1. Read the deployments for the resolved scope, bounded by the page bound
   in D11.
2. Group them by environment name; within a group keep the deployment with
   the greatest id — the newest deployment of that environment, whose status
   describes its current state.
3. Order the survivors by id descending and keep the first 20.
4. Read statuses for those 20 deployments, and no others.

Steps 2 and 3 are separate, and the order matters: the cap is applied to the
**newest deployment per environment**, not to the newest 20 deployments. A
branch with 40 environments therefore yields one row per environment, capped
at the 20 newest environments, rather than 20 rows that might all belong to
one environment.

**Decided — D13.** Whenever a deployment, an environment, or a deployment's
statuses was collected but not named — by the page bound in D11 or the cap in
D12 — the report records a gap naming the bound that was hit: on
`preview_deployments` for the deployments bounds, on `preview_environments` for
the environments bound, and on `preview_statuses` for a statuses bound. The
gap's detail states the bound in the preview surface's own words:

| Bound hit | Gap key | Detail |
|---|---|---|
| The page bound on deployments (D11) | `preview_deployments` | the forge reported more deployments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest |
| The name cap (D12) | `preview_deployments` | the forge reported deployments for more environments than this report names; the newest deployment for each of the most recently updated environments is listed |
| The page bound on environments (D11) | `preview_environments` | the forge reported more environments than this report reads; the list is not exhaustive and a larger page bound would be needed to read the rest |
| The page bound on a deployment's statuses (D11) | `preview_statuses` | the forge reported more statuses for at least one deployment than this report reads; the listed status may not be the newest |

A bound gap's **state** is `temporarily_unavailable`, mirroring the discovery
report's truncation gaps: the forge did answer, so the capability stays
`supported`, and the gap says the answer was not exhaustive.

No bound detail says adapter support would be needed. The adapter exists and
the instance answered; what was not read is a matter of this report's own
bounds, and the detail says which bound.

**Decided — D14.** Ordering is by the provider's own **id**, descending.
Deployment and status ids increase monotonically, and the discovery surface
already relies on that ordering for commit statuses
(`web/bridge/discovery_forge_github.go:251`). Ordering by a parsed timestamp
would need a second clock and a second failure mode, for no gain.

### 5.5 Failures

**Decided — D15.** Every failure is classified by the **shared** contextual
classifier, with the same rules the discovery surface uses:

| Outcome | State |
|---|---|
| The read succeeded | `supported` |
| No usable personal access token credential for this repository | `unconfigured` |
| The instance's `apiBase` is not configured (Gitea/Forgejo) | `unconfigured` |
| HTTP 401 | `unconfigured` |
| HTTP 403 without a retry signal | `insufficient_permission` |
| HTTP 403 with `Retry-After` | `temporarily_unavailable` |
| HTTP 404 **after** repository metadata was readable | `unsupported` |
| HTTP 404 **before** repository metadata was readable | `insufficient_permission` |
| HTTP 429, HTTP 5xx, transport failure, undecodable body | `temporarily_unavailable` |
| Any other 4xx | `unsupported` |

The metadata probe runs **first**, for the same reason it does in the
discovery report: it establishes whether a `404` means "this instance does
not serve this endpoint" or "this credential cannot see this repository".
That ordering is what makes `unsupported` an instance fact rather than a
guess.

**Decided — D16.** A declared forge whose metadata probe fails as
`temporarily_unavailable` produces `status: "unavailable"` with a non-empty
top-level `error`, and every preview capability is
`temporarily_unavailable`. It is a visible outage, never a silent downgrade
to "no previews exist".

**Decided — D17.** The Git-only path — a repository with no declared forge —
makes **no request at all**. Every preview capability is `unsupported`, and
the gap says no forge is declared for this repository. The report never
implies a repository has no previews when it simply was not asked a forge.

**Decided — D18.** One failure degrades one capability, **unless another
read depends on it**. A refused environments read leaves `preview_deployments`
and `preview_statuses` `supported`, and the report's `gaps` names only the
refused key.

Deployment statuses are a sub-resource of deployments, so the dependency
propagates: if the deployments read fails, the instance does not serve the
statuses either, and reporting `preview_statuses` as `supported` would claim a
capability the instance does not have. A failed deployments read therefore
degrades `preview_statuses` with the **same state and cause**, in its own gap,
so each key still carries exactly one gap. The environments read is
independent of the other two, so a refused environments read propagates to
nothing.

Because `preview_statuses` is read per deployment, several status reads can
fail with different causes. The capability and its one gap then carry the
**most actionable** state among them, using the discovery contract's severity
order — `unconfigured` > `insufficient_permission` >
`temporarily_unavailable` > `unsupported`
(`web/bridge/discovery_report.go:533`, `discoveryStateSeverity`) — so the
report names the failure the operator can act on first. A deployment whose
status read failed keeps its row with empty status fields; only the
capability state and the gap reflect the failure.

### 5.6 Gaps and their fixes

**Decided — D19.** A gap's detail states the cause **and** what would
resolve it, in the discovery surface's own shape. The preview surface's fix
rules are:

| State | Fix |
|---|---|
| `unconfigured` | this repository needs a personal access token credential to read preview environments; register or repair one and retry |
| `insufficient_permission` | grant the credential read access to the repository's deployments and environments, and retry |
| `temporarily_unavailable` | the forge could not answer right now; retry later |
| `unsupported` | this forge instance does not expose this resource; no adapter can read what the instance does not serve |

The Git-only path has a different cause — **no forge is declared** — so its
`unsupported` fix is `declare github or gitea for this repository`, not the
instance text above. The instance text would tell the operator to look for a
resource their instance was never asked about.

**Decided — D20.** The `unsupported` fix says the **instance** does not
expose the resource. It does not say "adapter support would be needed", which
is the discovery surface's wording for a forge family with no adapter: here
the adapter exists and the instance answered.

### 5.7 Read-only guarantees

**Decided — D21.** The probe is observational on every path:

- Only `GET` requests reach the forge. No deployment is created, no status
  is posted, no environment is created or edited, and no branch protection
  or deployment policy is changed.
- No local repository, checkout or workspace is read or written. The
  preview surface does not touch the filesystem at all.
- No project, agent, factory, work item or workspace is created; no report
  is persisted; no audit event is emitted.
- No credential, token, request header or raw forge error body appears in
  the report, a gap, or an error string.

**Decided — D22.** The report carries the provider's values **verbatim**:
native ids, native URLs, the provider's own status strings, the provider's
own revision. Marshal does not normalise a status into its own vocabulary
and does not rewrite a URL.

### 5.8 The studio

**Decided — D23.** This slice is **bridge-only**. `web/ui/` is untouched,
exactly as in the discovery slice.

Two facts make this the right boundary rather than a shortcut. First, the
studio has **no** pull-request surface to put links into: no route exposes
forge pull requests, and `web/ui/src` contains no reference to the discovery
route either. Second, the refined presentation is the **unapproved
proposal**; building it now would be implementing a proposal. What this
slice delivers is the mechanism that lets a later slice decide the
presentation honestly.

### 5.9 What this does not change

- The discovery report's contract: its ten keys, their order, its five
  states, its route, its body discipline, its status boundary and its
  read-only guarantees are unchanged.
- `DiscoveryForge` and `Forge`: neither interface widens. `Forge` stays the
  mutating surface; `DiscoveryForge` stays the discovery read surface; the
  preview read is a third, separate, optional interface.
- The `Forge` family's supported set: `github` and `gitea` only.
- The studio's `forgeCapabilities` guess in `web/ui/src/lib/forge.ts`. It
  guesses from a name and a credential kind; the preview report answers from
  an instance. Reconciling the two is a later slice, and this slice does not
  change what the studio guesses.
- The narration contract and the recovery-routing contract.

## 6. The report contract

### 6.1 Route

```http
GET /api/repos/{id}/preview-environments[?ref=<ref>][&sha=<sha>]
```

| Status | Meaning |
|---|---|
| `200` | The report |
| `401` | The server has a token and the request did not carry it |
| `404` | No repository is registered under that id |
| `405` | A method other than `GET` |
| `503` | The server runs without a fleet, so there is no credential store |
| `500` | An unexpected in-process failure. Deliberately not `502`: `writeErr` reserves `502` for child-process failures (`web/bridge/http.go:279`) |

### 6.2 Capability keys

| # | Key | Reads | Gap `section` |
|---|---|---|---|
| 1 | `preview_environments` | The repository's configured deployment environments | `environments` |
| 2 | `preview_deployments` | The deployments recorded for the resolved scope | `deployments` |
| 3 | `preview_statuses` | The provider-reported status of each reported deployment | `statuses` |

### 6.3 Report fields

| Field | Type | Notes |
|---|---|---|
| `repoId` | string | The registered repository's id |
| `forge` | string | `github`, `gitea`, or empty for the Git-only path |
| `status` | string | `ok`, `degraded` or `unavailable` |
| `error` | string | Present only when `status` is `unavailable` |
| `ref` | string | The resolved ref scope; empty when a SHA scope was used |
| `sha` | string | The resolved SHA scope; empty when a ref scope was used |
| `capabilities` | array | The three keys above, always present, always in that order |
| `environments` | array | The configured deployment environments the forge reported |
| `deployments` | array | The deployments the forge reported for the scope |
| `gaps` | array | At most one entry per capability key |

`capabilities`, `environments`, `deployments` and `gaps` are always present
as arrays, never `null`. `gaps` is ordered by the frozen capability-key
order — `preview_environments`, `preview_deployments`, `preview_statuses` —
so the report's arrays line up the way the discovery report's do.

### 6.4 Deployment and environment fields

Every field is a value the provider reported, or empty when the provider did
not report it.

`deployments[]`:

| Field | Provider field | Notes |
|---|---|---|
| `id` | deployment `id` | The native id, as a string so it survives any provider |
| `environment` | deployment `environment` | The environment name the deployment targeted |
| `originalEnvironment` | deployment `original_environment` | The prior name when the environment was renamed |
| `ref` | deployment `ref` | The ref that was deployed |
| `sha` | deployment `sha` | The revision recorded at creation |
| `task` | deployment `task` | The provider's task name, e.g. `deploy` |
| `state` | status `state` | The provider's own status string, verbatim |
| `environmentUrl` | status `environment_url` | The provider's URL for accessing the environment |
| `targetUrl` | status `target_url` | The provider's associated URL |
| `logUrl` | status `log_url` | The provider's deployment-output URL |
| `description` | status `description` | The provider's description |
| `transient` | deployment `transient_environment` | The environment will not exist later |
| `production` | deployment `production_environment` | End users interact with this environment directly |
| `createdAt` | deployment `created_at` | As the provider reported it |
| `updatedAt` | deployment `updated_at` | As the provider reported it |

`environments[]`:

| Field | Provider field | Notes |
|---|---|---|
| `name` | environment `name` | The environment name |
| `id` | environment `id` | The native id |
| `htmlUrl` | environment `html_url` | The provider's browser URL for the environment |
| `branchPolicy` | environment `deployment_branch_policy` | `protected_branches`, `custom_branch_policies`, or empty |
| `protectionRuleCount` | environment `protection_rules` | How many rules the provider reported |

## 7. Missing access and partial failure

- A capability the credential cannot read degrades **that capability only**;
  the others keep their own state and their own facts. The one exception is
  the dependency in D18: a failed deployments read also degrades
  `preview_statuses`, because statuses are a sub-resource of deployments.
- A capability the instance does not serve is `unsupported`, with a gap that
  says so and a fix that does not promise an adapter can help.
- A forge that is entirely unreachable is `unavailable`, with a top-level
  error. That is never reported as "this repository has no previews".
- An empty collection is **not** a failure. A repository with no configured
  environments and no deployments reports empty arrays with every capability
  `supported`. Nothing is inferred from an unreadable target: absence and
  inaccessibility stay textually distinct, as they do in the discovery
  report (§6 of [the discovery spec](existing-project-discovery-spec.md)).

## 8. Open questions

These need the user, or a later slice, not an implementer's judgement.

1. **Where the links appear.** The agreed priority is to surface existing
   preview and test-environment capabilities in the pull-request experience.
   The pull-request experience this bridge has today is the **PR description**
   the factory writes (the narration slice), not a studio page — there is no
   studio pull-request surface yet (§3). Whether the links belong in the
   description, in a studio page, or in both is unresolved.
2. **The associated revision, precisely.** The report gives each deployment
   the revision it recorded, and the scope it was filtered by. Whether the
   studio should compare the two, and how it should word a preview that is
   behind the current head, is a presentation decision.
3. **Gitea and Forgejo.** Their preview links arrive as commit-status target
   URLs, which the CI-checks fact already carries. Whether those should be
   presented as preview links — and if so, whether that is a preview or a
   generic CI link — is a separate decision from this mechanism.
4. **GitLab.** GitLab's API exposes environments and deployments, but the
   bridge has no GitLab adapter at all. Adding one is a separate slice.
5. **The studio's `forgeCapabilities` guess.** It answers from a forge name
   and a credential kind. The preview report answers from an instance. The
   two should eventually be reconciled so the studio stops guessing.

## 9. Acceptance criteria

- A GitHub-wire-compatible endpoint exercised with a `pat` returns a report
  whose `environments` and `deployments` carry the provider's own ids, URLs,
  status strings and revisions, and whose `preview_environments`,
  `preview_deployments` and `preview_statuses` capabilities are `supported`.
- A simulated credential refused with `403` on the environments read yields
  `insufficient_permission` for `preview_environments` with a gap naming the
  cause and the fix, while `preview_deployments` and `preview_statuses` stay
  `supported` and their facts remain in the report.
- A `404` from an instance that serves no such resource, **after** its
  repository metadata was readable, yields `unsupported` — not
  `insufficient_permission` — and the same instance answering `404` **before**
  its metadata was readable yields `insufficient_permission`.
- A repository with no configured environments and no deployments reports
  empty `environments` and `deployments` arrays with every capability
  `supported` and no gaps; the status is `ok`. An unreadable section and an
  empty section are textually distinct in the response.
- A Gitea/Forgejo-shaped instance that answers `404` on the deployment
  resources reports `unsupported` for all three capabilities with a gap naming
  the resource, and the report does not claim the repository has no previews.
- A repository with no declared forge makes **no** forge request, reports
  every preview capability `unsupported` with a gap saying no forge is
  declared, and reports `status: "degraded"`.
- A forge reporting more deployments than the page bound holds reports the
  deployments it read, keeps `preview_deployments` `supported`, and records a
  gap saying the list is not exhaustive.
- The report names at most 20 deployments, one per environment — the newest
  deployment of each environment — and reads statuses only for the
  deployments it names; a ref with more environments than that records a gap
  on `preview_deployments` naming the bound that was hit.
- When no revision scope resolves at all, the deployments read is unfiltered,
  `ref` and `sha` are both empty, and `preview_deployments` carries the
  detail "no revision scope was resolvable; every deployment the forge
  reports is listed".
- The probe issues only `GET` requests to the forge, reads no local path, and
  writes nothing anywhere.
- The credential, any request header, and the forge's raw error body appear
  nowhere in the report, in a gap, or in an error string.
- `TestWebIsStdlibOnly` (`web/bridge/boundary_test.go`) continues to pass,
  and no new import from `marshal/internal/...` appears under `web/`.
- The discovery report's ten capability keys, their order, its route and its
  body discipline are unchanged, and `TestDiscoveryCapabilityOrderIsStable`
  still passes unmodified.

## 10. Non-goals

- Building, running, or managing any preview or test environment. Marshal
  creates no deployment, posts no deployment status, and creates no
  environment.
- The pull-request experience itself: no studio page, no PR-description
  section, no link rendering. That is the deferred proposal.
- Promoting a commit-status target URL to a preview environment.
- A GitLab adapter, or any change to the set of forge families the bridge
  supports.
- Extending the discovery report, its ten frozen keys, or either forge
  interface.
- Any write, anywhere: to a forge, to a repository, to a checkout, to the
  workspace, or to the report store.
- Persisting a report, caching one, or polling for freshness. Polling
  policy remains open in the design record.

## Implementation record

Delivered on branch `studio/preview-environment-discovery-implementation` as
Tasks 1–4 of the
[plan](plans/2026-11-14-preview-environment-discovery-plan.md), test-first,
with one commit per task, followed by two commits that close the gaps the
implementation review found.

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
provider. The whole-repository suite is run by the coordinator separately, not
by this slice.

## Branch review record

This specification received an independent `glm-5.3-flash` review through
`ollama-cloud` on 2026-11-13, against two questions: fidelity to the recorded
approved decisions, and implementability against the real code. Seven findings
were raised; all seven are resolved.

| Severity | Finding | Resolution |
|---|---|---|
| Important | D12 and D13 left the order of the two bounds unstated, so cap-then-dedup and dedup-then-cap both read consistently and produce different reports; the gap trigger was also worded as "environments" where a ref has deployments | D12 now fixes the pipeline in four ordered steps — read, group by environment keeping the greatest id, order by id descending, keep the first 20 — and states that the cap applies to the newest deployment **per environment**, not to the newest 20 deployments. D13 gives the two bound-hit details, and the §9 criterion was reworded |
| Minor | With statuses read per deployment, several reads can fail with different causes, and the spec never said which state the one capability and its one gap carry | D18 now folds several failures into the most actionable state, using the discovery contract's severity order (`web/bridge/discovery_report.go:533`) |
| Minor | The D8 example gap reused the discovery surface's fix text verbatim, including "or process" — vocabulary this surface does not use, since it never reads the filesystem | The example now uses D19's own `insufficient_permission` fix |
| Minor | Reusing the discovery truncation wording carried its "adapter support would be needed" tail, which contradicts D20: the adapter exists and the instance answered | D13 now states both truncation details in the preview surface's own words, naming the bound that was hit and promising no adapter work |
| Minor | D4 named four Go types it never defined; `PreviewFilter`'s contents were unstated; there was no rule for which status supplies a deployment row's fields; the gap `section` values and the `gaps` presence and ordering were unstated | D4 now names the four types by their wire shape, D4a states that the newest status supplies the row's status fields and that an absent field is left empty rather than back-filled, §6.2 gives each key's gap `section`, and §6.3 states that all four arrays are always present and that `gaps` follows the frozen key order |
| Minor | D6's scope resolution had no terminal case, so "no scope was resolvable" was indistinguishable from "an empty scope was applied" | D6 adds the terminal case: the read is unfiltered, `ref` and `sha` are empty, and the capability carries the detail "no revision scope was resolvable; every deployment the forge reports is listed". A matching §9 criterion was added |
| Minor | Two cited anchors drifted from the code they claimed | `web/bridge/discovery_forge_gitea.go:195` → `:207` (the `target_url` mapping) and `web/bridge/discovery_forge_github.go:263` → `:251` (the status-id ordering) |

The reviewer confirmed the two boundaries this slice must not cross: the
specification stays on the approved side of the agreed/proposal line in §1,
and the §4 provider claims — the three GitHub endpoints and their response
shapes, the scope requirements, and the absence of any deployment or
environment resource in the Gitea and Forgejo API descriptions — check out
against the providers' own published descriptions.

### Implementation review (PR #68)

The implementation received an independent `glm-5.3-flash` review through
`ollama-cloud` on 2026-11-13, against the contract above: whether it matches
the specification's decisions, whether it is correct, whether it is well
tested, and whether it disturbs anything else. Four findings were raised; all
four are resolved.

| Severity | Finding | Resolution |
|---|---|---|
| Important | Plan Task 5 was not executed: the implementation record and the archive README entry were missing, so nothing in the tree traced the §9 acceptance criteria to a gate | Task 5 is this documentation commit: both implementation records and the README entry name the commits and the acceptance gates |
| Minor | `preview_statuses` could report "not exercised: no deployment was reported for this scope" when deployments *were* reported but none carried an id the status URL could use, so the detail was false | The two cases now read differently: an empty deployment list keeps the original detail, and a non-empty list whose ids are all unusable reports "not exercised: no deployment carried a usable id" (`6d970434`) |
| Minor | An unknown forge family degenerated to a near-empty gap cause, `forge "gitlab"`, because the wrapped sentinel was re-classified | The cause is now written explicitly, as the discovery surface's equivalent path does: "forge %q has no read-only preview adapter in this bridge" (`6d970434`) |
| Minor | Three behaviours the contract fixes carried no test: a status fan-out where one read fails and another is truncated, the name cap and the deployments page bound hitting together, and the non-numeric-id rule | Three fixture-driven tests now cover them: `TestPreviewReportStatusFailureKeepsTheKeyAgainstTheBound`, `TestPreviewReportDeploymentsBoundsTogetherKeepThePageBound` and `TestPreviewReportNonNumericIDLosesWithinItsEnvironment` (`6d970434`) |

The reviewer confirmed that every specification decision D1–D24 and plan
decision 1–25 is implemented as decided; that the `404`-after-readable-metadata
(`unsupported`) versus `404`-before (`insufficient_permission`) distinction is
threaded into every read including the per-deployment status reads; that no
credential, request header or raw forge error body can reach the report, a gap
or an error string; that the probe issues only `GET`s, reads no local path and
writes nothing; that the resource bounds hold (≤5 pages of 100 per
collection, ≤20 named deployments, ≤20 status reads); and that the diff touches
exactly the slice's files plus one route-registration line in `http.go`, leaving
`Forge`, `DiscoveryForge`, the discovery report's ten frozen keys and the
shipped live-preview proxy untouched.
