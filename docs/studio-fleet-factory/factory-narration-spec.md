# Narrated engineering report in the pull request description — feature spec

Date: 2026-11-13
Status: spec for the third implementation slice of the studio-fleet-factory
archive. No product code is changed by this document.

## 1. Provenance

This is the design output for the remaining approved behaviour in
[design-notes §6](design-notes.md#6-forge-experience-and-narrated-engineering-reports)
("Forge experience and narrated engineering reports"), and it serves the
journey map's approved node
"[PR ready: CI first, narrated explanation](journey-map.md#overall-journey)".

What is already **approved** by the user, and is therefore treated here as a
requirement rather than a proposal — the §6 "Agreed priorities" list:

- "Show failing CI prominently, with useful detail and access to logs."
- "Compact or collapse successful checks while preserving visible success."
- "Connect findings and failures directly to factory repair."
- "Lead review with the issue and proposed solutions. Reading code is optional."
- "Implementation reports and PR descriptions should include code with
  technical explanations of why and what is happening."
- "Preferred narration is an engineer's technical reasoning, not merely a
  concise summary of the finished solution."

And the paragraph that follows them:

- "Narration should explain relevant existing behaviour, constraints,
  alternatives and tradeoffs, implementation choices, verification, and
  remaining limitations. Selected code excerpts support that account; avoid
  narrating every changed line."

The §6 "Proposals to refine" list is **not** approved and is therefore out of
scope: tying explanations and excerpts to exact revisions, asking reviewers to
check the report's claims, linking the account back to recorded planning
decisions, and the refined form of surfacing preview/test environment links
with their provider-reported status.

Two items need a more careful statement. Surfacing a repository's existing
branch testing, preview deployments or test environments in the pull request
experience is an **agreed** priority; only the refinement of it — showing the
links with their provider-reported status and associated revision — is a
proposal, and its discovery mechanism is explicitly open. That capability is
therefore **deferred pending the provider-capability discovery the proposal
leaves open**, not rejected. Separately, storing narration as structured
content while generating Markdown for external forges is a proposal; this
slice's own transport choice happens to match it, and the fuller proposal —
regenerating the description after repairs and tying passages to exact
revisions — stays out of scope.

The two earlier slices are implemented and their contracts are recorded in
[the discovery spec](existing-project-discovery-spec.md) and
[the project factory defaults spec](project-factory-defaults-spec.md).

## 2. Summary

A factory work item's pull request description becomes the engineer's account
of the change: the CI results first, then the issue and the proposed solution,
then the reasoning — the existing behaviour it had to respect, the constraints
it worked under, the alternatives it weighed, the choice it made, how it
verified the work, and what remains limited — with selected code excerpts
supporting that account.

The narration is produced by the implementer, validated by the bridge, stored
with the round it describes, rendered in the studio, and published to the pull
request when the item is marked ready.

## 3. What exists today

Measured on the reviewed stack head `09d6636b`.

| Piece | Where | State |
|---|---|---|
| Worker report | `web/bridge/factory_types.go:116` | `FactoryReport` carries an operation id, an outcome, a single `summary` string, a question, a reason, base/head SHAs, an inputs-accessible flag and review findings. There is no narration structure and no code excerpt |
| Round | `web/bridge/factory_types.go:128` | `FactoryRound` holds the round's base/head/tree SHAs, its CI evidence, its implementation report and its review report |
| Report parsing | `web/bridge/factory_prompts.go:11` | `ParseFactoryReport` accepts only the final structured block, checks the operation id, the outcome, the summary, and — for a reviewer — the inputs-accessible flag, the findings and the matching SHAs |
| Prompt building | `web/bridge/factory_prompts.go:68` | `BuildFactoryPrompt` asks the implementer for "a concise engineering report" and lists the JSON block's fields |
| Optional forge capability | `web/bridge/factory_forge.go:9` | `FactoryForge` covers CI observation, PR lookup and the draft/ready mutation; `FactoryForgeFor` (`factory_forge.go:18`) type-asserts it and reports `ErrFactoryForgeUnsupported` |
| Forge implementations | `web/bridge/forge_github_factory.go:73` | Only `*githubForge` implements `FactoryForge`. `giteaForge` does not, so the factory loop is GitHub-only today, matching the bootstrap scope |
| PR creation | `web/bridge/factory_effects.go:199` | The draft PR is created with the fixed body "Factory plan and specification are seeded. Implementation is in progress." and is never rewritten |
| Marking ready | `web/bridge/factory_effects.go:242` | The `set_ready`/`set_draft` effect re-reads the PR, checks its identity, state and revision, mutates the draft flag, re-reads it, then observes CI and compensates back to draft if the revision or the CI moved |
| Round assembly | `web/bridge/factory_controller.go:83`, `:252`, `:439` | The round is appended when the implement effect starts; its CI evidence is stored when the observation matches the round's head; its implementation report is stored when the effect completes |
| Studio rendering | `web/ui/src/lib/factory.ts:17`, `web/ui/src/views/FactoryItem.svelte:118` | The report is mirrored as a hand-written TypeScript interface, and the studio renders `round.implementation.summary` as one paragraph beside a CI card that already expands failing checks and collapses passing ones |

There is **no narration**. The implementer's account of the change is one
summary string, the pull request description is a fixed placeholder, and the
approved §6 behaviour is therefore unimplemented.

## 4. Design

### 4.1 The narration contract

The narration is a new optional field on the worker report:

```go
// FactoryNarration is the engineer's account of the change: the issue and the
// proposed solution, the reasoning behind the implementation, and the
// verification that backs it. It is the approved §6 narration — technical
// reasoning rather than a summary of the finished solution.
//
// The sections are the approved list. Constraints are narrated inside the
// approach and the implementation choice, because they are the reasons those
// two sections take the shape they do.
type FactoryNarration struct {
	Issue             string           `json:"issue"`
	Approach          string           `json:"approach"`
	ExistingBehaviour string           `json:"existingBehaviour"`
	Alternatives      string           `json:"alternatives"`
	Tradeoffs         string           `json:"tradeoffs"`
	Choice            string           `json:"choice"`
	Verification      string           `json:"verification"`
	Limitations       string           `json:"limitations"`
	Excerpts          []FactoryExcerpt `json:"excerpts,omitempty"`
}

// FactoryExcerpt is a selected piece of code that supports the narration. It
// is a selection, not a diff: the narration avoids narrating every changed
// line.
type FactoryExcerpt struct {
	Path string `json:"path"`
	Lang string `json:"lang,omitempty"`
	Code string `json:"code"`
}
```

`FactoryReport` gains one field:

```go
	Narration *FactoryNarration `json:"narration,omitempty"`
```

The narration lives on the report, not on the round. The round already stores
its implementation report (`factory_controller.go:439`), so the round's
narration is reachable at `round.implementation.narration`. A second copy on
the round would be state that can disagree with the report it came from.

### 4.2 What the implementer must produce

`BuildFactoryPrompt` asks the implementer for the narration and names its
fields in the final JSON block, so the model is told exactly what the bridge
will read.

`ParseFactoryReport` then validates it:

- for the **implementer** role with outcome `complete`, the report must carry a
  narration whose `issue` and `approach` are non-empty. The approved behaviour
  is that the review leads with the issue and the proposed solution, so those
  two sections are the account's floor.
- every other section may be empty. A change with no meaningful alternatives
  says so by leaving them empty; the bridge does not invent content.
- excerpts are optional. Each excerpt's `path` must be repository-relative: not
  empty, not absolute, no leading `/`, no leading `~`, no `..` segment, no
  backslash, and no empty path segment. A dot-prefixed file name such as
  `.gitignore` is repository-relative and is allowed; a `.` or `..` path
  segment is not. The executable rule is pinned in the implementation plan.
- a reviewer report and an implementer `needs_input` report need no narration.

The bounds are fixed, so that an oversized report fails at parse time rather
than at publish time:

```go
const (
	// factoryNarrationMaxExcerpts is how many excerpts one narration may
	// carry. The narration is a selection, not a diff.
	factoryNarrationMaxExcerpts = 8
	// factoryNarrationMaxSectionRunes bounds each narration section.
	factoryNarrationMaxSectionRunes = 8_000
	// factoryNarrationMaxExcerptRunes bounds one excerpt's code.
	factoryNarrationMaxExcerptRunes = 4_000
	// factoryNarrationMaxTotalRunes bounds the whole narration.
	factoryNarrationMaxTotalRunes = 64_000
)
```

A section over its bound, an excerpt over its bound, more than
`factoryNarrationMaxExcerpts` excerpts, or a narration over
`factoryNarrationMaxTotalRunes` is a report failure. The bounds are checked in
`ParseFactoryReport`, so an oversized narration never reaches the store, the
journal or the pull request.

A completed implementer report without a usable narration is a **report
failure**, exactly as a missing summary is today. It is never silently
downgraded to a summary: the approved behaviour is that the narration is the
account, and a summary alone is the thing the user complained about.

The requirement applies to newly parsed reports only. Journal entries written
before this slice carry no narration and remain valid; the bridge does not
re-validate a stored report's narration on load.

### 4.3 The pull request description

The bridge composes a Markdown body and publishes it to the pull request when
the item is marked ready.

**When.** The body is written in the `set_ready` path, after the existing
revision pre-check and **before** the draft flag is cleared. Two consequences
follow from that position:

- a body write that fails leaves the pull request a draft, so the item is
  never marked ready without its narration;
- the body is written for the exact head SHA the pre-check confirmed, so it
  cannot describe a revision the item did not review.

The `set_draft` path does not write a body.

The compensation path — the `set_ready` effect returning the pull request to
draft because the revision or the CI moved — leaves the body as written. That
body describes the head the item reviewed, which is the reviewed head when only
the CI result moved. When the **revision** raced, the pull request now points
at a different head and the body describes the previously reviewed one until
the next round republishes it. The item is back in draft, so the body is not
presented as ready evidence; it is transient, and the next round's
`set_ready` rewrites it.

**What.** The body is composed from the newest round:

- the round's CI evidence, when it was observed for that round's head SHA.
  Evidence observed for another revision is not this revision's evidence, so
  it reads as unavailable;
- the round's implementation report's narration.

The composition leads with CI, then the narration, in the approved order:

~~~~
## CI
<the failing checks, expanded, with their conclusion, a link and the log tail>
<details><summary>N passing checks</summary>…</details>

## Issue
…
## Approach
…
## Existing behaviour
…
## Alternatives and trade-offs
…
## Implementation choice
…
## Verification
…
## Limitations
…

## Selected code
```<lang>
<path>
<code>
```
~~~~

Sections with no content are omitted rather than rendered empty. The CI
section is always present: when the evidence is unavailable or incomplete the
body says so, because the approved behaviour is that CI is visible and its
absence is never silent.

The body is composed from the newest round only. Earlier rounds' narration
stays in the journal, and the published body always describes the revision the
item is presenting.

A work item whose newest round carries no narration — a pre-upgrade item
whose journal predates this slice — cannot produce a body. `set_ready` is
refused with a recorded reason, mirroring the unsupported-capability
handling, rather than publishing a narration-less description.

### 4.4 The forge capability

Publishing a description is a new **optional** forge capability, separate from
`FactoryForge`:

```go
// FactoryPRDescription is optional: a forge that cannot update a pull
// request description must not pretend it did.
type FactoryPRDescription interface {
	UpdateFactoryPRDescription(ctx context.Context, repo Repo, number int, body string, cred Credential) error
}
```

It is separate rather than another method on `FactoryForge` because
`FactoryForgeFor` is a type assertion: adding a method to `FactoryForge` would
stop a forge that supports CI observation and the draft lifecycle from
satisfying the interface at all, losing capabilities it does have. A separate
interface with its own `ErrFactoryPRDescriptionUnsupported` follows the
existing optional-capability pattern.

`UpdateFactoryPRDescription` is implemented on `*githubForge`, which is the
only forge the factory loop supports today: it PATCHes
`/repos/{owner}/{repo}/pulls/{number}` with the body. Gitea support follows
when the factory gains it; the bootstrap scope is one GitHub repository.

A forge that cannot update a description reports
`ErrFactoryPRDescriptionUnsupported`, and the item is not marked ready.

The capability is asserted the same way `FactoryForge` is: an accessor takes
the `Forge` value and reports `ErrFactoryPRDescriptionUnsupported` when that
value does not implement the interface. The `set_ready` path keeps the
`Forge` value for that assertion rather than discarding it. The accessor
mirrors `FactoryForgeFor`, and the two capabilities are independent: a forge
may support the draft lifecycle without supporting descriptions, and the
recorded reason names the unsupported capability.

### 4.5 The studio

The studio renders the narration instead of the bare summary paragraph: the
issue and the approach first, then the remaining sections, then the excerpts
as code blocks. The CI card is unchanged — it already expands failing checks
and collapses passing ones, which is the approved CI presentation.

The TypeScript mirror in `web/ui/src/lib/factory.ts` gains the narration and
excerpt types and the `narration` field on the report interface, matching the
Go types as the file's header comment requires.

Because the studio is embedded in the binary, the slice rebuilds the embedded
assets (`npm run build` in `web/ui`) and commits them with the change.

### 4.6 What this does not change

- The factory loop, its effect kinds, its state machine and its recovery are
  unchanged. The narration rides the existing `set_ready` effect; no new effect
  kind is introduced.
- The readiness gate is unchanged: `set_ready` still requires the CI evidence
  to be complete and passing for the reviewed head, and the draft-compensation
  race path is untouched.
- The approval contract, the round accounting, the five-round allowance and
  the CI-acknowledgement rule are unchanged.
- The `Forge` interface, the persisted workspace structures and the workspace
  version are unchanged.
- The reviewer role is unchanged: a reviewer's report still carries findings
  and needs no narration.

## 5. Contract sketch

Proposed, not approved. Shapes follow the existing `FactoryReport` and
`FactoryRound` conventions.

```
POST /api/factory/items/{id}/commands   (kind: "retry" with a replacement)
  -> the replacement is a handoff (requestId, title, plan, spec, config);
     it carries no report. The narration arrives with the new round's
     implementer report.

GET /api/factory/items/{id}
  -> 200 {
       "item": {
         "rounds": [
           {
             "number": 1,
             "headSha": "8e4c1b2f…",
             "ci": { "headSha": "8e4c1b2f…", "complete": true, "checks": [ … ] },
             "implementation": {
               "outcome": "complete",
               "summary": "…",
               "narration": {
                 "issue": "The importer dropped rows when a page was empty.",
                 "approach": "Read the page cursor before deciding the batch was done.",
                 "existingBehaviour": "The importer treated an empty page as the end of the stream.",
                 "alternatives": "A sentinel row was considered and rejected.",
                 "tradeoffs": "The cursor check costs one extra comparison per page.",
                 "choice": "Check the cursor, because a sentinel would leak into the data path.",
                 "verification": "Added a fixture with an empty middle page; the importer now imports every row.",
                 "limitations": "A page that legitimately returns no rows still ends the stream.",
                 "excerpts": [
                   { "path": "internal/importer/page.go", "lang": "go", "code": "…" }
                 ]
               }
             }
           }
         ]
       }
     }
```

The pull request description is not a separate endpoint: it is the forge's own
pull request, written by the bridge.

## 6. Missing access and partial failure

- A forge that cannot update a description is an unsupported capability, not a
  silent success. The item is not marked ready and the reason is recorded.
- A body write that fails leaves the pull request a draft. The effect's
  outcome is uncertain, because the forge may or may not have applied the
  write, and the existing recovery path handles it.
- A completed implementer report without a usable narration is a report
  failure. The round does not advance and no summary-only body is published.
- CI evidence that is unavailable or incomplete is stated in the body. It is
  never omitted, and the readiness gate is unchanged.
- A narration section with no content is omitted from the body. It is never
  filled with invented content.

## 7. Parallel-safety against the daemon track

The daemon track is now P5 (the fleet screen). Its plan 2 adds
`web/bridge/inbox.go` and touches the `http.go` route table.

| Surface | Collision risk |
|---|---|
| This specification, and docs generally | none |
| `web/ui` | none — P5 plan 2 does not touch it |
| `web/bridge/factory_types.go`, `factory_prompts.go`, `factory_forge.go`, `factory_effects.go`, `factory_controller.go` | none — P5 plan 2 does not touch them |
| `web/bridge/forge_github_factory.go` | none — P5 plan 2 does not touch it |
| `web/bridge/http.go` route registration | none — this slice adds no route |

This slice adds no HTTP route, so it does not collide with P5's route-table
change. The landing-time re-check is retained.

## 8. Open questions

Not decided here; they need the user, not an implementer's judgement:

1. Whether the narration should be editable by the operator after the fact, or
   is always the implementer's own account.
2. Whether a repair round's narration should be shown alongside the earlier
   round's in the studio, or only the newest.
3. Whether the pull request description should also carry the review findings
   and the repair history, or only the implementation account and the CI.
4. Whether Gitea and Forgejo should gain the factory loop, and with it the
   description capability.

## 9. Acceptance criteria

For this slice:

- a completed implementer report carrying a valid narration produces a pull
  request description that leads with the CI results and contains the issue,
  the approach, and every non-empty narration section, with the excerpts as
  code blocks;
- a completed implementer report with a missing or malformed narration is
  rejected as a report failure — the round does not advance and no
  summary-only description is published;
- an excerpt with an absolute path, a leading `~`, a `..` segment, a
  backslash, an over-long body, or too many excerpts is rejected rather than
  published, and so is an over-long narration section or an over-long
  narration as a whole;
- a forge that cannot update a pull request description refuses `set_ready`
  with a recorded reason, and the pull request stays a draft;
- a work item whose newest round carries no narration — a pre-upgrade journal
  entry — refuses `set_ready` with a recorded reason rather than publishing a
  narration-less description;
- the description is written for the reviewed head SHA, and a pull request
  whose revision has moved is refused before any body is written;
- a body write that fails leaves the pull request a draft and the item not
  ready;
- the CI section is always present, and states that the evidence is
  unavailable or incomplete when it is — including when it was observed for
  another revision rather than this one;
- the studio renders the narration sections and the excerpts, and still renders
  CI with failing checks expanded and passing checks collapsed;
- the factory journal replays a narration-carrying item identically, and
  entries written before this slice remain valid;
- `TestWebIsStdlibOnly` passes, and no new `marshal/internal` import appears
  under `web/`;
- the bootstrap factory's existing acceptance contract is unchanged: the same
  handoff required fields, size limit, host rules, built-in defaults,
  CI-acknowledgement rule, readiness gate and draft-compensation path.

## 10. Non-goals

- the fuller §6 proposals: regenerating the description after repairs, tying
  explanations and excerpts to exact revisions, asking reviewers to check the
  report's claims, linking the account to recorded planning decisions, and
  showing preview/test environment links with their provider-reported status.
  This slice's structured narration and generated Markdown are its own transport
  choice, not an implementation of those proposals. Republishing the
  description at the next round's ordinary `set_ready` is the normal ready
  path, not the excluded regeneration proposal;
- surfacing a repository's existing branch testing, preview deployments or test
  environments in the pull request experience — an agreed priority, deferred
  pending the provider-capability discovery its refinement leaves open;
- automatic merging, GitLab, specialist review focuses, notifications, and
  workflow extensibility — each recorded with its own decision status;
- changing the factory loop, its effect kinds, its state machine, its recovery
  or its readiness gate;
- a new HTTP route, a new forge, or a change to the `Forge` interface;
- editing narration by the operator, or showing earlier rounds' narration
  beside the newest.

## Branch review record

This specification received an independent `glm-5.3-flash` review through
`ollama-cloud` on 2026-11-13, followed by a focused re-review of the fix
commit.

| Review finding | Resolution |
|---|---|
| The compensation-path claim was wrong for the revision race | §4.3 now states that after a revision-race compensation the body describes the previously reviewed head until the next round republishes it, and that the item is back in draft meanwhile |
| The promised bounds were never named, and the narration sections were unbounded | §4.2 now fixes the four bounds — eight excerpts, 8,000 runes per section, 4,000 runes per excerpt, 64,000 runes per narration — and requires them to be checked in `ParseFactoryReport`, so an oversized report fails at parse time |
| The preview/test-environment capability was misclassified as unapproved | §1 and §10 now record it as an agreed priority deferred pending the provider-capability discovery its refinement leaves open |
| A promised behaviour had no acceptance criterion | §9 now covers a forge that cannot update a description refusing `set_ready` with a recorded reason |
| The `create_pr` citation pointed at the case label | §3 now cites `factory_effects.go:199`, the fixed-body `CreatePR` call |
| The §5 sketch described a report on a retry replacement | §5 now states that a replacement is a handoff carrying no report, and that the narration arrives with the new round's implementer report |
| §10 contradicted §1 about structured narration | §10 now excludes the fuller proposals and states that this slice's structured narration and generated Markdown are its own transport choice |
| A pre-upgrade item with no narration had no defined outcome | §4.3 now refuses `set_ready` with a recorded reason rather than publishing a narration-less description |
| The final acceptance bullet read as if the report contract were untouched | §9 now says "the handoff required fields" |

The implementation plan received a second `glm-5.3-flash` review through
`ollama-cloud` on 2026-11-13. Its findings were about the plan's
executability, and each is resolved in the plan.

| Plan review finding | Resolution |
|---|---|
| Task 4's gate could not pass: the `racingFactoryForge` double and the `set_ready` race tests live in `factory_effects_test.go`, but their fixture updates were assigned to Task 5, whose file list excludes that file | The plan's reconciled decision 15 now puts each fixture change in the task whose rule invalidates it: the double, the `openForge` fakes and the race tests change in Task 4; the `deliveryHarness` implementer report changes in Task 2; Task 5 keeps only the harness GitHub handler's description-update branch |
| P4 forbade neither a leading `~` nor a hidden `.` segment, so it contradicted the Task 2 path table | P4 now forbids a leading `~`, an empty segment and a `.`/`..` segment, and states that a dot-prefixed name such as `.gitignore` is allowed; §4.2 states the same rule |
| The CI section did not say what happens to evidence observed for another head SHA | P15 and §4.3 now read that evidence as unavailable, because it describes another revision |
| `TestFactoryPRBodyUsesTheNewestRound` named a behaviour the composer does not have | It is now `TestFactoryPRBodyRendersTheGivenRound`: the composer renders the round it is given, and choosing the newest round is the effect's job |
| The studio test names were Go-style names in a vitest file | Task 6 now names its cases as vitest `it(...)` descriptions, and the acceptance matrix cites them the same way |
| The order of the capability check and the narration check was unstated | P17 pins the order, and states that when both are missing the recorded reason names the unsupported capability |

A focused re-review of that fix commit confirmed all six findings resolved and
found four residual clarity gaps, all now closed: a duplicated Task 4
checklist bullet, reconciled decision 6 omitting the `~` prohibition, the
plan not saying what realises a capability-less forge, and Task 4
understating the ten `Forge` methods the double must stub.
