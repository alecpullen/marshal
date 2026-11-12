# Project factory defaults and per-handoff overrides — feature spec

Date: 2026-11-13
Status: spec for the second implementation slice of the studio-fleet-factory
archive. No product code is changed by this document.

## 1. Provenance

This is the design output for gap 2 of the
[journey map](journey-map.md#prioritised-gap-list) — "Factory creation and
adoption of independent work" — whose stated next design output is "Create/edit
a factory from the working process; map defaults and handoff overrides; adopt a
conversation or existing work".

Gap 1 (existing-project connection and read-only discovery) is implemented in
[the discovery slice](existing-project-discovery-spec.md) and its
[implementation record](existing-project-discovery-spec.md#implementation-record).

What is already **approved** by the user, and is therefore treated here as a
requirement rather than a proposal:

- **Optional per-project defaults should reduce repeated setup**
  ([design-notes §4](design-notes.md#4-handoff-and-delegated-authority)).
- **Configuration can supply defaults and individual handoffs can override
  them** ([early-exploration-readme](early-exploration-readme.md#fleet-and-project-factory-decisions)).
- **The handoff is the primary place to select configuration**
  ([design-notes §4](design-notes.md#4-handoff-and-delegated-authority)).
- **Each factory belongs to one project**; cross-project factory operation is
  not an initial goal
  ([design-notes §2](design-notes.md#2-product-structure--agreed-direction),
  [early-exploration-readme](early-exploration-readme.md#fleet-and-project-factory-decisions)).
- **Factory configuration starts with a working plan → implement → review → fix
  process, then allows customisation**
  ([design-notes §2](design-notes.md#2-product-structure--agreed-direction)).

The bootstrap factory already implements the *create a factory from a pasted
plan and specification* half of this gap
([bootstrap design §1](bootstrap-factory-design.md#1-goal-and-scope)). This
slice adds the **defaults and overrides** half. The **adoption** half is
explicitly deferred — see §9.

## 2. Summary

A project records the factory configuration its work items normally use. A
handoff inherits that configuration for every field it does not set itself, and
overrides it for every field it does. The resolved configuration is snapshotted
into the work item when the handoff is created, so editing a project's defaults
later never changes work that is already approved or running.

## 3. What exists today

Measured on `main` at `907730fb`, with the bootstrap factory merged.

| Piece | Where | State |
|---|---|---|
| Factory configuration | `web/bridge/factory_types.go:8` | `FactoryConfig` carries the whole per-handoff configuration: repository, project root, host, target branch, workspace, native flag, implementer/reviewer routing, reviewer instructions, implementer-auto flag, expected CI checks, no-required-CI acknowledgement, and the round/CI limits |
| Handoff creation | `web/bridge/factory_types.go:26` | `FactoryCreate` carries a request id, title, plan, specification and the configuration |
| Create path | `web/bridge/factory_http.go:52` | `Create` normalises, preflights, then stores the item |
| Normalisation | `web/bridge/factory_store.go:240` | `factoryNormalize` requires a request id, title, plan and repository; applies the built-in defaults (host `local`, five rounds, 15 s CI poll, 1800 s CI wait); rejects a native/workspace combination and a non-local host |
| Preflight | `web/bridge/factory_http.go:113` | Requires a registered GitHub repository, a target branch, a trusted registered project root, and either native execution or a resolvable published workspace |
| Project settings | `web/bridge/workspace.go:122` | `ProjectSettings` is "a project's defaults for new agents and runs, and its intake wiring"; it already carries pointer-typed tri-state booleans (`Auto`, `Isolated`) |
| Project settings API | `web/bridge/projects.go:328` | `GET`/`PUT /api/projects/settings?root=` already read and write the whole settings record |
| Workspace schema | `web/bridge/workspace.go:15` | `workspaceVersion` is 11; an optional field does not change it (the `Automations` field was added that way) |

There is **no defaults mechanism**. Every handoff must state its whole
configuration, so the same repository, branch, workspace and routing are
re-entered for every work item in a project.

## 4. Design

### 4.1 Where the defaults live

The defaults are a new optional field on `ProjectSettings`:

```go
// FactoryDefaults are a project's default factory configuration. A handoff
// inherits every field it leaves unset.
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

`ProjectSettings` gains `FactoryDefaults *FactoryDefaults`. It is optional, so
the workspace file format does not change and `workspaceVersion` stays 11 —
the same reasoning the `Automations` field already follows.

The defaults are read and written through the **existing** project-settings
surface (`GET`/`PUT /api/projects/settings?root=`). No new route is added, and
the SPA needs no change to carry them.

### 4.2 Which fields are inheritable

The `FactoryDefaults` type contains exactly the fields that describe *how work
runs*, and nothing else. The type is the rule: a field that is not in it can
never be inherited.

**Inheritable** — the configuration of the run:

`TargetBranch`, `Workspace`, `HostID`, `ImplementerRouting`,
`ReviewerRouting`, `ReviewerInstructions`, `ExpectedChecks`, `MaxRounds`,
`CIPollSeconds`, `CIWaitSeconds`.

**Not inheritable**, and deliberately absent from the type:

- `RepoID` and `ProjectRoot` — they identify *what* the work item is about and
  *which project* it belongs to. A default that silently retargeted a handoff
  at another repository or project would be a silent scope change.
- `Native`, `ImplementerAuto` and `NoRequiredCIAcknowledged` — these are
  booleans whose `false` is a meaningful choice, so "unset" and "explicitly
  false" cannot be told apart in the handoff's plain `FactoryConfig`. Making
  them inheritable would need a tri-state field on the shipped `FactoryConfig`
  contract. `ProjectSettings.Auto` and `ProjectSettings.Isolated` show the
  pointer pattern for when that is done; it is deferred (§9).

### 4.3 How an override is expressed

A handoff overrides a default by setting the field itself. "Unset" is
per-type, and is unambiguous for every inheritable field:

| Field type | Unset | Explicit |
|---|---|---|
| `string` | `""` | any non-empty value |
| `int` | `0` | any non-zero value |
| `json.RawMessage` | `nil` | any non-nil value, including `{}` |
| `[]FactoryCheckKey` | `nil` | any non-nil slice, **including an empty one** |

The slice case matters: an explicitly empty `expectedChecks` means "no checks
are required", which is different from "inherit the project's expected checks",
and the existing approval rule distinguishes them.

### 4.4 Resolution

Resolution happens **once**, when a handoff is created, and the resolved
configuration is what the work item stores:

```
resolved = defaults ⊕ handoff
```

where `⊕` means: for each inheritable field, take the handoff's value when it
is set, otherwise the project's default.

The order of operations in the create path is:

1. resolve the project's defaults into the handoff's configuration;
2. normalise the resolved configuration (built-in defaults, validation);
3. preflight the resolved configuration;
4. store the resolved configuration in the work item's draft.

Resolution must run **before** normalisation, because normalisation fills the
built-in defaults (five rounds, 15 s CI poll, 1800 s CI wait) into unset
fields; resolving afterwards would mean a project default for those limits
could never take effect.

Because the resolved configuration is stored in the draft, and the approval
snapshots the draft's configuration, **editing a project's defaults later never
changes work that already exists**. The defaults are a convenience for the
next handoff, not a live link to existing work.

A `retry` command that carries a replacement configuration is an explicit,
operator-authored configuration and is **not** re-resolved against the current
defaults: the operator is deliberately changing the handoff, and the resolved
configuration of the existing draft is the baseline they are editing.

### 4.5 Which project's defaults apply

The project is resolved in this order:

1. `Config.ProjectRoot`, when the handoff names one;
2. otherwise the project whose intake names the repository
   (`Workspace.ProjectRootByRepo`), the same resolution the discovery slice
   uses;
3. otherwise no defaults apply and the handoff behaves exactly as it does
   today.

### 4.6 Conflicts

The resolved configuration must be valid, exactly as a hand-written one must
be. A project default that conflicts with the handoff is rejected with an
error that names the conflict and both sources — for example a project default
`workspace` combined with a handoff `native: true`, which the existing
normalisation already rejects as mutually exclusive.

### 4.7 What this does not change

The bootstrap factory's acceptance contract is untouched:

- `factoryNormalize` still requires a request id, title, plan and repository;
- the plan and specification size limit is unchanged;
- the local-host requirement is unchanged;
- the built-in defaults (five rounds, 15 s CI poll, 1800 s CI wait) are
  unchanged, and a project default may set them explicitly;
- the rule that a handoff must select expected CI checks or acknowledge that
  none are required is unchanged;
- the factory loop, its CI evidence, and its review/repair semantics are
  unchanged.

## 5. Contract sketch

Proposed, not approved. Shapes follow the existing `ProjectSettings` and
`FactoryConfig` conventions.

```
PUT /api/projects/settings?root=/path/to/project
  -> 200 {
       "workspace": "",
       "routing": null,
       "factoryDefaults": {
         "targetBranch": "main",
         "workspace": "ws://published/example",
         "implementerRouting": {"preset": "frontier"},
         "reviewerRouting": {"preset": "frontier"},
         "maxRounds": 5,
         "ciPollSeconds": 15,
         "ciWaitSeconds": 1800
       },
       "intake": {"repoId": "repo-1"}
     }

POST /api/factory/items
  -> 200 {
       "id": "item-1",
       "draft": {
         "title": "Add the thing",
         "config": {
           "repoId": "repo-1",
           "projectRoot": "/path/to/project",
           "targetBranch": "main",
           "workspace": "ws://published/example",
           "implementerRouting": {"preset": "frontier"},
           "maxRounds": 5
         }
       }
     }
```

The handoff in that request set only `repoId`, `projectRoot` and `title`; the
rest came from the project's defaults, and the stored draft shows the resolved
values.

## 6. Missing access and partial failure

- A project with no defaults behaves exactly as it does today.
- A project whose defaults are unreadable or invalid is a configuration error
  at the point the defaults are written, not a silent fallback.
- A handoff that cannot be resolved (no project, no defaults) is not an error:
  it is the current behaviour.
- A conflict between a default and a handoff is an error naming both sources.

## 7. Parallel-safety against the daemon track

This slice is chosen partly because it can proceed while the daemon work
(`p4-10-fleet`) continues.

| Surface | Collision risk |
|---|---|
| This specification, and docs generally | none |
| `web/ui` | none — this slice is backend-only |
| `web/bridge/factory_types.go`, `factory_store.go`, `factory_http.go` | none — the measured `p4-10-fleet` diff does not touch them |
| `web/bridge/workspace.go` | none — the measured `p4-10-fleet` diff does not touch it |
| `web/bridge/projects.go` | none — the measured `p4-10-fleet` diff does not touch it |

The measured `p4-10-fleet` diff overlaps only daemon/child-process files and
their tests. The landing-time re-check is retained.

## 8. Open questions

Not decided here; they need the user, not an implementer's judgement:

1. Where the SPA exposes editing of a project's factory defaults, and whether
   the defaults editor is part of the project settings screen or its own view.
2. Whether `Native`, `ImplementerAuto` and `NoRequiredCIAcknowledged` should
   become inheritable, which needs a tri-state field on the shipped
   `FactoryConfig` contract.
3. Whether defaults should also be settable per repository rather than only
   per project.
4. Whether a `retry` replacement should re-resolve against the current
   defaults rather than being taken as written (§4.4).
5. The adoption transition itself — see §9.

## 9. Deferred: adopting independent work into a factory

The user said adopting an independent agent while it is still running would be
useful if feasible, and approved the planner filling in missing specifications
from the existing conversation and surfacing ambiguities that need human
input. The transition mechanics — the daemon contracts, concurrent-edit
handling, and the point at which factory control takes effect — are recorded as
unresolved, and the daemon track that owns them is still in flight.

This slice therefore does **not** implement adoption. It is deferred, not
rejected, and it is the next candidate once the daemon contracts settle.

## 10. Acceptance criteria

For this slice:

- a project with defaults creates a factory whose stored draft configuration
  carries the default value for every field the handoff left unset;
- a handoff that sets a field explicitly wins over the project's default for
  that field;
- `RepoID`, `ProjectRoot`, `Native`, `ImplementerAuto` and
  `NoRequiredCIAcknowledged` are never taken from the defaults;
- an explicitly empty `expectedChecks` is preserved rather than replaced by the
  project's default;
- changing a project's defaults after a work item exists does not change that
  work item's stored or approved configuration;
- a project default that conflicts with the handoff is rejected with an error
  naming the conflict;
- a project with no defaults produces exactly the behaviour it produces today;
- the defaults are read and written through the existing project-settings
  surface, with no new route;
- the bootstrap factory's existing acceptance contract still holds: the same
  required fields, size limit, host rules, built-in defaults and
  CI-acknowledgement rule apply.

## 11. Non-goals

- adopting an independent session into a factory (§9);
- editable workflow graphs, stage types, branching or loop semantics;
- reusable templates carrying workflow structure to other projects;
- cross-project factories;
- any SPA surface for editing the defaults;
- inheritable boolean configuration (§4.2);
- changing the factory loop, its CI evidence, its review/repair semantics, or
  its acceptance contract.
