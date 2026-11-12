# Existing-project read-only discovery implementation plan

Date: 2026-11-12
Status: approved for autonomous execution by the user's implementation request.
Base: `a710903a` (`docs: specify existing-project discovery slice`).
Specification: [existing-project-discovery-spec.md](../existing-project-discovery-spec.md).

**Goal:** deliver the first gap-1 slice from the studio-fleet-factory archive: a
read-only, authenticated bridge endpoint that reports what a registered forge
and credential can access, what can be detected in an existing local checkout,
what is inaccessible, and which setup options Marshal recommends—without
creating projects or agents, changing repository configuration, or modifying
the checkout.

## Scope and boundaries

This slice adds:

- `POST /api/repos/{id}/discover` for an already-registered repository;
- a per-instance/per-credential capability report with exactly five states;
- read-only GitHub and Gitea/Forgejo metadata probes;
- explicit, bounded inspection of an existing project-linked checkout;
- read-only local Git state (revision, branch, dirtiness);
- detected facts, actionable gaps, and advisory recommendations in one report.

It does not add a planning UI, create a Marshal project, clone or fetch a
repository, execute setup/build/install commands, start an agent, create a
factory, persist discovery state, apply a recommendation, or widen the
existing `Forge` interface.

All new Go code stays under `web/bridge/` and imports only the standard
library. `web/ui/` is untouched.

## Reconciled implementation decisions

The two Minimax planning briefs agreed on the route, local-only checkout rule,
five capability states, partial-failure model, and one-line route-registration
change. The following points resolve their differences.

1. **Checkout source.** Use only `Workspace.ProjectRootByRepo(repo.ID)`. If no
   project links the repository, checkout capabilities are `unconfigured` and
   the report says why. Do not clone, fetch, create a checkout, use a bare
   mirror, or persist a new path field. A repository URL is not treated as a
   local checkout.
2. **Supported-forge outage.** A total transport failure against a declared
   supported forge produces HTTP 200 with `status: "unavailable"`, a non-empty
   top-level `error`, forge capabilities in `temporarily_unavailable`, and
   actionable gaps. Locally observable checkout facts remain in the report.
   This is a visible error state, never a silent downgrade to Git-only. A
   per-section 403/404/429/5xx is a degraded section, not a total outage.
3. **Capability set.** Use the ten keys below, always present and always in
   this order. They map directly to the specification's independently
   observable reads; `issues`, `reviews`, and previews are not invented.
4. **Report status.** `unavailable` means a declared forge could not complete
   its orchestration because the service was temporarily unreachable; the
   report still preserves locally observable checkout facts and carries a
   top-level error. Otherwise, `degraded` means at least one capability is
   not `supported` or at least one gap exists; `ok` means every capability is
   supported and no gaps exist. A Git-only report is therefore `degraded`
   because its forge capabilities are `unsupported`.
5. **Capability state mapping.** Classification is contextual, not a blanket
   HTTP-status table. An absent, non-PAT, invalid, or expired credential is
   `unconfigured`; a declared Gitea/Forgejo instance with no `APIBase` is also
   `unconfigured` with an actionable detail. HTTP 401 is `unconfigured`. HTTP
   403 with a retry signal is `temporarily_unavailable`; HTTP 403 without one
   is `insufficient_permission`. HTTP 404 is `insufficient_permission`
   unless the same credential has already read repository metadata, in which
   case it is interpreted as `unsupported` for this adapter/instance. HTTP
   429, 5xx, and transport failures are `temporarily_unavailable`; a
   successful probe is `supported`. A capability with no matching resource to
   exercise is `supported` with a detail that says it was not exercised.
6. **Absent versus unreadable.** Absence is emitted only after a successful
   search: a detected fact with `value: "absent"`. An unreadable target is a
   gap with no absence fact. The two outcomes must be textually distinct.
7. **File reads.** Known instruction, documentation, and manifest targets are
   opened through `io.LimitReader` with a 1 MiB limit. The implementation reads
   at most one byte beyond that limit to detect overflow; content beyond the
   limit is treated as an unreadable inspection target and reported as a gap,
   and contents are never returned. Manifests are inspected only for the
   minimum fields needed to identify language/tooling. No arbitrary tree walk
   and no symlink following occur.
8. **Revision.** Forge facts carry the default-branch head observed during
   the probe; checkout facts carry the checkout `HEAD`. The top-level
   `revision` is the checkout `HEAD` when a checkout was inspected, otherwise
   the forge default-branch head, otherwise empty. This makes a stale forge
   fact detectable when the default branch advances without requiring a
   forbidden fetch.
9. **Report errors.** Unknown repository is 404; missing fleet is 503;
   malformed request is 400; an unexpected in-process discovery failure is
   500. A declared forge outage is a successful report whose
   `status`/`error` visibly say it failed, because checkout discovery remains
   independently useful.
10. **No persistence or audit event.** Discovery is observational. It does not
    rewrite the workspace, bump its version, or create an audit record.

## HTTP and JSON contract

### Request

```http
POST /api/repos/{id}/discover
Authorization: Bearer <token>   # when the server has a token
Content-Length: 0
```

The body is empty. The repository id comes from the registered `Repo` record.

### Response

```json
{
  "repoId": "repo-1",
  "forge": "github",
  "status": "ok",
  "error": "",
  "revision": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
  "branch": "main",
  "worktree": {
    "path": "/Users/alecpullen/projects/example",
    "branch": "main",
    "head": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
    "dirty": false,
    "dirtyFiles": []
  },
  "capabilities": [
    {"key": "repository_metadata", "state": "supported", "detail": ""},
    {"key": "branches", "state": "supported", "detail": ""},
    {"key": "pull_requests", "state": "supported", "detail": ""},
    {"key": "ci_checks", "state": "supported", "detail": ""},
    {"key": "ci_logs", "state": "supported", "detail": "no failing check to exercise"},
    {"key": "checkout_instructions", "state": "supported", "detail": ""},
    {"key": "checkout_docs", "state": "supported", "detail": ""},
    {"key": "checkout_tooling", "state": "supported", "detail": ""},
    {"key": "checkout_environment", "state": "supported", "detail": ""},
    {"key": "checkout_ci_definition", "state": "supported", "detail": ""}
  ],
  "facts": [
    {
      "origin": "detected",
      "section": "instructions",
      "kind": "instructions",
      "value": "AGENTS.md",
      "source": "checkout:AGENTS.md",
      "revision": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
      "size": 9120,
      "detail": ""
    }
  ],
  "recommended": [
    {
      "origin": "recommended",
      "section": "environment",
      "kind": "environment",
      "value": "Define a development environment",
      "reason": "no recognised environment definition was found"
    }
  ],
  "gaps": [
    {
      "key": "ci_checks",
      "section": "ci",
      "state": "insufficient_permission",
      "detail": "credential lacks permission to read CI checks"
    }
  ]
}
```

All slices are non-nil. `status` is `ok`, `degraded`, or `unavailable` under
the explicit boundary in Reconciled implementation decision 4.
`worktree.dirty` is a pointer: `null` means not determined, which differs
from a known clean checkout. `worktree.branch` is empty for a detached HEAD,
while `worktree.head` still carries its SHA and `worktree.detail` says
`detached HEAD`. `dirtyFiles` is capped at 100 entries; when truncated,
`worktree.detail` states how many additional entries were omitted.

### Capability order and meanings

| Key | Meaning |
|---|---|
| `repository_metadata` | default branch, visibility, description, topics, language, size |
| `branches` | branch names and default-branch head revision |
| `pull_requests` | open PR/MR number, state, title, and head revision |
| `ci_checks` | check/workflow names, conclusions, status, URL, and identity |
| `ci_logs` | availability of detailed logs for failing checks |
| `checkout_instructions` | `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.cursorrules`, `README*` |
| `checkout_docs` | `docs/README.md`, `docs/index.md`, `docs/INDEX.md` |
| `checkout_tooling` | `go.mod`, `package.json`, `pyproject.toml`, `Cargo.toml`, `tsconfig.json` |
| `checkout_environment` | `.devcontainer/`, `Dockerfile`, `docker-compose.yml`, `compose.yaml` |
| `checkout_ci_definition` | `.github/workflows/*.yml` and `*.yaml` |

### Shared Go types

Task 1 owns these names verbatim. Later tasks consume them without renaming.

```go
type DiscoveryCapabilityState string

const (
    DiscoveryCapabilitySupported            DiscoveryCapabilityState = "supported"
    DiscoveryCapabilityUnconfigured         DiscoveryCapabilityState = "unconfigured"
    DiscoveryCapabilityInsufficientPermission DiscoveryCapabilityState = "insufficient_permission"
    DiscoveryCapabilityTemporarilyUnavailable DiscoveryCapabilityState = "temporarily_unavailable"
    DiscoveryCapabilityUnsupported          DiscoveryCapabilityState = "unsupported"
)

type DiscoveryCapability struct {
    Key    string                   `json:"key"`
    State  DiscoveryCapabilityState `json:"state"`
    Detail string                   `json:"detail,omitempty"`
}

type DiscoveryFact struct {
    Origin   string `json:"origin"`
    Section  string `json:"section"`
    Kind     string `json:"kind"`
    Value    string `json:"value"`
    Source   string `json:"source"`
    Revision string `json:"revision,omitempty"`
    Size     int64  `json:"size,omitempty"`
    Detail   string `json:"detail,omitempty"`
}

type DiscoveryRecommendation struct {
    Origin  string `json:"origin"`
    Section string `json:"section"`
    Kind    string `json:"kind"`
    Value   string `json:"value"`
    Reason  string `json:"reason"`
}

type DiscoveryGap struct {
    Key     string                   `json:"key"`
    Section string                   `json:"section"`
    State   DiscoveryCapabilityState `json:"state"`
    Detail  string                   `json:"detail"`
}

type DiscoveryWorktree struct {
    Path       string   `json:"path"`
    Branch     string   `json:"branch,omitempty"`
    Head       string   `json:"head,omitempty"`
    Dirty      *bool    `json:"dirty"`
    DirtyFiles []string `json:"dirtyFiles,omitempty"`
    Detail     string   `json:"detail,omitempty"`
}

type DiscoveryReport struct {
    RepoID       string                   `json:"repoId"`
    Forge        string                   `json:"forge"`
    Status       string                   `json:"status"`
    Error        string                   `json:"error,omitempty"`
    Revision     string                   `json:"revision,omitempty"`
    Branch       string                   `json:"branch,omitempty"`
    Worktree     *DiscoveryWorktree       `json:"worktree,omitempty"`
    Capabilities []DiscoveryCapability    `json:"capabilities"`
    Facts        []DiscoveryFact          `json:"facts"`
    Recommended  []DiscoveryRecommendation `json:"recommended"`
    Gaps         []DiscoveryGap           `json:"gaps"`
}

type DiscoveryRepositoryMetadata struct {
    DefaultBranch string
    Visibility    string
    Description   string
    Topics        []string
    Language      string
    SizeBytes     int64
}

type DiscoveryBranch struct {
    Name string
    SHA  string
}

type DiscoveryPullRequest struct {
    Number  int
    State   string
    Title   string
    HeadSHA string
}

type DiscoveryCheck struct {
    Name       string
    Conclusion string
    Status     string
    URL        string
    AppID      int64
    Context    string
    JobID      int64
}
```

The classifier is contextual because HTTP 404 alone cannot distinguish an
unsupported endpoint from a resource hidden from the credential. Task 1 owns
this signature and the sentinel errors:

```go
var errDiscoveryUnconfigured = errors.New("bridge: discovery capability is unconfigured")
var errDiscoveryUnsupported = errors.New("bridge: discovery capability is unsupported")

func discoveryCapabilityState(err error, metadataReadable bool) (DiscoveryCapabilityState, string)
```

`metadataReadable` is true only after the same credential has successfully
read repository metadata. The classifier uses `errors.As` with the existing
`*forgeAPIError`, maps 401 to `unconfigured`, maps a 403 carrying
`Retry-After` to `temporarily_unavailable` and other 403 responses to
`insufficient_permission`, maps 404 to `unsupported` only when
`metadataReadable` is true and otherwise to `insufficient_permission`, and
maps 429, 5xx, and transport failures to `temporarily_unavailable`.
`errDiscoveryUnconfigured` and `errDiscoveryUnsupported` are handled before
HTTP classification.

## File and dependency map

All paths are relative to the repository root.

| Task | Files | Depends on |
|---|---|---|
| 1 | `web/bridge/discovery.go`, `web/bridge/discovery_test.go` | — |
| 2 | `web/bridge/discovery_forge.go`, `web/bridge/discovery_forge_github.go`, `web/bridge/discovery_forge_gitea.go`, `web/bridge/discovery_forge_test.go` | 1 |
| 3 | `web/bridge/discovery_checkout.go`, `web/bridge/discovery_checkout_test.go` | 1 |
| 4 | `web/bridge/discovery_report.go`, `web/bridge/discovery_report_test.go` | 1–3 |
| 5 | `web/bridge/discovery_http.go`, `web/bridge/discovery_http_test.go`; one line in `web/bridge/http.go` | 4 |
| 6 | specification/README updates and acceptance coverage | 1–5 |

The implementation tasks are sequential: they compile as one Go package and
share one frozen contract. Do not run two implementers against `web/bridge`
at the same time.

## Task 1: Freeze the report contract and state classifier

**Files:** create `web/bridge/discovery.go` and `web/bridge/discovery_test.go`.

- [ ] Write `TestDiscoveryCapabilityStatesAreExactlyFive`: pin the five state
  strings and reject additions/removals.
- [ ] Write `TestDiscoveryCapabilityOrderIsStable`: pin the ten keys and their
  order from the table above.
- [ ] Write `TestDiscoveryCapabilityStateForForgeErrors`: table-drive nil,
  HTTP 401, 403 without a retry signal, 403 with `Retry-After`, 404 with
  `metadataReadable` false, 404 with `metadataReadable` true, 429, 500,
  transport failure, `errDiscoveryUnconfigured`, and
  `errDiscoveryUnsupported` to the agreed state.
- [ ] Write `TestDiscoveryReportJSONUsesStableShape`: assert non-nil slices,
  the `dirty` tri-state, and absence of a credential literal.
- [ ] Run
  `go test ./web/bridge -run '^TestDiscovery(Capability|ReportJSON)' -count=1`;
  confirm the new tests fail because the contract does not exist.
- [ ] Implement the shared types and constants above, the capability-order
  slice, the two sentinel errors, and
  `discoveryCapabilityState(err error, metadataReadable bool)` using
  `errors.As` with the existing `*forgeAPIError`. Do not add methods to
  `Forge` and do not touch persisted structures.
- [ ] Run
  `go test ./web/bridge -run '^TestDiscovery(Capability|ReportJSON)' -count=1`
  and `gofmt -l web/bridge/discovery.go web/bridge/discovery_test.go`; require
  PASS and no output.
- [ ] Commit only this task: `feat(discovery): define capability report contract`.

## Task 2: Add read-only GitHub and Gitea discovery adapters

**Files:** create `web/bridge/discovery_forge.go`,
`web/bridge/discovery_forge_github.go`,
`web/bridge/discovery_forge_gitea.go`, and
`web/bridge/discovery_forge_test.go`.

Use an optional interface; do not widen `Forge`:

```go
type DiscoveryForge interface {
    RepositoryMetadata(ctx context.Context, repo Repo, cred Credential) (DiscoveryRepositoryMetadata, error)
    Branches(ctx context.Context, repo Repo, cred Credential) ([]DiscoveryBranch, error)
    PullRequests(ctx context.Context, repo Repo, cred Credential) ([]DiscoveryPullRequest, error)
    Checks(ctx context.Context, repo Repo, ref string, cred Credential) ([]DiscoveryCheck, error)
    CheckLogs(ctx context.Context, repo Repo, check DiscoveryCheck, cred Credential) (string, error)
}
```

- [ ] Write `TestDiscoveryForgeGitHubReportsRepositoryMetadata`: an httptest
  server returns GitHub's repository shape; assert default branch, visibility,
  description, topics, language, byte size, Bearer auth, and API-base use.
- [ ] Write `TestDiscoveryForgeGitHubListsBranchesPullRequestsAndChecks`: pin
  the three paths and normalize branch SHA, PR number/state/title/head, and
  check name/conclusion/status/URL/App ID.
- [ ] Write `TestDiscoveryForgeGitHubReadsCheckLog`: reuse the existing
  Actions job/log shape and bound returned content to `checkLogLimit`.
- [ ] Write `TestDiscoveryForgeGiteaUsesTokenAuthAndVersionedBase`: register
  `APIBase` as `https://host/api/v1` — the versioned prefix is already part of
  the base, exactly as `giteaForge.apiBase` returns it — and assert
  `Authorization: token ...`, request paths of `<APIBase>/repos/{owner}/{name}`
  and its sub-resources, and Gitea's status context identity.
- [ ] Write `TestDiscoveryForgeGiteaWithoutAPIBaseIsUnconfigured`: a declared
  Gitea repo with an empty `APIBase` returns `errDiscoveryUnconfigured` with a
  detail naming the instance API base, and makes no HTTP request.
- [ ] Write `TestDiscoveryForgeNeverEchoesCredential`: make every endpoint
  fail and assert the PAT is absent from errors and responses.
- [ ] Run
  `go test ./web/bridge -run '^TestDiscoveryForge' -count=1`; confirm failure.
- [ ] Implement the optional interface and the two adapters in new files.
  Reuse `parseOwnerRepo`, `doJSON`, `hardenedGitArgs`, and the existing
  auth/base conventions. Use `Repo.APIBase` for deterministic tests. A 404
  is returned to the classifier; the adapter does not silently omit a
  capability. A Gitea/Forgejo repo with no `APIBase` returns
  `errDiscoveryUnconfigured` rather than issuing a request to an empty base.
- [ ] Run `go test ./web/bridge -run '^TestDiscoveryForge' -count=1`; require
  PASS. Run `gofmt -l` on the four task files; require no output.
- [ ] Commit only this task: `feat(discovery): read forge metadata without mutation`.

## Task 3: Inspect an existing checkout without changing it

**Files:** create `web/bridge/discovery_checkout.go` and
`web/bridge/discovery_checkout_test.go`.

- [ ] Write `TestInspectCheckoutFindsKnownSetup`: a temporary tree containing
  instruction, docs, manifest, environment, and workflow files yields the
  expected sections and `checkout:<relative-path>` sources.
- [ ] Write `TestInspectCheckoutBoundsEveryFileRead`: an oversized instruction,
  documentation, or manifest target produces a gap and no fact rather than
  being read or decoded without a limit.
- [ ] Write `TestInspectCheckoutDistinguishesAbsentFromUnreadable`: an absent
  instruction produces an `absent` fact and no gap; a directory named
  `AGENTS.md` or an unreadable known file produces a gap and no absence fact.
- [ ] Write `TestInspectCheckoutDoesNotFollowSymlinks`: a symlink to an
  outside secret and a symlinked directory produce no outside source.
- [ ] Write `TestInspectCheckoutDoesNotExecuteSetup`: marker-producing
  Makefile, package script, and devcontainer command remain unexecuted.
- [ ] Write `TestDiscoveryGitStateIsReadOnly`: on a local repository, capture
  the working-tree file set (excluding `.git/`), hashes, modes, `HEAD`, and
  porcelain status before and after; assert byte identity, clean state, and
  detached-state handling. The test compares working-tree content rather than
  Git's internal cache files.
- [ ] Write `TestDiscoveryGitStateReportsDirtyWithoutRepair`: modify one
  tracked file and add one untracked file; assert dirty status and unchanged
  working-tree bytes/status afterwards. Assert the allowed git verbs are only
  `rev-parse`, `symbolic-ref`, `status`, and `for-each-ref`.
- [ ] Write `TestDiscoveryGitStateBoundsDirtyFilesAndRepresentsDetachedHead`:
  create more than 100 dirty entries and assert the list is capped with the
  omitted count in `worktree.detail`; use a detached checkout and assert
  `branch` is empty, `head` is present, and `detail` says `detached HEAD`.
- [ ] Run
  `go test ./web/bridge -run '^(TestInspectCheckout|TestDiscoveryGitState)' -count=1`;
  confirm failure.
- [ ] Implement bounded inspection of the exact known paths. Use `os.Lstat`
  and do not follow symlinks. Open every known instruction, documentation,
  and manifest target through `io.LimitReader` with a 1 MiB limit (read at
  most 1 MiB plus one byte so exceeding the limit is detectable); report an
  oversized target as a gap
  and return no content. Read only the minimum manifest fields needed for
  language/tooling facts. Use the existing hardened git runner with an empty
  `Credential{}` for every local read, so no forge PAT is placed in the child
  environment, for `rev-parse HEAD`, `symbolic-ref --quiet --short HEAD`,
  `--no-optional-locks status --porcelain --untracked-files=all`, and
  `for-each-ref --format=%(refname:short) refs/heads`. Represent a detached
  HEAD with an empty branch, its SHA in `head`, and `detached HEAD` in
  `detail`. Cap `dirtyFiles` at 100 and state the omitted count in `detail`;
  when detached HEAD and truncation apply together, `detail` contains both
  clauses. Never use fetch, checkout, switch, reset, clean, clone, pull, push,
  or write git commands.
- [ ] Run the named tests; require PASS. Run `gofmt -l` on the two task files;
  require no output.
- [ ] Commit only this task: `feat(discovery): inspect existing checkouts read-only`.

## Task 4: Assemble capability, fact, gap, and recommendation sections

**Files:** create `web/bridge/discovery_report.go` and
`web/bridge/discovery_report_test.go`.

- [ ] Write `TestDiscoverReportReportsSupportedGitHubWithPAT`: use httptest
  plus a registered PAT repo; assert supported forge capabilities, checkout
  facts, non-empty revision, and no credential leakage.
- [ ] Write `TestDiscoverReportKeepsCheckoutFactsWhenForgeUnavailable`: make
  the fake API fail at the transport layer; assert HTTP-level orchestrator
  success, `status: unavailable`, a visible error, temporary-unavailable forge
  capabilities/gaps, and preserved checkout facts.
- [ ] Write `TestDiscoverReportDegradesOnlyTheDeniedSection`: return 403 for
  CI checks only; assert CI is `insufficient_permission` with an actionable
  gap, report status is `degraded`, and repository, branch, PR, and checkout
  sections remain present.
- [ ] Write `TestDiscoverReportClassifiesHiddenResourceAsPermissionDenied`:
  make repository metadata readable but a section endpoint return 404; assert
  that section is `insufficient_permission`, not `unsupported`, because the
  same credential has not proved that endpoint is genuinely unsupported.
- [ ] Write `TestDiscoverReportClassifiesMissingGiteaBaseAsUnconfigured`: a
  declared Gitea repo with no `APIBase` reports `unconfigured` with an
  actionable detail and makes no HTTP request.
- [ ] Write `TestDiscoverReportGitOnlyUsesLocalFacts`: with no declared forge,
  assert forge capabilities are unsupported, no HTTP request occurs, and
  local Git/checkout facts remain.
- [ ] Write `TestDiscoverReportDistinguishesAbsentFromUnreadable`: assert the
  serialized responses are textually distinct and absence is never inferred
  from a gap.
- [ ] Write `TestDiscoverReportRecommendationsAreAdvisory`: assert a missing
  environment may produce a recommendation, but no repository file or project
  setting is created or changed.
- [ ] Write `TestDiscoverReportRevisionAdvancesWithDefaultBranch`: use a fake
  GitHub branch response whose SHA changes; assert the forge fact revision
  changes while no fetch occurs.
- [ ] Run
  `go test ./web/bridge -run '^TestDiscoverReport' -count=1`; confirm failure.
- [ ] Implement `func (f *Fleet) DiscoverRepo(ctx context.Context, repo Repo) (*DiscoveryReport, error)` in the new file. Resolve the checkout only through
  `f.ws.ProjectRootByRepo(repo.ID)`. Resolve a credential only for a declared
  forge. Probe repository metadata first and pass whether that same credential
  read it into `discoveryCapabilityState` for later sections. Probe each forge
  section independently, map failures to gaps, and never convert a declared
  forge failure into Git-only. Derive recommendations only from successfully
  inspected sections. Set top-level `revision` from the checkout `HEAD` first,
  then the forge default-branch head. Sort facts, recommendations, and gaps
  for deterministic output; keep the fixed capability order.
- [ ] Run the named tests; require PASS. Run `gofmt -l` on the two task files;
  require no output.
- [ ] Commit only this task: `feat(discovery): assemble partial access reports`.

## Task 5: Expose the authenticated discovery route

**Files:** create `web/bridge/discovery_http.go` and
`web/bridge/discovery_http_test.go`; modify `web/bridge/http.go` by adding
one route-registration line.

- [ ] Write `TestDiscoverRouteReturnsCapabilitiesAndReportTogether`: POST
  returns 200, JSON content type, the repository id, all ten capabilities,
  facts, recommendations, and gaps.
- [ ] Write `TestDiscoverRouteRequiresFleet`: a registry-only server returns
  503 and does not panic.
- [ ] Write `TestDiscoverRouteUnknownRepo`: an unknown id returns 404.
- [ ] Write `TestDiscoverRouteMethodMismatch`: GET returns 405.
- [ ] Write `TestDiscoverRouteHonoursBearerAuth`: with a configured token, an
  unauthenticated request returns 401 and an authenticated request succeeds.
- [ ] Write `TestDiscoverRouteDoesNotPersistState`: the workspace file bytes
  and version are unchanged before and after a successful call.
- [ ] Write `TestDiscoverRouteInternalFailureIs500`: force an in-process
  discovery failure and assert 500, not the child-failure 502 convention.
- [ ] Run
  `go test ./web/bridge -run '^TestDiscoverRoute' -count=1`; confirm failure.
- [ ] Implement `discoveryRoutes()` and `discoverRepo` in the new file. The
  handler requires fleet mode, resolves the registered repo, calls
  `Fleet.DiscoverRepo`, and writes JSON. It accepts an empty body. Add exactly
  one `s.discoveryRoutes()` call at the end of the flat registration list in
  `routes()`; do not reorder existing lines. Return 500 directly for an
  unexpected in-process discovery error rather than using `writeErr`, whose
  502 convention denotes a child-process failure.
- [ ] Run
  `go test ./web/bridge -run '^(TestDiscoverRoute|TestWebIsStdlibOnly)' -count=1`;
  require PASS. Inspect this task's commit with
  `git show --format= -- web/bridge/http.go` and confirm it contains exactly
  one added route-registration line, independent of any later base-branch
  movement.
- [ ] Commit only this task: `feat(discovery): expose read-only repo discovery`.

## Task 6: Finalize the specification and prove acceptance

**Files:** modify
`docs/studio-fleet-factory/existing-project-discovery-spec.md` and
`docs/studio-fleet-factory/README.md`; add no production code unless a test
first exposes a defect.

- [ ] Update specification §5 from “Proposed” to the implemented contract,
  document the ten capability keys, the registered-`repoId` identity, the
  checkout-source rule, the `ok`/`degraded`/`unavailable` boundary, the
  contextual 401/403/404 classification, the missing-Gitea-`APIBase` case,
  the `supported` not-exercised detail convention, and the
  bounded-read/symlink guarantees.
- [ ] Keep the plan link in the README's `Active specification` section.
- [ ] Run the complete feature suite:
  `go test ./web/bridge -run 'Discovery|DiscoverRoute|InspectCheckout|WebIsStdlibOnly' -count=1 -v`.
- [ ] Run the package gate: `go test ./web/bridge -count=1`.
- [ ] Run the boundary and build gates:
  `go test ./web/bridge -run '^TestWebIsStdlibOnly$' -count=1`,
  `gofmt -l web/bridge`, `go vet ./web/bridge/...`, and
  `CGO_ENABLED=1 go build ./...`.
- [ ] Run the repository suite: `CGO_ENABLED=1 go test ./... -count=1 -timeout 10m`.
  Record unrelated baseline failures separately; do not claim an overall pass
  when any required gate remains red.
- [ ] Commit docs and any test-first defect fixes:
  `docs(discovery): record implemented discovery contract`.

## Acceptance matrix

| Specification criterion | Owning tests |
|---|---|
| Every capability uses one of five states | Task 1 classifier/order tests; Task 4 invariant assertion |
| GitHub-wire-compatible PAT report | Task 2 GitHub tests; Task 4 supported report test |
| Under-scoped CI credential is not an empty section | Task 4 denied-section test |
| Hidden resource is not reported as unsupported | Task 4 contextual-404 test |
| Missing self-hosted instance base is unconfigured | Task 2 and Task 4 Gitea tests |
| Absent instructions differ from unreadable instructions | Task 3 and Task 4 contrast tests |
| Clean checkout remains byte-identical | Task 3 read-only Git test |
| Dirty checkout is reported and unchanged | Task 3 dirty test |
| `web/` remains standard-library only | Existing `TestWebIsStdlibOnly` plus Task 6 gate |
| Default-branch advance changes a fact revision | Task 4 revision test |
| Supported-forge failure stays visible | Task 4 unavailable test |
| Git-only fallback remains supported | Task 4 Git-only test |
| Setup/build/install never runs | Task 3 no-execution test |
| Recommendations never rewrite setup | Task 4 advisory test |

All forge tests use `httptest` loopback only. The only real subprocess is Git
against a local disposable repository. No test reads developer credentials,
calls a live forge, or contacts a model provider.

## Execution, commits, and PR policy

- Execute Tasks 1–6 sequentially with one DeepSeek
  `deepseek-v4.1-flash:1017` implementer per task through the
  `ollama-cloud` provider.
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

- Coverage: specification §§4–6 and §9 map to Tasks 1–5; §10 remains
  excluded; the active archive index is updated in Task 6.
- Contract consistency: the ten keys and five states are fixed in Task 1;
  later tasks consume the exact Go names above.
- Safety: no persisted schema change, no `Forge` widening, no clone/fetch,
  no setup execution, no symlink escape, no secret in the report, and one
  shared-file line in `http.go`.
- Testability: every acceptance criterion has an offline deterministic test;
  no live forge or model dependency is required.
- Collision control: all implementation tasks are sequential because they
  compile as one Go package and share the frozen contract.

## Branch review record

PR #59 received two independent `glm-5.3-flash` reviews through
`ollama-cloud` on 2026-11-12. All findings are addressed in this fix set; the
focused re-review is recorded in the PR review thread.

| Review finding | Resolution |
|---|---|
| HTTP 401/404 were mapped without enough context | Classification is now contextual: 401 is `unconfigured`; 404 is `insufficient_permission` unless repository metadata is readable with the same credential, in which case it is `unsupported` |
| The public-GitHub acceptance wording had no offline owner | The criterion now names a GitHub-wire-compatible endpoint exercised with a PAT through `httptest`; the offline suite is the CI gate |
| Auto-creating the Marshal project was an implicit deferral | The specification now lists it explicitly as a later-slice non-goal |
| `repoId` implied owner/slug | The specification and plan now define it as the registered repository identifier |
| `ci_logs` used `supported` without saying it was unexercised | The state table and plan now define the not-exercised detail convention |
| The `p4-10-fleet` collision claim was stale | The measured diff is recorded, with a landing-time re-check retained |
| The Gitea path assertion appended `/api/v1` twice | The plan now uses the existing versioned `APIBase` convention |
| A Gitea instance with no `APIBase` was misclassified | It is now `unconfigured`, makes no HTTP request, and has adapter and report tests |
| Adapter result shapes were not frozen | Task 1 now owns the four adapter result types and their fields |
| The README section and route-diff check were imprecise | The plan names the actual `Active specification` section and checks the task commit rather than a moving base diff |
| Local Git reads could carry a PAT or compare `.git` cache files | The plan requires `Credential{}` for local reads and compares working-tree content outside `.git/` |
| Report status had no `ok`/`degraded` boundary | The plan now defines `unavailable`, `degraded`, and `ok` explicitly and requires the degraded-section test to assert `degraded` |
| A rate-limited 403 could be misreported as missing permission | A 403 carrying `Retry-After` is now `temporarily_unavailable`, with a classifier test case |
| Git-only capability meaning was implicit | The spec and plan now distinguish an unengaged Git-only adapter (`unsupported`) from missing declared-forge setup (`unconfigured`) |
| Checkout and manifest reads were not uniformly bounded | Every known file target now has a 1 MiB limit and an oversized-target test |
| Dirty output and detached HEAD were underspecified | `dirtyFiles` is capped at 100 with an omitted count; detached HEAD has empty branch, SHA in `head`, and `detached HEAD` detail |
| Top-level revision priority was ambiguous | Checkout `HEAD` wins, then the forge default-branch head, otherwise empty |
| In-process failures reused the child-failure 502 convention | The route now returns 500 for an unexpected in-process discovery failure and has a named test |
| Route registration referenced nonexistent route groups | The plan now says one `s.discoveryRoutes()` call at the end of the flat registration list |
