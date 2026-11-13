# Narrated engineering report — implementation plan

Date: 2026-11-13
Status: approved for autonomous execution by the user's implementation request.
Base: `e6c473cb` (the reviewed narration specification).
Specification: [factory-narration-spec.md](../factory-narration-spec.md).

**Goal:** a factory work item's pull request description becomes the engineer's
account of the change — CI first, then the issue and the proposed solution, then
the reasoning — with selected code excerpts, produced by the implementer,
validated by the bridge, stored with the round, rendered in the studio, and
published to the pull request when the item is marked ready.

## Scope and boundaries

This slice adds:

- `FactoryNarration` and `FactoryExcerpt`, and a `narration` field on the
  worker report;
- parse-time validation of the narration: required sections, four bounds, and
  the excerpt-path rule;
- a composed pull request description — CI first, then the narration;
- an optional forge capability that writes a pull request description;
- the body write inside the existing `set_ready` effect;
- the studio rendering of the narration, with the embedded assets rebuilt.

It does not add a route, widen `Forge` or `FactoryForge`, change the effect
kinds, the state machine, the readiness gate, the draft-compensation path, the
handoff contract, or the workspace format.

All new Go code stays under `web/bridge/` and imports only the standard
library.

## Reconciled implementation decisions

The two Minimax planning briefs agreed on the type, the parse-time
validation, the body composition, the optional forge capability, the write
position, and the studio change. The points below resolve their differences
and record the discrepancies they found.

1. **The narration lives on the report, not the round.** The round already
   stores its implementation report, so the narration is reachable at
   `round.implementation.narration`. A second copy on the round would be state
   that can disagree with the report it came from.
2. **Validation happens at parse time, not at publish time.** An oversized
   narration must fail where the report is read, so it never reaches the
   store, the journal or the pull request.
3. **Only `ParseFactoryReport` validates the narration.** The controller does
   not re-validate it on completion, so a journal entry written before this
   slice stays valid.
4. **The four bounds are fixed**: eight excerpts, 8,000 runes per section,
   4,000 runes per excerpt, 64,000 runes per narration. A value exactly at a
   bound is accepted; only a value over it is rejected.
5. **The narration total is** the sum of the eight section lengths plus the sum
   of the excerpt code lengths. Excerpt paths and languages are excluded.
6. **An excerpt path is repository-relative**: not empty, not starting with
   `/` or `~`, no `\` anywhere, no control character, no `..` segment, and no
   Windows drive prefix. A `.` segment and an empty segment are also
   rejected, so `internal//x.go`, `./x.go` and `internal/` are all refused. A
   dot-prefixed *name* such as `.gitignore` is not a `.` segment, and is
   accepted. The path is bounded at 512 runes.
7. **The body is composed from the newest round.** A round with no
   implementation report, or one whose report carries no narration, cannot
   produce a body: `set_ready` is refused with a recorded reason.
8. **The body write is an uncertain failure, the refusals are not.** A failed
   write is `ErrFactoryUncertain`, because the forge may or may not have
   applied it. A missing capability or a missing narration is
   `ErrFactoryRefused`, which the controller already turns into a recorded
   `conflict` decision.
9. **The description capability is asserted on the `Forge` value**, the same
   value `FactoryForgeFor` is asserted on, so one type assertion covers both
   capabilities of a GitHub forge.
10. **Two narration fields map to one heading.** `## Alternatives and
    trade-offs` is emitted when either `alternatives` or `tradeoffs` is
    non-empty, and each non-empty field becomes its own paragraph under it.
    The studio shows the two as separate headings, which the specification
    leaves free.
11. **Fences are computed, not fixed.** A log tail or an excerpt containing a
    triple backtick would terminate its block early, so the fence is one
    backtick longer than the longest run inside the content.
12. **The studio renders the narration in place of the summary paragraph**
    when a narration is present, and falls back to the summary when it is
    not — which is what a pre-upgrade item carries.
13. **The `preflight` path is untouched.** The capability check belongs in
    `set_ready`, so the handoff contract and its acceptance tests are not
    disturbed.
14. **The embedded assets are rebuilt at the end.** `web/ui/vite.config.ts`
    writes `web/bridge/static/`, which `web/bridge/assets.go` embeds, so the
    build runs only once the studio source is final.
15. **A fixture change lands in the task that invalidates it.** Task 2's
    parse-time rule refuses an implementer `complete` report without a
    narration, so it invalidates the `deliveryHarness` implementer report —
    that fixture gains a narration in Task 2, not Task 5. Task 4's write
    path invalidates the `racingFactoryForge` double and the `set_ready`
    race tests, so those change in Task 4, not Task 5. Task 5 keeps only
    the harness GitHub handler's description-update branch. Without this
    split, Task 2 and Task 4 would leave the tree red at their own commits.

## Contract pins

These are the details the specification left open. They are fixed here, and
every test below depends on them.

| # | Pin | Value |
|---|---|---|
| P1 | Narration total | Σ(eight section rune lengths) + Σ(excerpt `code` rune lengths) |
| P2 | Bound comparison | Reject only when strictly over the bound |
| P3 | Section measurement | The raw string length in runes; not trimmed |
| P4 | Excerpt path rule | Not empty; no leading `/`; no leading `~`; no `\`; no control character; every segment non-empty and neither `.` nor `..`; no Windows drive prefix (a letter, then `:`, then a separator or the end); at most 512 runes. A dot-prefixed name such as `.gitignore` is allowed |
| P5 | Excerpt `code` may be empty | Allowed — an empty excerpt is not malformed |
| P6 | Excerpt `lang` | Optional. When present: no control character, no backtick, at most 64 runes. An omitted language yields a bare fence |
| P7 | Which round supplies the body | The newest round, whatever its head SHA |
| P8 | Newest round with no implementation report | Treated as no narration: `set_ready` is refused |
| P9 | CI wording | "CI evidence is unavailable for this revision." (no CI evidence, or evidence observed for another head SHA) / "CI evidence is incomplete for this revision." / "CI evidence is complete with no reported checks." |
| P10 | Description write failure | `ErrFactoryUncertain` |
| P11 | Refusal class | `ErrFactoryRefused`, which the controller records as a `conflict` decision |
| P12 | Write position | After the revision pre-check, before `SetFactoryDraft`; `set_draft` writes no body |
| P13 | No `preflight` change | The capability check is in `set_ready` |
| P14 | Capability assertion | On the `Forge` value returned by `f.forge` |
| P15 | CI revision match | The round's CI evidence counts only when `round.CI != nil` and `round.CI.HeadSHA == round.HeadSHA`; otherwise the CI section says "unavailable" |
| P16 | Description accessor | `FactoryPRDescriptionFor(f Forge) (FactoryPRDescription, error)` — the same shape as `FactoryForgeFor`, asserted on the `Forge` value, which the `set_ready` branch therefore keeps instead of discarding |
| P17 | Check order in `set_ready` | The capability check runs first, then the narration check, then the compose and the write. When the forge lacks the capability and the round lacks a narration, the recorded reason names the unsupported capability |
| P18 | CI bucketing | A check passes only when its conclusion is `success`. The failed checks are those whose conclusion is set and is neither `success`, `neutral` nor `skipped`; they are expanded with their conclusion, a link and the log tail. The passing checks collapse under their count. The rest — a check with no conclusion, or one that is `neutral` or `skipped` — are listed with their conclusion. A check that did not pass is never counted as passing |
| P19 | Excerpt fence | The fence is computed over the language, the path and the code together, so none of them can close it early; the language is additionally validated at parse time |

## Shared Go types

Task 1 owns these names verbatim. Later tasks consume them without renaming.

```go
// FactoryNarration is the engineer's account of the change: the issue and the
// proposed solution, the reasoning behind the implementation, and the
// verification that backs it.
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

`FactoryReport` gains one field, after `Findings`:

```go
	Narration *FactoryNarration `json:"narration,omitempty"`
```

`FactoryRound`, `FactoryEffect`, `FactoryOutcome` and `FactoryItem` are
unchanged.

## File and dependency map

All paths are relative to the repository root.

| Task | Files | Depends on |
|---|---|---|
| 1 | `web/bridge/factory_types.go`, `web/bridge/factory_prbody.go` (new), `web/bridge/factory_prbody_test.go` (new) | — |
| 2 | `web/bridge/factory_prompts.go`, `web/bridge/factory_prompts_test.go`, `web/bridge/factory_integration_test.go` | 1 |
| 3 | `web/bridge/factory_forge.go`, `web/bridge/forge_github_factory.go`, `web/bridge/forge_github_factory_test.go` | 1 |
| 4 | `web/bridge/factory_effects.go`, `web/bridge/factory_effects_test.go` | 1, 2, 3 |
| 5 | `web/bridge/factory_controller_test.go`, `web/bridge/factory_store_test.go`, `web/bridge/factory_integration_test.go` | 2, 4 |

Tasks 2 and 5 both touch `web/bridge/factory_integration_test.go`: Task 2
gives the harness's implementer report a narration, Task 5 gives its GitHub
handler the description-update branch. Because the Go tasks run sequentially
under one implementer, the second edit is applied on top of the first.
| 6 | `web/ui/src/lib/factory.ts`, `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts` | 1 |
| 7 | `web/bridge/static/**` (rebuilt), `web/bridge/factory_defaults_http_test.go` (unchanged), `web/bridge/boundary_test.go` (unchanged), docs | 1–6 |

`web/bridge` is one Go package, so Tasks 1–5 are sequential under one
implementer. Task 6 is a separate npm package and may run concurrently with
Tasks 1–5: it depends only on the JSON contract frozen in Task 1. Task 7 runs
last, because the asset build writes `web/bridge/static/`.

## Task 1: The narration types and the composed description

**Files:** modify `web/bridge/factory_types.go`; create
`web/bridge/factory_prbody.go` and `web/bridge/factory_prbody_test.go`.

- [ ] Write `TestFactoryPRBodyComposesCIThenNarration`: the body starts with
  `## CI`, and the headings appear in order — `## CI`, `## Issue`,
  `## Approach`, `## Existing behaviour`, `## Alternatives and trade-offs`,
  `## Implementation choice`, `## Verification`, `## Limitations`,
  `## Selected code`.
- [ ] Write `TestFactoryPRBodyOmitsEmptySections`: a narration with only
  `issue` and `approach` yields those two headings and none of the others,
  and no `## Selected code`.
- [ ] Write `TestFactoryPRBodyRendersExcerptsAsFencedBlocks`: each excerpt
  renders as a fenced block whose info string is the language, whose first line
  is the path, and whose body is the code, in narration order.
- [ ] Write `TestFactoryPRBodyAlwaysCarriesCISection`: the CI section is
  present for missing evidence ("unavailable"), incomplete evidence
  ("incomplete"), a complete observation with no checks ("complete with no
  reported checks"), a failing check (expanded with its conclusion, a link and
  the log tail), and passing checks only (collapsed in
  `<details><summary>N passing checks</summary>`); CI evidence observed for
  another head SHA reads as "unavailable", because it describes a revision
  this body does not describe. The checks are bucketed as P18 pins.
- [ ] Write `TestFactoryPRBodyRendersTheGivenRound`: given a round with a
  narration, the body carries that narration. The composer renders the round
  it is given; choosing the newest round is the effect's job, not the
  composer's.
- [ ] Write `TestFactoryPRBodyRefusesARoundWithoutNarration`: a round whose
  implementation report is absent, or whose report carries no narration,
  returns an error.
- [ ] Run
  `go test ./web/bridge -run '^TestFactoryPRBody' -count=1 -v`; confirm the
  new tests fail because the composition does not exist.
- [ ] Implement:
  - the `FactoryNarration` and `FactoryExcerpt` types and the
    `FactoryReport.Narration` field, exactly as frozen above;
  - `func composeFactoryPRBody(round FactoryRound) (string, error)` — the CI
    section first, then the narration sections in order, empty sections
    omitted, then the excerpts; an error when the round carries no narration;
  - `func codeFence(content string) string` — a fence of at least three
    backticks, one longer than the longest run inside the content.
- [ ] Run `gofmt -w` on the three task files; run the named tests; require
  PASS. Run `gofmt -l` on them; require no output.
- [ ] Commit only this task: `feat(factory): add narration types and description composer`.

## Task 2: Parse and prompt the narration

**Files:** modify `web/bridge/factory_prompts.go`,
`web/bridge/factory_prompts_test.go` and
`web/bridge/factory_integration_test.go`.

- [ ] Write `TestParseFactoryReportRequiresNarrationForCompletedImplementer`:
  an implementer report with outcome `complete` and no narration, a null
  narration, an empty `issue`, or an empty `approach` is rejected, and the
  error names the offending field.
- [ ] Write `TestParseFactoryReportNarrationRequirementByRole`: a reviewer
  report and an implementer `needs_input` report parse without a narration.
- [ ] Write `TestParseFactoryReportRejectsInvalidExcerptPaths`: the table of
  path cases — a relative path, a nested relative path and a dot-prefixed
  file name (`.gitignore`, `.github/ci.yml`) are accepted; an absolute path, a
  leading slash, a leading `~`, a `..` segment, a `..` segment mid-path, a
  backslash, a Windows drive path, a forward-slash drive path, an empty path,
  a double slash, a `.` segment and a trailing slash are rejected.
- [ ] Write `TestParseFactoryReportRejectsInvalidExcerptLanguage`: a language
  carrying a backtick, a newline or a control character, or longer than 64
  runes, is rejected, and the error names the offending field.
- [ ] Write `TestParseFactoryReportAcceptsNarrationAtBounds`: a section of
  exactly 8,000 runes, an excerpt of exactly 4,000 runes, exactly eight
  excerpts, and a narration of exactly 64,000 runes are accepted.
- [ ] Write `TestParseFactoryReportRejectsOversizedNarration`: a section over
  its bound, an excerpt over its bound, nine excerpts, and a narration over
  its total bound are rejected, and the error names the offending field.
- [ ] Write `TestFactoryPromptNamesNarrationFields`: the implementer prompt
  names the narration fields, and the reviewer's prompt does not.
- [ ] Update the existing fixtures this task's rule invalidates, so the tree
  stays green: the shared `valid` report in `factory_prompts_test.go` gains a
  narration, and the `deliveryHarness` implementer report in
  `factory_integration_test.go` gains a narration — this task's rule refuses
  an implementer `complete` report without one, and the end-to-end test
  parses exactly that report. Extend the tests; do not weaken them.
- [ ] Run
  `go test ./web/bridge -run '^(TestParseFactoryReport|TestFactoryPrompt)' -count=1 -v`;
  confirm the new tests fail.
- [ ] Run `go test ./web/bridge -run '^TestFactoryEndToEnd' -count=1`; require
  PASS, proving the fixtures this task invalidates are updated.
- [ ] Implement:
  - the four bound constants, exactly as frozen in the contract pins;
  - a `Narration *FactoryNarration` field on the wire struct;
  - the implementer/complete check for a narration with a non-empty `issue`
    and `approach`, placed after the reviewer block so a reviewer report is
    never rejected for a missing narration;
  - `validateFactoryNarration` and `factoryNarrationPathOK`, enforcing the
    pins and naming the offending field in every message;
  - the implementer instruction naming the narration fields, and the final
    JSON block instruction naming them for the implementer only;
  - `Narration: wire.Narration` on the returned report.
- [ ] Run `gofmt -w` on the two task files; run the named tests; require
  PASS. Run `gofmt -l` on them; require no output.
- [ ] Commit only this task: `feat(factory): validate and request narration in reports`.

## Task 3: The optional description capability

**Files:** modify `web/bridge/factory_forge.go`,
`web/bridge/forge_github_factory.go` and
`web/bridge/forge_github_factory_test.go`.

- [ ] Write `TestFactoryGitHubUpdatesPRDescription`: an httptest server
  receives `PATCH /repos/o/r/pulls/7` with the body `{"body": …}` and the
  forge returns nil.
- [ ] Write `TestFactoryGitHubDescriptionUpdateRejectsFailure`: a 403, a 422,
  a 500 and a mismatched pull request number each produce an error.
- [ ] Write `TestFactoryPRDescriptionCapability`: the accessor succeeds for
  the GitHub forge and reports `ErrFactoryPRDescriptionUnsupported` for a
  forge without the capability.
- [ ] Run
  `go test ./web/bridge -run '^TestFactoryGitHub.*Description|^TestFactoryPRDescriptionCapability' -count=1 -v`;
  confirm the new tests fail.
- [ ] Implement:
  - the `FactoryPRDescription` interface, its sentinel error and the
    `FactoryPRDescriptionFor` accessor, in `factory_forge.go`. The accessor
    takes the `Forge` value, exactly like `FactoryForgeFor`, and reports
    `ErrFactoryPRDescriptionUnsupported` when that value does not implement
    the interface;
  - `(*githubForge).UpdateFactoryPRDescription`, which PATCHes
    `/repos/{owner}/{repo}/pulls/{number}` with `{"body": …}` and confirms the
    returned pull request number.
  `FactoryForge` itself is unchanged: a forge that supports CI observation
  and the draft lifecycle must not lose those capabilities.
- [ ] Run `gofmt -w` on the three task files; run the named tests; require
  PASS. Run `gofmt -l` on them; require no output.
- [ ] Commit only this task: `feat(factory): publish pull request descriptions`.

## Task 4: Write the description when marking ready

**Files:** modify `web/bridge/factory_effects.go` and
`web/bridge/factory_effects_test.go`.

- [ ] Write `TestFactoryEffectReadyPublishesComposedDescription`: the body
  the forge recorded equals the composed body, and the forge's event log shows
  the description write before the draft clear.
- [ ] Write `TestFactoryEffectReadyWritesDescriptionForReviewedHead`: with two
  rounds, the body carries the newest round's narration and not the older
  one's.
- [ ] Write `TestFactoryEffectReadyRefusesMovedRevisionBeforeDescriptionWrite`:
  a moved head, a moved base and a changed identity are each refused with no
  body written and no draft mutation.
- [ ] Write `TestFactoryEffectReadyDescriptionFailureLeavesDraft`: a failed
  write is uncertain, and the draft flag was never cleared.
- [ ] Write `TestFactoryEffectReadyRefusesItemWithoutNarration`: the effect is
  refused with a reason naming the missing narration, and nothing is written.
- [ ] Write `TestFactoryEffectSetDraftWritesNoDescription`: the `set_draft`
  effect writes no body.
- [ ] Write `TestFactoryEffectReadyRefusesForgeWithoutDescriptionCapability`:
  the `Forge` value carries no description capability, so the effect is
  refused with the unsupported-capability reason, and nothing is written.
- [ ] Extend the test doubles in `web/bridge/factory_effects_test.go`:
  `racingFactoryForge` implements `Forge` as well as `FactoryForge`, so it
  can be returned in the `Forge` slot as well as the `FactoryForge` slot, and
  it gains the description capability and records the bodies it is given.
  `Forge` has ten methods that the factory effect path never calls
  (`CreatePR`, `GetIssue`, `ListIssues`, `CommentIssue`, `RepoSize`,
  `ListPRs`, `GetPR`, `PostReview`, `ListFailedChecks`, `CheckLog`); stub
  them so they panic if called, rather than silently passing. Add a second
  double without the description capability — it implements `Forge` and
  `FactoryForge` but not `FactoryPRDescription` — to back the refusal test
  above and Task 5's `TestFactoryControllerRecordsReasonForUnsupportedDescription`.
  The `openForge` fakes must return the capable double in both slots, so the
  `Forge` value the effect asserts on is never nil. The existing `set_ready`
  race tests give their item a round whose implementation report carries a
  narration, so they reach the write instead of stopping at the narration
  check. Extend these tests; do not weaken them.
- [ ] Run
  `go test ./web/bridge -run '^TestFactoryEffect' -count=1 -v`; confirm the
  new tests fail.
- [ ] Implement:
  - in the `set_ready`/`set_draft` branch, keep the `Forge` value instead of
    discarding it;
  - on `set_ready` only, after the revision pre-check and before
    `SetFactoryDraft`, in this order: assert the description capability on
    the `Forge` value and refuse when it is absent; select the newest round
    and refuse when it carries no narration; compose the body; write the
    body — an uncertain failure for a failed write, a refusal for a missing
    narration or capability. When both the capability and the narration are
    missing, the recorded reason names the unsupported capability.
  The draft-compensation path, the readiness gate and the effect kinds are
  untouched.
- [ ] Run `gofmt -w` on the two task files; run the named tests; require
  PASS. Run `gofmt -l` on them; require no output.
- [ ] Commit only this task: `feat(factory): publish the description when marking ready`.

## Task 5: Controller, store and end-to-end evidence

**Files:** modify `web/bridge/factory_controller_test.go`,
`web/bridge/factory_store_test.go` and
`web/bridge/factory_integration_test.go`.

- [ ] Write `TestFactoryControllerRecordsReasonForMissingNarration`: an item
  whose newest round has no narration reaches `set_ready` and is refused; the
  recorded decision names the missing narration, the pull request is still a
  draft, and the item is not ready.
- [ ] Write `TestFactoryControllerRecordsReasonForUnsupportedDescription`: the
  same for a forge without the capability.
- [ ] Write `TestFactoryControllerDescriptionFailureLeavesItemNotReady`: a
  failed description write leaves the item not ready, the pull request a
  draft, and the effect uncertain.
- [ ] Write `TestFactoryControllerReportFailureDoesNotAdvanceRound`: an
  implement effect whose report is rejected leaves the round unimplemented.
- [ ] Write `TestFactoryStoreReplaysNarrationItem`: an item whose round
  carries a narration replays identically after the store is reopened.
- [ ] Write `TestFactoryStoreReplaysPreSliceEntryWithoutNarration`: a
  hand-crafted journal entry without a narration opens cleanly and loads with
  no narration.
- [ ] Update the existing test this slice changes by design: the
  `deliveryHarness` GitHub handler gains a branch for the description
  update, so the end-to-end run publishes a body rather than a bare pull
  request. Extend the test; do not weaken it.
- [ ] Run `go test ./web/bridge -run '^TestFactory' -count=1`; require PASS.
- [ ] Run `gofmt -w` on the three task files; run `gofmt -l` on them; require
  no output.
- [ ] Commit only this task: `test(factory): cover narration publication and replay`.

## Task 6: Render the narration in the studio

**Files:** modify `web/ui/src/lib/factory.ts`,
`web/ui/src/views/FactoryItem.svelte` and
`web/ui/src/views/FactoryItem.test.ts`.

- [ ] Write the vitest case `it('renders the narration sections in order')`:
  the narration sections render in order with their headings, and the section
  bodies are present.
- [ ] Write the vitest case
  `it('renders each excerpt with its path and code')`: each excerpt renders
  its path and its code.
- [ ] Write the vitest case
  `it('renders the narration alongside the CI card')`: a failing check is
  expanded with its conclusion, the passing checks are collapsed, and the
  narration is present.
- [ ] Write the vitest case
  `it('falls back to the summary when there is no narration')`: an item with
  no narration still renders the summary paragraph.
- [ ] Run `cd web/ui && npm run test`; confirm the new tests fail.
- [ ] Implement:
  - the `FactoryExcerpt` and `FactoryNarration` interfaces and the
    `narration` field on the report interface, matching the Go JSON names
    exactly;
  - the narration rendering in place of the summary paragraph when a
    narration is present, falling back to the summary when it is not, with
    the excerpts as code blocks.
  The CI card is not touched: it already expands failing checks and collapses
  passing ones.
- [ ] Run `cd web/ui && npm run test`; require PASS.
- [ ] Commit only these three files: `feat(web): render the factory narration`.

## Task 7: Rebuild the assets, prove acceptance, and record the outcome

**Files:** rebuild `web/bridge/static/**`; update
`docs/studio-fleet-factory/factory-narration-spec.md` and this plan.

- [ ] Run `cd web/ui && npm install && npm run build`; confirm
  `web/bridge/static/` changed.
- [ ] Run the feature suite:
  `go test ./web/bridge -run 'TestFactoryPRBody|TestParseFactoryReport|TestFactoryPrompt|TestFactoryEffect|TestFactoryGitHub.*Description|TestFactoryPRDescriptionCapability|TestFactoryController|TestFactoryStore|TestWebIsStdlibOnly' -count=1 -v`.
- [ ] Run the pre-existing contract gates: `go test ./web/bridge -run 'Factory' -count=1`
  and `go test ./web/bridge -run '^TestWebIsStdlibOnly$' -count=1`.
- [ ] Run the package gate: `go test ./web/bridge -count=1`.
- [ ] Run `gofmt -l web/bridge`, `go vet ./web/...`,
  `CGO_ENABLED=1 go build ./...` and `cd web/ui && npm run test`.
- [ ] Run the repository suite: `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 20m`.
  Record unrelated baseline failures separately; do not claim an overall pass
  when any required gate remains red.
- [ ] Add an "Implementation record" to the specification: the branch, the
  task commits, the files touched, the gates and their results, and the open
  questions from §8 that remain open.
- [ ] Record the plan's outcome here: the task/commit table and the gates
  actually run.
- [ ] Commit: `docs(factory): record implemented narrated engineering report`.

## Task 8: Close the branch-review findings

**Files:** modify `web/bridge/factory_prbody.go`,
`web/bridge/factory_prbody_test.go`, `web/bridge/factory_prompts.go` and
`web/bridge/factory_prompts_test.go`.

The `glm-5.3-flash` branch review of this branch found one Important and three
Minor issues. They amend the contract, so the specification and the contract
pins are amended with them: P4 gains the control-character, Windows
drive-prefix and 512-rune path rules, P6 gains the language rule, and P18 and
P19 are new.

- [ ] Write `TestFactoryPRBodyFenceSurvivesAnUnvalidatedLanguage`: an
  excerpt whose language carries a newline and a backtick cannot close its own
  fence, and the composed body keeps the excerpt inside one block.
- [ ] Write `TestFactoryPRBodyBucketsChecksByConclusion`: a `success` check is
  counted as passing; a `failure`, `error`, `cancelled`, `timed_out` or
  `action_required` check is expanded as failed; a `neutral` or `skipped`
  check, and a check with no conclusion, is listed with its conclusion and is
  never counted as passing.
- [ ] Write `TestParseFactoryReportRejectsInvalidExcerptLanguage`: a language
  carrying a backtick, a newline or a control character, or longer than 64
  runes, is rejected, and the error names the offending field.
- [ ] Extend `TestParseFactoryReportRejectsInvalidExcerptPaths` with the
  control-character, Windows drive-prefix and over-512-rune path cases.
- [ ] Run
  `go test ./web/bridge -run 'TestFactoryPRBody|TestParseFactoryReport' -count=1 -v`;
  confirm the new tests fail.
- [ ] Implement:
  - validate the excerpt language at parse time — no control character, no
    backtick, at most 64 runes — and reject a path carrying a control
    character, naming a Windows drive prefix, or longer than 512 runes;
  - compute the excerpt fence over the language, the path and the code
    together, so none of them can close it early;
  - bucket the CI checks by conclusion as P18 pins: only `success` passes,
    the failed set is expanded, and the rest are listed with their
    conclusion.
- [ ] Run `gofmt -w` on the four task files; run the named tests; require
  PASS. Run `gofmt -l` on them; require no output.
- [ ] Run `go test ./web/bridge -count=1`; require PASS.
- [ ] Commit only this task: `fix(factory): close the narration fence and CI bucketing gaps`.

## Acceptance matrix

| Specification criterion | Owning tests |
|---|---|
| A valid narration produces a description that leads with CI and contains every non-empty section and the excerpts | Task 1 `TestFactoryPRBodyComposesCIThenNarration`, `…OmitsEmptySections`, `…RendersExcerptsAsFencedBlocks`; Task 4 `TestFactoryEffectReadyPublishesComposedDescription` |
| A missing or malformed narration is a report failure and no summary-only description is published | Task 2 `TestParseFactoryReportRequiresNarrationForCompletedImplementer`; Task 4 `TestFactoryEffectReadyRefusesItemWithoutNarration`; Task 5 `TestFactoryControllerReportFailureDoesNotAdvanceRound` |
| An excerpt with a bad path, an over-long body, too many excerpts, or an over-long section or narration is rejected | Task 2 `TestParseFactoryReportRejectsInvalidExcerptPaths`, `…RejectsOversizedNarration`, `…AcceptsNarrationAtBounds` |
| An excerpt language carrying a backtick, a newline or a control character, or longer than 64 runes, is rejected | Task 8 `TestParseFactoryReportRejectsInvalidExcerptLanguage` |
| The excerpt fence cannot be closed early by the language, the path or the code | Task 8 `TestFactoryPRBodyFenceSurvivesAnUnvalidatedLanguage` |
| A check that did not pass is never counted as passing, and the CI section buckets the checks as the studio does | Task 8 `TestFactoryPRBodyBucketsChecksByConclusion` |
| The description is written for the reviewed head SHA, and a moved revision is refused before any body is written | Task 4 `TestFactoryEffectReadyWritesDescriptionForReviewedHead`, `…RefusesMovedRevisionBeforeDescriptionWrite` |
| A body write that fails leaves the pull request a draft and the item not ready | Task 4 `TestFactoryEffectReadyDescriptionFailureLeavesDraft`; Task 5 `TestFactoryControllerDescriptionFailureLeavesItemNotReady` |
| The CI section is always present and states unavailable or incomplete | Task 1 `TestFactoryPRBodyAlwaysCarriesCISection` |
| A forge that cannot update a description refuses `set_ready` with a recorded reason and the pull request stays a draft | Task 3 `TestFactoryPRDescriptionCapability`; Task 4 `TestFactoryEffectReadyRefusesForgeWithoutDescriptionCapability`; Task 5 `TestFactoryControllerRecordsReasonForUnsupportedDescription` |
| A pre-upgrade item with no narration refuses `set_ready` with a recorded reason | Task 4 `TestFactoryEffectReadyRefusesItemWithoutNarration`; Task 5 `TestFactoryControllerRecordsReasonForMissingNarration` |
| The studio renders the narration sections and the excerpts, and still renders CI with failures expanded and passes collapsed | Task 6 `it('renders the narration sections in order')`, `it('renders each excerpt with its path and code')`, `it('renders the narration alongside the CI card')` |
| The journal replays a narration-carrying item identically, and pre-slice entries remain valid | Task 5 `TestFactoryStoreReplaysNarrationItem`, `TestFactoryStoreReplaysPreSliceEntryWithoutNarration` |
| `TestWebIsStdlibOnly` passes and no new `marshal/internal` import appears under `web/` | The existing boundary test, run in Task 7 |
| The bootstrap factory's acceptance contract is unchanged | The existing factory tests, run unmodified in Task 7 |

Every test is offline and deterministic: no live forge, no container runtime,
no model provider, and — outside the one end-to-end harness — no real git.

## Execution, commits, and PR policy

- Execute Tasks 1–5 sequentially with one DeepSeek
  `deepseek-v4.1-flash:1017` implementer through the `ollama-cloud` provider.
  Task 6 runs in parallel in the npm package, and Task 7 runs last.
- Each implementer follows test-driven development: add the named failing
  test, capture the expected failure, implement the smallest owning change,
  run the named verification, and commit only that task's files.
- If the `ollama-cloud` provider is unavailable or materially slow, retry the
  same scoped task with `openrouter/deepseek-v4.1-flash:1017` or
  `opencode-go/deepseek-v4.1-flash:1017`; do not split a task across models.
- The implementation PR targets this branch and contains Tasks 1–7.
- Every PR is created as draft. Before it can become ready, dispatch a
  `glm-5.3-flash` reviewer through `ollama-cloud` (fallback:
  `openrouter/glm-5.3-flash` or `opencode-go/glm-5.3-flash`) to review the
  branch against its base. Address every substantive finding, rerun the
  affected gates, and have the reviewer verify the fixes before changing the
  PR from draft to ready.
- Do not merge automatically. The user retains the merge decision.

## Plan self-review

- Coverage: the specification's §4 and §9 map to Tasks 1–7; §10's non-goals
  are respected.
- Contract consistency: the types, the bounds, the path rule, the body shape
  and the write position are fixed in Tasks 1–4 and consumed unchanged.
- Fixture ownership: every fixture a task's rule invalidates is changed in
  that task, so each task's own gate passes and the tree stays green at
  every commit.
- Safety: no route, no widened interface, no change to the effect kinds, the
  state machine, the readiness gate, the draft-compensation path, the handoff
  contract or the workspace format.
- Testability: every acceptance criterion has an offline deterministic test,
  and the pre-existing factory tests are the regression net for the untouched
  contract.
- Collision control: the Go tasks are sequential because they compile as one
  package; the studio task is separate; the asset rebuild runs last.

## Plan outcome record

Executed on branch `studio/factory-narration-implementation`, one commit per
task, test-first. The Go tasks ran sequentially under one implementer; the
studio task ran concurrently in a separate worktree, because `web/ui` is a
separate npm package with a disjoint file set.

| Task | Commit | Outcome |
|---|---|---|
| 1 | `ad1087b1` | The narration types, the `FactoryReport.Narration` field, the composed description and the computed fence |
| 2 | `0a406a0d` | The parse-time narration requirement, the four bounds, the excerpt-path rule, the implementer prompt, and the fixtures that rule invalidates |
| 3 | `3b09a57a` | The optional `FactoryPRDescription` capability, its accessor and the GitHub `PATCH /pulls/{number}` implementation |
| 4 | `65e81c3b` | The `set_ready` write path and the test doubles that carry the capability |
| 5 | `efa47ea7` | The controller, store and end-to-end evidence |
| 6 | `f96781a0` | The studio rendering of the narration sections and excerpts |
| 7 | `70fca650` | The rebuilt embedded assets and the gates below |
| 8 | `4cdaaeec` | The branch-review fixes: the excerpt language validated at parse time, the path refusing control characters, Windows drive prefixes and over-long paths, the fence computed over the language, the path and the code together, and the CI checks bucketed the way the studio's CI card buckets them |

The gates Task 7 ran, and their results, are recorded in the
[specification's implementation record](../factory-narration-spec.md#implementation-record).
Every gate passed: the feature suite, the pre-existing factory contract
gates, the `web/bridge` package gate, `gofmt -l`, `go vet ./web/...`,
`CGO_ENABLED=1 go build ./...`, the 937 studio tests, and the whole-repository
suite `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 20m` (exit 0, no
`FAIL` line).

Two plan decisions were settled during execution, both recorded in the
specification's implementation record: the CI section renders a failing check
as its conclusion, URL and log tail, and the passing checks collapse under the
pinned "N passing checks" wording; and the excerpt fence is computed over the
whole block content, so a path cannot terminate its own fence early.

The open questions in the specification's §8 remain open and are not decided
by this slice.
