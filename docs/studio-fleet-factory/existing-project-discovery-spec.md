# Existing-project connection and read-only discovery — feature spec

Date: 2026-11-12
Status: the first implementation slice is implemented; §5 records the contract
the bridge actually serves, and the [implementation
record](#implementation-record) lists the commits and the acceptance gates.
The executable plan is
[plans/2026-11-12-existing-project-discovery-plan.md](plans/2026-11-12-existing-project-discovery-plan.md).

## 1. Provenance

This is the design output for gap 1 of the
[journey map](journey-map.md#prioritised-gap-list) — "First-use connections and
existing-project import" — whose stated next design output is "Connect a
forge/provider, discover repository capabilities, and recover from missing
permissions while preserving planning."

What is already **approved** by the user, and is therefore treated here as a
requirement rather than a proposal:

- Connecting an existing repository **automatically creates a Marshal project**,
  initially named after the repository, with renaming available. It does not
  create a factory or start an agent
  ([design-notes §11](design-notes.md#existing-project-onboarding--current-exploration)).
- **Automatic read-only discovery** when a repository is connected: read
  project instructions and documentation, language and tooling, environment
  configuration, CI metadata and results, and existing branches/PRs where
  available. Distinguish **detected facts from recommendations**.
- Initial inspection **reads files and metadata**; setup scripts and builds
  belong to workspace execution.
- **API-first** forge integration (GitHub, GitLab, Gitea, Forgejo) with optional
  CLI tools and a basic Git-only fallback; standard Git for repository data and
  working state ([design-notes §6](design-notes.md#external-forge-integration--api-first-direction-approved)).
- Adapters expose **capabilities per instance and credential set** rather than
  assuming a provider name implies a complete feature set, and must distinguish
  **unsupported, unconfigured, insufficient permission and temporarily
  unavailable** states.
- Discovery **must not silently rewrite** an existing project's setup
  ([journey map](journey-map.md#prioritised-gap-list)).

Explicitly **open** and *not* decided by this spec: duplicate connections,
project identity, attaching a repository to an already-existing planning
project, the first-agent transition, and the exact location of forge
credentials relative to daemon/ACP design.

## 2. Summary

Connecting an existing repository becomes three steps: identify the repository
and forge, report what this instance and credential can actually do, then read
the checkout read-only and report what was found — separating facts from
suggestions, and degrading loudly rather than silently when access is partial.

The first slice delivers the **capability report** and the **read-only
discovery report** behind the existing project-registration surface. It does
not add a planning UI.

## 3. What exists today

Measured on `main` at `907730fb`, before this slice. The table is retained as
the starting-point record; §5 and the implementation record describe what the
slice added.

| Piece | Where | State |
|---|---|---|
| Forge abstraction | `web/bridge/forge.go:96` | `Forge` has PR/issue/check/review operations only: `CreatePR`, `GetPR`, `ListPRs`, `GetIssue`, `ListIssues`, `CommentIssue`, `RepoSize`, `PostReview`, `ListFailedChecks`, `CheckLog` |
| Registered repository | `web/bridge/workspace.go:165` | `Repo{ID, URL, Branch, CredRef, OwnerID, Forge, APIBase}`; `Forge` is `"github"` or `"gitea"`, declared not sniffed |
| Credential | `web/bridge/credential.go:24` | `Credential{ID, Kind, EnvVar, KeyPath, OwnerID, User, Ref}`; kinds include `none`, `pat`, `ssh`, `vault` |
| Project routes | `web/bridge/http.go:166-168` | `GET`/`POST`/`DELETE /api/projects` |
| GitHub + Gitea adapters | `web/bridge/forge_github.go`, `forge_gitea.go` | Implement the `Forge` operations above |
| Local disk access | `web/bridge/disk.go:46`, `:180`, `:199` | Already walks directories and reads files with `os` / `filepath` |
| Git operations | `web/bridge/factory_git.go` | The factory path already runs git in the bridge (branch creation and local commits) |
| Boundary rule | `web/bridge/boundary_test.go:21` | `TestWebIsStdlibOnly` — nothing under `web/` may import `marshal/internal/...` |

There is **no discovery implementation**. A search of `web/bridge/*.go` for
`Discover`, `Inspect`, `Capabilities` or `Detect` returns no matches, and no
route exposes repository facts. That is the gap.

## 4. Design

### 4.1 Connection flow

Following the approved suggested flow in
[design-notes §6](design-notes.md#external-forge-integration--api-first-direction-approved):
repository or instance URL → detected/specified forge type → authentication →
**capability summary** → repository discovery.

The capability summary is presented before discovery so the user can see why a
later section is empty instead of inferring that it is empty because the
repository lacks the feature.

### 4.2 Capability reporting

Capabilities are reported per **instance × credential**, never derived from the
forge name. Each reported capability carries one of five states:

| State | Meaning |
|---|---|
| `supported` | The adapter implements it and the credential is usable for it; when no matching resource existed to exercise, `detail` says that it was not exercised |
| `unconfigured` | Not set up for this instance (for example no credential, an invalid credential, or a missing self-hosted instance API base) |
| `insufficient_permission` | The credential is valid but lacks the scope, or the resource is hidden from that credential |
| `temporarily_unavailable` | The adapter supports it, but the instance or service could not answer right now |
| `unsupported` | The adapter does not implement it for this instance/version |

Classification is contextual rather than a blanket HTTP-status mapping. An
invalid or absent credential is `unconfigured`, and a valid credential
receiving 403 without a `Retry-After` signal is
`insufficient_permission`. A 404 is `insufficient_permission` while
repository metadata is not known to be readable, for example when the
repository itself is hidden from the credential. After the same credential has
successfully read repository metadata, a later 404 is interpreted as the
endpoint not being implemented for this adapter/instance and is `unsupported`.
A 403 carrying `Retry-After`, HTTP 429, 5xx, and transport failures are
`temporarily_unavailable`.

A repository with no declared forge uses the sanctioned Git-only path. Its
forge capabilities are `unsupported` because no forge adapter is engaged,
while a declared forge with missing setup is `unconfigured`.

A failed API connection on a **supported** forge stays a visible error and does
not silently downgrade to generic Git (approved in §6).

### 4.3 What discovery reads

Discovery has two sources, and the report labels which one each fact came from.

**From the forge API** (metadata, cheap, no checkout required):

- default branch, visibility, description, topics;
- primary language and size;
- open PRs/MRs and their state;
- CI: workflow/check names, latest conclusions, and the check-run identity
  (name plus GitHub App ID, or commit-status context);
- branch list and default-branch head revision.

**From the checkout** (read-only, files and metadata only):

- project instructions and documentation: `AGENTS.md`, `CLAUDE.md`,
  `GEMINI.md`, `.cursorrules`, `README*`, and any `docs/` index;
- language and tooling: manifests such as `go.mod`, `package.json`,
  `pyproject.toml`, `Cargo.toml`;
- environment configuration: `.devcontainer/`, `Dockerfile`,
  `docker-compose.yml`, `compose.yaml`;
- CI definition presence: `.github/workflows/*.yml`.

Discovery **reads** these. It never executes setup scripts, never builds, and
never installs anything — that belongs to workspace execution.

### 4.4 Facts versus recommendations

Every reported item carries an origin:

- `detected` — read from the forge or the checkout, with the path or API
  resource that produced it, and a revision (commit SHA) so the fact can be
  invalidated when the branch moves.
- `recommended` — Marshal's suggestion, never applied automatically.

Recommendations are advisory only. No recommendation is written into the
repository, and nothing is applied to an existing project's configuration
without an explicit user action.

### 4.5 Read-only guarantee

Discovery must be safe to run against a repository the user has not agreed to
modify. The slice enforces:

- no writes to the checkout — if the working tree is dirty the report says so
  rather than attempting to correct it;
- no branch creation, checkout or fetch that changes working state;
- no repository settings, files or CI configuration are modified.

Where git is needed for branches or revisions, it is read-only plumbing.

### 4.6 Where discovery runs, and the boundary that shapes it

`web/` is standard-library only (`TestWebIsStdlibOnly`), so the bridge cannot
reuse the agent-side repository intelligence in `internal/repo` — the scanner,
repository map, tree-sitter symbols and gitignore handling are unavailable to
it.

The slice therefore implements **minimal, explicit detection** in the bridge:
directory reads for known paths plus manifest sniffing for language and
tooling. It deliberately does not attempt a repository map or symbol graph. If
richer inspection is later wanted, the correct route is a data contract fed by
the agent side over ACP, or a dedicated capability, not an exception to the
boundary rule.

## 5. Implemented contract

Implemented in `web/bridge/discovery.go`, `discovery_forge.go`,
`discovery_forge_github.go`, `discovery_forge_gitea.go`,
`discovery_checkout.go`, `discovery_report.go` and `discovery_http.go`. Every
statement below is a behaviour those files have and their tests exercise; the
[implementation record](#implementation-record) names the commits and gates.

### 5.1 Route

```http
POST /api/repos/{id}/discover
Authorization: Bearer <token>   # when the server has a token
Content-Length: 0
```

- `{id}` is the **registered** repository identifier (`Repo.ID`), not an
  owner/slug pair. Owner and slug appear only as forge-derived facts.
- The body is empty and is not read. The repository always comes from its
  registered record, never from the caller.
- The route is `POST` because discovery may be expensive, not because it
  mutates.
- Request-level statuses: `200` with the report; `401` when the server has a
  token and the request does not carry it (bearer auth wraps every `/api`
  route); `404` for an unregistered id; `405` for a non-`POST` method; `503`
  when the server runs without a fleet; `500` for an unexpected in-process
  discovery failure. The 500 is deliberate: the shared `writeErr` default of
  `502` denotes a child-process failure, which this is not.

### 5.2 Response

One document carries the capability summary and the report that explains it,
so a caller cannot observe capabilities without the facts, recommendations and
gaps behind them.

```json
{
  "repoId": "repo-1",
  "forge": "github",
  "status": "degraded",
  "revision": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
  "branch": "main",
  "worktree": {
    "path": "/Users/alecpullen/projects/example",
    "branch": "main",
    "head": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9",
    "dirty": false
  },
  "capabilities": [
    {"key": "repository_metadata", "state": "supported"},
    {"key": "branches", "state": "supported"},
    {"key": "pull_requests", "state": "supported"},
    {"key": "ci_checks", "state": "insufficient_permission",
     "detail": "the credential lacks permission to read this resource (HTTP 403)"},
    {"key": "ci_logs", "state": "supported",
     "detail": "not exercised: the CI checks section could not be read"},
    {"key": "checkout_instructions", "state": "supported"},
    {"key": "checkout_docs", "state": "supported"},
    {"key": "checkout_tooling", "state": "supported"},
    {"key": "checkout_environment", "state": "supported"},
    {"key": "checkout_ci_definition", "state": "supported"}
  ],
  "facts": [
    {"origin": "detected", "section": "instructions", "kind": "instructions",
     "value": "AGENTS.md", "source": "checkout:AGENTS.md",
     "revision": "8e4c1b2f0e1d2c3b4a5968778695a4b3c2d1e0f9", "size": 2048}
  ],
  "recommended": [],
  "gaps": [
    {"key": "ci_checks", "section": "ci", "state": "insufficient_permission",
     "detail": "the credential lacks permission to read this resource (HTTP 403); grant the credential or process read access and retry"}
  ]
}
```

Field notes the code holds to:

- `repoId` is the registered identifier; `forge` echoes the declared family
  (`"github"`, `"gitea"`, or empty for the Git-only path).
- `status` is `ok`, `degraded` or `unavailable` (§5.4). `error` is present
  only on `unavailable`.
- `revision` and `branch` are the top-level identity of the inspected state
  (§5.9); both are omitted when empty.
- `worktree` is omitted entirely when no checkout was inspected. Inside it,
  `path` is the linked project root, `dirty` is always present and is `null`
  when the state could not be determined — which differs from `false` — and
  `detail` carries a detached-HEAD clause, the dirty-list truncation clause,
  or the reason Git state could not be read.
- `capabilities`, `facts`, `recommended` and `gaps` are always present and are
  JSON arrays, never `null`, because the slices are always non-nil.
- A **fact** carries `origin` (`"detected"`), `section`, `kind`, `value`,
  `source` (`forge:<resource>` or `checkout:<relative-path>`), a `revision`
  when one is known, and an optional `size` or `detail`.
- A **recommendation** carries `origin: "recommended"`, `section`, `kind`,
  `value` and `reason`. Discovery never applies one.
- A **gap** carries the capability `key`, its `section`, the five-way `state`,
  and a `detail` that ends with what would fix it.
- `facts`, `recommended` and `gaps` are ordered deterministically.
  Capabilities keep the frozen key order, and `gaps` holds at most one entry
  per capability key.

### 5.3 Capability keys and states

All ten keys are always present, always in this order:

| # | Key | Reads |
|---|---|---|
| 1 | `repository_metadata` | default branch, visibility, description, topics, language, size |
| 2 | `branches` | branch names and their head revisions |
| 3 | `pull_requests` | open pull/merge requests |
| 4 | `ci_checks` | checks or commit statuses on the observed head |
| 5 | `ci_logs` | bounded detail for one failing check |
| 6 | `checkout_instructions` | `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.cursorrules`, `README*` |
| 7 | `checkout_docs` | `docs/README.md`, `docs/index.md`, `docs/INDEX.md` |
| 8 | `checkout_tooling` | `go.mod`, `package.json`, `pyproject.toml`, `Cargo.toml`, `tsconfig.json` |
| 9 | `checkout_environment` | `.devcontainer/`, `Dockerfile`, `docker-compose.yml`, `compose.yaml` |
| 10 | `checkout_ci_definition` | `.github/workflows/*.yml`, `.github/workflows/*.yaml` |

Each carries one of exactly five states — `supported`, `unconfigured`,
`insufficient_permission`, `temporarily_unavailable`, `unsupported` — with the
meanings in §4.2. A key no code path probed stays `unsupported` with the detail
`discovery did not probe this capability`, so it never silently vanishes from
the wire.

### 5.4 The `ok` / `degraded` / `unavailable` boundary

- `ok` — every one of the ten capabilities is `supported` and `gaps` is empty.
- `degraded` — any capability is not `supported`, or any gap exists. A Git-only
  report is therefore `degraded`, because its five forge capabilities are
  `unsupported`.
- `unavailable` — the declared forge's **repository metadata** probe failed as
  `temporarily_unavailable`. The report keeps every locally observable checkout
  fact, sets a non-empty top-level `error` naming the outage, marks the forge
  capabilities `temporarily_unavailable`, and does not attempt the remaining
  forge probes. This is a visible failure, never a silent downgrade to
  Git-only.

A per-section failure after repository metadata was readable degrades only that
section and yields `degraded`, not `unavailable`.

### 5.5 Contextual classification

Classification is contextual, not a blanket HTTP-status table:

| Outcome | State |
|---|---|
| the probe returned no error | `supported` |
| the adapter raised `errDiscoveryUnconfigured` | `unconfigured` |
| the adapter raised `errDiscoveryUnsupported` | `unsupported` |
| a transport failure, or an undecodable response | `temporarily_unavailable` |
| HTTP 401 | `unconfigured` |
| HTTP 403 carrying `Retry-After` | `temporarily_unavailable` |
| HTTP 403 without a retry signal | `insufficient_permission` |
| HTTP 404 while repository metadata is not known readable | `insufficient_permission` |
| HTTP 404 after this credential read repository metadata | `unsupported` |
| HTTP 429 | `temporarily_unavailable` |
| HTTP 5xx | `temporarily_unavailable` |
| any other 4xx | `unsupported` |
| anything else | `temporarily_unavailable` |

The 404 rule is why the metadata probe runs first: the same status code means
"this instance does not serve the endpoint" or "this credential cannot see the
resource", and only a prior successful metadata read distinguishes them. Gap
details name the state and the fix and never echo the credential, a request
header, or the forge's raw error body.

When one section produced several gaps, the capability reports the most
actionable state among them, ordered `unconfigured` >
`insufficient_permission` > `temporarily_unavailable` > `unsupported`.

### 5.6 Credential and forge availability

- **Declared forge.** A credential is resolved only for a repository that
  declares one. Only a `pat` credential with a usable secret can authenticate
  an API probe; an absent, non-PAT, invalid or expired credential makes every
  forge capability `unconfigured`, and no request is attempted with it.
- **No declared forge.** The sanctioned Git-only path: no adapter is engaged,
  no credential or host is touched, no HTTP request happens, and all five forge
  capabilities are `unsupported` with the gap "no forge is declared for this
  repository".
- **Forge with no read-only adapter.** `unsupported`, with a detail naming the
  declared forge.
- **Gitea/Forgejo with no `APIBase`.** `unconfigured`, with a detail naming the
  setting to fix (`apiBase`, `https://<host>/api/v1`) and **no request built**.
  GitHub differs on purpose: an empty `APIBase` falls back to the public
  `https://api.github.com`, because GitHub has a public API default and a
  self-hosted Gitea instance does not.
- **An unusable instance setting or credential found by the metadata probe.**
  The cause is instance-wide, so all five forge capabilities share it and the
  remaining probes are skipped.

### 5.7 `supported` and the not-exercised detail

A capability can be `supported` without a resource to read. The report says so
in `detail` instead of implying an empty result:

- no revision is known to check — `ci_checks` and `ci_logs` are `supported`
  with `not exercised: no default branch revision is known to check`;
- the checks section could not be read — `ci_logs` is `supported` with
  `not exercised: the CI checks section could not be read`;
- checks are readable with no failing check — `ci_logs` is `supported` with
  `no failing check to exercise`;
- a log was read — `ci_logs` is `supported` with
  `detailed logs readable for <check>`, plus a `check_log` fact whose `size` is
  the bounded byte count. For Gitea, whose commit status has no generic log
  endpoint, the "log" is the check's name and link, bounded like any other, and
  no request is invented.

A readable section with nothing in it reports a **detected fact** with
`value: "absent"` and a `detail` naming what was searched. That is a completed
search, not a failure: absence is never inferred from an unreadable target,
which is a gap with no absence fact instead. The two outcomes stay textually
distinct in the response.

### 5.8 Checkout source and inspection

- The checkout comes from exactly one place:
  `Workspace.ProjectRootByRepo(repo.ID)`. A repository URL is never treated as
  a local path, and discovery never clones, fetches, creates a checkout, uses a
  bare mirror, or persists a path.
- No linked project — all five checkout capabilities are `unconfigured` with
  the gap "no Marshal project links this repository, so there is no checkout to
  inspect".
- The root is stat'ed; everything below it is `lstat`'ed and no symlink is ever
  followed. A symlinked file target is skipped without a read. A symlinked
  `docs/`, `.devcontainer/` or `.github/workflows` directory is not entered, so
  that section reports a completed search rather than reading through it.
  Nothing outside the checkout can be read.
- Only the exact paths in §5.3 are looked at: no tree walk, and no glob beyond
  `README*`, which is resolved from one listing of the checkout root.
  Environment definitions are reported by presence only; nothing inside
  `.devcontainer/` is listed or opened. Workflow files are detected by name,
  never parsed.
- Every instruction, documentation and manifest target is opened through
  `io.LimitReader` at **1 MiB**, reading one byte beyond the limit so overflow
  is detectable. An oversized target is a gap in the `unsupported` state, and
  its content is neither decoded, quoted nor returned: the bytes are read only
  to establish readability and the bound.
- A target that exists but cannot be inspected is a gap whose state names the
  fix: a filesystem access failure → `insufficient_permission`; a directory or
  device where a file belongs, or an oversized file → `unsupported`; a target
  that changed between `lstat` and `open` → `temporarily_unavailable`.
- Manifests yield only the minimum language/tooling detail: `go.mod` → Go,
  `package.json` → JavaScript, or TypeScript with a sibling `tsconfig.json`,
  `pyproject.toml` → Python, `Cargo.toml` → Rust.

### 5.9 Local Git state, revision and branch

Git state is read through the hardened runner with an **empty** credential, so
no forge PAT reaches a local read's environment, and only four allowlisted
verbs are used: `rev-parse HEAD`, `symbolic-ref --quiet --short HEAD`,
`for-each-ref --format=%(refname:short) refs/heads`, and
`-c core.quotepath=false --no-optional-locks status --porcelain
--untracked-files=all`. `core.quotepath=false` is an argument to the status
verb, not a new verb: it makes non-ASCII dirty paths arrive literal instead of
C-quoted escapes. Nothing that fetches, checks out, switches, resets, cleans,
clones, pulls or pushes is ever run, and `--no-optional-locks` keeps
`git status` from refreshing the index.

- A **detached HEAD** is an empty `worktree.branch`, the SHA in
  `worktree.head`, and `detached HEAD` in `worktree.detail` — plus
  `no local branches` when the checkout has none. Clauses are joined with
  `; `, so a detached, truncated checkout states both.
- `worktree.dirtyFiles` is capped at **100** entries. The status output itself
  is read through a bounded runner at **1 MiB** (stdout and stderr), so a
  heavily dirty checkout cannot materialise an unbounded string before the cap
  applies. When the output was not truncated, the detail adds the exact
  `<n> additional dirty entries omitted (dirtyFiles is capped at 100)`. When it
  was truncated, the trailing partial porcelain line is dropped and the detail
  says `at least <n> additional dirty entries omitted`, because the exact count
  is unknowable once the output is cut. Renames and copies report their
  destination path; any path git still C-quotes (one containing a space or a
  quote) is unquoted, so `dirtyFiles` carries real paths.
- A dirty checkout is reported, never repaired, and its working tree is
  untouched.
- **Top-level `revision` and `branch` source priority** is frozen: the checkout
  `HEAD` and its current branch when a checkout was inspected (empty branch for
  a detached HEAD), otherwise the forge's default-branch head and default
  branch, otherwise empty. A checkout therefore wins even when the forge's
  default branch has moved, which is what makes a stale forge fact detectable
  without a forbidden fetch.
- Forge facts carry the default-branch head observed during the probe
  (`default_branch`, falling back to the repository's registered branch);
  checkout facts carry the checkout `HEAD` they were read at. Advancing the
  default branch changes a forge fact's `revision` with no local mutation.

### 5.10 Read-only guarantees

Discovery is observational on every path it has:

- no writes to the checkout, its Git state or its index; a dirty checkout is
  reported, not corrected;
- no write to the workspace: its files and version are unchanged, and no report
  is persisted;
- no audit event, and no project, agent, factory or work item is created;
- no clone, fetch, pull, push, checkout, reset or clean;
- no setup script, build, Makefile target, package script, devcontainer command
  or workflow step is executed — discovery reads files and runs the four
  allowlisted local Git reads, nothing else;
- no forge request at all for a repository with no declared forge, and no
  request with an unusable credential;
- no credential, token, request header or raw forge error body in the report, a
  gap, or an error string.

## 6. Missing access and partial failure

- A missing capability degrades **that section only**; the remaining sections
  still report.
- Each gap states the five-way capability state and a human-readable `detail`
  naming what would fix it (scope, credential kind, instance configuration, or
  adapter support).
- A supported-forge connection error is surfaced as an error, not as an empty
  report.
- No inference of absence from inaccessibility: "could not read" and "not
  present" are distinct outcomes and must render differently.

## 7. Parallel-safety against the daemon track

This slice is chosen partly because it can proceed while the daemon work
(`p4-10-fleet`) continues.

| Surface | Collision risk |
|---|---|
| This specification, and docs generally | none |
| `web/ui` | none — the measured `p4-10-fleet` diff does not touch it |
| `forge*.go`, `credential.go`, new discovery files | none — the measured `p4-10-fleet` diff does not touch them |
| `web/bridge/http.go` route registration | no current overlap — the measured `p4-10-fleet` diff does not modify `http.go`; re-check at landing time |

The measured `p4-10-fleet` diff currently overlaps only daemon/child-process
files and their tests, not the discovery surfaces. The implementation still
keeps its route registration to one adjacent line and re-checks the base at
landing time so a later daemon change cannot turn that into a broad conflict.

## 8. Open questions

Not decided here; they need the user, not an implementer's judgement:

1. Duplicate connections — what identifies the same repository connected twice,
   and is the second connection refused or merged?
2. Attaching a repository to an **already-existing planning project** — the
   approved behaviour covers auto-creating a project, not adopting into one.
3. Automatic project naming for collisions and for non-repository-local
   checkouts.
4. The first-agent transition: what the discovery report leads to, and whether
   any of it pre-fills a handoff.
5. Where forge credentials live relative to the daemon/ACP boundary; §6 flags
   this as needing reconciliation with the daemon design.
6. Whether `recommended` items ever become one-click actions, and if so which.

## 9. Acceptance criteria

For the first slice:

- connecting a repository already registered with `forge` and a credential
  returns a capability report in which every capability carries one of the five
  states;
- a GitHub-wire-compatible endpoint exercised with a `pat` produces the
  report, and a simulated under-scoped credential makes the CI section report
  `insufficient_permission` rather than empty;
- a repository without `AGENTS.md` reports instructions as absent, and the same
  repository with unreadable contents reports a gap — the two are textually
  distinct in the response;
- discovery on a clean checkout leaves the checkout byte-identical (verified by
  comparing working-tree status before and after), and on a dirty checkout it
  reports the dirtiness without modifying anything;
- `TestWebIsStdlibOnly` continues to pass, and no new import from
  `marshal/internal/...` appears under `web/`;
- a fact's `revision` changes after the default branch advances, so a stale
  report is detectable.

## 10. Non-goals

- repository creation on a forge, or scaffolding a new project;
- creating the Marshal project on connection (approved in §1, but deferred to
  a later slice);
- executing setup scripts, building, or installing dependencies;
- a repository map or symbol graph in the bridge (see §4.6);
- adopting an existing planning conversation;
- auto-applying any recommendation;
- creating a factory, a work item, or starting an agent.

## Implementation record

Delivered on branch `studio/existing-project-discovery-implementation` as
Tasks 1–6 of the
[plan](plans/2026-11-12-existing-project-discovery-plan.md), test-first, with
one commit per task.

| Task | Deliverable | Commit |
|---|---|---|
| 1 | Frozen contract: five capability states, ten ordered keys, contextual classifier, four adapter result types | `fb00e3cf` |
| 2 | Read-only GitHub and Gitea/Forgejo metadata probes behind an optional `DiscoveryForge` interface | `f78bbd0e` |
| 3 | Read-only existing-checkout inspection and local Git state | `8361d0b1` |
| — | Gap-state fix: a checkout gap reports the state that names its fix | `8cdec12b` |
| 4 | Capability, fact, gap and recommendation assembly with the status boundary | `10f0a83b` |
| 5 | Authenticated `POST /api/repos/{id}/discover` route | `fd350efa` |
| 6 | Specification, README and plan outcome record | this documentation commit |

Files: `web/bridge/discovery.go`, `discovery_forge.go`,
`discovery_forge_github.go`, `discovery_forge_gitea.go`,
`discovery_checkout.go`, `discovery_report.go`, `discovery_http.go`, their
tests, and one route-registration line in `web/bridge/http.go`. All new code is
standard library only; `web/ui/` is untouched, and the existing `Forge`
interface is not widened.

Acceptance gates, run from the repository root on this branch:

| Gate | Result |
|---|---|
| `go test ./web/bridge -run 'Discovery\|DiscoverRoute\|InspectCheckout\|WebIsStdlibOnly' -count=1 -v` | PASS — `ok marshal/web/bridge 1.545s` |
| `go test ./web/bridge -count=1` | PASS — `ok marshal/web/bridge 119.328s` |
| `go test ./web/bridge -run '^TestWebIsStdlibOnly$' -count=1` | PASS — `ok marshal/web/bridge 0.375s` |
| `gofmt -l web/bridge` | no output |
| `go vet ./web/bridge/...` | no output, exit 0 |
| `CGO_ENABLED=1 go build ./...` | exit 0 |

The forge probes are exercised only against `httptest` loopback servers; the
only real subprocess is local `git` against a disposable repository. No test
reads developer credentials, calls a live forge, or contacts a model provider.
The whole-repository suite is run by the coordinator separately, not by this
slice.

### Branch-review fix pass (PR #60)

Two independent `glm-5.3-flash` reviews of PR #60 raised seven findings. This
pass resolves all seven, each covered by a named test.

| # | Finding | Resolution | Commit |
|---|---|---|---|
| 1 | GitHub `ci_checks` read only check-runs, so a status-only repo yielded a false "no checks" | `Checks` now reads the combined commit status (`/commits/{ref}/status`), merges the newest status per context (by status id, else last seen) alongside check-runs, and sets the context as the check identity | `0d1be4ac` |
| 2 | `git status` output was read unboundedly | A new additive `runCtxLimited` caps stdout and stderr at 1 MiB; the status read drops a truncated trailing partial line and reports `at least <n>` omitted entries when the output was cut | `0d1be4ac` |
| 3 | Porcelain quoting was ignored | The status invocation adds `-c core.quotepath=false` (an argument to the status verb, not a new verb); remaining C-quoted paths are unquoted so `dirtyFiles` carries real paths | `0d1be4ac` |
| 4 | Forge collections were single-page and silently truncated | Branches, pull requests and checks paginate up to five pages on both adapters; a full final page returns the items plus `errDiscoveryTruncated`, and the report keeps them, stays `supported`, and records a `temporarily_unavailable` gap instead of an absence fact | `0d1be4ac` |
| 5 | The gap fix told an operator to declare an already-declared forge | The unsupported fix now says the declared forge does not expose the capability and adapter support would be needed; only an undeclared forge is told to declare one | `0d1be4ac` |
| 6 | The dirty-file parser had no direct table test | `TestDiscoveryDirtyFilesPorcelainTable` exercises rename, quoted path, exactly-at-cap and cap-plus-one records directly, without a git binary | `0d1be4ac` |
| 7 | A symlinked environment definition produced a "define a development environment" recommendation | A skipped symlink qualifies the section's absence fact; the recommendation pass ignores qualified absence. The symlink is never read or reported as a fact | `0d1be4ac` |

Verification for this pass: `go test ./web/bridge -run
'Discovery|DiscoverRoute|InspectCheckout|WebIsStdlibOnly' -count=1` PASS,
`go test ./web/bridge -count=1` PASS, `gofmt -l web/bridge` no output,
`go vet ./web/bridge/...` clean, `git diff --check` clean.
