# Existing-project connection and read-only discovery — feature spec

Date: 2026-11-12
Status: spec for the first implementation slice. No product code is changed by
this document. The executable plan is
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

Measured on `main` at `907730fb`.

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

## 5. Contract sketch

Proposed, not approved. Shapes follow the existing `Repo`/`Credential`
conventions.

```
POST /api/repos/{id}/discover
  -> 200 {
       "repoId":      "repo-1",
       "revision":    "<commit sha examined>",
       "forge":       "github",
       "capabilities": [ {"key":"pull_requests","state":"supported"}, ... ],
       "facts":  [ {"origin":"detected","kind":"language","value":"Go",
                    "source":"forge:languages"}, ... ],
       "recommended": [ {"kind":"instructions","value":"Add AGENTS.md",
                         "reason":"no project instructions found"} ],
       "gaps":   [ {"key":"ci_logs","state":"insufficient_permission",
                    "detail":"token lacks checks:read"} ]
     }
```

Notes on the shape:

- one endpoint returns the capability summary **and** the report, so a caller
  cannot observe capabilities without the report they explain;
- `repoId` is the registered repository identifier; owner/slug is forge-derived
  fact rather than the repository's local identity;
- `revision` makes the fact set invalidatable;
- `gaps` is the only channel for partial access, so an empty `facts` list is
  never ambiguous;
- discovery is `POST` because it may be expensive, not because it mutates.

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
