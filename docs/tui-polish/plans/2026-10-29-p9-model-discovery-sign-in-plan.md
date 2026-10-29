# P9 model discovery and sign-in — implementation plan

Date: 2026-10-29. Status: proposed; not executed.
Base: `bc564242b461d8f44f817d38a7b26d39c71639e4`.
Requirements: [triage item 10](../triage.md#p2--interaction-and-visibility),
including [selection moving during discovery](../audit.md#model-discovery-can-move-the-selection-while-the-user-is-choosing),
[late skipped probe results](../audit.md#skipped-provider-probes-can-interrupt-later-wizard-steps),
and [MCP authorization URL fallback](../audit.md#mcp-sign-in-fallback-cannot-expose-a-long-authorization-url).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** asynchronous discovery cannot silently change the selected model or
revive an abandoned setup step, and MCP sign-in works without an opened browser.

**Non-goals:** change provider routing, OAuth protocol, credential storage,
model ranking, privacy gates, other pickers' default behavior, or web UI.
This is phase 9 in the plan sequence and addresses P2 triage item 10.

**Decisions and assumptions:**

- Identify a model by the existing picker `Item.Value`: in all-provider mode
  it encodes provider and model using `encodeModelValue`, so identical model
  IDs from different providers remain distinct. Preserve the custom-value row
  as a distinct selection. If a selected row disappears after a refresh,
  show `Selected model is no longer available; choose another` and leave no
  row selected; Enter does nothing until the user moves or edits the filter.
  Never silently pick the replacement row. If the list was initially empty
  and never had a selection, the first arrival may select the first match.
- Setup probes and `/models` discovery have different owners. A setup result
  is accepted only by the wizard instance, active probe generation, provider,
  and `stepProbing` that launched it. A newer retry, skip, cancellation or
  replaced wizard invalidates it. Guard before changing either session
  discovery or disk cache; success and failure are equally stale.
- `/models` launches probes in the parent model. For each provider, only its
  newest dispatched request may update discovery, cache or an open picker.
  A live result may populate the shared cache after the picker closes, but it
  may refresh a connect picker only while that picker is in the all-provider
  model-selection step. A setup wizard must never treat it as its own probe.
  Retain existing privacy gating, timeout, authentication and cache behavior.
- `c` copies the exact current MCP authorization URL using the host's
  existing clipboard path and feedback. Also render the entire URL as a
  pageable, cell-width-wrapped body, so manual transcription remains possible
  if the local clipboard or OSC 52 is unavailable. Escape still cancels.
  Do not put the URL in logs or transcripts. Copy success and OSC 52 dispatch
  are distinct: OSC 52 cannot confirm that the terminal accepted the copy.
- Use local fixtures and injected auth, browser and clipboard dependencies.
  Preserve unrelated untracked files. Tasks run sequentially and can be
  committed individually after their checks.

## Contracts

Task 1 adds the following picker method, used by Task 2:

```go
// ReplaceItems refreshes the choices while retaining the filter input and
// selected value (including the custom row). It returns false when a former
// selection disappeared; in that case there is no selection until user input.
func (m *Model) ReplaceItems(items []Item) bool
```

Task 3 introduces this message and validation method in `connect`, used by
the parent TUI to reject stale setup probes before shared cache updates:

```go
type WizardProbeResult struct {
    Owner      *Model
    Generation uint64
    Result     probe.ResultMsg
}

func (m *Model) AcceptsWizardProbe(msg WizardProbeResult) bool
```

Task 4 adds the parent-private completion type below. Generation is assigned
per provider when dispatched; `Result.Provider` must equal `Provider`.

```go
type modelsProbeResultMsg struct {
    Provider   string
    Generation uint64
    Result     probe.ResultMsg
}
```

Task 5 adds the following request in `mcpauth`, handled by the host in Task 6:

```go
type CopyLinkMsg struct {
    Panel *Panel
    URL   string
}

func (p *Panel) AcceptsCopyLink(msg CopyLinkMsg) bool
```

The message captures the panel identity and URL. `AcceptsCopyLink` verifies
that identity, that the URL is nonempty and still equals the current display,
and that cancellation has not begun. No clipboard or browser implementation
enters the panel.

## Task 1: Preserve picker selection across item replacement

**Goal:** callers can update picker items without moving an existing choice or
discarding filter text and cursor position.

**Files:** `internal/app/tui/picker/picker.go`,
`internal/app/tui/picker/picker_test.go`.

**Steps:**

1. Implement `ReplaceItems` from Contracts. Snapshot the selected match's
   value and whether it is the synthetic custom row (`matches` index `-1`),
   replace `items` with a copy, refilter without changing the textinput or its
   cursor, then restore the same matching value and custom/ordinary identity.
   If no matching selection survives, set cursor to `-1`, set the exact error
   in Decisions, and return false. If no row was selected before replacement
   because the picker initially had no matches, select the first new match;
   keep a distinct pending-selection state after a disappearance so a second
   refresh cannot silently select a row.
2. Update `Model.Update` and `View` for cursor `-1`: Enter emits no
   `PickedMsg`; Up or Down selects the first available match; filter typing
   or paste clears the error and chooses the normal first match; rendering
   has no selection marker. Reserve a visible line for the missing-selection
   error inside the actual dock height, including at 80×24, rather than letting
   the frame clip the error below the list. Preserve all pre-existing behavior
   when callers never invoke `ReplaceItems`.
3. Add `TestReplaceItemsPreservesSelectionAndFilterCursor` for reordered
   rows, identical labels with distinct values, custom input, and a middle
   text cursor. Add `TestReplaceItemsRequiresExplicitChoiceWhenMissing` for
   disappearance, repeated refresh, Enter, and explicit navigation/typing;
   test empty-to-populated arrival and synthetic custom-vs-real collisions.

**Verify:**

```bash
go test ./internal/app/tui/picker -count=1
gofmt -l internal/app/tui/picker
```

Expected: tests pass and formatting prints no paths.

## Task 2: Keep the current model choice while discoveries arrive

**Goal:** rebuilding `/models` choices preserves the exact provider/model or
requires an explicit new choice if that value disappeared.

**Files:** `internal/app/tui/connect/connect.go`,
`internal/app/tui/connect/connect_test.go`.

**Steps:**

1. Change `rebuildModelPicker` to build fresh items with `buildModelPicker`
   and call the existing picker's `ReplaceItems` rather than replacing the
   picker. If the picker is nil, initialize it normally. Keep its original
   title, footer, filter and custom behavior.
2. In `handleProbeResult`, apply all-provider list results only when
   `m.allProviders && m.step == stepPickModel`; otherwise ignore them. Do
   not treat a parent `FieldID: "models"` result as a setup probe. Setup
   result correlation is added in Task 3; do not alter its behavior here.
3. Add `TestDiscoveryKeepsSelectedProviderModel` using real `Update` keys:
   select a non-first model, deliver a different provider's success and
   failure, and assert Enter returns the originally selected encoded value.
   Include same model ID from two providers, a nonempty filter and the custom
   row. Add `TestDiscoveryRemovedSelectionRequiresChoice` for removal and
   explicit recovery, including a second result arriving before user input.
   Add `TestAllProviderResultDoesNotReopenLaterWizardStep` for success and
   failure delivered after selection has advanced.

**Verify:**

```bash
go test ./internal/app/tui/connect -count=1
gofmt -l internal/app/tui/connect
```

Expected: tests pass and formatting prints no paths.

## Task 3: Correlate setup probes with the active wizard

**Goal:** skipped, superseded, cancelled and replaced setup probes cannot
change wizard state or discovery cache.

**Files:** `internal/app/tui/connect/connect.go`,
`internal/app/tui/connect/connect_test.go`, `internal/app/tui/model.go`,
`internal/app/tui/model_test.go`.

**Steps:**

1. Add `WizardProbeResult` and `AcceptsWizardProbe` from Contracts. Add a
   monotonically increasing private generation to `connect.Model`.
   `enterProbing` increments it and wraps the `runProbe` command's
   `probe.ResultMsg` with the wizard pointer and captured generation; retries
   get a new generation. `AcceptsWizardProbe` checks owner identity, current
   generation, `stepProbing`, and exact provider. `skipProbe` and `cancel`
   invalidate the generation before advancing; an inactive step is rejected
   even if no explicit invalidation occurred.
2. Handle `WizardProbeResult` in `connect.Model.Update` only after validation;
   pass its inner result to the existing `handleProbeResult`. Remove the
   setup path's acceptance of raw `probe.ResultMsg`; raw `FieldID: "models"`
   results remain exclusively for the all-provider picker. Update existing
   setup tests to construct the real current wizard result, not an untagged
   message; do not weaken stale-result checks for test convenience.
3. In parent `Model.Update`, handle `connect.WizardProbeResult` only if the
   current dock panel is `connect.Panel`, its `Model` equals `Owner`, and
   `AcceptsWizardProbe` succeeds. Only then apply the existing
   `PreserveThinkingOptions`, session discovery update and cache persistence
   to successful results before forwarding the validated wrapper to the
   dock. Ignore stale success/failure entirely, including when the panel has
   been closed or replaced. Leave ordinary probe results for other consumers
   on their existing path.
4. Add `TestSetupProbeRejectsSkippedAndSupersededResults`: delay two
   controlled results, skip or retry, deliver old success and error after
   moving into model selection and a later wizard step, and assert picker,
   chosen value, error and discovered data remain unchanged. Deliver the
   current retry and assert normal success. Add
   `TestStaleWizardProbeCannotPopulateHostCache` using the real parent update
   and temporary model-cache directory; cover same provider with two wizard
   instances, close, skip and retry, plus an accepted result.

**Verify:**

```bash
go test ./internal/app/tui/connect -count=1
go test ./internal/app/tui -run 'Test(StaleWizardProbeCannotPopulateHostCache|Probe)' -count=1 -timeout 90s
gofmt -l internal/app/tui/connect internal/app/tui/model.go internal/app/tui/model_test.go
```

Expected: tests pass and formatting prints no paths. Use isolated auth and
controlled result messages, not real endpoints or credentials.

## Task 4: Order parent model-discovery results per provider

**Goal:** an older `/models` refresh cannot overwrite a newer result.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/model_test.go`.

**Steps:**

1. Add the private `modelsProbeResultMsg` from Contracts and a per-provider
   generation map to the parent `Model`. In `probeProviders`, increment the
   generation only when a probe is actually dispatched; wrap its existing
   `probe.ProviderWithAuth("models", ...)` command to return the captured
   provider and generation alongside its result. Maintain existing cache,
   privacy and auth checks. The generation map persists for the model's
   lifetime, including picker closure/reopening.
2. In `Model.Update`, accept this wrapper only when its provider matches the
   inner result and the generation is current. Apply the existing successful
   model merge, in-memory and disk cache updates, and relevant model-options
   refresh; forward the inner result only to a live connect panel (Task 2
   checks the correct step). Current failures may update that picker's
   provider failure note but must not evict a good cache. Drop stale success
   and failure before any side effect. Keep raw `probe.ResultMsg` handling for
   `thinking-options` and other non-`models` consumers; reject untagged
   `FieldID: "models"` completions and update existing model tests to use
   actual wrapped commands or explicit current-generation fixtures.
3. Add `TestModelDiscoveryNewestProbeWins`: deliver newest then older success
   and failure for one provider, interleave another provider, and assert
   selected row, session discovery and on-disk cache retain the newest value.
   Exercise repeated Ctrl+R, close/reopen, different providers and an error
   after a prior cached success. Use isolated fakes or local `httptest` only.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(ModelDiscoveryNewestProbeWins|.*Cache|.*Model)' -count=1 -timeout 90s
go test ./internal/app/tui/connect ./internal/app/tui -count=1 -timeout 2m
gofmt -l internal/app/tui/model.go internal/app/tui/model_test.go
```

Expected: tests pass and formatting prints no paths.

## Task 5: Provide a pageable MCP authorization URL

**Goal:** the user can reach and request a copy of the entire authorization
URL without a browser or working clipboard.

**Files:** `internal/app/tui/mcpauth/panel.go`,
`internal/app/tui/mcpauth/panel_test.go`.

**Steps:**

1. Add `CopyLinkMsg` from Contracts. In `Panel.Update`, `c` emits it with the
   panel pointer and exact `Display.URL()` when nonempty and not cancelling;
   implement `AcceptsCopyLink` as specified in Contracts. Before the URL is
   available and after cancellation it does nothing. Keep tick and Escape
   handling and the mutex-protected display unchanged.
2. In `Panel.View`, wrap the full URL to the panel's cell width and render it
   in a scrollable area with above/below or position indicators. Add PgUp,
   PgDn, Home and End handling and record the last viewport size; initial
   offset is zero and resize clamps it. Keep the copy and cancel instructions
   visible within the dock height where possible. Render control characters
   safely without modifying the raw copied URL. Do not write the URL into
   logs or error messages. Replace the `OpenBrowser` comment that claims the
   one-line display itself is sufficient for manual copy.
3. Add `TestMCPAuthLongURLCanBePagedAndCopied`: deliver a long local fixture
   URL with meaningful query suffix, navigate actual key updates at 80×24 and
   a short dock height, accumulate every visible URL segment, and assert the
   copy message contains the exact original. Add
   `TestMCPAuthCopyWaitsForURLAndStopsAfterCancel`, plus resize and unchanged
   cancellation assertions. Bound frame height and cover monochrome.

**Verify:**

```bash
go test ./internal/app/tui/mcpauth -count=1
gofmt -l internal/app/tui/mcpauth
```

Expected: tests pass and formatting prints no paths.

## Task 6: Wire MCP copying and verify the composed flows

**Goal:** the real TUI host copies the complete link and reports the outcome,
and model selection stays stable through host-delivered results.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/mcp_test.go`,
`internal/app/tui/model_test.go`.

**Steps:**

1. In `Model.Update`, handle `mcpauth.CopyLinkMsg` through the same
   `m.copyText(msg.URL)` path as `connect.CopyLinkMsg`. Accept it only while
   an MCP authorization panel is still active and its `AcceptsCopyLink`
   accepts the message. This prevents a delayed copy request from acting on a
   replacement auth flow, including one showing an identical URL.
2. Add `TestMCPAuthCopyThroughModel` using an injected `fakeClipboard`,
   browser stub and an isolated long URL: drive `c` through `Model.Update`,
   execute and return commands, assert exact clipboard bytes and the existing
   confirmed-copy flash. With a failing local writer assert OSC 52 is sent
   and its feedback does not claim terminal confirmation; cover no URL,
   cancellation and a replaced panel. No real browser or clipboard is used.
3. Add `TestModelSelectionSurvivesHostDiscovery` using the real host update
   path and wrapped probe results: select a non-first provider/model, deliver
   interleaved results and press Enter; assert the chosen provider/model and
   any resulting messages. Exercise 80×24 and a wider frame.

**Verify:**

```bash
go test ./internal/app/tui -run 'Test(MCPAuthCopyThroughModel|ModelSelectionSurvivesHostDiscovery)' -count=1 -timeout 90s
go test ./internal/app/tui/... -count=1 -timeout 2m
gofmt -l internal/app/tui/model.go internal/app/tui/mcp_test.go internal/app/tui/model_test.go
```

Expected: tests pass and formatting prints no paths.

## Final verification and integration

After all tasks pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p9-discovery ./cmd/marshal
go test ./internal/app/tui/... -count=1 -timeout 2m
go test -race ./internal/app/tui/connect ./internal/app/tui/picker ./internal/app/tui/mcpauth -count=1
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui/connect internal/app/tui/picker internal/app/tui/mcpauth internal/app/tui/model.go internal/app/tui/model_test.go internal/app/tui/mcp_test.go
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p9-discovery go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Formatting must print no paths. Compare full-suite failures against the latest triage baseline and report
actual results. The scripted usability suite is help-only; it does not prove
manual OAuth or picker discovery. If a controlling terminal is available,
manually exercise resize, pagination, keyboard copy and OSC 52 fallback at
80×24 and over a multiplexer/SSH connection using a local fixture URL;
otherwise record the missing environments. After verification update the
three audit findings and triage item 10 with evidence and limitations, and
commit the named plan and documentation with the work.

## Plan self-review

- The three findings have named tasks and tests; selection changes and both
  kinds of probe completion are exercised through their actual owners.
- Stale setup results are rejected before cache writes, and refresh ordering
  is per provider rather than a global generation that could drop another
  provider's legitimate result.
- Copy and manual paging are independent fallbacks, with truthful clipboard
  feedback. No source implementation was performed during planning.
