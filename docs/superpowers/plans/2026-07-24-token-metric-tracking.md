# Token Metric Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Track aggregate token consumption (input, output, reasoning, cache read, cache write) with estimated cost, broken down per model and provider — surfaced via a `/usage` command, a live dock panel, and a status-line cost segment — across both session and project scopes.

**Architecture:** Approach A — extend the existing `TokenUsage` → `chat.go` → `TurnMetrics` → `MetricsObserver` → DB pipeline. Widen `schema.TokenUsage` with reasoning/cache fields; widen `UsageObserver` to pass the full struct; widen `TurnMetrics` + the DB `turn_metrics` table with the new fields + estimated cost. A new `internal/llm/pricing` package provides a built-in pricing table + config override. A new in-memory `UsageAggregator` (fed by `MetricsObserver`) provides session-scope live totals; the DB provides project-scope aggregates. A dock panel + `/usage` command render the breakdown with `n/a` for unreported categories.

**Tech Stack:** Go 1.x, `marshal/internal/llm/schema` (TokenUsage), `internal/llm/provider` (wire parsing), `internal/llm/pricing` (new), `internal/llm/routing` (ModelPreset), `internal/agent` (metrics, runner, chat, usage aggregator), `internal/agent/swarm` (meter), `internal/rollover` (UsageCounter), `internal/db` (turn_metrics schema + queries), `internal/app/config` (preset pricing), `internal/app/tui/usage` (new panel), `internal/app/tui` (status, commands), `internal/commands` (registration), `internal/app` (wiring). Tests via `go test ./...`; formatting via `gofmt -w .`.

## Global Constraints

- No new external dependencies; standard library only.
- Cost is stored as `int64` hundredths-of-a-cent (1/10000 of a dollar) — integer math, no floats, to avoid drift across thousands of turns. The display layer divides by 10000 for dollars.
- Local models (Ollama, absent from the pricing table) default to $0 cost.
- `UsageObserver` signature changes from `func(promptTokens, completionTokens int)` to `func(usage schema.TokenUsage)` — this is a breaking change contained to the observer type, its call sites (chat.go, app.go, swarm orchestrator), and the rollover `UsageCounter.Observe` call (which extracts `usage.PromptTokens`).
- `TokenMeter.Observe` signature changes from `Observe(role AgentRole, promptTokens, completionTokens int)` to `Observe(role AgentRole, usage schema.TokenUsage)`.
- Unreported token categories render as `n/a` (compact) or `unsupported` (wide), never as bare `0` — the distinction between "0 tokens" and "not reported" is tracked via a per-model `TokenCategorySet`.
- The `turn_metrics` table needs `ALTER TABLE` migrations (it's not in the introspection allowlist today) — add it to `allowedTableInfo` and the four new columns to `migrationColumns`.
- Anthropic-native wire parsing is "schema-ready, adapter deferred" — no code is written for a non-existent provider; the `TokenUsage` struct is the contract.
- Commit messages follow `<type>(<scope>): <subject>` (see `git log --oneline`).
- Run `gofmt -w .` before each commit; the final task runs the full suite.
- Every code step ships with its test; TDD ordering (test fails → implement → test passes → commit).

---

### Task 1: Widen `schema.TokenUsage` with reasoning and cache fields

**Files:**
- Modify: `internal/llm/schema/event.go`
- Modify: `internal/llm/schema/event_test.go` (if it exists; otherwise add assertions to an existing schema test file or create one)

**Interfaces:**
- Consumes: nothing (foundation).
- Produces: `schema.TokenUsage` with four new `int` fields: `ReasoningTokens`, `CacheReadTokens`, `CacheWriteTokens`. Existing `PromptTokens`/`CompletionTokens`/`TotalTokens` unchanged.

- [ ] **Step 1: Write the failing test**

Create `internal/llm/schema/event_test.go` (or append if it exists):

```go
package schema

import "testing"

func TestTokenUsageZeroValue(t *testing.T) {
	var u TokenUsage
	if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 {
		t.Errorf("zero-value TokenUsage should be all-zero: %+v", u)
	}
	if u.ReasoningTokens != 0 || u.CacheReadTokens != 0 || u.CacheWriteTokens != 0 {
		t.Errorf("new fields should be zero by default: %+v", u)
	}
}

func TestTokenUsageNewFieldsSettable(t *testing.T) {
	u := TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		ReasoningTokens:  20,
		CacheReadTokens:  30,
		CacheWriteTokens: 10,
	}
	if u.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %d, want 20", u.ReasoningTokens)
	}
	if u.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30", u.CacheReadTokens)
	}
	if u.CacheWriteTokens != 10 {
		t.Errorf("CacheWriteTokens = %d, want 10", u.CacheWriteTokens)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/schema/ -run TestTokenUsage -v`
Expected: build failure — `TokenUsage` has no field `ReasoningTokens`/`CacheReadTokens`/`CacheWriteTokens`.

- [ ] **Step 3: Widen the struct**

In `internal/llm/schema/event.go`, replace the `TokenUsage` struct (lines 56-60) with:

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

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/schema/ -v`
Expected: PASS.

- [ ] **Step 5: Run the full build to check for breakage**

Run: `go build ./...`
Expected: builds clean — the new fields are zero-valued by default, so existing code constructing `TokenUsage{PromptTokens:..., CompletionTokens:..., TotalTokens:...}` is unaffected.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/llm/schema/event.go internal/llm/schema/event_test.go
git add internal/llm/schema/event.go internal/llm/schema/event_test.go
git commit -m "feat(schema): widen TokenUsage with reasoning and cache read/write fields"
```

---

### Task 2: Parse the new wire fields in the OpenAI-compatible provider

**Files:**
- Modify: `internal/llm/provider/openai_compatible_wire.go`
- Modify: `internal/llm/provider/openai_compatible.go`
- Modify: `internal/llm/provider/openai_compatible_test.go`

**Interfaces:**
- Consumes: `schema.TokenUsage` new fields from Task 1.
- Produces: `tokenUsageFrom` populates `ReasoningTokens`/`CacheReadTokens`/`CacheWriteTokens` from the OpenAI-compatible wire format (prompt_tokens_details.cached_tokens, completion_tokens_details.reasoning_tokens, DeepSeek prompt_cache_hit_tokens/prompt_cache_miss_tokens).

- [ ] **Step 1: Write the failing tests**

Append to `internal/llm/provider/openai_compatible_test.go`:

```go
func TestChatStreamingTokenUsageWithCacheAndReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens_details":{"reasoning_tokens":20}}}` + "\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(true))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var done schema.ChatEvent
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			done = ev
		}
	}
	if done.Usage == nil {
		t.Fatal("expected token usage in done event, got nil")
	}
	if done.Usage.CacheReadTokens != 40 {
		t.Errorf("CacheReadTokens = %d, want 40", done.Usage.CacheReadTokens)
	}
	if done.Usage.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %d, want 20", done.Usage.ReasoningTokens)
	}
	if done.Usage.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens = %d, want 0 (OpenAI doesn't report cache writes)", done.Usage.CacheWriteTokens)
	}
}

func TestChatNonStreamingTokenUsageWithDeepSeekCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":80,"total_tokens":280,"prompt_cache_hit_tokens":150,"prompt_cache_miss_tokens":50}}`,
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var done schema.ChatEvent
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			done = ev
		}
	}
	if done.Usage == nil {
		t.Fatal("expected token usage, got nil")
	}
	if done.Usage.CacheReadTokens != 150 {
		t.Errorf("CacheReadTokens = %d, want 150 (DeepSeek prompt_cache_hit_tokens)", done.Usage.CacheReadTokens)
	}
	if done.Usage.CacheWriteTokens != 50 {
		t.Errorf("CacheWriteTokens = %d, want 50 (DeepSeek prompt_cache_miss_tokens)", done.Usage.CacheWriteTokens)
	}
}

func TestChatTokenUsageNoCacheFieldsZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	events, err := p.Chat(t.Context(), chatReq(false))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var done schema.ChatEvent
	for ev := range events {
		if ev.Type == schema.ChatEventDone {
			done = ev
		}
	}
	if done.Usage == nil {
		t.Fatal("expected token usage, got nil")
	}
	if done.Usage.ReasoningTokens != 0 || done.Usage.CacheReadTokens != 0 || done.Usage.CacheWriteTokens != 0 {
		t.Errorf("provider without cache/reasoning fields should be zero: %+v", done.Usage)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/provider/ -run "TestChatStreamingTokenUsageWithCacheAndReasoning|TestChatNonStreamingTokenUsageWithDeepSeekCache|TestChatTokenUsageNoCacheFieldsZero" -v`
Expected: FAIL — the new wire fields aren't parsed, so `CacheReadTokens`/`ReasoningTokens`/`CacheWriteTokens` are all 0.

- [ ] **Step 3: Widen the wire types**

In `internal/llm/provider/openai_compatible_wire.go`, replace the `usageBody` struct (lines 25-29) with:

```go
type usageBody struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// OpenAI usage detail breakdowns.
	PromptTokensDetails     *tokenDetails `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *tokenDetails `json:"completion_tokens_details,omitempty"`
	// DeepSeek extends the top-level usage object directly (not under
	// _details). Pointers so absent = nil: a 0 cache hit is meaningful
	// and distinct from "not reported".
	PromptCacheHitTokens  *int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens *int `json:"prompt_cache_miss_tokens,omitempty"`
}

// tokenDetails holds the OpenAI usage detail breakdowns.
type tokenDetails struct {
	CachedTokens    int `json:"cached_tokens"`    // prompt cache hits
	ReasoningTokens int `json:"reasoning_tokens"` // reasoning model output
}
```

- [ ] **Step 4: Widen `tokenUsageFrom`**

In `internal/llm/provider/openai_compatible.go`, replace `tokenUsageFrom` (lines 275-285) with:

```go
// tokenUsageFrom maps the wire usage body to the schema type.
func tokenUsageFrom(u *usageBody) *schema.TokenUsage {
	if u == nil {
		return nil
	}
	out := &schema.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	// OpenAI detail objects.
	if u.PromptTokensDetails != nil {
		out.CacheReadTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	// DeepSeek top-level cache fields override only when the OpenAI
	// detail objects didn't populate them (DeepSeek doesn't use _details).
	if out.CacheReadTokens == 0 && u.PromptCacheHitTokens != nil {
		out.CacheReadTokens = *u.PromptCacheHitTokens
	}
	if u.PromptCacheMissTokens != nil {
		out.CacheWriteTokens = *u.PromptCacheMissTokens
	}
	return out
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/llm/provider/ -v`
Expected: all PASS — new tests plus existing `TestChatStreamingTokenUsage`/`TestChatNonStreamingTokenUsage` (which only check the 3 original fields, still populated correctly).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/llm/provider/openai_compatible_wire.go internal/llm/provider/openai_compatible.go internal/llm/provider/openai_compatible_test.go
git add internal/llm/provider/openai_compatible_wire.go internal/llm/provider/openai_compatible.go internal/llm/provider/openai_compatible_test.go
git commit -m "feat(provider): parse cache-read/cache-write/reasoning tokens from OpenAI-compatible wire"
```

---

### Task 3: Add the `internal/llm/pricing` package

**Files:**
- Create: `internal/llm/pricing/prices.go`
- Create: `internal/llm/pricing/pricing.go`
- Create: `internal/llm/pricing/pricing_test.go`

**Interfaces:**
- Consumes: `routing.ModelPreset` (for the config-override path), `schema.TokenUsage` (for cost computation).
- Produces:
  - `type ModelPricing struct { InputPerMTokCents, OutputPerMTokCents, ReasoningPerMTokCents, CacheReadPerMTokCents, CacheWritePerMTokCents int64 }`
  - `func Lookup(preset routing.ModelPreset) ModelPricing` — config preset pricing (if set) → built-in table by model name → zero.
  - `func EstimateCostCents(u schema.TokenUsage, p ModelPricing) int64` — `(tokens * rate) / 1_000_000` per category, summed.

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/pricing/pricing_test.go`:

```go
package pricing

import (
	"testing"

	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func TestLookupBuiltInTable(t *testing.T) {
	p := Lookup(routing.ModelPreset{Model: "gpt-4o"})
	if p.InputPerMTokCents == 0 {
		t.Error("gpt-4o should have a non-zero input price in the built-in table")
	}
}

func TestLookupLocalModelIsZero(t *testing.T) {
	p := Lookup(routing.ModelPreset{Model: "qwen2.5-coder:14b", Provider: "ollama"})
	if p.InputPerMTokCents != 0 || p.OutputPerMTokCents != 0 {
		t.Errorf("local model should be zero-priced: %+v", p)
	}
}

func TestLookupConfigOverrideWins(t *testing.T) {
	cfg := routing.ModelPreset{
		Model: "gpt-4o",
		Pricing: &ModelPricing{
			InputPerMTokCents:  999,
			OutputPerMTokCents: 999,
		},
	}
	p := Lookup(cfg)
	if p.InputPerMTokCents != 999 {
		t.Errorf("config override should win: got InputPerMTokCents=%d, want 999", p.InputPerMTokCents)
	}
}

func TestEstimateCostCents(t *testing.T) {
	p := ModelPricing{
		InputPerMTokCents:      250,  // $2.50/M
		OutputPerMTokCents:     1000, // $10.00/M
		ReasoningPerMTokCents:  1000,
		CacheReadPerMTokCents:  125,  // $1.25/M
		CacheWritePerMTokCents: 300,  // $3.00/M
	}
	u := schema.TokenUsage{
		PromptTokens:     1_000_000, // 1M input → 250 cents
		CompletionTokens: 500_000,   // 0.5M output → 500 cents
		ReasoningTokens:  100_000,   // 0.1M reasoning → 100 cents
		CacheReadTokens:  200_000,   // 0.2M cache read → 25 cents
		CacheWriteTokens: 100_000,   // 0.1M cache write → 30 cents
	}
	got := EstimateCostCents(u, p)
	want := int64(250 + 500 + 100 + 25 + 30) // 905 cents = $9.05
	if got != want {
		t.Errorf("EstimateCostCents = %d, want %d", got, want)
	}
}

func TestEstimateCostCentsZeroPricing(t *testing.T) {
	p := ModelPricing{} // all zero
	u := schema.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	if got := EstimateCostCents(u, p); got != 0 {
		t.Errorf("zero pricing should give zero cost: got %d", got)
	}
}

func TestEstimateCostCentsSubCentTruncates(t *testing.T) {
	p := ModelPricing{InputPerMTokCents: 250} // $2.50/M
	u := schema.TokenUsage{PromptTokens: 100} // 100 tokens → 0.025 cents → truncates to 0
	if got := EstimateCostCents(u, p); got != 0 {
		t.Errorf("sub-cent should truncate to 0: got %d", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/llm/pricing/ -v`
Expected: build failure — package doesn't exist, `ModelPricing`/`Lookup`/`EstimateCostCents` undefined.

- [ ] **Step 3: Create the pricing type and table**

Create `internal/llm/pricing/prices.go`:

```go
package pricing

// ModelPricing holds per-token-category rates in cents per million tokens
// (integer math, no floats). Used by EstimateCostCents to compute the
// estimated cost of a turn. Zero values mean free (local models default to
// all-zero). Rates are in hundredths-of-a-cent per million tokens, so
// InputPerMTokCents=250 means $2.50 per million input tokens.
type ModelPricing struct {
	InputPerMTokCents      int64
	OutputPerMTokCents     int64
	ReasoningPerMTokCents  int64
	CacheReadPerMTokCents  int64
	CacheWritePerMTokCents int64
}

// builtInTable is the seeded pricing for common hosted models, keyed by
// model name. Local models (Ollama) are absent → lookup returns zero.
// Prices are approximate and maintained manually; users can override per
// preset in config.toml. Rates in cents per million tokens.
var builtInTable = map[string]ModelPricing{
	"gpt-4o": {
		InputPerMTokCents:      250,
		OutputPerMTokCents:     1000,
		CacheReadPerMTokCents:  125,
		CacheWritePerMTokCents: 250,
	},
	"gpt-4o-mini": {
		InputPerMTokCents:      15,
		OutputPerMTokCents:     60,
		CacheReadPerMTokCents:  7,
		CacheWritePerMTokCents: 15,
	},
	"claude-sonnet-4": {
		InputPerMTokCents:      300,
		OutputPerMTokCents:     1500,
		CacheReadPerMTokCents:  30,
		CacheWritePerMTokCents: 375,
	},
	"deepseek-chat": {
		InputPerMTokCents:      14,
		OutputPerMTokCents:     28,
		CacheReadPerMTokCents:  1,
		CacheWritePerMTokCents: 14,
	},
	"deepseek-reasoner": {
		InputPerMTokCents:      55,
		OutputPerMTokCents:     219,
		ReasoningPerMTokCents:  219,
		CacheReadPerMTokCents:  14,
		CacheWritePerMTokCents: 55,
	},
}
```

- [ ] **Step 4: Create the lookup and cost functions**

Create `internal/llm/pricing/pricing.go`:

```go
package pricing

import (
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Lookup returns the pricing for a given preset. Precedence: config preset
// pricing (if the preset's Pricing pointer is set) → built-in table by
// model name → zero (local/unpriced). Pure function, no I/O.
func Lookup(preset routing.ModelPreset) ModelPricing {
	if preset.Pricing != nil {
		return *preset.Pricing
	}
	if p, ok := builtInTable[preset.Model]; ok {
		return p
	}
	return ModelPricing{}
}

// EstimateCostCents computes the estimated cost of a turn's token usage
// in hundredths-of-a-cent (1/10000 of a dollar). Each category is
// (tokens * rate) / 1_000_000, summed. Sub-cent amounts truncate via
// integer division, which is correct for an estimate.
func EstimateCostCents(u schema.TokenUsage, p ModelPricing) int64 {
	cost := int64(0)
	cost += (int64(u.PromptTokens) * p.InputPerMTokCents) / 1_000_000
	cost += (int64(u.CompletionTokens) * p.OutputPerMTokCents) / 1_000_000
	cost += (int64(u.ReasoningTokens) * p.ReasoningPerMTokCents) / 1_000_000
	cost += (int64(u.CacheReadTokens) * p.CacheReadPerMTokCents) / 1_000_000
	cost += (int64(u.CacheWriteTokens) * p.CacheWritePerMTokCents) / 1_000_000
	return cost
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/llm/pricing/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/llm/pricing/prices.go internal/llm/pricing/pricing.go internal/llm/pricing/pricing_test.go
git add internal/llm/pricing/prices.go internal/llm/pricing/pricing.go internal/llm/pricing/pricing_test.go
git commit -m "feat(pricing): add built-in pricing table, lookup, and cost estimation"
```

---

### Task 4: Add `Pricing` to `routing.ModelPreset` and wire config merge/save

**Files:**
- Modify: `internal/llm/routing/types.go`
- Modify: `internal/app/config/save.go`
- Modify: `internal/app/config/save_test.go`

**Interfaces:**
- Consumes: `pricing.ModelPricing` from Task 3.
- Produces: `routing.ModelPreset.Pricing *pricing.ModelPricing` field (TOML tag `pricing`), merged and persisted automatically (since `fileModels.Presets` is `map[string]routing.ModelPreset` directly).

- [ ] **Step 1: Write the failing test**

Append to `internal/app/config/save_test.go`:

```go
func TestPresetPricingRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/.marshal/config.toml"
	cfg := Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"hosted": {
			Name:     "hosted",
			Provider: "openai",
			Model:    "gpt-4o",
			Pricing: &pricing.ModelPricing{
				InputPerMTokCents:  250,
				OutputPerMTokCents: 1000,
			},
		},
	}
	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := loaded.Models.Presets["hosted"]
	if !ok {
		t.Fatal("preset 'hosted' not found after round-trip")
	}
	if got.Pricing == nil {
		t.Fatal("Pricing is nil after round-trip")
	}
	if got.Pricing.InputPerMTokCents != 250 || got.Pricing.OutputPerMTokCents != 1000 {
		t.Errorf("Pricing = %+v, want Input=250 Output=1000", got.Pricing)
	}
}
```

Add `"marshal/internal/llm/pricing"` to the test file imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/config/ -run TestPresetPricingRoundTrip -v`
Expected: FAIL — `routing.ModelPreset` has no `Pricing` field.

- [ ] **Step 3: Add the field to `ModelPreset`**

In `internal/llm/routing/types.go`, add to `ModelPreset` (after `LocalOnly`, line 49):

```go
	// Pricing holds optional per-token-category rates for cost estimation.
	// When nil, pricing.Lookup falls back to the built-in table by model
	// name, then zero. Set via [models.preset.<name>.pricing] in config.
	Pricing *pricing.ModelPricing `toml:"pricing,omitempty"`
```

Add `"marshal/internal/llm/pricing"` to the imports in `types.go`.

- [ ] **Step 4: Update the save path to persist pricing**

In `internal/app/config/save.go`, in the preset save loop (lines 200-208), add `Pricing` to the constructed `routing.ModelPreset`:

```go
		for name, p := range cfg.Models.Presets {
			preset := routing.ModelPreset{
				Provider: p.Provider, Model: p.Model, ContextWindow: p.ContextWindow,
				MaxOutputTokens: p.MaxOutputTokens,
				ToolCalling:      p.ToolCalling, LocalOnly: p.LocalOnly,
				Pricing:          p.Pricing,
			}
			preset.Name = name
			file.Models.Presets[name] = preset
		}
```

The merge path needs no change — `file.Models.Presets` is `map[string]routing.ModelPreset` directly, so TOML deserialization populates `Pricing` automatically.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/app/config/ -v`
Expected: PASS — new test plus existing config tests.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/llm/routing/types.go internal/app/config/save.go internal/app/config/save_test.go
git add internal/llm/routing/types.go internal/app/config/save.go internal/app/config/save_test.go
git commit -m "feat(routing): add Pricing field to ModelPreset with config round-trip"
```

---

### Task 5: Widen `TurnMetrics` and compute cost in `emitMetrics`

**Files:**
- Modify: `internal/agent/metrics.go`
- Modify: `internal/agent/runner.go`
- Modify: `internal/agent/chat.go`
- Modify: `internal/agent/metrics_test.go`

**Interfaces:**
- Consumes: `schema.TokenUsage` new fields (Task 1), `pricing.ModelPricing`/`pricing.EstimateCostCents` (Task 3).
- Produces:
  - `TurnMetrics` gains `ReasoningTokens int`, `CacheReadTokens int`, `CacheWriteTokens int`, `EstimatedCostCents int64`.
  - `Runner` gains a `Pricing pricing.ModelPricing` field (set by app.go from the resolved route preset).
  - `chat.go` records all usage fields into `turnStats`.
  - `emitMetrics` computes `EstimatedCostCents` via `pricing.EstimateCostCents`.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/metrics_test.go`:

```go
func TestTurnMetricsRecordsAllUsageFields(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{"done"},
		Usages: []*schema.TokenUsage{
			{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, ReasoningTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 10},
		},
		FinishReasons: []string{"stop"},
		ProviderCaps:  schema.ProviderCapabilities{},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "gpt-4o")
	runner.NativeTools = true
	runner.Pricing = pricing.ModelPricing{
		InputPerMTokCents:      250,
		OutputPerMTokCents:     1000,
		ReasoningPerMTokCents:  1000,
		CacheReadPerMTokCents:  125,
		CacheWritePerMTokCents: 300,
	}

	var got *TurnMetrics
	runner.MetricsObserver = func(m TurnMetrics) { got = &m }

	if err := runner.Run(context.Background(), "test"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got == nil {
		t.Fatal("no TurnMetrics emitted")
	}
	if got.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %d, want 20", got.ReasoningTokens)
	}
	if got.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30", got.CacheReadTokens)
	}
	if got.CacheWriteTokens != 10 {
		t.Errorf("CacheWriteTokens = %d, want 10", got.CacheWriteTokens)
	}
	if got.EstimatedCostCents <= 0 {
		t.Errorf("EstimatedCostCents = %d, want > 0 for a priced model", got.EstimatedCostCents)
	}
}
```

Add `"marshal/internal/llm/pricing"` to the test file imports. Confirm `newTestState` exists in `runner_misc_test.go` (it does — used throughout the test suite).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestTurnMetricsRecordsAllUsageFields -v`
Expected: FAIL — `TurnMetrics` has no `ReasoningTokens`/`CacheReadTokens`/`CacheWriteTokens`/`EstimatedCostCents` fields; `Runner` has no `Pricing` field.

- [ ] **Step 3: Widen `TurnMetrics`**

In `internal/agent/metrics.go`, add to `TurnMetrics` (after `CompletionTokens`, line 28):

```go
	ReasoningTokens  int
	CacheReadTokens  int
	CacheWriteTokens int
	// EstimatedCostCents is the estimated cost in hundredths of a cent
	// (1/10000 of a dollar), computed from the token counts and the
	// pricing table at metrics-emission time. 0 for local/unpriced models.
	EstimatedCostCents int64
```

- [ ] **Step 4: Add `Pricing` to the runner**

In `internal/agent/runner.go`, add to the `Runner` struct (after `NativeTools`, ~line 163):

```go
	// Pricing holds the resolved per-token-category rates for the active
	// model, used by emitMetrics to compute EstimatedCostCents. Set by
	// app.go from the resolved route preset via pricing.Lookup. Zero value
	// means local/unpriced (cost = 0).
	Pricing pricing.ModelPricing
```

Add `"marshal/internal/llm/pricing"` to the imports in `runner.go`.

Add `Pricing` to `CopyFrom` (runner.go:276-310) so swarm sub-runners inherit it:

```go
	r.Pricing = other.Pricing
```

- [ ] **Step 5: Record all usage fields in `chat.go`**

In `internal/agent/chat.go`, replace the usage-recording block (lines 99-107):

```go
	if r.UsageObserver != nil && usage != nil {
		r.UsageObserver(*usage)
	}
	if usage != nil {
		r.withStats(func(s *turnStats) {
			s.m.PromptTokens += usage.PromptTokens
			s.m.CompletionTokens += usage.CompletionTokens
			s.m.ReasoningTokens += usage.ReasoningTokens
			s.m.CacheReadTokens += usage.CacheReadTokens
			s.m.CacheWriteTokens += usage.CacheWriteTokens
		})
	}
```

Note: `UsageObserver` now receives `*usage` (the full `schema.TokenUsage`) instead of two ints. This is the signature change from Task 1's constraint.

- [ ] **Step 6: Widen `UsageObserver` and compute cost in `emitMetrics`**

In `internal/agent/runner.go`, change the `UsageObserver` type (line 146):

```go
type UsageObserver func(usage schema.TokenUsage)
```

In `internal/agent/metrics.go`, update `emitMetrics` (lines 79-91) to compute cost:

```go
func (r *Runner) emitMetrics(task *Task) {
	if r.MetricsObserver == nil {
		return
	}
	r.statsMu.Lock()
	m := r.stats.m
	r.statsMu.Unlock()
	m.DurationMs = r.Now().Sub(m.StartedAt).Milliseconds()
	m.Class = string(task.Class)
	m.Outcome = outcomeFor(task)
	m.SalvageReason = task.SalvageReason
	m.EstimatedCostCents = pricing.EstimateCostCents(schema.TokenUsage{
		PromptTokens:     m.PromptTokens,
		CompletionTokens:  m.CompletionTokens,
		ReasoningTokens:  m.ReasoningTokens,
		CacheReadTokens:  m.CacheReadTokens,
		CacheWriteTokens: m.CacheWriteTokens,
	}, r.Pricing)
	r.MetricsObserver(m)
}
```

Add `"marshal/internal/llm/pricing"` to the imports in `metrics.go`.

- [ ] **Step 7: Update the `UsageObserver` call sites in app.go and the swarm**

In `internal/app/app.go`, update the `UsageObserver` closure (lines 496-501):

```go
	runner.UsageObserver = func(usage schema.TokenUsage) {
		state.SetTurnUsage(usage.PromptTokens + usage.CompletionTokens)
		if usageCounter != nil {
			usageCounter.Observe(usage.PromptTokens)
		}
	}
```

Add `"marshal/internal/llm/schema"` to the imports in `app.go` if not present.

In `internal/agent/swarm/orchestrator.go`, update the two `UsageObserver` closures (lines 124-126 and 236-238):

For the scout closure (line 124):
```go
			runner.UsageObserver = func(usage schema.TokenUsage) {
				hasRealUsage = true
				meter.Observe(agent.RoleRepoScout, usage)
			}
```

For the `runRole` closure (line 236):
```go
	runner.UsageObserver = func(usage schema.TokenUsage) {
		hasRealUsage = true
		meter.Observe(role, usage)
		o.State.UpdateSwarmTokens(meter.Total(), o.MaxTotalTokens)
	}
```

Add `"marshal/internal/llm/schema"` to the imports in `orchestrator.go` if not present.

- [ ] **Step 8: Widen the swarm `TokenMeter` and `EstimateMeter`**

In `internal/agent/swarm/meter.go`, change the `TokenMeter` interface (line 15) and `EstimateMeter.Observe` (line 38):

```go
type TokenMeter interface {
	Observe(role agent.AgentRole, usage schema.TokenUsage)
	Total() int
}
```

```go
func (m *EstimateMeter) Observe(_ agent.AgentRole, usage schema.TokenUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total += usage.PromptTokens + usage.CompletionTokens
}
```

Add `"marshal/internal/llm/schema"` to the imports in `meter.go`.

Update the `observe` fallback (orchestrator.go:251-253) which constructs estimated usage:

```go
func (o *Orchestrator) observe(meter TokenMeter, role agent.AgentRole, prompt, answer string) {
	meter.Observe(role, schema.TokenUsage{
		PromptTokens:     EstimateText(prompt),
		CompletionTokens: EstimateText(answer),
	})
	o.State.UpdateSwarmTokens(meter.Total(), o.MaxTotalTokens)
}
```

- [ ] **Step 9: Update the swarm meter test and orchestrator test**

In `internal/agent/swarm/meter_test.go`, update `TestEstimateMeterAccumulates` (lines 8-18):

```go
func TestEstimateMeterAccumulates(t *testing.T) {
	m := NewEstimateMeter()
	if m.Total() != 0 {
		t.Fatalf("new meter Total = %d, want 0", m.Total())
	}
	m.Observe(agent.RolePlanner, schema.TokenUsage{PromptTokens: 100, CompletionTokens: 50})
	m.Observe(agent.RoleImplementer, schema.TokenUsage{PromptTokens: 200, CompletionTokens: 80})
	if got := m.Total(); got != 430 {
		t.Fatalf("Total = %d, want 430", got)
	}
}
```

Add `"marshal/internal/llm/schema"` to the imports in `meter_test.go`.

The `TestOrchestratorUsesRealTokenUsage` test (orchestrator_test.go:422) constructs a `usageScriptedProvider` with `schema.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}` — this still works since the new fields default to 0. No change needed there unless the test asserts on the meter total (it asserts on the final message containing `~1050 / 10000`, which is `100+50` per role × 7 roles ≈ 1050 — unchanged). Verify by running the test.

- [ ] **Step 10: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestTurnMetricsRecordsAllUsageFields -v`
Run: `go test ./internal/agent/swarm/ -v`
Run: `go test ./internal/app/ -run TestApp -v -count=1` (or the relevant app tests that wire UsageObserver)
Expected: PASS. If any test constructs a `UsageObserver` with the old 2-int signature, update it to the new `func(usage schema.TokenUsage)` signature. Search for `UsageObserver = func(` across the test suite and update each.

- [ ] **Step 11: Commit**

```bash
gofmt -w internal/agent/metrics.go internal/agent/runner.go internal/agent/chat.go internal/agent/metrics_test.go internal/agent/swarm/meter.go internal/agent/swarm/orchestrator.go internal/agent/swarm/meter_test.go internal/app/app.go
git add internal/agent/metrics.go internal/agent/runner.go internal/agent/chat.go internal/agent/metrics_test.go internal/agent/swarm/meter.go internal/agent/swarm/orchestrator.go internal/agent/swarm/meter_test.go internal/app/app.go
git commit -m "feat(agent): widen TurnMetrics with reasoning/cache/cost and widen UsageObserver"
```

---

### Task 6: Widen the DB `turn_metrics` table and add `AggregateTurnMetrics`

**Files:**
- Modify: `internal/db/migrations.go`
- Modify: `internal/db/turnmetrics.go`
- Modify: `internal/db/turnmetrics_test.go`

**Interfaces:**
- Consumes: `TurnMetrics` new fields from Task 5.
- Produces:
  - `TurnMetricsRow` gains `ReasoningTokens`/`CacheReadTokens`/`CacheWriteTokens`/`EstimatedCostCents`.
  - `InsertTurnMetrics`/`RecentTurnMetrics` read/write the new columns.
  - `func (db *DB) AggregateTurnMetrics(projectID int64) (UsageTotals, []ModelBreakdown, error)` — project-scope `GROUP BY provider, model` query.

- [ ] **Step 1: Write the failing tests**

Append to `internal/db/turnmetrics_test.go`:

```go
func TestInsertAndRecentTurnMetricsWithNewFields(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	row := sampleRow(projectID, "s1")
	row.ReasoningTokens = 20
	row.CacheReadTokens = 30
	row.CacheWriteTokens = 10
	row.EstimatedCostCents = 427

	id, err := database.InsertTurnMetrics(row)
	if err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}
	if id == 0 {
		t.Fatal("InsertTurnMetrics returned id 0")
	}

	rows, err := database.RecentTurnMetrics(projectID, 10)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %d, want 20", rows[0].ReasoningTokens)
	}
	if rows[0].CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30", rows[0].CacheReadTokens)
	}
	if rows[0].CacheWriteTokens != 10 {
		t.Errorf("CacheWriteTokens = %d, want 10", rows[0].CacheWriteTokens)
	}
	if rows[0].EstimatedCostCents != 427 {
		t.Errorf("EstimatedCostCents = %d, want 427", rows[0].EstimatedCostCents)
	}
}

func TestAggregateTurnMetrics(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	// Two turns from one model, one from another.
	r1 := sampleRow(projectID, "s1")
	r1.Provider = "openai"
	r1.Model = "gpt-4o"
	r1.PromptTokens = 100
	r1.CompletionTokens = 50
	r1.EstimatedCostCents = 200
	r2 := sampleRow(projectID, "s1")
	r2.Provider = "openai"
	r2.Model = "gpt-4o"
	r2.PromptTokens = 200
	r2.CompletionTokens = 100
	r2.EstimatedCostCents = 400
	r3 := sampleRow(projectID, "s2")
	r3.Provider = "ollama"
	r3.Model = "qwen2.5-coder:14b"
	r3.PromptTokens = 500
	r3.CompletionTokens = 250
	r3.EstimatedCostCents = 0

	for _, r := range []TurnMetricsRow{r1, r2, r3} {
		if _, err := database.InsertTurnMetrics(r); err != nil {
			t.Fatalf("InsertTurnMetrics: %v", err)
		}
	}

	totals, breakdowns, err := database.AggregateTurnMetrics(projectID)
	if err != nil {
		t.Fatalf("AggregateTurnMetrics: %v", err)
	}
	if totals.PromptTokens != 800 {
		t.Errorf("totals PromptTokens = %d, want 800", totals.PromptTokens)
	}
	if totals.CompletionTokens != 400 {
		t.Errorf("totals CompletionTokens = %d, want 400", totals.CompletionTokens)
	}
	if totals.EstimatedCostCents != 600 {
		t.Errorf("totals EstimatedCostCents = %d, want 600", totals.EstimatedCostCents)
	}
	if totals.Turns != 3 {
		t.Errorf("totals Turns = %d, want 3", totals.Turns)
	}
	if len(breakdowns) != 2 {
		t.Fatalf("got %d breakdowns, want 2", len(breakdowns))
	}
	// Breakdowns sorted by cost descending: gpt-4o (600) first.
	if breakdowns[0].Model != "gpt-4o" {
		t.Errorf("breakdown[0].Model = %q, want gpt-4o", breakdowns[0].Model)
	}
	if breakdowns[0].EstimatedCostCents != 600 {
		t.Errorf("breakdown[0].EstimatedCostCents = %d, want 600", breakdowns[0].EstimatedCostCents)
	}
	if breakdowns[1].Model != "qwen2.5-coder:14b" {
		t.Errorf("breakdown[1].Model = %q, want qwen2.5-coder:14b", breakdowns[1].Model)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db/ -run "TestInsertAndRecentTurnMetricsWithNewFields|TestAggregateTurnMetrics" -v`
Expected: FAIL — `TurnMetricsRow` lacks the new fields; `AggregateTurnMetrics` undefined.

- [ ] **Step 3: Add the migration columns**

In `internal/db/migrations.go`, add `turn_metrics` to `allowedTableInfo` (line 128-133):

```go
var allowedTableInfo = map[string]bool{
	"tool_calls":     true,
	"files":          true,
	"messages":       true,
	"agent_sessions": true,
	"turn_metrics":   true,
}
```

Add the four new columns to `migrationColumns` (after line 67, before the closing `}`):

```go
	{"turn_metrics", "reasoning_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"turn_metrics", "cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"turn_metrics", "cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"turn_metrics", "estimated_cost_cents", "INTEGER NOT NULL DEFAULT 0"},
```

- [ ] **Step 4: Widen `TurnMetricsRow` and `InsertTurnMetrics`/`RecentTurnMetrics`**

In `internal/db/turnmetrics.go`, add to `TurnMetricsRow` (after `CompletionTokens`, line 32):

```go
	ReasoningTokens   int
	CacheReadTokens   int
	CacheWriteTokens  int
	EstimatedCostCents int64
```

Update `InsertTurnMetrics` (lines 35-76) — add the four new columns to the INSERT and the values:

```go
	res, err := db.sqlDB.Exec(
		`INSERT INTO turn_metrics (
			project_id, session_id, started_at, duration_ms, class, role,
			provider, model, goal, iterations, tool_calls, tool_errors,
			cache_hits, parse_failures, soft_stalls, hard_stalls, outcome,
			salvage_reason, prompt_tokens, completion_tokens,
			reasoning_tokens, cache_read_tokens, cache_write_tokens,
			estimated_cost_cents
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ProjectID,
		sessionID,
		row.StartedAt.UTC().Format(time.RFC3339),
		row.DurationMs,
		row.Class,
		row.Role,
		row.Provider,
		row.Model,
		row.Goal,
		row.Iterations,
		row.ToolCalls,
		row.ToolErrors,
		row.CacheHits,
		row.ParseFailures,
		0, // soft_stalls
		row.HardStalls,
		row.Outcome,
		row.SalvageReason,
		row.PromptTokens,
		row.CompletionTokens,
		row.ReasoningTokens,
		row.CacheReadTokens,
		row.CacheWriteTokens,
		row.EstimatedCostCents,
	)
```

Update `RecentTurnMetrics` (lines 78-129) — add the four new columns to the SELECT and the Scan:

In the SELECT query (line 86-93), add after `completion_tokens`:
```sql
			, reasoning_tokens, cache_read_tokens, cache_write_tokens, estimated_cost_cents
```

In the Scan call (lines 106-112), add after `&r.CompletionTokens,`:
```go
			&r.ReasoningTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.EstimatedCostCents,
```

- [ ] **Step 5: Add `AggregateTurnMetrics`**

Append to `internal/db/turnmetrics.go`:

```go
// AggregateTurnMetrics returns project-scoped aggregate token usage broken
// down by (provider, model). The grand total is the sum of all rows; the
// breakdowns are one entry per (provider, model) pair, sorted by
// EstimatedCostCents descending. Used by the /usage command and dock panel
// for the project-lifetime scope.
func (db *DB) AggregateTurnMetrics(projectID int64) (agent.UsageTotals, []agent.ModelBreakdown, error) {
	rows, err := db.sqlDB.Query(
		`SELECT provider, model,
			SUM(prompt_tokens), SUM(completion_tokens), SUM(reasoning_tokens),
			SUM(cache_read_tokens), SUM(cache_write_tokens), SUM(estimated_cost_cents),
			COUNT(*)
		 FROM turn_metrics
		 WHERE project_id = ?
		 GROUP BY provider, model
		 ORDER BY SUM(estimated_cost_cents) DESC`,
		projectID,
	)
	if err != nil {
		return agent.UsageTotals{}, nil, fmt.Errorf("query aggregate turn metrics: %w", err)
	}
	defer rows.Close()

	var totals agent.UsageTotals
	var breakdowns []agent.ModelBreakdown
	for rows.Next() {
		var b agent.ModelBreakdown
		if err := rows.Scan(
			&b.Provider, &b.Model,
			&b.PromptTokens, &b.CompletionTokens, &b.ReasoningTokens,
			&b.CacheReadTokens, &b.CacheWriteTokens, &b.EstimatedCostCents,
			&b.Turns,
		); err != nil {
			return agent.UsageTotals{}, nil, fmt.Errorf("scan aggregate row: %w", err)
		}
		totals.PromptTokens += b.PromptTokens
		totals.CompletionTokens += b.CompletionTokens
		totals.ReasoningTokens += b.ReasoningTokens
		totals.CacheReadTokens += b.CacheReadTokens
		totals.CacheWriteTokens += b.CacheWriteTokens
		totals.EstimatedCostCents += b.EstimatedCostCents
		totals.Turns += b.Turns
		breakdowns = append(breakdowns, b)
	}
	if err := rows.Err(); err != nil {
		return agent.UsageTotals{}, nil, fmt.Errorf("iterate aggregate rows: %w", err)
	}
	return totals, breakdowns, nil
}
```

Add `"marshal/internal/agent"` to the imports in `turnmetrics.go`. Note: this creates a dependency from `internal/db` to `internal/agent` for the `UsageTotals`/`ModelBreakdown` types. If that creates an import cycle (agent imports db), define `UsageTotals`/`ModelBreakdown` in a shared package instead. Check for a cycle: `internal/agent` imports `internal/db` (yes, via `dbMemoryProvider` and `MetricsObserver` persistence). To avoid the cycle, define `UsageTotals` and `ModelBreakdown` in `internal/db` (or a new `internal/usage` package) and have `internal/agent` re-export or alias them. **Resolve this in Task 7** where `UsageAggregator` is defined — the types should live in `internal/agent` and `AggregateTurnMetrics` should return them via a lightweight interface or the db package should define its own row types that the agent package adapts. For this task, define `UsageTotals`/`ModelBreakdown` in `internal/db` (since the DB owns the aggregate query) and have the agent package's `UsageAggregator` (Task 7) use the same types via a type alias or by importing db. Since agent already imports db, agent can use `db.UsageTotals`/`db.ModelBreakdown` directly. Adjust the `AggregateTurnMetrics` return types accordingly:

```go
func (db *DB) AggregateTurnMetrics(projectID int64) (UsageTotals, []ModelBreakdown, error)
```

With `UsageTotals`/`ModelBreakdown` defined in the `db` package. The agent package's `UsageAggregator` (Task 7) uses `db.UsageTotals`/`db.ModelBreakdown` for its session-scope snapshot too, so the types are shared. Remove the `agent` import from `turnmetrics.go`.

- [ ] **Step 6: Define `UsageTotals` and `ModelBreakdown` in the db package**

In `internal/db/turnmetrics.go` (or a new `internal/db/usage.go`), add:

```go
// UsageTotals is the aggregate token usage across a set of turns.
type UsageTotals struct {
	PromptTokens       int64
	CompletionTokens   int64
	ReasoningTokens    int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	EstimatedCostCents int64
	Turns              int
}

// ModelBreakdown is the per-(provider, model) aggregate.
type ModelBreakdown struct {
	UsageTotals
	Provider string
	Model    string
	// Reported tracks which token categories the provider has ever
	// surfaced non-zero values for. Populated by the in-memory aggregator
	// (session scope); nil/zero for DB-derived (project scope) breakdowns,
	// which use a coarser "sum is 0 → n/a" heuristic.
	Reported TokenCategorySet
}

// TokenCategorySet is a bitset of which token categories a provider has
// reported across a session. Used to distinguish "0 tokens" from "not
// reported" (rendered as n/a).
type TokenCategorySet uint8

const (
	CatPrompt     TokenCategorySet = 1 << iota
	CatCompletion
	CatReasoning
	CatCacheRead
	CatCacheWrite
)
```

Update `AggregateTurnMetrics` to return `UsageTotals, []ModelBreakdown` (defined in db, no agent import).

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/db/ -v`
Expected: PASS — new tests plus existing `TestInsertAndRecentTurnMetricsRoundTrip` (which doesn't set the new fields — they default to 0, and the existing Scan doesn't read them, but we just widened the Scan, so existing rows from `sampleRow` need the new fields to be 0. Verify `sampleRow` (turnmetrics_test.go:26) returns a row with zero new fields — it does, since Go zero-values them).

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/db/migrations.go internal/db/turnmetrics.go internal/db/turnmetrics_test.go
git add internal/db/migrations.go internal/db/turnmetrics.go internal/db/turnmetrics_test.go
git commit -m "feat(db): widen turn_metrics with reasoning/cache/cost columns and add AggregateTurnMetrics"
```

---

### Task 7: Add the `UsageAggregator` for session-scope live tracking

**Files:**
- Create: `internal/agent/usage.go`
- Create: `internal/agent/usage_test.go`

**Interfaces:**
- Consumes: `TurnMetrics` (Task 5), `db.UsageTotals`/`db.ModelBreakdown`/`db.TokenCategorySet` (Task 6).
- Produces:
  - `type UsageAggregator struct` — thread-safe in-memory accumulator.
  - `func NewUsageAggregator() *UsageAggregator`
  - `func (a *UsageAggregator) Observe(m TurnMetrics)` — adds a turn to the grand total + per-model bucket, updating the `Reported` capability set.
  - `func (a *UsageAggregator) Snapshot() (db.UsageTotals, []db.ModelBreakdown)` — returns the session aggregate + per-model breakdowns sorted by cost descending.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/usage_test.go`:

```go
package agent

import (
	"testing"

	"marshal/internal/db"
)

func TestUsageAggregatorAccumulates(t *testing.T) {
	a := NewUsageAggregator()
	a.Observe(TurnMetrics{
		Provider: "openai", Model: "gpt-4o",
		PromptTokens: 100, CompletionTokens: 50,
		ReasoningTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 10,
		EstimatedCostCents: 200,
	})
	a.Observe(TurnMetrics{
		Provider: "openai", Model: "gpt-4o",
		PromptTokens: 200, CompletionTokens: 100,
		EstimatedCostCents: 400,
	})
	a.Observe(TurnMetrics{
		Provider: "ollama", Model: "qwen2.5-coder:14b",
		PromptTokens: 500, CompletionTokens: 250,
		EstimatedCostCents: 0,
	})

	totals, breakdowns := a.Snapshot()
	if totals.PromptTokens != 800 {
		t.Errorf("totals PromptTokens = %d, want 800", totals.PromptTokens)
	}
	if totals.CompletionTokens != 400 {
		t.Errorf("totals CompletionTokens = %d, want 400", totals.CompletionTokens)
	}
	if totals.EstimatedCostCents != 600 {
		t.Errorf("totals EstimatedCostCents = %d, want 600", totals.EstimatedCostCents)
	}
	if totals.Turns != 3 {
		t.Errorf("totals Turns = %d, want 3", totals.Turns)
	}
	if len(breakdowns) != 2 {
		t.Fatalf("got %d breakdowns, want 2", len(breakdowns))
	}
	// Sorted by cost descending: gpt-4o (600) first.
	if breakdowns[0].Model != "gpt-4o" {
		t.Errorf("breakdown[0].Model = %q, want gpt-4o", breakdowns[0].Model)
	}
	if breakdowns[0].PromptTokens != 300 {
		t.Errorf("breakdown[0].PromptTokens = %d, want 300", breakdowns[0].PromptTokens)
	}
}

func TestUsageAggregatorCapabilityTracking(t *testing.T) {
	a := NewUsageAggregator()
	// First turn: gpt-4o reports reasoning + cache read, but NOT cache write.
	a.Observe(TurnMetrics{
		Provider: "openai", Model: "gpt-4o",
		PromptTokens: 100, CompletionTokens: 50,
		ReasoningTokens: 20, CacheReadTokens: 30,
	})
	_, breakdowns := a.Snapshot()
	if len(breakdowns) != 1 {
		t.Fatalf("got %d breakdowns, want 1", len(breakdowns))
	}
	r := breakdowns[0].Reported
	if r&db.CatReasoning == 0 {
		t.Error("CatReasoning should be reported after a non-zero reasoning turn")
	}
	if r&db.CatCacheRead == 0 {
		t.Error("CatCacheRead should be reported after a non-zero cache-read turn")
	}
	if r&db.CatCacheWrite != 0 {
		t.Error("CatCacheWrite should NOT be reported (never seen non-zero)")
	}
}

func TestUsageAggregatorLocalModelNoCapabilities(t *testing.T) {
	a := NewUsageAggregator()
	a.Observe(TurnMetrics{
		Provider: "ollama", Model: "qwen2.5-coder:14b",
		PromptTokens: 100, CompletionTokens: 50,
	})
	_, breakdowns := a.Snapshot()
	if len(breakdowns) != 1 {
		t.Fatalf("got %d breakdowns, want 1", len(breakdowns))
	}
	r := breakdowns[0].Reported
	// Prompt and completion are always reported (they're the core fields).
	if r&db.CatPrompt == 0 {
		t.Error("CatPrompt should always be reported")
	}
	if r&db.CatCompletion == 0 {
		t.Error("CatCompletion should always be reported")
	}
	if r&db.CatReasoning != 0 {
		t.Error("CatReasoning should NOT be reported for a local model without reasoning")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestUsageAggregator -v`
Expected: FAIL — `UsageAggregator`/`NewUsageAggregator` undefined.

- [ ] **Step 3: Implement the aggregator**

Create `internal/agent/usage.go`:

```go
package agent

import (
	"sort"
	"sync"

	"marshal/internal/db"
)

// UsageAggregator accumulates session-scoped token usage in memory, fed by
// the runner's MetricsObserver. It tracks per-(provider, model) totals and
// which token categories each provider has reported (so the display can
// distinguish "0 tokens" from "not reported" → n/a). Thread-safe.
type UsageAggregator struct {
	mu      sync.Mutex
	totals  db.UsageTotals
	byModel map[string]*db.ModelBreakdown
}

func NewUsageAggregator() *UsageAggregator {
	return &UsageAggregator{byModel: map[string]*db.ModelBreakdown{}}
}

func modelKey(provider, model string) string {
	return provider + "/" + model
}

// Observe adds a turn's metrics to the grand total and the per-model bucket,
// updating the Reported capability set when a category is seen non-zero.
func (a *UsageAggregator) Observe(m TurnMetrics) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.totals.PromptTokens += int64(m.PromptTokens)
	a.totals.CompletionTokens += int64(m.CompletionTokens)
	a.totals.ReasoningTokens += int64(m.ReasoningTokens)
	a.totals.CacheReadTokens += int64(m.CacheReadTokens)
	a.totals.CacheWriteTokens += int64(m.CacheWriteTokens)
	a.totals.EstimatedCostCents += m.EstimatedCostCents
	a.totals.Turns++

	key := modelKey(m.Provider, m.Model)
	b, ok := a.byModel[key]
	if !ok {
		b = &db.ModelBreakdown{Provider: m.Provider, Model: m.Model}
		// Prompt and completion are always reported (core fields).
		b.Reported = db.CatPrompt | db.CatCompletion
		a.byModel[key] = b
	}
	b.PromptTokens += int64(m.PromptTokens)
	b.CompletionTokens += int64(m.CompletionTokens)
	b.ReasoningTokens += int64(m.ReasoningTokens)
	b.CacheReadTokens += int64(m.CacheReadTokens)
	b.CacheWriteTokens += int64(m.CacheWriteTokens)
	b.EstimatedCostCents += m.EstimatedCostCents
	b.Turns++

	// Update capability set: a category is "reported" once seen non-zero.
	if m.ReasoningTokens > 0 {
		b.Reported |= db.CatReasoning
	}
	if m.CacheReadTokens > 0 {
		b.Reported |= db.CatCacheRead
	}
	if m.CacheWriteTokens > 0 {
		b.Reported |= db.CatCacheWrite
	}
}

// Snapshot returns the session aggregate totals and per-model breakdowns
// sorted by EstimatedCostCents descending. Safe to call concurrently.
func (a *UsageAggregator) Snapshot() (db.UsageTotals, []db.ModelBreakdown) {
	a.mu.Lock()
	defer a.mu.Unlock()

	breakdowns := make([]db.ModelBreakdown, 0, len(a.byModel))
	for _, b := range a.byModel {
		breakdowns = append(breakdowns, *b)
	}
	sort.Slice(breakdowns, func(i, j int) bool {
		return breakdowns[i].EstimatedCostCents > breakdowns[j].EstimatedCostCents
	})
	return a.totals, breakdowns
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestUsageAggregator -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/usage.go internal/agent/usage_test.go
git add internal/agent/usage.go internal/agent/usage_test.go
git commit -m "feat(agent): add UsageAggregator for session-scope token tracking with capability sets"
```

---

### Task 8: Wire the aggregator and pricing into app construction

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/runtime.go`

**Interfaces:**
- Consumes: `UsageAggregator` (Task 7), `pricing.Lookup` (Task 3), `db.AggregateTurnMetrics` (Task 6).
- Produces: `app.go` constructs a `UsageAggregator`, wires it as the runner's `MetricsObserver` (alongside the existing DB recorder), and seeds `runner.Pricing` from the resolved route preset.

- [ ] **Step 1: Construct the aggregator and wire the observer**

In `internal/app/app.go`, after the runner is constructed (line 481), add the aggregator and wire a combined `MetricsObserver`:

```go
	runner := agent.NewRunner(resolvedProvider, reg, pol, state, route.Preset.Model)
	runner.SkillIndex = skillIndex
	runner.RouteResolver = resolver
	runner.MemoryProvider = &dbMemoryProvider{db: database}
	runner.ProjectID = projectID

	// Token metric tracking: the aggregator accumulates session-scope
	// totals for the /usage command and dock panel; the DB recorder
	// persists per-turn rows for project-scope queries.
	usageAggregator := agent.NewUsageAggregator()
	dbRecorder := metricsRecorder(database, projectID, state.SessionID(), state.Logger())
	runner.MetricsObserver = func(m agent.TurnMetrics) {
		usageAggregator.Observe(m)
		dbRecorder(m)
	}

	// Seed the runner's pricing from the resolved route preset.
	runner.Pricing = pricing.Lookup(route.Preset)
```

Add `"marshal/internal/llm/pricing"` to the imports in `app.go`.

- [ ] **Step 2: Expose the aggregator to the TUI**

The TUI needs the aggregator handle for the `/usage` command and dock panel. The cleanest seam: store it on the `Runtime` struct so the TUI can access it. In `internal/app/runtime.go`, add to the `Runtime` struct (after `State *session.State`, line 61):

```go
	UsageAggregator *agent.UsageAggregator
```

In `app.go`, after constructing the aggregator, set it on the runtime (find where the `Runtime` is assembled — it's the return value of the build function; set `rt.UsageAggregator = usageAggregator` before returning, or pass it through the construction). Inspect how the `Runtime` is built and wire the field.

- [ ] **Step 3: Run the app tests to verify no regressions**

Run: `go test ./internal/app/ -v -count=1`
Expected: PASS. If a test constructs a runner with a `MetricsObserver` that has the old signature, update it. Search for `MetricsObserver =` in app tests and verify they use `func(m agent.TurnMetrics)` (they should, since `MetricsObserver`'s type didn't change — only `UsageObserver` changed).

- [ ] **Step 4: Commit**

```bash
gofmt -w internal/app/app.go internal/app/runtime.go
git add internal/app/app.go internal/app/runtime.go
git commit -m "feat(app): wire UsageAggregator and pricing into runner construction"
```

---

### Task 9: Add the `/usage` command and status-line cost segment

**Files:**
- Modify: `internal/commands/commands.go`
- Modify: `internal/commands/commands_test.go`
- Modify: `internal/app/tui/commands_dispatch.go`
- Modify: `internal/app/tui/status.go`
- Modify: `internal/app/tui/status_test.go`
- Modify: `internal/app/tui/model.go`

**Interfaces:**
- Consumes: `UsageAggregator` (Task 7), `db.AggregateTurnMetrics` (Task 6), `db.TokenCategorySet` (Task 6).
- Produces:
  - `/usage` command registration (TUIOnly) + dispatch handler that prints the transcript summary.
  - Status-line `cost $X.XX` segment (session total, when > 0).
  - `Model.usageAggregator` field + wiring.

- [ ] **Step 1: Register the `/usage` command**

In `internal/commands/commands.go`, add a new command entry (in the `groupSettings` group, near `/config`):

```go
		{
			Name:        "usage",
			Description: "Show token usage and cost breakdown (session + project)",
			Group:       groupSettings,
			TUIOnly:     true,
		},
```

- [ ] **Step 2: Write the command-registration test**

Append to `internal/commands/commands_test.go`:

```go
func TestUsageCommandRegistered(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, ok := cmdReg.Lookup("usage")
	if !ok {
		t.Fatal("usage command not registered")
	}
	if !cmd.TUIOnly {
		t.Error("usage should be TUIOnly")
	}
	if cmd.Group != groupSettings {
		t.Errorf("usage group = %q, want %q", cmd.Group, groupSettings)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/commands/ -run TestUsageCommandRegistered -v`
Expected: FAIL — `usage` not registered.

- [ ] **Step 4: Add the `/usage` dispatch handler**

In `internal/app/tui/commands_dispatch.go`, add to the `tuiCommandEffects` map:

```go
		"usage": func(m *Model, _ []string) (tea.Model, tea.Cmd) {
			m.openUsagePanel()
			m.refreshViewport()
			return m, nil
		},
```

- [ ] **Step 5: Add `usageAggregator` to the Model and the `openUsagePanel` method**

In `internal/app/tui/model.go`, add a field to the `Model` struct:

```go
	usageAggregator *agent.UsageAggregator
```

Add `"marshal/internal/agent"` to the imports if not present (it likely is via `AgentRunner`).

Add the `openUsagePanel` method:

```go
func (m *Model) openUsagePanel() {
	if m.usageAggregator == nil {
		m.state.AddMessage(session.RoleSystem, "Usage tracking not available.", session.ContentTypePlain)
		return
	}
	panel := usage.NewPanel(m.usageAggregator, m.db, m.state.ProjectID(), m.state.WorkingDir)
	m.dock.Open(panel)
}
```

Add `"marshal/internal/app/tui/usage"` to the imports. Inspect whether `m.db` and `m.state.ProjectID()` exist on the Model/State — search for how the memory panel accesses the DB (`m.memoryDB` and `m.memoryProject` in `commands_dispatch.go:38-43`). Mirror that pattern: if the Model holds `memoryDB *db.DB` and `memoryProject int64`, add equivalent `usageDB`/`usageProjectID` fields or reuse the same DB handle. Adapt to the existing pattern.

- [ ] **Step 6: Wire the aggregator into the Model**

In `internal/app/tui/model.go`, find where the Model is constructed (the `New` function or `WithRunner`). The aggregator comes from the `Runtime` (Task 8, Step 2). Inspect how the TUI receives the runtime — it's passed via `WithRunner`/`WithSwarmRunner` options. Add a `WithUsageAggregator` option or set it from the runtime in `app.go`'s TUI construction. Find the seam and wire `m.usageAggregator = rt.UsageAggregator`.

- [ ] **Step 7: Add the status-line cost segment**

In `internal/app/tui/status.go`, in `statusLeftSegments` (after the swarm-tokens segment, ~line 152), add:

```go
	if m.usageAggregator != nil {
		totals, _ := m.usageAggregator.Snapshot()
		if totals.EstimatedCostCents > 0 {
			segs = append(segs, statusSeg{
				text:     fmt.Sprintf("cost $%s", formatCost(totals.EstimatedCostCents)),
				priority: 6,
			})
		}
	}
```

Add the `formatCost` helper:

```go
// formatCost renders hundredths-of-a-cent as a dollar string: 427 → "0.0427".
func formatCost(cents int64) string {
	dollars := cents / 10000
	remainder := cents % 10000
	return fmt.Sprintf("%d.%04d", dollars, remainder)
}
```

- [ ] **Step 8: Write the status-line cost test**

Append to `internal/app/tui/status_test.go`:

```go
func TestStatusLineShowsCostWhenNonZero(t *testing.T) {
	m := newTestModel()
	m.usageAggregator = agent.NewUsageAggregator()
	m.usageAggregator.Observe(agent.TurnMetrics{
		Provider: "openai", Model: "gpt-4o",
		PromptTokens: 1_000_000, CompletionTokens: 500_000,
		EstimatedCostCents: 427,
	})
	line := m.renderStatusLine(120)
	if !strings.Contains(line, "cost $") {
		t.Errorf("status line should show cost when non-zero: %s", line)
	}
}

func TestStatusLineHidesCostWhenZero(t *testing.T) {
	m := newTestModel()
	m.usageAggregator = agent.NewUsageAggregator()
	// Local model, zero cost.
	m.usageAggregator.Observe(agent.TurnMetrics{
		Provider: "ollama", Model: "qwen2.5-coder:14b",
		PromptTokens: 1000, CompletionTokens: 500,
		EstimatedCostCents: 0,
	})
	line := m.renderStatusLine(120)
	if strings.Contains(line, "cost $") {
		t.Errorf("status line should not show cost when zero: %s", line)
	}
}
```

Add `"marshal/internal/agent"` to the test file imports. Confirm `newTestModel` exists (search `model_test.go`).

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/commands/ -run TestUsageCommand -v`
Run: `go test ./internal/app/tui/ -run TestStatusLine -v`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
gofmt -w internal/commands/commands.go internal/commands/commands_test.go internal/app/tui/commands_dispatch.go internal/app/tui/status.go internal/app/tui/status_test.go internal/app/tui/model.go
git add internal/commands/commands.go internal/commands/commands_test.go internal/app/tui/commands_dispatch.go internal/app/tui/status.go internal/app/tui/status_test.go internal/app/tui/model.go
git commit -m "feat(tui): add /usage command and status-line cost segment"
```

---

### Task 10: Build the usage dock panel

**Files:**
- Create: `internal/app/tui/usage/panel.go`
- Create: `internal/app/tui/usage/panel_test.go`

**Interfaces:**
- Consumes: `agent.UsageAggregator` (Task 7), `db.AggregateTurnMetrics`/`db.ModelBreakdown`/`db.TokenCategorySet` (Task 6).
- Produces: `usage.Panel` implementing `dock.Panel` — a live breakdown table with session/project scope toggle and `n/a`/`unsupported` rendering for unreported categories.

- [ ] **Step 1: Write the failing test**

Create `internal/app/tui/usage/panel_test.go`:

```go
package usage

import (
	"strings"
	"testing"

	"marshal/internal/agent"
	"marshal/internal/db"
)

func TestPanelRendersSessionTotals(t *testing.T) {
	agg := agent.NewUsageAggregator()
	agg.Observe(agent.TurnMetrics{
		Provider: "openai", Model: "gpt-4o",
		PromptTokens: 100, CompletionTokens: 50,
		ReasoningTokens: 20, CacheReadTokens: 30,
		EstimatedCostCents: 200,
	})
	agg.Observe(agent.TurnMetrics{
		Provider: "ollama", Model: "qwen2.5-coder:14b",
		PromptTokens: 500, CompletionTokens: 250,
		EstimatedCostCents: 0,
	})

	p := NewPanel(agg, nil, 0, "")
	view := p.View(120, 20)

	if !strings.Contains(view, "gpt-4o") {
		t.Errorf("panel should list gpt-4o: %s", view)
	}
	if !strings.Contains(view, "qwen2.5-coder:14b") {
		t.Errorf("panel should list qwen2.5-coder:14b: %s", view)
	}
	if !strings.Contains(view, "$0.0200") {
		t.Errorf("panel should show cost $0.0200: %s", view)
	}
}

func TestPanelRendersNAForUnreportedCategories(t *testing.T) {
	agg := agent.NewUsageAggregator()
	agg.Observe(agent.TurnMetrics{
		Provider: "ollama", Model: "qwen2.5-coder:14b",
		PromptTokens: 100, CompletionTokens: 50,
		// No reasoning, cache read, or cache write reported.
	})

	p := NewPanel(agg, nil, 0, "")
	view := p.View(120, 20)

	// The local model row should show n/a for reasoning/cache columns.
	if !strings.Contains(view, "n/a") {
		t.Errorf("panel should show n/a for unreported categories: %s", view)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/tui/usage/ -v`
Expected: build failure — package doesn't exist.

- [ ] **Step 3: Implement the panel**

Create `internal/app/tui/usage/panel.go`:

```go
package usage

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/agent"
	"marshal/internal/app/tui/dock"
	"marshal/internal/db"
	"marshal/internal/strutil"
)

// Panel is the docked usage breakdown. It shows a live table of per-model
// token consumption and cost, with a session/project scope toggle.
type Panel struct {
	agg        *agent.UsageAggregator
	db         *db.DB
	projectID  int64
	scope      string // "session" or "project"
	projCache  *projectCache
}

type projectCache struct {
	totals     db.UsageTotals
	breakdowns []db.ModelBreakdown
}

var _ dock.Panel = (*Panel)(nil)

func NewPanel(agg *agent.UsageAggregator, database *db.DB, projectID int64, _ string) *Panel {
	return &Panel{agg: agg, db: database, projectID: projectID, scope: "session"}
}

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch k := msg.(type) {
	case tea.KeyPressMsg:
		switch k.String() {
		case "s":
			if p.scope == "session" {
				p.scope = "project"
				p.refreshProject()
			} else {
				p.scope = "session"
			}
		case "esc":
			// The dock host closes on Esc; nothing to do here.
		}
	}
	return nil
}

func (p *Panel) refreshProject() {
	if p.db == nil {
		return
	}
	totals, breakdowns, err := p.db.AggregateTurnMetrics(p.projectID)
	if err != nil {
		return
	}
	p.projCache = &projectCache{totals: totals, breakdowns: breakdowns}
}

func (p *Panel) View(width, maxHeight int) string {
	var totals db.UsageTotals
	var breakdowns []db.ModelBreakdown

	if p.scope == "project" && p.projCache != nil {
		totals = p.projCache.totals
		breakdowns = p.projCache.breakdowns
	} else {
		totals, breakdowns = p.agg.Snapshot()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Usage (%s)  [s] toggle scope\n\n", p.scope)
	fmt.Fprintf(&b, "  Input: %s  Output: %s  Reasoning: %s  Cache read: %s  Cache write: %s\n",
		strutil.CompactTokens(int(totals.PromptTokens)),
		strutil.CompactTokens(int(totals.CompletionTokens)),
		strutil.CompactTokens(int(totals.ReasoningTokens)),
		strutil.CompactTokens(int(totals.CacheReadTokens)),
		strutil.CompactTokens(int(totals.CacheWriteTokens)))
	fmt.Fprintf(&b, "  Estimated cost: $%s\n\n", formatCost(totals.EstimatedCostCents))

	fmt.Fprintf(&b, "  %-30s %8s %8s %8s %8s %8s %10s\n",
		"Model", "Input", "Output", "Reason", "CacheR", "CacheW", "Cost")
	for _, br := range breakdowns {
		fmt.Fprintf(&b, "  %-30s %8s %8s %8s %8s %8s %10s\n",
			br.Provider+"/"+br.Model,
			strutil.CompactTokens(int(br.PromptTokens)),
			strutil.CompactTokens(int(br.CompletionTokens)),
			renderCategory(br.ReasoningTokens, br.Reported, db.CatReasoning),
			renderCategory(br.CacheReadTokens, br.Reported, db.CatCacheRead),
			renderCategory(br.CacheWriteTokens, br.Reported, db.CatCacheWrite),
			"$"+formatCost(br.EstimatedCostCents),
		)
	}
	return b.String()
}

// renderCategory renders a token count, or "n/a" if the provider never
// reported that category (the Reported bitset is zero for DB-derived
// project-scope breakdowns, so those use the "sum is 0 → n/a" heuristic).
func renderCategory(tokens int64, reported db.TokenCategorySet, cat db.TokenCategorySet) string {
	if reported&cat != 0 {
		return strutil.CompactTokens(int(tokens))
	}
	// Not reported in this session. For project-scope (Reported == 0),
	// fall back to: if the summed total is 0, show n/a; otherwise show
	// the count (some turns must have reported it).
	if reported == 0 && tokens > 0 {
		return strutil.CompactTokens(int(tokens))
	}
	return "n/a"
}

func formatCost(cents int64) string {
	dollars := cents / 10000
	remainder := cents % 10000
	return fmt.Sprintf("%d.%04d", dollars, remainder)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/tui/usage/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/app/tui/usage/panel.go internal/app/tui/usage/panel_test.go
git add internal/app/tui/usage/panel.go internal/app/tui/usage/panel_test.go
git commit -m "feat(tui): add usage dock panel with per-model breakdown and n/a rendering"
```

---

### Task 11: Full-suite verification

- [ ] **Step 1: Run the complete test suite**

Run: `go test ./...`
Expected: PASS across all packages. If any test outside the touched packages breaks, investigate — likely a test that constructed `UsageObserver` or `TokenMeter.Observe` with the old signature. Search for `func(promptTokens, completionTokens int)` across the test suite and update each to `func(usage schema.TokenUsage)`.

- [ ] **Step 2: Run gofmt + vet**

Run: `gofmt -w . && go vet ./...`
Expected: clean.

- [ ] **Step 3: Commit any remaining fixes**

```bash
git add -A
git commit -m "test: fix remaining UsageObserver signature call sites after widening"
```

---

## Self-Review

**1. Spec coverage:**
- §2 Data model (`TokenUsage`/`TurnMetrics` widening) → Task 1 (TokenUsage), Task 5 (TurnMetrics + cost).
- §3 Provider wire parsing (OpenAI-compatible + Anthropic-ready) → Task 2 (wire types + tokenUsageFrom). Anthropic "schema-ready, adapter deferred" is covered by Task 1's struct + the explicit note in the spec.
- §4 Pricing table + cost computation → Task 3 (pricing package), Task 4 (config override), Task 5 (cost in emitMetrics).
- §5 Aggregation (UsageAggregator + DB) → Task 6 (DB schema + AggregateTurnMetrics), Task 7 (UsageAggregator), Task 8 (wiring).
- §6 Display (/usage command + dock panel + status segment) → Task 9 (command + status), Task 10 (dock panel).
- §7 File map → all listed files touched across Tasks 1-10.
- §8 Out of scope → respected (no live API fetch, no ACP method, no sparklines, no per-tool attribution, no auto-updates).

**2. Placeholder scan:** No TBD/TODO/"handle edge cases." The steps that say "inspect" (Task 8 Step 2 wiring the aggregator to the Runtime, Task 9 Step 5/6 accessing the DB handle on the Model) are intentional — they tell the implementer to verify the exact field/option names that vary by construction path, with the contract specified. Concrete code is provided for every implementation step. The import-cycle resolution (Task 6 Step 5-6) is explicitly resolved: `UsageTotals`/`ModelBreakdown`/`TokenCategorySet` live in `internal/db`, and `internal/agent` imports `internal/db` (already does), so no cycle.

**3. Type consistency:**
- `schema.TokenUsage` fields (`ReasoningTokens`/`CacheReadTokens`/`CacheWriteTokens`) — defined in Task 1, used in Task 2 (wire mapping), Task 5 (chat.go recording), Task 3 (EstimateCostCents input).
- `pricing.ModelPricing` — defined in Task 3, used in Task 4 (ModelPreset.Pricing), Task 5 (Runner.Pricing + emitMetrics).
- `pricing.Lookup(preset routing.ModelPreset) ModelPricing` — defined in Task 3, used in Task 8 (app.go seeding).
- `pricing.EstimateCostCents(u schema.TokenUsage, p ModelPricing) int64` — defined in Task 3, used in Task 5 (emitMetrics).
- `TurnMetrics` new fields (`ReasoningTokens`/`CacheReadTokens`/`CacheWriteTokens`/`EstimatedCostCents int64`) — defined in Task 5, used in Task 6 (DB row), Task 7 (aggregator Observe).
- `UsageObserver func(usage schema.TokenUsage)` — defined in Task 5, used in Task 5 (chat.go, app.go, swarm orchestrator).
- `TokenMeter.Observe(role AgentRole, usage schema.TokenUsage)` — defined in Task 5, used in Task 5 (orchestrator closures, observe fallback).
- `db.UsageTotals`/`db.ModelBreakdown`/`db.TokenCategorySet` — defined in Task 6, used in Task 6 (AggregateTurnMetrics return), Task 7 (aggregator), Task 10 (panel).
- `agent.UsageAggregator`/`NewUsageAggregator`/`Observe(m TurnMetrics)`/`Snapshot() (db.UsageTotals, []db.ModelBreakdown)` — defined in Task 7, used in Task 8 (app.go), Task 9 (status + command), Task 10 (panel).
- `usage.NewPanel(agg *agent.UsageAggregator, db *db.DB, projectID int64, _ string) *Panel` — defined in Task 10, used in Task 9 (openUsagePanel).
- `formatCost(cents int64) string` — defined in Task 9 (status.go) and Task 10 (panel.go). These are two local copies in different packages — acceptable (no shared dependency needed for a 2-line helper), but if the reviewer prefers DRY, the panel can import a shared `strutil.FormatCost`. The plan leaves them as local helpers for simplicity; both produce identical output (`"%d.%04d"`).

All names and signatures are consistent across tasks.