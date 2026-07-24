# Token Metric Tracking Design

**Goal:** Track aggregate token consumption (input, output, reasoning, cache read, cache write) with estimated cost, broken down per model and provider — surfaced via a `/usage` command, a live dock panel, and a status-line cost segment — across both session and project scopes.

**Status:** Approved design, pending implementation plan.

---

## 1. Background & motivation

Today Marshal tracks token usage minimally:

- **`schema.TokenUsage`** (`internal/llm/schema/event.go`): only `PromptTokens`/`CompletionTokens`/`TotalTokens`. No reasoning, cache read/write, or cost.
- **`agent.TurnMetrics`** (`internal/agent/metrics.go`): per-turn, emitted via `MetricsObserver`. Already has `Provider`/`Model` and `PromptTokens`/`CompletionTokens`. Persisted to SQLite (`turn_metrics` table) via `InsertTurnMetrics`.
- **`UsageObserver`** (`internal/agent/runner.go:146`): `func(promptTokens, completionTokens int)` — the swarm meter uses it for ceiling enforcement. Only two fields.
- **Provider wire types** (`usageBody`): only the 3 OpenAI fields. No cache-read/cache-write/reasoning token fields parsed.
- **No cost/pricing concept anywhere** — `ModelPreset` has no price fields.
- **Swarm `EstimateMeter`**: sums prompt+completion across roles, no per-model breakdown.
- **TUI status line**: shows swarm token usage (`tokens used/max`), no cost or per-model breakdown.

The user wants full token-metric tracking: aggregate input/output/reasoning/cache-read/cache-write + estimated cost, with a per-model and per-provider breakdown for multi-agent swarms using different models.

## 2. Data model — `TokenUsage` and `TurnMetrics` widening

The foundation is widening the two structs that carry token data through the pipeline.

**`schema.TokenUsage`** — add four fields:

```go
type TokenUsage struct {
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
    // ReasoningTokens is the reasoning/thinking token count reported by
    // reasoning models (DeepSeek-R1, o1, etc.). Part of completion tokens
    // on some providers; reported separately on others. 0 when absent.
    ReasoningTokens int
    // CacheReadTokens is prompt-billing tokens served from a prompt cache
    // (OpenAI cached_tokens, Anthropic cache_read_input_tokens, DeepSeek
    // prompt_cache_hit_tokens). 0 when the provider doesn't report it.
    CacheReadTokens int
    // CacheWriteTokens is prompt tokens written to a prompt cache for
    // future reuse (Anthropic cache_creation_input_tokens, DeepSeek
    // prompt_cache_miss_tokens). 0 when absent.
    CacheWriteTokens int
}
```

**`agent.TurnMetrics`** — add the four usage fields + an estimated cost:

```go
    ReasoningTokens  int
    CacheReadTokens  int
    CacheWriteTokens int
    // EstimatedCostCents is the estimated cost in hundredths of a cent
    // (1/10000 of a dollar), computed from the token counts and the
    // pricing table at metrics-emission time. 0 for local/unpriced models.
    EstimatedCostCents int64
```

**Why cost in hundredths-of-a-cent (int64):** avoids float drift across thousands of turns, keeps the DB column an integer, and the display layer divides by 10000 for dollars. Local models (Ollama) have a $0 price entry → `EstimatedCostCents = 0`.

**The swarm `UsageObserver`** widens from `func(promptTokens, completionTokens int)` to `func(usage schema.TokenUsage)`. The call site in `chat.go` (currently `r.UsageObserver(usage.PromptTokens, usage.CompletionTokens)`) changes to `r.UsageObserver(*usage)`. The swarm `TokenMeter.Observe` signature (currently `Observe(role AgentRole, promptTokens, completionTokens int)`) widens to `Observe(role AgentRole, usage schema.TokenUsage)`, and `EstimateMeter` sums all fields. This is the one breaking-signature change, contained to the meter, the observer type, and their tests.

## 3. Provider wire parsing — OpenAI-compatible + Anthropic-native

The provider layer surfaces the new fields from the wire formats.

**OpenAI-compatible extensions** (`openai_compatible_wire.go`): widen `usageBody` with the detail objects:

```go
type usageBody struct {
    PromptTokens     int                  `json:"prompt_tokens"`
    CompletionTokens int                  `json:"completion_tokens"`
    TotalTokens      int                  `json:"total_tokens"`
    PromptTokensDetails     *tokenDetails `json:"prompt_tokens_details,omitempty"`
    CompletionTokensDetails *tokenDetails `json:"completion_tokens_details,omitempty"`
    // DeepSeek extends the top-level usage object directly.
    PromptCacheHitTokens  *int `json:"prompt_cache_hit_tokens,omitempty"`
    PromptCacheMissTokens *int `json:"prompt_cache_miss_tokens,omitempty"`
}

type tokenDetails struct {
    CachedTokens    int `json:"cached_tokens"`     // prompt cache hits
    ReasoningTokens int `json:"reasoning_tokens"`  // reasoning model output
}
```

The DeepSeek cache fields are pointers so absent = nil (a 0 cache hit is meaningful and distinct from "not reported").

**`tokenUsageFrom`** maps all fields:
- `CacheReadTokens` = `PromptTokensDetails.CachedTokens` (OpenAI) OR `PromptCacheHitTokens` (DeepSeek) — prefer the details object, fall back to the DeepSeek top-level field.
- `CacheWriteTokens` = `PromptCacheMissTokens` (DeepSeek) — OpenAI doesn't report cache writes.
- `ReasoningTokens` = `CompletionTokensDetails.ReasoningTokens`.

**Anthropic-native path**: there's no Anthropic provider today, but the spec calls for full coverage. Anthropic uses a different wire shape (`cache_read_input_tokens`, `cache_creation_input_tokens`, `cache_creation_1h_input_tokens` directly on the response, not under `usage`). Since there's no Anthropic provider to parse, this is handled as: the `schema.TokenUsage` type is ready to receive Anthropic's fields, and when an Anthropic provider adapter is added it maps `cache_read_input_tokens → CacheReadTokens`, `cache_creation_input_tokens → CacheWriteTokens`. No code is written for a non-existent provider — the schema is the contract. The design explicitly notes this as "schema-ready, adapter deferred."

**Local models (Ollama)**: Ollama reports standard `prompt_tokens`/`completion_tokens` but no cache/reasoning fields → those default to 0. No special handling needed.

## 4. Pricing table and cost computation

A new `internal/llm/pricing` package holds the built-in table and the lookup.

**The pricing model** — per-model, per-token-category rates in cents per million tokens (integer math, no floats):

```go
type ModelPricing struct {
    InputPerMTokCents      int64  // $/M input tokens
    OutputPerMTokCents     int64  // $/M output tokens
    ReasoningPerMTokCents  int64  // $/M reasoning tokens (often = output rate)
    CacheReadPerMTokCents  int64  // $/M cache-read tokens (typically 0.5x input)
    CacheWritePerMTokCents int64  // $/M cache-write tokens (typically 1.25x input)
}
```

**Built-in table**: a `map[string]ModelPricing` keyed by model name string. Seeded with common hosted models (e.g. `gpt-4o`, `gpt-4o-mini`, `claude-sonnet-4`, `deepseek-chat`, `deepseek-reasoner`, `o3-mini`). Local models (`qwen2.5-coder:14b`, `llama3`, etc.) are absent from the table → lookup returns a zero-value `ModelPricing` → cost = 0. The table is a Go map literal in a `prices.go` file, not a data file — no I/O, no parsing, works offline.

**Config override**: a new `[models.preset.<name>]` sub-table `pricing` that lets the user set rates for any model, including local ones if they want to track electricity/compute cost:

```toml
[models.preset.coder]
provider = "ollama"
model = "qwen2.5-coder:14b"

[models.preset.coder.pricing]
input_per_mtok_cents = 0
output_per_mtok_cents = 0
```

This adds a `Pricing *ModelPricing` field to `routing.ModelPreset` (pointer so nil = use built-in table). The lookup order: config preset pricing (if set) → built-in table by model name → zero (local/unpriced).

**Lookup function**:
```go
func Lookup(preset routing.ModelPreset) ModelPricing
```
Returns the merged pricing for a given preset. Pure function, no I/O.

**Cost computation** — a function that takes `TokenUsage` + `ModelPricing` and returns hundredths-of-a-cent:
```go
func EstimateCostCents(u schema.TokenUsage, p ModelPricing) int64
```
Computes `(tokens * rate) / 1_000_000` per category, summed. Integer division is fine — sub-cent amounts truncate, which is correct for an estimate. The runner calls this at `TurnMetrics` emission time (in `emitMetrics`), setting `m.EstimatedCostCents`.

**Where the preset comes from**: the runner already knows its `Model` and `Provider` (set on the `Runner` struct / route). The runner needs access to the resolved `ModelPreset` to look up pricing. The cleanest seam: `app.go` constructs the runner after route resolution and sets a `Pricing pricing.ModelPricing` field on the runner (resolved from the route's preset). Swarm sub-runners copy it via `CopyFrom`.

## 5. Aggregation — `UsageAggregator` and DB persistence

Two aggregation layers: in-memory for session scope, DB for project scope.

**`UsageAggregator`** (new, `internal/agent/usage.go`) — an in-memory accumulator fed by `MetricsObserver`:

```go
type UsageAggregator struct {
    mu      sync.Mutex
    totals  UsageTotals
    byModel map[modelKey]UsageTotals
}

type modelKey struct {
    Provider string
    Model    string
}

type UsageTotals struct {
    PromptTokens        int64
    CompletionTokens    int64
    ReasoningTokens     int64
    CacheReadTokens     int64
    CacheWriteTokens    int64
    EstimatedCostCents  int64
    Turns               int
}
```

`Observe(m TurnMetrics)` adds a turn's metrics to both the grand total and the `(provider, model)` bucket. `Snapshot()` returns the aggregate `UsageTotals` plus a `[]ModelBreakdown` (one `UsageTotals` per model key, sorted by cost descending). Thread-safe. The TUI holds one instance (created in `app.go`, fed by the runner's `MetricsObserver`); swarm sub-runners report through the same observer.

**Per-model capability tracking**: the `UsageAggregator` records, per `(provider, model)` bucket, which fields the provider actually reported across the session. A field that was never non-zero *and* never reported gets rendered as `n/a`; a field that was reported (even if 0 on some turns) renders as `0`. Concretely:

```go
type ModelBreakdown struct {
    UsageTotals
    Provider string
    Model    string
    // Reported tracks which token categories the provider has ever
    // surfaced non-zero values for across the session. A category not
    // in this set renders as "n/a" rather than "0".
    Reported TokenCategorySet
}
```

Where `TokenCategorySet` is a bitset of `Prompt|Completion|Reasoning|CacheRead|CacheWrite`. A category enters the set the first time the provider reports a non-zero value for it. Until then it's `n/a` (unsupported/unreported).

This is session-derived, not static — a provider that reports cache reads on some turns and not others still shows the column as supported once it's seen a non-zero value. Project-scope (DB) doesn't have this capability metadata, so project-scope renders `n/a` only when the summed total is 0 across all turns (a coarser heuristic, but acceptable for the historical view).

**DB persistence** — widen the `turn_metrics` table:
- Add columns: `reasoning_tokens INTEGER NOT NULL DEFAULT 0`, `cache_read_tokens INTEGER NOT NULL DEFAULT 0`, `cache_write_tokens INTEGER NOT NULL DEFAULT 0`, `estimated_cost_cents INTEGER NOT NULL DEFAULT 0`.
- Add `turn_metrics` to the `allowedTableInfo` introspection allowlist (db.go:128) and the four columns to `migrationColumns` (db.go:45) so existing DBs get them via `ALTER TABLE`.
- Widen `TurnMetricsRow` (turnmetrics.go:12) and `InsertTurnMetrics`/`RecentTurnMetrics` to read/write the new columns.

**Project-scope aggregate** — a new DB query method:
```go
func (db *DB) AggregateTurnMetrics(projectID int64) (UsageTotals, []ModelBreakdown, error)
```
A `SELECT provider, model, SUM(prompt_tokens), SUM(completion_tokens), SUM(reasoning_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens), SUM(estimated_cost_cents), COUNT(*) FROM turn_metrics WHERE project_id = ? GROUP BY provider, model`. Returns the grand total (sum of all rows) plus per-model rows. This is the project-scope source for `/usage` and the panel.

**Session scope**: the in-memory `UsageAggregator` is the source — it's reset when a new session starts. No DB query needed for live display.

**ACP**: the ACP path also has a `MetricsObserver` (the runner emits regardless of transport). The aggregator works the same way — it just has no dock panel to display it. The `/usage`-equivalent over ACP would be a future ACP method; for now ACP users get persisted metrics in the DB queryable via the same `AggregateTurnMetrics` path.

## 6. Display — `/usage` command and dock panel

Two surfaces, both reading from the aggregator (session) and `AggregateTurnMetrics` (project).

**`/usage` slash command** — a new TUIOnly command whose effect lives in `commands_dispatch.go` (mirroring `/memory`). It prints a formatted summary to the transcript:

```
Usage (this session):
  Input: 12.4k  Output: 3.1k  Reasoning: 890  Cache read: 2.1k  Cache write: 400
  Estimated cost: $0.0427

  Model                          Input    Output   Reason  CacheR   CacheW   Cost
  ollama/qwen2.5-coder:14b       8.2k     2.1k     n/a     n/a      n/a      $0.00
  openai/gpt-4o                  4.2k     1.0k     890     2.1k     400      $0.0427

Usage (project lifetime):
  Input: 1.2M  Output: 340k  Reasoning: 22k  Cache read: 180k  Cache write: 12k
  Estimated cost: $4.8120
  (3 models, 412 turns across 8 sessions)
```

Unreported categories render as `n/a`; reported-but-zero render as `0`. The command reads `m.usageAggregator` (session scope) and `m.db` (project scope). It's `TUIOnly: true` with no `Handler` (the dispatch closure provides the logic), mirroring `/memory` exactly.

**Dock panel** (`internal/app/tui/usage/panel.go`) — a new package mirroring `internal/app/tui/memory/`. The panel implements `dock.Panel`. It shows a live table:
- A header row: `Model | Input | Output | Reasoning | CacheR | CacheW | Cost`
- One row per `(provider, model)` bucket, sorted by cost descending
- A totals row at the bottom
- A scope toggle (session/project) via a key (e.g. `s` to switch scope)
- On session scope, it reads `m.usageAggregator.Snapshot()` (live, updated each render tick). On project scope, it queries `db.AggregateTurnMetrics(projectID)` (cached, refreshed on toggle).
- Opens via `/usage` (if no panel) or a dedicated panel open, and `Esc` closes.

The panel re-reads `Snapshot()` on each `View` render (the dock re-renders on every TUI tick, so no explicit event subscription is needed). For project scope, the DB query runs once on scope switch and is cached until the next switch.

**Rendering for unreported categories**: the panel/command rendering layer checks `Reported` per field and emits `n/a` for unreported categories. In the compact table (narrow terminals), `n/a` is used (3 chars); in a wider table or the `/usage` transcript, the full word `unsupported` is used when column width allows (11 chars). The render helper picks based on available width.

**Status line segment** — add a running-cost segment to the status line (`status.go`), mirroring the existing swarm-tokens segment: `cost $0.04` (session total). Priority 6 (same as tokens). Only shown when `EstimatedCostCents > 0` (local-only sessions with $0 cost don't clutter the line).

## 7. File map

Files created or modified:

- `internal/llm/schema/event.go` — widen `TokenUsage` with `ReasoningTokens`/`CacheReadTokens`/`CacheWriteTokens`.
- `internal/llm/provider/openai_compatible_wire.go` — widen `usageBody` with `PromptTokensDetails`/`CompletionTokensDetails` (token details), DeepSeek `PromptCacheHitTokens`/`PromptCacheMissTokens`.
- `internal/llm/provider/openai_compatible.go` — widen `tokenUsageFrom` to map all new fields.
- `internal/llm/provider/openai_compatible_test.go` — tests for the new wire fields → `TokenUsage` mapping.
- `internal/llm/pricing/prices.go` (new) — built-in `ModelPricing` table.
- `internal/llm/pricing/pricing.go` (new) — `Lookup(preset)`, `EstimateCostCents(usage, pricing)`.
- `internal/llm/pricing/pricing_test.go` (new) — table lookup, override, cost computation.
- `internal/llm/routing/types.go` — add `Pricing *ModelPricing` to `ModelPreset`.
- `internal/app/config/file_types.go` — add `pricing` sub-table to the preset file type.
- `internal/app/config/merge.go` — merge preset pricing.
- `internal/app/config/save.go` — persist preset pricing.
- `internal/app/config/config_test.go` / `save_test.go` — round-trip tests for preset pricing.
- `internal/agent/metrics.go` — widen `TurnMetrics` with `ReasoningTokens`/`CacheReadTokens`/`CacheWriteTokens`/`EstimatedCostCents`; compute cost in `emitMetrics`.
- `internal/agent/runner.go` — widen `UsageObserver` signature to `func(usage schema.TokenUsage)`; add `Pricing` field (resolved preset pricing for cost computation).
- `internal/agent/chat.go` — pass full `TokenUsage` to the widened `UsageObserver`; record all fields into `turnStats`.
- `internal/agent/usage.go` (new) — `UsageAggregator`, `UsageTotals`, `ModelBreakdown`, `TokenCategorySet`.
- `internal/agent/usage_test.go` (new) — aggregator accumulation, per-model breakdown, capability tracking.
- `internal/agent/swarm/meter.go` — widen `TokenMeter.Observe`/`EstimateMeter` to the full `TokenUsage`.
- `internal/agent/swarm/orchestrator.go` — update `UsageObserver` wiring to the new signature.
- `internal/agent/swarm/*_test.go` — update `usageScriptedProvider` and meter tests for the new signature.
- `internal/db/migrations.go` — add `turn_metrics` to `allowedTableInfo`; add the four new columns to `migrationColumns`.
- `internal/db/turnmetrics.go` — widen `TurnMetricsRow`, `InsertTurnMetrics`, `RecentTurnMetrics`; add `AggregateTurnMetrics`.
- `internal/db/turnmetrics_test.go` — round-trip tests for new columns; `AggregateTurnMetrics` grouping test.
- `internal/app/tui/usage/panel.go` (new) — dock panel rendering the breakdown table with `n/a`/`unsupported` for unreported categories.
- `internal/app/tui/usage/panel_test.go` (new) — panel rendering tests.
- `internal/app/tui/commands_dispatch.go` — `/usage` TUIOnly handler (opens panel + prints transcript summary).
- `internal/app/tui/model.go` — hold `usageAggregator *agent.UsageAggregator`; wire `MetricsObserver` to feed it; open the usage panel.
- `internal/app/tui/status.go` — add `cost $X.XX` segment (session total, when > 0).
- `internal/app/tui/status_test.go` — cost segment test.
- `internal/commands/commands.go` — register `/usage` command (TUIOnly).
- `internal/commands/commands_test.go` — `/usage` registration test.
- `internal/app/app.go` — construct `UsageAggregator`; wire it as the runner's `MetricsObserver` (alongside the existing observer if any); seed runner `Pricing` from the resolved route preset.

## 8. Out of scope

- A live-pricing API fetch (provider pricing APIs). The built-in table + config override covers the use cases; live fetch adds latency and network dependency unsuited to a local-first tool.
- An ACP method to query usage mid-session. ACP users get persisted metrics in the DB; a future ACP method can expose the aggregator.
- Real-time token-rate graphs or sparklines in the panel. The panel shows a current snapshot table; time-series visualization is a future enhancement.
- Per-turn cost attribution to individual tool calls. Cost is tracked per turn (which carries model/provider); tool-level attribution would require tracking which tool triggered which LLM call, which the current loop doesn't surface.
- Automatic pricing-table updates from a remote source. The table is a Go map literal; updates are a code change (and a release).