# Preview and test-environment links in the PR experience — implementation plan

Date: 2026-11-15
Status: awaiting user approval before execution.
Base: authored against `40c2194d`; recheck anchors against the current target
branch before execution. The source commit includes P5 work outside this slice;
do not merge it solely to obtain these documents.
Specification: [preview-environment-links-spec.md](../preview-environment-links-spec.md).

**Goal:** show discovered preview and test-environment links — provider-reported
status, preview link and revision — in the pull-request experience: a snapshot
section in the factory-written PR description at `set_ready`, and a live card on
the studio's work-item view, each badging the deployment's revision against the
PR head by SHA string equality (the Studio comparator is explicitly the last factory-
recorded head). The frozen preview report is read, never
changed; nothing is persisted.

## Scope and boundaries

This slice adds:

- a `## Preview and test environments` section in the body
  `composeFactoryPRBody` writes, rendering the preview report's deployment rows
  (environment, provider state, short revision, equality badge, preview link,
  logs link, description), the configured-environment links, labelled
  commit-status links from the round's CI evidence, and the report's gaps;
- a `previewSource` seam on `factoryEffects` so the `set_ready` effect fetches
  one report immediately before composing the body, best-effort — a fetch
  failure never fails `set_ready`;
- `web/ui/src/lib/preview.ts` wire mirrors, `getPreviewEnvironments` in
  `api.ts`, and a "Preview and test environments" card in `FactoryItem.svelte`
  fetched on view and on manual refresh.

It does not change the frozen preview contract (`previewCapabilityOrder`, the
wire types, `PreviewForge`), the discovery contract, `FactoryRound` /
`FactoryItem` / `contract.ts`, or the `Forge` / `DiscoveryForge` /
`PreviewForge` interfaces. It adds no forge request path beyond the reads
`Fleet.PreviewEnvironments` already makes. It does not build or manage any
preview environment, persist or poll any report, regenerate the description
after statuses change, or present ancestry ("N commits behind"). The check-run
exclusion from the labelled links and the exact badge strings beyond the two
the user chose are the spec's §11 flagged assumptions, applied as written.

## Contracts

```go
// web/bridge/factory_effects.go — one new field on factoryEffects (existing
// struct at factory_effects.go:19), beside openForge:
previewSource func(context.Context, Repo, PreviewFilter) (*PreviewReport, error)

// web/bridge/factory_prbody.go — composer signature change (was
// composeFactoryPRBody(round FactoryRound) (string, error)):
func composeFactoryPRBody(round FactoryRound, preview *PreviewReport, head string) (string, error)

// web/bridge/factory_prbody.go — the preview section (content below the
// heading; the composer writes the heading and calls this):
func factoryPreviewBody(round FactoryRound, preview *PreviewReport, head string) string

// Helpers, all pure:
func factoryPreviewBadge(deploySHA, head string) string // "matches the PR head" | "not the PR head" | "revision not reported by the provider"
func factoryInlineCode(value string) string             // bounded protected inline span (spec D10)
func factoryPreviewSafeURL(value string) bool             // absolute HTTP(S), spec D10
func factoryShortSHA(sha string) string                 // first 7 chars, verbatim when ≤7
func factoryPreviewGapLines(gaps []DiscoveryGap) string // grouped lines, spec §5.2
func factoryPreviewStatusLinks(round FactoryRound) []FactoryCheck // commit_status with URL, sorted by name then ID string
```

```ts
// web/ui/src/lib/preview.ts (new) — wire mirrors of the report's JSON:
export interface PreviewCapability { key: string; state: string; detail: string }
export interface PreviewGap { key: string; section: string; state: string; detail: string }
export interface PreviewEnvironment { name: string; id: string; htmlUrl: string; branchPolicy: string; protectionRuleCount: number }
export interface PreviewDeployment { id: string; environment: string; originalEnvironment: string; ref: string; sha: string; task: string; state: string; environmentUrl: string; targetUrl: string; logUrl: string; description: string; transient: boolean; production: boolean; createdAt: string; updatedAt: string }
export interface PreviewReport { repoId: string; forge: string; status: string; error?: string; ref: string; sha: string; capabilities: PreviewCapability[]; environments: PreviewEnvironment[]; deployments: PreviewDeployment[]; gaps: PreviewGap[] }

// web/ui/src/lib/api.ts:
export async function getPreviewEnvironments(repoId: string, ref: string): Promise<PreviewReport>
```

Every rendering string, ordering rule and edge case these contracts render is
fixed in the specification §5 (description) and §6 (studio card); tasks cite
the sections rather than restating them.

## Task 1: Compose the preview section in the PR description

**Files:** `web/bridge/factory_prbody.go`, `web/bridge/factory_prbody_test.go`,
`web/bridge/factory_effects.go`, `web/bridge/factory_effects_test.go`.

- [ ] In `factory_prbody_test.go`, update all ten `composeFactoryPRBody(...)`
  call sites (lines `:38`, `:60`, `:83`, `:109`, `:145`, `:206`, `:234`,
  `:241`, `:249`, `:253` — all use the `prBodyRound` helper at `:12`) to the
  new signature with `nil, ""`. Fix the placement assertions to expect the
  preview section between `## CI` and `## Issue` (spec D8): in
  `TestFactoryPRBodyComposesCIThenNarration` (`:37`) the order is now CI →
  `## Preview and test environments` → narration.
- [ ] Write `TestFactoryPreviewBodyBadgesAgainstTheHead` (spec D5): equal SHAs
  → `matches the PR head`; different → `not the PR head`; a case-only SHA
  difference → `matches the PR head`; empty deployment `sha` → `revision not
  reported by the provider` with no equality claim; empty `head` with a
  present deployment `sha` → `not the PR head`.
- [ ] Write `TestFactoryPreviewBodyRowsFollowTheRenderingContract` (spec
  §5.3): the row segments join with ` — `; empty `state` renders
  `(no status reported by the provider)` in place of `` `{state}` ``; empty
  `sha` replaces the revision-and-badge segment with `— revision not reported
  by the provider`; ` (logs: {logUrl})` follows the link after one space;
  when `environmentUrl` is empty the link segment and `logUrl` both drop;
  `targetUrl`, `task`, `transient`, `production`, `originalEnvironment`, `id`,
  `createdAt`, `updatedAt` never render.
- [ ] Write `TestFactoryPreviewBodyGroupsGapsByDetail` (spec §5.2): gaps
  sharing one `detail` render one `Not fully read —` line listing their keys
  joined `", "` in frozen capability order; distinct details render separate
  lines ordered by each group's first key in the report's `gaps` order.
- [ ] Write `TestFactoryPreviewBodyStatusLinksFilterSortAndGuard` (spec §5.5,
  D2): only `Key.Kind == "commit_status"` rows with a non-empty `URL` render,
  under `Links reported by commit statuses:`, sorted by name then ID string;
  the state uses `factoryCheckConclusion` wording; when `round.CI == nil` or
  `round.CI.HeadSHA != round.HeadSHA` the whole subsection is absent.
- [ ] Write `TestFactoryPreviewBodyStatesEmptyAndUnreadable` (spec §5.6):
  `preview == nil` and `preview.Status == "unavailable"` each render exactly
  the heading plus `Preview and test environments could not be read when this
  description was written.` with no scope line; a readable report with no
  deployment rows, no environment rows and no gaps renders the scope line plus
  `No preview deployments or test environments are configured for this
  revision.` even when status links render below it; a readable report with
  gaps and nothing else renders gap lines and no empty line.
- [ ] Write `TestFactoryPreviewBodyTruncatesAtTenRowsPerList` (spec D9): 11
  deployments (or environments, or status links) render 10 plus `…and 1 more`;
  the report order is preserved.
- [ ] Write `TestFactoryPreviewBodyProtectsProviderStrings` (spec D10): a
  provider string containing a newline renders on one line inside a backtick
  run one longer than its longest internal run, padded when it begins or ends
  with a backtick; ref and gap details are protected and bounded. Valid HTTP(S)
  URLs use safe Markdown autolinks; javascript, data, malformed, whitespace,
  angle-bracket and oversized URLs appear only as bounded protected text.
  Exercise environment, deployment, log and status URL fields.
- [ ] Run `go test ./web/bridge -run '^(TestFactoryPreviewBody|TestFactoryPRBody)' -count=1`;
  confirm failure.
- [ ] In this same task update the production call in `factory_effects.go`
  and the call in `factory_effects_test.go` to pass `nil, ""`. This keeps the
  package compilable before Task 2 supplies the real report and head.
- [ ] Implement in `factory_prbody.go`: change `composeFactoryPRBody` to the
  contract signature; after the `## CI` block write the heading
  `## Preview and test environments` and `factoryPreviewBody(round, preview,
  head)`, before the `## Issue` section. `factoryPreviewBody` renders, in
  order, the rules of spec §5.1–§5.6: the scope line (short head via
  `factoryShortSHA`; the "every revision" wording when `report.Ref` is empty),
  gap lines, `Configured environments:` rows (`- \`{name}\` — {htmlUrl}`,
  URL dropped when empty), deployment rows, `Links reported by commit
  statuses:` rows (`- \`{name}\` (\`{state}\`): {url}`), and the empty or
  unreadable line. Implement the four pure helpers per the Contracts
  signatures. The unreadable case is `preview == nil || preview.Status ==
  "unavailable"` (D11).
- [ ] Run `go test ./web/bridge -run '^(TestFactoryPreviewBody|TestFactoryPRBody)' -count=1`;
  require PASS. Run `gofmt -l web/bridge` (no output) and
  `go vet ./web/bridge/...` (clean).
- [ ] Commit only this task: `feat(preview-links): compose the preview section in the PR description`.

## Task 2: Fetch the report at `set_ready` through the `previewSource` seam

**Files:** `web/bridge/factory_effects.go`, `web/bridge/factory_effects_test.go`.

- [ ] Write `TestFactoryEffectReadyFetchesPreviewAtRender` (spec D3, D4,
  D13): with a stubbed `previewSource`, `set_ready` calls it with
  `PreviewFilter{Ref: before.HeadRef}` — never a `SHA` — and the composed
  description contains the preview section computed against `before.HeadSHA`
  (e.g. a deployment equal to the head badges `matches the PR head`).
- [ ] Write `TestFactoryEffectReadyPreviewFailureStillWritesDescription`
  (spec D11): a `previewSource` returning an error — and a separate case
  returning a report with `status == "unavailable"` — still yields a
  successful `set_ready`, a written description containing exactly the §5.6
  unreadable line, and the draft flag cleared. `set_draft` writes no body and
  never calls `previewSource`.
- [ ] Write `TestFactoryEffectReadyPreviewTimeout`: a stub waits for the
  preview child context, verifies its five-second deadline, returns its error,
  and the description and ready transition succeed with a live parent. Also
  test that a canceled parent stops the effect rather than forcing a write.
- [ ] Run `go test ./web/bridge -run '^TestFactoryEffectReady' -count=1`;
  confirm failure on the new tests.
- [ ] Implement: add `previewSource` to `factoryEffects` (the `openForge`
  seam's sibling at `factory_effects.go:19-24`) and resolve it exactly as
  `forge()` resolves `openForge` — the field when set, otherwise
  `f.fleet.PreviewEnvironments`. In the `set_ready` branch, after the
  narration guard and before `composeFactoryPRBody` (`factory_effects.go:276`),
  create a five-second `context.WithTimeout` child of the effect context; fetch
  with it and `PreviewFilter{Ref: before.HeadRef}`, cancel the child immediately
  afterwards, and turn a preview error into `nil`. If the parent is canceled,
  return its error. Otherwise compose and write using the original parent
  context and pass `preview, before.HeadSHA` to the composer. Task 1 already
  updated the test and production call signatures.
- [ ] Run `go test ./web/bridge -run '^TestFactoryEffect' -count=1`; require
  PASS. Run `go test ./web/bridge -count=1`; require PASS.
- [ ] Commit only this task: `feat(preview-links): fetch the preview report at set_ready`.

## Task 3: Mirror the report wire types and add the studio fetch

**Files:** create `web/ui/src/lib/preview.ts`; edit `web/ui/src/lib/api.ts`.

- [ ] Create `web/ui/src/lib/preview.ts` with the five mirror interfaces
  exactly as in Contracts (the `factory.ts` hand-mirror precedent; do not
  touch `contract.ts`).
- [ ] In `api.ts`, beside the factory wrappers (`getFactoryItem` at
  `api.ts:1346`), add `getPreviewEnvironments(repoId, ref)` per Contracts:
  `request<PreviewReport>('GET', \`/api/repos/${encodeURIComponent(repoId)}/preview-environments${ref ? \`?ref=${encodeURIComponent(ref)}\` : ''}\`)`,
  importing the types from `./preview`. The `ref` query parameter is omitted
  when `ref` is empty (spec D12).
- [ ] Run `cd web/ui && npm run check && npm test`; require PASS.
- [ ] Commit only this task: `feat(preview-links): mirror preview report types and add the studio fetch`.

## Task 4: Show the preview card on the work-item view

**Files:** `web/ui/src/views/FactoryItem.svelte`,
`web/ui/src/views/FactoryItem.test.ts`.

- [ ] Write tests in `FactoryItem.test.ts` (extend the existing
  `vi.mock('../lib/api', …)` at `:6` with `getPreviewEnvironments: vi.fn()`):
  `it('shows preview rows badged against the newest PR head')` — a mocked
  report with one deployment matching `item.prs.at(-1).headSha` shows
  `matches the last factory-recorded PR head`, and one differing shows
  `differs from the last factory-recorded PR head`;
  `it('shows preview rows unbadged when no PR is recorded')` — empty
  `item.prs`, rows show revisions with no badge text;
  `it('lists commit-status links as provider links')` — rows under `Links
  reported by commit statuses:`;
  `it('shows the preview empty state')`; `it('shows the preview error
  state')`; `it('refreshes the preview report on demand')` — the Refresh
  button triggers a second `getPreviewEnvironments` call. Add tests
  that advancing repeated item poll ticks does not issue extra preview reads;
  an external PR push cannot make the card claim its recorded SHA is current;
  and every provider URL rejects unsafe schemes and malformed values.
- [ ] Run `cd web/ui && npm test`; confirm the preview tests fail.
- [ ] Implement the card in `FactoryItem.svelte`, after the "Pull requests"
  card (`FactoryItem.svelte:156`) and its `{/if}`: state
  `preview = $state<PreviewReport | null>(null)`, `previewLoading`,
  `previewError`; a `loadPreview()` that calls
  `api.getPreviewEnvironments(item.repoId, item.branch)` when `repoId` is
  non-empty, invoked once when the first item loads and from a `Refresh`
  button; do not call it on the regular item polling path. Guard concurrent
  manual refreshes and ignore stale responses after unmount or a scope change.
  Render per spec §6: the card titled `Preview and test
  environments`; loading `Reading preview environments…`; fetch error
  `Preview and test environments could not be read.` with the error as muted
  text; `status === 'unavailable'` the same line with `report.error` muted;
  the §5.6 empty line when there are no deployment rows, no environment rows
  and no gaps. Rows mirror §5's content untruncated (links as
  `<a target="_blank" rel="noopener noreferrer">`); the badge comparator is
  `item.prs?.at(-1)?.headSha` — display the short SHA and label matches as
  `matches the last factory-recorded PR head` or `differs from the last
  factory-recorded PR head`. Do not imply it is the current forge head; rows
  carry no equality badge when no recorded head exists.
  Gaps render one line per gap: `{key} — {state} — {detail}` with `key` in
  `<code>`. Svelte interpolation escapes text, but does not validate hrefs:
  apply D10 validation to all links and render invalid URLs as text. No
  `{@html}`. Bound provider text including ref, SHA and gap details as D10 says.
- [ ] Run `cd web/ui && npm run check && npm test`; require PASS.
- [ ] Commit only this task: `feat(preview-links): show the preview card on the work-item view`.

## Task 5: Record the outcome and prove acceptance

**Files:** `docs/studio-fleet-factory/preview-environment-links-spec.md`,
`docs/studio-fleet-factory/README.md`.

- [ ] Add an "Implementation record" section to the spec naming this plan's
  tasks with their commit hashes and the acceptance gates below, in the shape
  the discovery spec's implementation record uses.
- [ ] Add the archive README entry for this slice, beside the discovery
  slice's entry.
- [ ] Run the acceptance gates of spec §9:
  `go test ./web/bridge -count=1` (PASS),
  `gofmt -l web/bridge` (no output), `go vet ./web/bridge/...` (clean),
  `CGO_ENABLED=1 go build ./...` (exit 0),
  `cd web/ui && npm run check && npm test` (PASS). Confirm the frozen-contract
  tests pass unmodified: `go test ./web/bridge -run
  '^(TestPreviewCapabilityOrderIsStable|TestPreviewContractJSONUsesStableShape|TestDiscoveryCapabilityOrderIsStable)$'
  -count=1`, and `go test ./web/bridge/contract/tsgen -run
  TestTypeScriptContractIsCurrent -count=1` for `contract.ts`. Confirm
  `TestWebIsStdlibOnly` passes.
- [ ] Commit only this task: `docs(preview-links): record the implementation and acceptance gates`.

## Full-project verification

After all tasks, from the repository root:

- `CGO_ENABLED=1 go build ./...` — exit 0.
- `go test ./...` — PASS (the repository's own suite; CI excludes named
  laptop latency budgets locally if any fail on this machine).
- `go vet ./...` and `gofmt -l .` — clean.
- `cd web/ui && npm run check && npm test && npm run build` — PASS.
- The boundary invariants (`TestWebIsStdlibOnly`,
  `TestTUIStaysOnTheClientSeam`) run inside `go test ./...`.

## Integration notes

- The composer signature change is deliberate (spec D3): one production call
  site (`factory_effects.go:276`) and the test call sites listed in Task 1.
  If any other `composeFactoryPRBody` caller appears at execution time, update
  it to `(round, nil, "")` and note it in the task commit.
- The preview fetch reuses `Fleet.PreviewEnvironments` (`preview_environment_report.go:48`)
  unchanged; the report's own gaps and bounds carry all partial-failure
  semantics, so the composer never classifies failures itself.
- The studio card deliberately mirrors the description's row content rather
  than inventing a second vocabulary (spec D1); when adjusting rendering,
  adjust both surfaces and the spec together.
