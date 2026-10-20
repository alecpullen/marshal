# Context limits implementation plan

Status: ready for plan review; implementation has not started.
Date: 2026-10-20.
Base: `d104ac374112aebb6e75a109f2b6619c635b5dc1`.
Approved design: [feature spec](../feature-spec.md).
Workflow: `marshal-writing-plans`, then `marshal-executing-plans` after this
plan receives separate approval. Tasks run sequentially.

## Goal

Three surgical patches, each in the file that already owns the concern:

1. **Clamp** a preset's declared context window down to the provider's own
   reported limit, with a one-per-session warning notice.
2. **Recover** from a provider rejecting an oversize prompt: prune, compact
   once, retry once, then fail with an actionable error.
3. **Stop the model inventing context pressure**: an honest truncation note,
   an explicit prompt rule, and a live budget line in the volatile tail.

Ordering is **1 → 3 → 2**. Patch 1 defines the arithmetic Patch 3 displays.
Patch 2 is inert until a provider rejects a prompt.

## Non-goals

- A `TurnBudget` value type bundling window, threshold, and source.
- Persisting a corrected window back into config. Presets stay the user's
  declaration; the clamp is per-request. `presetflow.Materialize` is untouched.
- New provider probes or extra network calls.
- TUI work beyond the clamp's natural effect on the displayed window.
- Changes to the compaction algorithm itself.
- Overflow recovery inside pipeline subagents or the rollover digest call.

## Assumptions

- The provider probe outranks the third-party limits table because it is a
  statement by the party enforcing the limit; the table is a cross-provider
  inference that prefers the larger figure.
- "Provider wins" means **`min` of the two**, never replacement: a
  conservative preset is never expanded.
- One compaction attempt per turn is the right recovery budget.
- `agent.max_turn_context_tokens`, when set, still wins over the derived
  threshold — the override of last resort for a user who knows their model
  better than the probe does.
- **Deviation from the spec, verified against the code:** the spec said
  `withWorkingState` gains a budget parameter. It does not need one.
  `State.TurnBudget()` (`internal/app/session/session.go:775`) already returns
  `(window, threshold, source)` and is populated at `runner.go:1276`, before
  the loop. `withWorkingState` reads it internally, so `chat.go:659` and
  `cacheprefix_test.go:190` are untouched. Only `BuildWorkingStateMessage`
  gains a parameter.

## Contracts

Every type and signature more than one task uses. Declarations are final;
tasks below reference them rather than restating them.

```go
// internal/agent/route.go

// ModelProbe is the provider's own reported limits for one model, as
// recorded by the provider's /models probe. OK=false means the probe is
// absent, stale, or hash-invalidated — never clamp on a guess.
type ModelProbe struct {
	Window, MaxOutput int
	OK                bool
}

// ModelLimits is a preset's resolved budget plus the provenance a caller
// needs to report a clamp. Probe*/Table* are 0 when that source had
// nothing to say.
type ModelLimits struct {
	Window, MaxOutput          int    // in use
	PresetWindow, PresetOutput int    // as declared in config
	ProbeWindow, ProbeOutput   int    // provider-reported, 0 = unknown
	TableWindow, TableOutput   int    // third-party limits table, 0 = unknown
	Clamped                    bool
	ClampSource                string // "", "probe", "table", "table-variant"
}

func ResolveModelLimits(preset routing.ModelPreset, table *limits.Table, probe ModelProbe, logger *slog.Logger) *ModelLimits

// internal/agent/runner.go — new Runner fields

	// ModelProbe reports the provider's own cached limits for a model.
	// Nil means "no probe available" — resolution degrades to the limits
	// table and the catalog, and nothing clamps.
	ModelProbe func(provider, model string) ModelProbe

	// clampNoticed guards the one-per-session clamp notice: resolveRoute
	// runs every turn and SetNotice replaces unconditionally.
	clampNoticed bool

// internal/agent/prompts.go

// WorkingStateBudget is the live per-turn budget shown to the model.
// Used excludes the working-state tail itself.
type WorkingStateBudget struct {
	Used, Window, Threshold int
}

func BuildWorkingStateMessage(pack contextpack.Pack, budget WorkingStateBudget) (schema.ChatMessage, bool)

// internal/agent/chat.go

// errContextOverflow marks a provider rejection caused by an oversized
// prompt, so the runner can compact and retry instead of failing the turn.
var errContextOverflow = errors.New("agent: provider rejected the prompt as too long")

func classifyContextOverflow(err error) error
```

---

## Task 1: Clamp resolution to the provider probe

**Goal.** `ResolveModelLimits` returns provenance and lowers a resolved
window or max-output to a known provider-reported or table-reported limit,
never raising one.

**Files.** `internal/agent/route.go`, `internal/agent/route_test.go`.

**Steps.**

1. In `internal/agent/route.go`, add the `ModelProbe` and `ModelLimits` types
   from Contracts immediately above `ResolveModelLimits`.

2. Replace the whole of `ResolveModelLimits` (currently
   `func ResolveModelLimits(preset routing.ModelPreset, table *limits.Table, logger *slog.Logger) (window, maxOutput int)`)
   with:

```go
// ResolveModelLimits resolves a preset's context window and max output tokens.
// Each field falls back independently: explicit config always wins; then the
// fetched limits table, which matches across provider naming variance; then
// the curated local catalog, which returns conservative defaults for unknown
// models. A zero return is only possible when the preset's model ID is empty;
// in that case the caller keeps its configured budget rather than guessing.
//
// After resolution, a known provider-reported limit (probe) or a known
// third-party table limit clamps the result down. The clamp only ever lowers:
// a deliberately conservative preset is never expanded. The probe outranks
// the table per field, because the probe is a statement by the party that
// enforces the limit. catalog.Lookup is never a clamp source — it fills only
// zeros, and clamping an unlisted model to its conservative guess would
// cripple it.
//
// logger is threaded through to catalog.Lookup so the "unknown model" warning
// lands in the log file rather than stderr; pass nil to suppress it.
func ResolveModelLimits(preset routing.ModelPreset, table *limits.Table, probe ModelProbe, logger *slog.Logger) *ModelLimits {
	lim := &ModelLimits{
		Window:       preset.ContextWindow,
		MaxOutput:    preset.MaxOutputTokens,
		PresetWindow: preset.ContextWindow,
		PresetOutput: preset.MaxOutputTokens,
	}

	var tableLim limits.Limit
	var tableKind limits.MatchKind
	if table != nil {
		tableLim, tableKind = table.Lookup(preset.Provider, preset.Model)
		if tableKind != limits.MatchNone {
			lim.TableWindow = tableLim.ContextWindow
			lim.TableOutput = tableLim.MaxOutputTokens
			if lim.Window == 0 {
				lim.Window = tableLim.ContextWindow
			}
			if lim.MaxOutput == 0 {
				lim.MaxOutput = tableLim.MaxOutputTokens
			}
		}
	}

	if lim.Window == 0 || lim.MaxOutput == 0 {
		catWindow, catOutput := catalog.Lookup(preset.Model, logger)
		if lim.Window == 0 {
			lim.Window = catWindow
		}
		if lim.MaxOutput == 0 {
			lim.MaxOutput = catOutput
		}
	}

	if probe.OK {
		lim.ProbeWindow = probe.Window
		lim.ProbeOutput = probe.MaxOutput
	}

	// Post-pass clamp. The probe outranks the table per field; the table is
	// consulted only for a field the probe did not report.
	if probe.OK && probe.Window > 0 {
		if clampField(&lim.Window, probe.Window) {
			lim.Clamped, lim.ClampSource = true, "probe"
		}
	} else if tableKind != limits.MatchNone {
		if clampField(&lim.Window, tableLim.ContextWindow) {
			lim.Clamped, lim.ClampSource = true, tableClampSource(tableKind)
		}
	}
	if probe.OK && probe.MaxOutput > 0 {
		if clampField(&lim.MaxOutput, probe.MaxOutput) {
			lim.Clamped = true
			if lim.ClampSource == "" {
				lim.ClampSource = "probe"
			}
		}
	} else if tableKind != limits.MatchNone {
		if clampField(&lim.MaxOutput, tableLim.MaxOutputTokens) {
			lim.Clamped = true
			if lim.ClampSource == "" {
				lim.ClampSource = tableClampSource(tableKind)
			}
		}
	}
	return lim
}

// clampField lowers *cur to limit when limit is known and smaller. It
// reports whether it clamped.
func clampField(cur *int, limit int) bool {
	if limit > 0 && *cur > limit {
		*cur = limit
		return true
	}
	return false
}

// tableClampSource labels a table-sourced clamp. MatchVariant is an
// inference across differently-named entries, so it is reported separately
// and never banners.
func tableClampSource(kind limits.MatchKind) string {
	if kind == limits.MatchVariant {
		return "table-variant"
	}
	return "table"
}
```

3. Replace `resolveModelLimits` (currently returns `(window, maxOutput int)`)
   with:

```go
func (r *Runner) resolveModelLimits(preset routing.ModelPreset) *ModelLimits {
	var logger *slog.Logger
	if r.State != nil {
		logger = r.State.Logger()
	}
	var probe ModelProbe
	if r.ModelProbe != nil {
		probe = r.ModelProbe(preset.Provider, preset.Model)
	}
	return ResolveModelLimits(preset, r.LimitsTable, probe, logger)
}
```

4. In `resolveRoute`, replace the line

```go
	window, maxOut := r.resolveModelLimits(route.Preset)
```

with

```go
	lim := r.resolveModelLimits(route.Preset)
	window, maxOut := lim.Window, lim.MaxOutput
```

Leave the following `route.Window = window` / `route.MaxOutput = maxOut` /
`r.State.SetTurnContextWindow(window)` lines unchanged — propagation is free.

5. Update the five existing tests in `internal/agent/route_test.go` that
   destructure the old pair. Each becomes a `*ModelLimits` read:

   - `TestResolveModelLimitsPrefersPreset` — `lim := r.resolveModelLimits(...)`;
     assert `lim.Window == 111 && lim.MaxOutput == 222`.
   - `TestResolveModelLimitsUsesProvidedTable` — call
     `ResolveModelLimits(preset, &tbl, ModelProbe{}, nil)`; assert
     `lim.Window == 128000 && lim.MaxOutput == 8192`.
   - `TestResolveModelLimitsUsesTable` — `lim := r.resolveModelLimits(...)`;
     assert `lim.Window == 200000 && lim.MaxOutput == 64000`.
   - `TestResolveModelLimitsFallsBackToCatalog` — `lim := r.resolveModelLimits(...)`;
     assert `lim.Window == 32768`.
   - `TestResolveModelLimitsNilTableDoesNotPanic` — same shape.

6. Add these tests to `internal/agent/route_test.go`:

   - `TestResolveModelLimitsClampsPresetToProbe` — preset
     `{ContextWindow: 1048576, MaxOutputTokens: 131062}` with
     `ModelProbe{Window: 65536, MaxOutput: 8192, OK: true}`. Assert
     `lim.Window == 65536`, `lim.MaxOutput == 8192`, `lim.Clamped`,
     `lim.ClampSource == "probe"`, `lim.PresetWindow == 1048576`,
     `lim.ProbeWindow == 65536`. This is the reported bug as a unit test.
   - `TestResolveModelLimitsNeverExpandsBeyondPreset` — preset
     `{ContextWindow: 65536, MaxOutputTokens: 8192}` with probe
     `{Window: 1048576, MaxOutput: 131062, OK: true}`. Assert
     `lim.Window == 65536`, `lim.MaxOutput == 8192`, `!lim.Clamped`.
   - `TestResolveModelLimitsProbeMissDoesNotClamp` — preset 1048576,
     `ModelProbe{}` (OK false), nil table. Assert `lim.Window == 1048576`,
     `!lim.Clamped`.
   - `TestResolveModelLimitsProbeBeatsTable` — preset 1048576, table entry
     `{ContextWindow: 1048576, MaxOutputTokens: 131062}`, probe
     `{Window: 65536, MaxOutput: 8192, OK: true}`. Assert
     `lim.Window == 65536`, `lim.ClampSource == "probe"`.
   - `TestResolveModelLimitsVariantMatchIsSilent` — table keyed
     `"anthropic/claude-sonnet-4.5"` with `{ContextWindow: 200000}` and preset
     `{Provider: "anthropic", Model: "claude-sonnet-4-5-20250929", ContextWindow: 400000}`,
     no probe. Assert `lim.Window == 200000`, `lim.ClampSource == "table-variant"`.
   - `TestResolveModelLimitsCatalogIsNeverClamped` — empty table, no probe,
     preset `{Provider: "ollama", Model: "qwen2.5-coder:7b"}`. Assert
     `lim.Window == 32768`, `!lim.Clamped`.
   - `TestClampedThresholdStaysPositive` — for `w` in `{2, 3, 7, 8192, 65536}`
     assert `effectiveTurnThreshold(w, w/8, 0)` returns a threshold `> 0`.
     Add a documented sub-case: at `w == 1` the threshold is `0` because
     `int(0.85) == 0` and the `minDerivedTurnTokens` floor is `window/2 == 0`;
     assert `== 0` with a comment saying no real model has a one-token window.

**Verify.**

```bash
CGO_ENABLED=1 go test ./internal/agent/ -run 'ResolveModelLimits|ClampedThreshold' -count=1
go vet ./internal/agent/
```

Expected: all pass. `TestResolveModelLimitsClampsPresetToProbe` fails if the
clamp is removed or pointed at the catalog.

---

## Task 2: Wire the probe and the clamp notice

**Goal.** The runner receives a provider-probe lookup, and a clamp produces
exactly one warning notice per session naming both figures.

**Files.** `internal/agent/runner.go`, `internal/agent/route.go`,
`internal/app/app.go`, `internal/agent/route_test.go`.

**Steps.**

1. In `internal/agent/runner.go`, add the two fields from Contracts
   immediately after the existing `LimitsTable *limits.Table` field
   (`runner.go:324`).

2. In `Runner.CopyFrom` (`runner.go:675`), add `r.ModelProbe = other.ModelProbe`
   immediately after the existing `r.LimitsTable = other.LimitsTable`
   (`runner.go:701`). Do **not** copy `clampNoticed` — it is per-session
   state and must start false.

3. In `internal/agent/route.go`, in `resolveRoute`, immediately after
   `r.State.SetTurnContextWindow(window)`, insert:

```go
	if lim.Clamped && lim.ClampSource != "table-variant" && !r.clampNoticed {
		r.clampNoticed = true
		reported := lim.ProbeWindow
		if lim.ClampSource != "probe" {
			reported = lim.TableWindow
		}
		r.State.SetNotice(session.Notice{
			Category: session.NoticeProvider,
			Severity: session.SeverityWarn,
			Message: fmt.Sprintf(
				"Preset %s declares a %d-token context window, but the %s reports %d. Using %d.",
				route.Preset.Name, lim.PresetWindow, clampSourceLabel(lim.ClampSource), reported, lim.Window),
			Hint:   "Set context_window on the preset to match, or raise agent.max_turn_context_tokens to override.",
			Source: "route",
		})
	}
```

4. Add beside `tableClampSource` in `internal/agent/route.go`:

```go
// clampSourceLabel names the source of a clamp for the user-facing notice.
func clampSourceLabel(source string) string {
	if source == "probe" {
		return "provider"
	}
	return "limits table"
}
```

5. In `internal/app/app.go`, immediately after
   `runner.LimitsTable = limitsTable` (`app.go:1183`), add:

```go
	runner.ModelProbe = modelProbeFunc(cfg, dataDir)
```

6. Add `modelProbeFunc` beside `loadLimitsTable` (`app.go:1496`). Add the
   `marshal/internal/llm/provider/modelcache` import if absent; `time` and
   `agent` are already imported.

```go
// modelProbeFunc returns a lookup over the provider's own cached /models
// probe, so the runner can clamp a preset that claims more than the provider
// serves. It reads the same discovered.json that /models populates; a
// missing, stale, or hash-invalidated entry reports OK=false and nothing
// clamps. Nil when dataDir is empty.
func modelProbeFunc(cfg config.Config, dataDir string) func(provider, model string) agent.ModelProbe {
	if dataDir == "" {
		return nil
	}
	return func(providerName, modelID string) agent.ModelProbe {
		pc, ok := cfg.Providers[providerName]
		if !ok {
			return agent.ModelProbe{}
		}
		models, ok := modelcache.Load(dataDir).Lookup(providerName, pc, modelcache.DefaultTTL, time.Now())
		if !ok {
			return agent.ModelProbe{}
		}
		for _, m := range models {
			if m.ID != modelID {
				continue
			}
			if m.ContextWindow == 0 && m.MaxOutputTokens == 0 {
				return agent.ModelProbe{}
			}
			return agent.ModelProbe{Window: m.ContextWindow, MaxOutput: m.MaxOutputTokens, OK: true}
		}
		return agent.ModelProbe{}
	}
}
```

7. Add these tests to `internal/agent/route_test.go`:

   - `TestResolveRoutePropagatesClampedWindow` — build a runner with the
     existing `staticResolver` fixture (`runner_testhelpers_test.go:123`,
     fields `route routing.Route` and `provider provider.Provider`) returning
     a preset declaring 1048576, and a `ModelProbe` returning
     `{Window: 65536, MaxOutput: 8192, OK: true}`. Call `resolveRoute`.
     Assert the returned `route.Window == 65536` and that the pack budget
     became `contextpack.BudgetForWindow(65536) == 8192`. Mirror the existing
     `BudgetForWindow` assertion style in this file.
   - `TestResolveRouteBannersClampOncePerSession` — call `resolveRoute` twice
     with the same clamping probe. Assert `State.Notice()` is set after the
     first call and that the second call does not change `Notice().SetAt`
     (capture it between calls).

**Verify.**

```bash
CGO_ENABLED=1 go test ./internal/agent/ -run 'ResolveRoute|ResolveModelLimits' -count=1
CGO_ENABLED=1 go test ./internal/app/ -run 'TestBuildAgentRunner' -count=1
go vet ./internal/agent/ ./internal/app/
```

Expected: all pass. `TestResolveRouteBannersClampOncePerSession` fails if the
`clampNoticed` guard is dropped.

---

## Task 3: Honest truncation note

**Goal.** The truncation note names the pack budget and denies the
context-window inference, while keeping `Context pack truncated` as its
leading words so the existing assertions still pass.

**Files.** `internal/contextpack/render.go`, `internal/contextpack/render_test.go`.

**Steps.**

1. In `renderPart` (`internal/contextpack/render.go`), replace

```go
		fmt.Fprintf(&b, "Context pack truncated (~%d/%d tokens).\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
```

with

```go
		fmt.Fprintf(&b, "Context pack truncated to its %d-token budget (~%d tokens selected). This limits what is included in this request, not the model's context window.\n", pack.TokenUsage.MaxTokens, pack.TokenUsage.EstimatedTokens)
```

2. Add to `internal/contextpack/render_test.go`:

   - `TestRenderVolatileTruncationNoteNamesPackBudget` — build a truncated
     pack the way `TestRenderStableUnaffectedByVolatileChanges` does. Assert
     `RenderVolatile` contains the budget figure, contains
     `"not the model's context window"`, and still contains
     `"Context pack truncated"`. Assert `RenderStable` contains none of the
     three.

**Verify.**

```bash
CGO_ENABLED=1 go test ./internal/contextpack/ -run 'Render' -count=1
```

Expected: all pass, including the unchanged
`TestRenderStableUnaffectedByVolatileChanges`.

---

## Task 4: Prompt rule against self-finalising on context

**Goal.** Both rule compositions tell the model the runner compacts
automatically and that it must never end a turn believing its context is
nearly exhausted — scoped to context only, so the tool-iteration pressure
message still works.

**Files.** `internal/agent/prompts.go`, `internal/agent/prompts_test.go`.

**Steps.**

1. In `internal/agent/prompts.go`, add this constant beside the other rule
   constants (after `ruleGitAndWorkspace`):

```go
	// ruleContextBudget counters self-finalisation on a false belief that
	// context is exhausted. The runner compacts automatically and reports
	// the live budget on every request, so the model has no basis for that
	// inference — and ending a turn on it abandons work. Scoped to context
	// only: the tool-iteration pressure message is a real signal and must
	// still be obeyed.
	ruleContextBudget = "- The runner compacts automatically as the context budget fills and reports the live budget on every request. Never end a turn because you believe your context is nearly exhausted. If you are genuinely blocked, say what is blocked, not that context is short.\n"
```

2. Append `ruleContextBudget` to **both** `baseRules` and `baseRulesSystem`,
   immediately after `ruleGitAndWorkspace` in each composition. Both, because
   the system-access variant drifts independently.

3. Add to `internal/agent/prompts_test.go`:

   - `TestBaseRulesForbidsContextExhaustionFinalize` — for each of
     `baseRules` and `baseRulesSystem`, assert it contains
     `"Never end a turn because you believe your context is nearly exhausted"`.
     Follow the loop style of `TestBaseRulesCarryNoRepoSpecificWorkflow`.
   - `TestContextRuleIsScopedToContext` — assert `ruleContextBudget` contains
     `"context"` and does **not** contain `"tool"` or `"iteration"`. This is
     the collision guard against `finalizePressureMessage`.

**Verify.**

```bash
CGO_ENABLED=1 go test ./internal/agent/ -run 'BaseRules|ContextRule' -count=1
```

Expected: all pass, including the unchanged
`TestBaseRulesCarryNoRepoSpecificWorkflow`.

---

## Task 5: Live budget line in the volatile tail

**Goal.** Every request's working-state tail opens with the live budget, and
the numbers ride the volatile tail so the prompt prefix stays stable.

**Files.** `internal/agent/prompts.go`, `internal/agent/route.go`,
`internal/agent/prompts_test.go`, `internal/agent/cacheprefix_test.go`.

**Steps.**

1. In `internal/agent/prompts.go`, add the `WorkingStateBudget` type from
   Contracts immediately above `BuildWorkingStateMessage`.

2. Replace `BuildWorkingStateMessage` with:

```go
// BuildWorkingStateMessage renders the turn-volatile part of the context
// pack (todos, semantic snippets, tool output) as the per-request tail,
// prefixed by the live context budget. It is marked Volatile so providers
// keep cache breakpoints in front of it.
func BuildWorkingStateMessage(pack contextpack.Pack, budget WorkingStateBudget) (schema.ChatMessage, bool) {
	rendered := contextpack.RenderVolatile(pack)
	line := budgetLine(budget)
	if rendered == "" && line == "" {
		return schema.ChatMessage{}, false
	}
	content := rendered
	if line != "" {
		if content == "" {
			content = line
		} else {
			content = line + "\n" + content
		}
	}
	return schema.ChatMessage{Role: schema.RoleUser, Content: content, Volatile: true}, true
}

// budgetLine renders the live context budget, or "" when the window is
// unknown. It must never print zeros.
func budgetLine(b WorkingStateBudget) string {
	if b.Window <= 0 {
		return ""
	}
	return fmt.Sprintf("Context budget: ~%d tokens in this request, %d window, compaction at %d.", b.Used, b.Window, b.Threshold)
}
```

3. In `internal/agent/route.go`, replace `withWorkingState` with:

```go
func (r *Runner) withWorkingState(messages []schema.ChatMessage) []schema.ChatMessage {
	if r.State == nil {
		return messages
	}
	window, threshold, _ := r.State.TurnBudget()
	msg, ok := BuildWorkingStateMessage(r.State.ContextPack(), WorkingStateBudget{
		Used:      r.calibratedEstimate(messages),
		Window:    window,
		Threshold: threshold,
	})
	if !ok {
		return messages
	}
	out := make([]schema.ChatMessage, len(messages), len(messages)+1)
	copy(out, messages)
	return append(out, msg)
}
```

Keep the existing doc comment above it, extended with one sentence: `Used`
excludes the tail itself, so the line is not recursive.

4. Update the one test caller: `internal/agent/cacheprefix_test.go:190` calls
   `r.withWorkingState(in)` — the signature is unchanged, so this compiles as
   is. No edit needed; verify it still passes.

5. Add to `internal/agent/prompts_test.go`:

   - `TestBuildWorkingStateIncludesBudgetLine` — call
     `BuildWorkingStateMessage(pack, WorkingStateBudget{Used: 485000, Window: 65536, Threshold: 47513})`
     with a non-empty volatile pack. Assert the content contains `485000`,
     `65536`, `47513`, and that `msg.Volatile` is true.
   - `TestBuildWorkingStateOmitsBudgetWhenUnknown` — same call with
     `Window: 0`. Assert the content does not contain `"Context budget"` and
     does not contain `"0 window"`.
   - `TestBudgetLineDoesNotBreakPrefixStability` — drive two requests with
     different `Used` counts through the scripted provider and assert the
     existing `assertPrefixStable` helper passes. Reuse the construction in
     `cacheprefix_test.go`.

**Verify.**

```bash
CGO_ENABLED=1 go test ./internal/agent/ -run 'WorkingState|BudgetLine|PromptPrefix|CachePrefix' -count=1
```

Expected: all pass. `TestPromptPrefixStableAcrossSteps` and its reasoning
variant must pass **unchanged** — they are the guard, not something to adapt.

---

## Task 6: Classify provider context-overflow rejections

**Goal.** A provider 400/413 whose body matches a known oversize-prompt
wording becomes an error that `errors.Is(err, errContextOverflow)` recognises,
and the generic retry loop still refuses to resend it.

**Files.** `internal/agent/chat.go`, `internal/agent/chat_test.go`.

**Steps.**

1. In `internal/agent/chat.go`, add beside `isRetryableChatError`:

```go
// errContextOverflow marks a provider rejection caused by an oversized
// prompt, so the runner can compact and retry instead of failing the turn.
var errContextOverflow = errors.New("agent: provider rejected the prompt as too long")

// contextOverflowPatterns are the provider wordings that mean "prompt too
// large". Matching requires both a 400/413 status and one of these, so an
// unrelated 400 is never misread as an overflow.
var contextOverflowPatterns = []string{
	"context_length_exceeded",
	"maximum context length",
	"too many tokens",
	"prompt is too long",
	"input is too long",
}

// classifyContextOverflow wraps err with errContextOverflow when it is a
// provider 400/413 whose body matches a known oversize-prompt wording.
// Any other error is returned unchanged.
func classifyContextOverflow(err error) error {
	if err == nil {
		return nil
	}
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		return err
	}
	if pe.StatusCode != 400 && pe.StatusCode != 413 {
		return err
	}
	body := strings.ToLower(pe.Body)
	for _, p := range contextOverflowPatterns {
		if strings.Contains(body, p) {
			return fmt.Errorf("%w: %s", errContextOverflow, overflowFirstLine(pe.Body))
		}
	}
	return err
}

// overflowFirstLine returns the first line of s, bounded, for an error
// message.
func overflowFirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
```

2. Do **not** change `isRetryableChatError`. Overflow is a 4xx, so it already
   returns false there and is not reconnect-eligible.

3. Add to `internal/agent/chat_test.go`:

   - `TestClassifyContextOverflow` — table-driven. For each pattern, a
     `&provider.ProviderError{StatusCode: 400, Body: "..."}` must satisfy
     `errors.Is(classifyContextOverflow(err), errContextOverflow)`; also
     check status 413. The same body at status 500, an unrelated 400 body
     (`"invalid api key"`), an empty body, and a non-`ProviderError` must all
     return an error that does **not** satisfy `errors.Is`.
   - `TestChatWithRetryDoesNotRetryOverflow` — scripted provider with
     `Errs: []error{overflowErr}` and `MaxRetries: 2`. Assert `p.Calls == 1`
     and `errors.Is(err, errContextOverflow)` survives `chatWithRetry`'s
     wrapping.

**Verify.**

```bash
CGO_ENABLED=1 go test ./internal/agent/ -run 'ClassifyContextOverflow|DoesNotRetryOverflow' -count=1
```

Expected: all pass.

---

## Task 7: Recover from overflow in the runner loop

**Goal.** An overflow rejection prunes stale tool output, compacts once,
retries once, and on a second failure ends the turn with an actionable error
naming the model, window, threshold, and config key.

**Files.** `internal/agent/runner.go`, `internal/agent/overflow_test.go` (new).

**Steps.**

1. In `internal/agent/runner.go`, add `overflowRetried := false` immediately
   after `pressureMessageSent := false` (`runner.go:1416`), so it resets per
   turn.

2. In the `if err != nil {` block that follows
   `res, err := r.chatWithRetry(...)`, insert the overflow branch **before**
   the existing guard
   `if strings.TrimSpace(res.Text) == "" || errors.Is(err, context.Canceled) {`.
   The branch must precede that guard: a provider 400 delivers no text, so the
   guard would otherwise fail the turn first and make the branch unreachable.

   Replace this exact three-line anchor (it is unique in the file):

```go
		res, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages, effectiveRF)
		r.setTurnFinishReason(res.FinishReason)
		if err != nil {
```

   with the same two lines followed by

```go
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return task, r.failTurn(task, err)
			}
			err = classifyContextOverflow(err)
			if errors.Is(err, errContextOverflow) {
				// Prune first: compaction resends the transcript, so an
				// oversize wire must shrink before summarisation can succeed.
				if prunedMsgs, n := pruneStaleToolOutputs(messages, pruneMinSizeDefault); n > 0 {
					messages = prunedMsgs
					if r.calibratedEstimate(r.withWorkingState(messages)) <= turnThreshold {
						r.State.AddMessage(session.RoleSystem,
							fmt.Sprintf("Pruned %d stale tool result(s) after the provider rejected the prompt as too long.", n),
							session.ContentTypeCompaction)
						continue
					}
				}
				if !overflowRetried {
					overflowRetried = true
					beforeMsgs, beforeTokens := len(messages), r.calibratedEstimate(messages)
					if fresh, cerr := rolloverAndContinue(ctx, r, messages, goal, turnThreshold); cerr == nil {
						r.State.RecordCompaction(session.CompactionInfo{
							MessagesBefore: beforeMsgs,
							MessagesAfter:  len(fresh),
							TokensBefore:   beforeTokens,
							TokensAfter:    r.calibratedEstimate(fresh),
							Generation:     r.State.Generation().Seq,
						})
						messages = fresh
						r.resetTokenRatio()
						if r.CompactionObserver != nil {
							r.CompactionObserver()
						}
						pressureMessageSent = false
						continue
					}
				}
				return task, r.failTurn(task, fmt.Errorf(
					"model %s rejected the prompt as too long (window in use: %d, compaction threshold: %d). Raise agent.max_turn_context_tokens or lower the preset's context_window: %w",
					turnModel, route.Window, turnThreshold, err))
			}
```

   Then change the existing guard from

```go
			if strings.TrimSpace(res.Text) == "" || errors.Is(err, context.Canceled) {
```

   to

```go
			if strings.TrimSpace(res.Text) == "" {
```

   The cancellation half moved into the new branch above, so behaviour is
   unchanged for cancellation and the salvage path below is untouched.

3. Create `internal/agent/overflow_test.go` with these tests, using
   `newTestState(t)` (`runner_testhelpers_test.go:165`) and
   `agenttest.ScriptedProvider`:

   - `TestRunnerRecoversFromContextOverflowOnce` — `Errs: []error{overflowErr, nil}`,
     `Responses: []string{"", "Done."}`. Assert the run completes and that
     `p.Requests[1]` carries strictly fewer messages than `p.Requests[0]`.
     This is the only test proving recovery shrank the wire rather than
     resending it.
   - `TestRunnerOverflowBranchIsReachableOnEmptyText` — scripted overflow with
     empty `res.Text`. Assert the compaction path ran (a compaction record
     exists) instead of `failTurn`. Mutation-sensitive: moving the branch
     after the `res.Text == ""` guard must fail this test.
   - `TestRunnerOverflowPrunesBeforeCompacting` — wire with superseded
     oversize tool outputs (fixtures in `prune_test.go` style) sized so
     pruning alone lands under `turnThreshold`. Assert no compaction record
     and no controller call.
   - `TestRunnerFailsAfterSecondOverflow` — `Errs: []error{overflowErr, overflowErr}`.
     Assert `p.Calls == 2`, the error names the model, the window, the
     threshold, and `agent.max_turn_context_tokens`, and there is no third
     attempt.
   - `TestRunnerOverflowCompactionFailureIsActionable` — rollover returns an
     error. Assert an immediate actionable failure with no retry loop and no
     double error.
   - `TestRunnerOverflowRespectsCancellation` — cancel the context during the
     forced compaction. Assert `errors.Is(err, context.Canceled)` wins over
     the overflow error.

**Verify.**

```bash
CGO_ENABLED=1 go test ./internal/agent/ -run 'Overflow' -count=1
```

Expected: all pass. `TestRunnerOverflowBranchIsReachableOnEmptyText` fails if
the branch is moved after the empty-text guard.

---

## Task 8: Full verification

**Goal.** The whole change builds, vets, formats, and passes the affected
packages plus the app wiring.

**Files.** None — verification only.

**Steps.**

1. Format check:

```bash
gofmt -l internal/agent internal/contextpack internal/app
```

Expected: no output.

2. Vet:

```bash
go vet ./internal/agent/ ./internal/contextpack/ ./internal/app/
```

Expected: clean.

3. Targeted suites:

```bash
CGO_ENABLED=1 go test ./internal/agent/ -run 'ResolveModelLimits|ResolveRoute|Clamp|Overflow|WorkingState|BudgetLine|PromptPrefix|BaseRules|ContextRule' -count=1
CGO_ENABLED=1 go test ./internal/contextpack/ -run 'Render|Budget' -count=1
```

Expected: all pass.

4. Package suites:

```bash
CGO_ENABLED=1 go test ./internal/agent/ ./internal/contextpack/ ./internal/app/ -count=1
```

Expected: all pass. `TestPromptPrefixStableAcrossSteps` and its reasoning
variant pass unchanged.

5. Build:

```bash
CGO_ENABLED=1 go build ./cmd/marshal
```

Expected: exit 0.

6. Mutation checks — confirm coverage is real, then revert each:

   - Point the clamp's comparison at the catalog instead of probe/table →
     `TestResolveModelLimitsClampsPresetToProbe` must fail.
   - Move the overflow branch after the `res.Text == ""` guard →
     `TestRunnerOverflowBranchIsReachableOnEmptyText` must fail.
   - Move the budget line from `RenderVolatile` into `RenderStable` →
     `TestPromptPrefixStableAcrossSteps` must fail.

## Integration notes

- **Patch 3(a) is a release dependency of Patch 1, not a follow-up.** The
  clamp takes `BudgetForWindow(65536)` to `8192`, down from `64000` — a 7.8×
  cut in repo context. Truncation notes therefore fire far more often after
  the clamp, so shipping Patch 1 without Task 3 increases the frequency of
  exactly the misleading signal that caused the original failure. Tasks 1–5
  land together.
- **The probe is hash-gated.** `modelcache.Lookup` returns false after a
  provider config edit or 24h, so the clamp silently stops applying. That is
  the right trade — never clamp on stale data — but the notice must not claim
  permanence, and a probe miss means *no clamp*, never a fuzzy one.
- **`agent.max_turn_context_tokens` remains the escape hatch.** A probe
  false-positive is overridable through config, because
  `effectiveTurnThreshold` returns `configured` immediately.
- **No test reads `discovered.json`, fetches limits, or touches the network.**
  Every new test injects a fake `ModelProbe` or a scripted provider.
- **Commit per task** as `<plan-slug>: task N — <task title>`, after that
  task's verification passes. The spec artifact
  (`docs/context-limits/feature-spec.md`) commits with Task 1, per AGENTS.md.
