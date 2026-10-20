# Context limits: clamp to the provider, recover from overflow, stop inventing pressure

Status: approved design and written specification; not yet implemented.
Date: 2026-10-20.
Source baseline: `d104ac374112aebb6e75a109f2b6619c635b5dc1`.
Workflow: [Marshal brainstorming](../../internal/skills/builtin/brainstorming.md), followed by Marshal's internal plan-writing skill after written-spec review.

## Context and classification

On 2026-10-19 a 49-task implementation run in `sess_1792413865698753000` stopped after 15 committed tasks and Task 16 partial. The model narrated "my context is nearly exhausted" across 33 consecutive messages (ids 18475–18511) and then self-finalised with *"I ran out of context before completing the plan."* (id 18512). The TUI showed 485k / 1048k.

The model was wrong, and the interface had told it so. Three independent defects combine:

1. **A preset window that the provider does not honour.** The preset declares 1048576; the provider's own probe reports 65536. Neither value is reconciled, warned about, or clamped.
2. **No recovery when a provider rejects an oversize prompt.** A 4xx terminates the turn without compaction.
3. **No token-budget guidance, and a misleading truncation note.** The model cannot distinguish "this request's context pack was truncated" from "your context window is nearly full", so it invents the second.

This is a **bounded** change: three surgical patches, each in the file that already owns the concern, each independently testable. It adds no subsystem, no value type, and no network call.

## Evidence

**The runner never asked the model to stop.** `turn_metrics` 2398 records `outcome='answered'`, `salvage_reason=''`. `max_tool_iterations = 0` (`~/.config/marshal/config.toml:6`) and `DefaultMaxToolIterations = 0` is unlimited, while `LocalDefaultMaxToolIterations = 25` applies only when `route.Preset.LocalOnly` — not the case for `ollama-cloud`. So `turnBudget.exhausted()` never fired on the tool axis, and a runner-enforced stop would have recorded a salvage reason.

**The threshold was never tripped.** With `window = 1048576` and `maxOutput = 131062`, `effectiveTurnThreshold` gives `reserve = min(131062, 131072) = 131062` and `threshold = int(0.85 * 1048576) - 131062 = 760227`. At the displayed 485k the overflow branch never ran. Confirmed in the session database: zero messages with `content_type='compaction'`, zero `pruned` / `truncated` / `tool budget` system messages.

**Nothing in the prompt says "say you are out of context."** `internal/agent/prompts.go` contains no token-budget or exhaustion guidance. The pressure cue is the model's inference. The plausible source is `RenderVolatile`'s note, currently `Context pack truncated (~%d/%d tokens).` (`internal/contextpack/render.go:92`) — an unqualified token pair that reads as a window.

**The preset and the provider disagree by 16×.** `~/.local/share/marshal/models/discovered.json` records `ContextWindow: 65536, MaxOutputTokens: 8192` for `ollama-cloud/deepseek-v4.1-flash:1017`; the preset declares `context_window = 1048576, max_output_tokens = 131062` (`~/.config/marshal/config.toml:233-239`).

**The two limits stores are not the same store.** This is the correction that shapes Patch 1:

- `limits.Table` is **third-party**. `loadLimitsTable` → `limits.LoadTable` → `Refresh` → `Fetch(ctx, "openrouter")` + `Fetch(ctx, "litellm")`. It never asks the configured provider anything, and its merge rule prefers the **larger** window between sources.
- `modelcache` **mirrors whatever `/models` returned**, and most `/models` endpoints report no limits. For `openai_compatible` (`internal/llm/provider/openai_compatible.go:161`) and `ollama_native` (`ollama_native.go:609`) the `ContextWindow`/`MaxOutputTokens` fields in `discovered.json` are filled from `limits.Table`, so they are third-party data wearing a provider-shaped cache. Only Anthropic (`max_input_tokens`/`max_tokens`) and the Codex catalog report limits themselves. The cache is keyed by provider config hash with a 24h TTL.
- `ResolveModelLimits(preset, table, logger)` (`internal/agent/route.go:206`) can only see the table. Clamping against it alone would not have fixed the reported case, because OpenRouter/LiteLLM likely either lack a `:1017`-suffixed entry or report the same 1M.

**A rollover did fire once, cleanly.** `session_generations` for the session has two rows: seq 0 ended with `digest_source='rollover'`, seq 1 with `end_reason='error'`. The compaction path is not broken; it simply did not trip on the offending turn, and the model finalised before it could.

## Scope

| Patch | Defect | Files | Behaviour change |
|---|---|---|---|
| 1 | Preset claims more than the provider serves | `agent/route.go`, `agent/runner.go` | Window clamped down; warn notice |
| 2 | Provider rejects an oversize prompt | `agent/chat.go`, `agent/runner.go` | Prune → compact once → retry once |
| 3 | Model invents context pressure | `contextpack/render.go`, `agent/prompts.go`, `agent/route.go` | Honest note, explicit rule, live budget line |

Ordering **1 → 3 → 2**. Patch 1 is the behavioural change and defines the arithmetic everything else displays. Patch 3 depends on 1 for its numbers, not its code. Patch 2 is inert until a provider rejects a prompt.

**Patch 3(a) is a release dependency of Patch 1, not a follow-up.** The clamp takes `BudgetForWindow(65536)` to `min(65536/8, 64000) = 8192`, down from 64000 — a 7.8× cut in repo context. Truncation notes therefore fire far more often after the clamp, so shipping Patch 1 without 3(a) increases the frequency of exactly the misleading signal that caused the original failure.

## Design

### Patch 1 — clamp to the smaller known limit

`ResolveModelLimits` gains a provider probe as a resolution source ranked above `limits.Table`, and returns provenance instead of a bare pair.

```go
// ModelProbe is the provider's own reported limits. OK=false means the
// probe is absent, stale, or hash-invalidated — never clamp on a guess.
type ModelProbe struct {
    Window, MaxOutput int
    OK                bool
}

// ModelLimits is a preset's resolved budget plus the provenance a caller
// needs to report a clamp. TableWindow/TableOutput are 0 when nothing was known.
type ModelLimits struct {
    Window, MaxOutput          int    // in use
    PresetWindow, PresetOutput int    // as declared in config
    TableWindow, TableOutput   int    // provider-reported, 0 = unknown
    Clamped                    bool
    ClampSource                string // "", "probe", "table", "table-variant"
}

func ResolveModelLimits(preset routing.ModelPreset, table *limits.Table, probe ModelProbe, logger *slog.Logger) *ModelLimits
```

Resolution order is unchanged — preset, then table fills zeros, then `catalog.Lookup` fills zeros — followed by a post-pass:

```
for each of {window, maxOutput}:
    if probe.OK and probeValue > 0 and resolved > probeValue:
        resolved = probeValue; Clamped = true; ClampSource = "probe"
```

Three properties are load-bearing:

- **`min` semantics.** A preset of 65536 against a provider reporting 1048576 keeps 65536. The clamp never expands a deliberately conservative config.
- **`catalog.Lookup` is structurally exempt.** It fills only zeros, and the clamp compares only against a provider-reported probe value. An unlisted model is never clamped to the catalog's conservative 8192/4096 guess — the failure mode that would cripple every unknown model.
- **Only provider-reported limits clamp.** `limits.Table` is third-party (OpenRouter/LiteLLM) and may name a smaller, differently-provided variant of the same model. It fills zeros but never lowers an explicit preset. A cache entry carries `ProviderReported` provenance, and the app's probe refuses table-derived entries. When the provider reports nothing, an oversize prompt is caught by Patch 2's overflow recovery instead.

Threading: `Runner` gains `ModelProbe func(provider, model string) ModelProbe`, injected at `internal/app/app.go:1182` beside `loadLimitsTable` where `dataDir` and `cfg.Providers` already live. This mirrors the existing `cachedThinkingOptions` shape and keeps `internal/agent` free of a `dataDir` dependency. `Clone` (`runner.go:701`) copies it alongside `LimitsTable`.

**Propagation is free.** `resolveRoute` assigns the resolved pair to `route.Window` / `route.MaxOutput`, so `effectiveTurnThreshold`, `historyBudget`, `contextpack.BudgetForWindow`, and `State.SetTurnBudget` all inherit the clamped numbers with no further edits. Where the provider genuinely reports a smaller window (Anthropic/Codex), the threshold becomes `int(0.85 * 65536) - min(8192, 8192) = 47513` and the UI denominator becomes 64k. Where it reports nothing (the reported `ollama-cloud` case), the preset's 1M stands and the UI shows 1M.

**Reporting.** `resolveRoute` sets `session.Notice{Category: NoticeProvider, Severity: SeverityWarn, Source: "route-limits"}` naming the declared and provider figures and the value in use. `resolveRoute` runs every turn and `SetNotice` replaces unconditionally (`internal/app/session/notice.go:53`), so the runner keys the banner to the clamp identity (`Runner.clampNoticeKey`): an unchanged clamp is not re-stamped, a different clamp re-notices, and a route that stops clamping clears only its own banner. Output-only clips get output-token wording; a clip of a table/catalog fallback the preset did not declare is silent.

`agent.max_turn_context_tokens`, when set, still wins over the derived threshold (`effectiveTurnThreshold` returns `configured` immediately). That remains the override of last resort for a user who knows their model better than the probe does.

### Patch 2 — prune, compact once, retry once

**Classifier.** A sentinel `errContextOverflow` and `classifyContextOverflow(statusCode int, body string) error`, applied where the provider's HTTP error becomes a Go error. It requires **both** a 400 or 413 **and** a body matching a curated pattern list (`context_length_exceeded`, `maximum context length`, `too many tokens`, `prompt is too long`, `input is too long`), and returns `fmt.Errorf("%w: %s", errContextOverflow, firstLine(body))`. The wrap is what lets `errors.Is` survive `chatWithRetry`'s own `%w` layering.

`isRetryableChatError` (`chat.go:526`) is **not** changed. Overflow stays non-retryable; letting the generic loop resend an oversize prompt up to the retry cap is precisely the waste being removed. Because it is a 4xx it is also not reconnect-eligible, so it returns immediately.

**Runner seam.** The insertion point is forced by control flow. A provider 400 delivers no text, so at `runner.go:1620`:

```go
if strings.TrimSpace(res.Text) == "" || errors.Is(err, context.Canceled) {
    return task, r.failTurn(task, err)
}
```

the turn dies before any other handler runs. The overflow branch must therefore precede that guard:

```go
if err != nil {
    if errors.Is(err, errContextOverflow) {
        // Prune first: compaction itself resends the transcript, so an
        // oversize wire must be shrunk before summarisation can succeed.
        if pruned, n := pruneStaleToolOutputs(messages, pruneMinSizeDefault); n > 0 {
            messages = pruned
            if r.calibratedEstimate(r.withWorkingState(messages)) <= turnThreshold {
                r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Pruned %d stale tool result(s) after the provider rejected the prompt as too long.", n), session.ContentTypePlain)
                continue
            }
        }
        if !overflowRetried {
            overflowRetried = true
            fresh, cerr := rolloverAndContinue(ctx, r, messages, goal, turnThreshold)
            if cerr == nil {
                messages = fresh
                r.resetTokenRatio() // the learned ratio was fit to a window that was wrong
                continue
            }
        }
        return task, r.failTurn(task, fmt.Errorf(
            "model %s rejected the prompt as too long (window in use: %d, compaction threshold: %d). Raise agent.max_turn_context_tokens or lower the preset's context_window: %w",
            turnModel, route.Window, turnThreshold, err))
    }
    // ... existing salvage path below, unchanged
}
```

`overflowRetried` is declared at loop scope beside `pressureMessageSent`, so it resets per turn: one prune, one compaction, one retry, then fail.

**Prune-before-compact is not optional.** Both compaction routes resend the transcript to a model — `summarizeAndContinue` appends `rollover.SummaryDirective` and calls `chatWithRetryNoNativeTools` (`handoff.go:24`) with the same oversize wire, and `compactContext` digests through `DigestProvider`. Without pruning first, a naive "on overflow, compact" issues a second oversize request, receives a second overflow, and fails with a confusing double error. Pruning is the only recovery step that shrinks the summarisation request itself.

`rolloverAndContinue` (`rollover.go:132`) needs no change: `compactContext` already forces regardless of `Controller.Due`, and falls back to `summarizeAndContinue` when the controller is nil.

### Patch 3 — truthful note, explicit rule, live budget line

**(a) Truncation note** (`internal/contextpack/render.go:92`). Must **retain `Context pack truncated` as its leading words**, because `render_test.go:91` asserts that substring and `:94` asserts `RenderStable` does not contain it:

```
Context pack truncated to its %d-token budget (~%d tokens selected). This limits
what is included in this request, not the model's context window.
```

**(b) Prompt rule** (`internal/agent/prompts.go`), added to **both** `baseRules` and `baseRulesSystem` — the system-access variant drifts independently:

> The runner compacts automatically as the budget fills and reports the budget on every request. Never end a turn because you believe your context is nearly exhausted. If you are genuinely blocked, say what is blocked, not that context is short.

The rule must be **scoped to context and must not claim relief from tool-iteration pressure**, or it teaches the model to ignore `finalizePressureMessage` and burn the tool budget instead.

**(c) Live budget line.** `BuildWorkingStateMessage` (`internal/agent/prompts.go:936`) gains a budget parameter, threaded from `withWorkingState` (`internal/agent/route.go:428`):

```go
// WorkingStateBudget is the live per-turn budget shown to the model.
// Used excludes the working-state tail itself.
type WorkingStateBudget struct {
    Used, Window, Threshold int
}
```

Rendered as a leading line in the volatile tail:

```
Context budget: ~485000 tokens in this request, 65536 window, compaction at 47513.
```

- **`Used` excludes the tail.** The tail's own size is what `withWorkingState` is estimating, so including it would be recursive. `Used` comes from `r.calibratedEstimate(messages)` — the wire alone. The line understates by roughly the tail's ~50 tokens and labels itself `~`.
- **It rides the volatile tail, never the system prompt.** The numbers change every request; in the prefix they would fail `TestPromptPrefixStableAcrossSteps`.
- **It must show the clamped window.** Rendering 1M while the provider enforces 64k would recreate the original confusion in a new place. This is the concrete 1 → 3 dependency and the reason for the ordering.
- **Omitted entirely when `Window == 0`** — never print `0`.

## Error handling

### Patch 1

| Condition | Behaviour |
|---|---|
| Probe absent **and** table `MatchNone`/nil | No clamp, no notice; preset stands |
| Probe 65536, preset 1048576 | Window 65536, `Clamped`, warn notice |
| Probe 1048576, preset 65536 | No clamp — `min` never expands |
| Table `MatchVariant` hit | Clamp applied, debug log, no banner |
| Probe and table disagree | Probe wins; both figures in the notice |

**Threshold safety, checked rather than assumed.** `effectiveTurnThreshold` reserves at most `window/8` (`route.go:65`), so `threshold >= 0.725 * window > 0` for any `window >= 2`, and no clamp value can drive the threshold to zero and silently disable the `turnThreshold > 0` branch at `runner.go:1545`. At exactly `window == 1` int truncation yields 0; the existing `minDerivedTurnTokens` floor does not rescue it because `window/2 == 0`. The threshold therefore degenerates to 0, but no real model has a one-token window. Documented in the test rather than guarded at runtime.

### Patch 2

- **Unmatched wording** → not classified → dies exactly as today. Accepted residual risk; the clamp is the primary defence.
- **False positive** → one wasted compaction, then the retry fails and the **original provider error surfaces verbatim**. Both a 400/413 **and** a pattern hit are required, which makes this rare.
- **Compaction fails** (`errEmptyHandoffSummary` from `handoff.go:14`, archive error, cancellation) → no retry; fall through to the actionable failure.
- **Second overflow after a successful retry** → that same failure. `overflowRetried` is loop-scoped, so a recovery loop is structurally impossible.
- **Cancellation wins.** `context.Canceled` is checked before the overflow branch, so Esc during a forced compaction respects the stop.

### Patch 3

- Rule/tool-budget collision — see above; the highest-risk wording in the change.
- Zero numbers omit the line.
- Rule text is constant in the prefix (safe); numbers are per-request in the tail.
- `BuildWorkingStateMessage` gains a parameter: `withWorkingState` plus three call sites in the threshold block (`runner.go:1545`, the `over` closure, the prune check at `runner.go:1559`) and `chat.go:659`.

### Deliberately unhandled

Overflow inside pipeline subagents or inside the rollover digest call is out of scope. If the digest overflows during recovery the user gets the actionable failure, not a corrupted transcript.

## Verification

Placement follows existing files; no new test package. Every new test uses `newTestState(t)` (`runner_testhelpers_test.go:165`) and an injected fake probe — no `discovered.json`, no `limits` fetch, no network, no `MARSHAL_CREDENTIALS_LIVE_TEST`.

### Patch 1 — `internal/agent/route_test.go`

| Test | Pins |
|---|---|
| `TestResolveModelLimitsClampsPresetToProbe` | The reported bug as a unit test: 1048576 vs 65536 → `Window == 65536`, `ClampSource == "probe"`, `PresetWindow` retained |
| `TestResolveModelLimitsNeverExpandsBeyondPreset` | 65536 vs probe 1048576 → 65536, not clamped |
| `TestResolveModelLimitsProbeMissDoesNotClamp` | `OK == false`, nil table → preset stands |
| `TestResolveModelLimitsProbeBeatsTable` | Probe wins when table disagrees |
| `TestResolveModelLimitsVariantMatchIsSilent` | `ClampSource == "table-variant"` drives banner suppression |
| `TestResolveModelLimitsCatalogIsNeverClamped` | Unknown model keeps the 8192/4096 catalog defaults |
| `TestClampedThresholdStaysPositive` | `w` in {2, 7, 8192, 65536} → positive; `w == 1` documented as the degenerate case |
| `TestResolveRoutePropagatesClampedWindow` | Pack budget becomes `BudgetForWindow(65536) == 8192` |
| `TestResolveRouteBannersClampOncePerSession` | Second turn does not re-stamp |

### Patch 2 — `internal/agent/chat_test.go`, `agent/overflow_test.go`

| Test | Pins |
|---|---|
| `TestClassifyContextOverflow` | Each pattern at 400/413; same body at 500, unrelated 400, empty body → not classified |
| `TestChatWithRetryDoesNotRetryOverflow` | `MaxRetries: 2` → one call; `errors.Is` survives the wrap |
| `TestRunnerRecoversFromContextOverflowOnce` | Turn completes **and the retry payload is strictly smaller than the failed one** |
| `TestRunnerOverflowBranchIsReachableOnEmptyText` | Compaction runs instead of `failTurn` on an empty `res.Text`; mutation-sensitive to the insertion point |
| `TestRunnerOverflowPrunesBeforeCompacting` | Pruning alone under threshold → no compaction record, no controller call |
| `TestRunnerFailsAfterSecondOverflow` | Two calls, actionable message, no third |
| `TestRunnerOverflowCompactionFailureIsActionable` | Rollover error → immediate failure, no double error |
| `TestRunnerOverflowRespectsCancellation` | `context.Canceled` wins |

`rolloverAndContinue` needs no test changes; `rollover_test.go` covers the nil-controller and due-controller branches.

### Patch 3 — `contextpack/render_test.go`, `agent/prompts_test.go`, `agent/cacheprefix_test.go`

| Test | Pins |
|---|---|
| `TestRenderVolatileTruncationNoteNamesPackBudget` | New note names the budget and denies the window inference; `render_test.go:91`/`:94` still pass unchanged |
| `TestBaseRulesForbidsContextExhaustionFinalize` | Present in **both** `baseRules` and `baseRulesSystem` |
| `TestContextRuleIsScopedToContext` | Mentions context, claims no tool-iteration relief |
| `TestBuildWorkingStateIncludesBudgetLine` | Used/window/threshold present; `msg.Volatile == true` |
| `TestBuildWorkingStateOmitsBudgetWhenUnknown` | `Window == 0` → no line, no zeros |
| `TestBudgetLineDoesNotBreakPrefixStability` | Two requests with different used counts keep the prefix stable |

`TestPromptPrefixStableAcrossSteps` and its reasoning variant must pass **unchanged** — they are the guard, not something to adapt.

### Mutation checks

Coverage is verified by mutation, not inspection:

1. Point the clamp's comparison at the catalog instead of probe/table → `TestResolveModelLimitsClampsPresetToProbe` must fail.
2. Move the overflow branch after the `res.Text == ""` guard → `TestRunnerOverflowBranchIsReachableOnEmptyText` must fail.
3. Move the budget line from `RenderVolatile` into `RenderStable` → `TestBudgetLineDoesNotBreakPrefixStability` must fail. (Moving the *whole* working-state tail is a coarser mutation that also fails `TestPromptPrefixStableAcrossSteps`; the budget line alone does not, because that test runs without a resolved route and therefore without a budget line.)

### Commands

```bash
CGO_ENABLED=1 go test ./internal/agent/ -run 'ResolveModelLimits|Clamp|Overflow|WorkingState|PromptPrefix|BaseRules' -count=1
CGO_ENABLED=1 go test ./internal/contextpack/ -run 'Render|Budget' -count=1
go vet ./internal/agent/ ./internal/contextpack/
CGO_ENABLED=1 go test ./internal/agent/ ./internal/contextpack/ ./internal/app/ -count=1
CGO_ENABLED=1 go build ./cmd/marshal
```

## Assumptions

- The provider probe outranks the third-party limits table because it is a statement by the party enforcing the limit; the table is a cross-provider inference that prefers the larger figure.
- "Provider wins" means **`min` of the two**, never replacement: a conservative preset is never expanded.
- One compaction attempt per turn is the right budget for recovery. Two would mean the transcript could not be shrunk enough to matter.
- The agent role needs explicit permission not to self-finalise, because it has no other source of truth about its budget.
- Users who deliberately set a larger window than the probe reports can keep it via `agent.max_turn_context_tokens`.

## Non-goals

- A `TurnBudget` value type to bundle window, threshold, and source (rejected: larger diff, no behaviour gained).
- Runtime persistence of a corrected window back into config. Presets remain the user's declaration; the clamp is per-request. `presetflow.Materialize` stays as is.
- New provider probes or extra network calls.
- TUI work beyond the clamp's natural effect on the displayed window.
- Changes to the compaction algorithm itself.
- Overflow recovery inside pipeline subagents or the rollover digest call.

## Revision after manual testing (2026-10-20)

The first implementation treated every `discovered.json` limit as provider-reported. It is not: `openai_compatible` and `ollama_native` fill `ModelInfo.ContextWindow` from `limits.Table`, and the table separately clamped the preset too. A manual test with an explicit 1M preset therefore still displayed 64k and compacted at ~47k — the exact failure this feature set out to remove, inverted.

Two corrections are now part of the design, and supersede the `table`/`table-variant` clamp branches and `ClampSource` values in Patch 1:

1. `schema.ModelInfo` carries `ProviderReported`. Anthropic and the Codex catalog set it; table-enriched `openai_compatible`/`ollama_native` entries do not, and `modelProbeFunc` refuses them.
2. `ResolveModelLimits` clamps only against a provider-reported probe. `limits.Table` fills zeros and never overrides an explicit preset.

`agent.max_turn_context_tokens` remains available to cap the threshold separately. When a provider reports no limits, a genuinely oversize request is handled by Patch 2's prune → compact → retry path, which produces an actionable failure rather than silently shrinking the window.

### Review follow-ups (2026-10-20)

The branch review's non-blocking findings are folded in:

- **Clamp banner identity, not a session bool.** `Runner.clampNoticeKey` re-notices when a different preset, provider figure, or resolved value clamps, and clears the `route-limits` banner when the route stops clamping. Output-only clips get output-token wording; a clip of a table/catalog fallback the preset never declared is silent.
- **Role runners share the probe.** Swarm and SDD role runners now receive the same `ModelProbe` as the main runner, and the rollover-window calculation uses it. They no longer clamp against the third-party table alone.
- **Compaction failure stays actionable.** The overflow failure names the compaction error (`compaction also failed: …`) before wrapping the original overflow, so `errEmptyHandoffSummary` and friends are visible.
- **Probe cache reads are memoized.** `modelProbeFunc` reuses the parsed `discovered.json` until its mtime or size moves, instead of re-reading and unmarshalling every turn.
- **Salvage survives a later overflow.** The retry ladder keeps the largest partial response across attempts; if a later attempt is rejected as too long, the runner continues with that partial instead of discarding it and compacting. Prune notices now share sentence casing.

## Revision: third-party limits table removed

The OpenRouter/LiteLLM limits table and the curated local catalog were removed outright. They caused more grief than benefit: the table's cross-provider inferences repeatedly overrode explicit user intent, and modern model datasheets have converged enough that a documented default is more accurate than a stale match.

The resolution rule is now:

1. An explicit `context_window` / `max_output_tokens` on the preset wins.
2. Otherwise `routing.DefaultModelLimits(local)` applies: **1M context / 128K output for remote models**, **8K / 4K for local endpoints**. New presets are seeded with these figures at creation (`defaultPreset`), and the confirm-limits screen lets the user edit them before saving.
3. A **provider-reported** probe may still lower either figure; it never raises one. Providers that report nothing leave the default in place, and an oversize request is caught by the prune → compact → retry path above.

`internal/llm/provider/limits` and `internal/llm/catalog` are deleted. `schema.ModelInfo.ProviderReported` remains only to distinguish genuine provider metadata from an enrichment pass, so no fabricated figure can clamp a preset. The `remote_limit_discovery` setting is gone; there is no table to fetch. `agent.max_turn_context_tokens` still caps the compaction threshold independently.

## Open questions

None.
