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

That snapshot behaviour follows the recorded proposal to save effective handoff
settings with each execution so later project-default changes do not silently
alter running work. It is consistent with that proposal, but is not itself an
approved decision.

## 3. What exists today

Measured on `main` at `907730fb`, with the bootstrap factory merged.

| Piece | Where | State |
|---|---|---|
| Factory configuration | `web/bridge/factory_types.go:8` | `FactoryConfig` carries the whole per-handoff configuration: repository, project root, host, target branch, workspace, native flag, implementer/reviewer routing, reviewer instructions, implementer-auto flag, expected CI checks, no-required-CI acknowledgement, and the round/CI limits |
| Handoff creation | `web/bridge/factory_types.go:26` | `FactoryCreate` carries a request id, title, plan, specification and the configuration |
| Create path | `web/bridge/factory_http.go:54` | `Create` normalises, preflights, then stores the item |
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

That last part holds only because the project-settings views read the record
and write it back unchanged. `PUT /api/projects/settings` replaces the whole
record, so a view that rebuilds the settings object from scratch would erase
`factoryDefaults` silently. Any future settings view must round-trip the
fetched record rather than reconstruct it.

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
  pointer pattern for when that is done; it is deferred (§8 item 2).
  `NoRequiredCIAcknowledged` has a second reason to stay per-handoff: it
  records a human acknowledgement that no CI checks are required, which is a
  decision about one handoff rather than a property of the project.

`ProjectSettings.Workspace` and `FactoryDefaults.Workspace` are two different
things under one name: the first is the project's default workspace for new
agents, the second is the factory's default environment. Neither is seeded
from the other.

### 4.3 How an override is expressed

A handoff overrides a default by setting the field itself. "Unset" is
per-type, and is unambiguous for every inheritable field:

| Field type | Unset | Explicit |
|---|---|---|
| `string` | `""` | any non-empty value |
| `int` | `0` | any non-zero value |
| `json.RawMessage` | `nil`, or zero length | any non-nil, non-empty value, including `{}` |
| `[]FactoryCheckKey` | `nil` | any non-nil slice, **including an empty one** |

The slice case matters: an explicitly empty `expectedChecks` means
"intentionally require no checks", which is distinct from "inherit the
project's expected checks". Resolution must preserve that distinction. It does
not bypass the existing approval rule, which tests the list's length: an empty
list is approved only when the handoff also sets `noRequiredCiAcknowledged`.

A project's own defaults cannot express "no checks": the field is stored with
`omitempty`, so an explicitly empty default list does not survive a save and
load and comes back unset. The handoff's explicitly empty list — the case the
acceptance criteria cover — is preserved.

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

**Proposed default:** a `retry` command that carries a replacement
configuration is an explicit, operator-authored configuration and is **not**
re-resolved against the current defaults: the operator is deliberately
changing the handoff, and the resolved configuration of the existing draft is
the baseline they are editing. This is the proposed behaviour for now; §8 item
4 asks the user whether a replacement should instead re-resolve.

The **create-replay identity** is the request the operator sent, not the
resolved configuration. The store recognises a replayed create by hashing the
request it received, so a client that retries an unchanged create after the
project's defaults have moved on still receives the original work item rather
than a conflict. The resolved configuration is the item's content.

### 4.5 Which project's defaults apply

A valid handoff always names a registered, trusted `ProjectRoot`: the existing
preflight rejects a handoff without one, and `ProjectRoot` is deliberately not
inheritable, so the defaults can never supply it. The named root is therefore
the only live resolution path, and **the named root's defaults apply**.

When the named root and the repository's intake project differ — a handoff may
name a project other than the one whose intake names the repository — the
named root wins, because the handoff is the primary place configuration is
selected.

The intake mapping (`Workspace.ProjectRootByRepo`) is retained only as a
defensive fallback for a handoff that names no root. Such a handoff is
rejected by preflight before any work item exists, so that fallback can only
ever choose which defaults are consulted for a handoff that is about to be
rejected; it never creates a work item and never supplies a root.

### 4.6 Validating the defaults

The defaults are validated when they are written, through the existing
`PUT /api/projects/settings` surface, and again when they are resolved.

**At write time**, the defaults are checked with the parts of
`factoryNormalize` that do not depend on a handoff:

- the limits are not negative;
- `HostID`, when set, is `local` — the factory requires a local host;
- every expected check carries a valid identity: a name, a kind of
  `check_run` or `commit_status`, and an app id that is present exactly for
  `check_run`.

The `workspace`/`native` exclusivity is not checked here: the defaults carry
no `native` field, so that conflict can only arise at resolve time against the
merged configuration (§4.7).

A default that fails any of these is rejected when it is written, with an
error naming the offending field. The identity requirements — a request id, a
title, a plan and a repository — do not apply to defaults, because a default
describes how work runs, not what work is.

The validator never fills anything in. A zero limit in a default means
"unset", and must stay unset so that a handoff can inherit it; the built-in
limits are applied later, to the merged configuration.

**At resolve time**, the defaults are validated again, because a workspace
file can be written by an older binary or edited by hand. A default that is
invalid on its own is an error that names the project's defaults as the
source, not a silent fallback to the handoff's own values. An older binary
that does not know the field silently drops it on its next save, which is a
second reason the resolve-time check exists.

**Conflicts** between a default and a handoff are reported the same way: the
error names both sources.

### 4.7 Conflicts

The resolved configuration must be valid, exactly as a hand-written one must
be. A project default that conflicts with the handoff is rejected with an
error that names the conflict and both sources — for example a project default
`workspace` combined with a handoff `native: true`, which the existing
normalisation already rejects as mutually exclusive.

That is the only cross-source conflict possible: `Native` is the only
non-inheritable field that can collide with an inheritable one. A handoff that
sets both `native` and `workspace` itself is a handoff-internal conflict and
stays with the unchanged normalisation, because the project's defaults are not
involved.

### 4.8 What this does not change

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

The shapes above match the real surfaces: the settings response omits empty
optional fields rather than rendering them, a routing object must carry a
`profile` the factory preflight recognises, and the create response wraps the
item in `item` and answers 201 on create and 200 on a replay.

The sketch is illustrative. `FactoryConfig` carries no `omitempty`, so a real
response renders every configuration field, including the ones the sketch
leaves out.

## 6. Missing access and partial failure

- A project with no defaults behaves exactly as it does today.
- A project whose defaults are invalid is rejected when they are written,
  and again when they are resolved (§4.6); it is never a silent fallback.
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
  naming both sources;
- a project default that is invalid on its own is rejected when it is written,
  and a default that reaches the workspace file another way is rejected when it
  is resolved, naming the project's defaults as the source;
- the defaults applied are the ones of the project named by `Config.ProjectRoot`,
  even when the repository's intake names a different project (§4.5);
- a `retry` replacement is taken as written and is not re-resolved against the
  current defaults (§4.4);
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

## Branch review record

This specification received two independent `glm-5.3-flash` reviews through
`ollama-cloud` on 2026-11-13, followed by a focused re-review of the fix
commit. All findings are resolved.

| Review finding | Resolution |
|---|---|
| The project-resolution order had an unreachable intake fallback | §4.5 now states that a valid handoff always names a registered, trusted `ProjectRoot`, that the named root is the only live resolution path and wins over the repository's intake project, and that the intake mapping is a defensive fallback that never creates a work item or supplies a root |
| Write-time validation of the defaults was promised but undefined | §4.6 now defines the write-time subset, the resolve-time behaviour for defaults that are invalid on their own, and the conflict reporting; §10 gained the matching criteria |
| The boolean-deferral cross-reference pointed at the adoption deferral | §4.2 now points at §8 item 2 and adds the per-handoff acknowledgement reason for `NoRequiredCIAcknowledged` |
| §4.3 misstated what the approval rule distinguishes | It now states that the rule tests the list's length, and that an empty list is approved only with `noRequiredCiAcknowledged` |
| The retry rule was stated as settled while §8 reopened it | §4.4 now marks it a proposed default and cross-links §8 item 4 |
| The snapshot semantics adopted a recorded proposal without saying so | §2 now marks it as consistent with the recorded proposal, not an approved decision |
| The create-path citation was off by two | §3 now cites `factory_http.go:54` |
| "The SPA needs no change" hid a whole-record-replacement caveat | §4.1 now states that the defaults survive only because the settings views round-trip the fetched record |
| A write-time check could not apply to the defaults | The `workspace`/`native` bullet was removed from §4.6's write-time list and the conflict left to §4.7 |
