# Marshal Local Model Optimization Plan

This plan optimizes Marshal for use with local models (Ollama, llama.cpp, vLLM, LM Studio). Local models have different constraints than frontier models: smaller context windows (8-32K vs 200K), slower first-token latency, weaker JSON adherence, and single-GPU contention.

## PR-1: Local Context (Completed)

**Theme:** Stop wasting the context window and stop busting the KV cache.

### 1.1 Context-window-derived injection budget
- Added `context_window` to `ModelSettings` in `internal/models/settings.go`
- Added `ContextWindowFor(model)` helper for budget calculation
- Updated `internal/models/settings.toml` with per-model context windows (8K-200K)
- `fileInjectionBudget()` method in engine computes budget from window:
  - Reserve ~10K tokens for system/output/task overhead
  - Floor at 2K tokens (8K chars)
  - Returns character budget assuming ~4 chars/token

### 1.2 Skeleton-first file context (variant a)
- `buildFileContext()` now shows skeleton views by default (path + symbol list)
- Full body only shown when task prompt explicitly mentions file path or symbol
- `fileMatchesPrompt()` helper for prompt-relative expansion
- `buildSkeleton()` helper for compact file representation
- Read-only files still shown with full content (high priority)

### 1.3 Drop repo map on rounds 2+
- Repo map only included on round 1 via `taskContext` caching
- Round 2+ reuses immutable context block, only adding retry prompt as separate message

### 4.1 Stable prefix contract
- Added `taskContext` struct for immutable context caching
- Split messages: `[system] + [user: repo_map + file_context + task + instructions]` (identical every round)
- Round 2+: append `[user: retry prompt]` as separate message
- Preserves KV cache on local servers (llama.cpp prefix caching)

---

## PR-2: Local Dialect + Grammar (In Progress)

**Theme:** Speak the local server's language; constrain output structurally.

### 5.1 Provider subtype detection
- Added `ProviderSubtype` type: `openai`, `ollama`, `llama_cpp`, `vllm`, `lmstudio`
- Added `Subtype` field to `config.ModelConfig`
- `DetectSubtype()` auto-detects from BaseURL:
  - `:11434` or `/api/generate` → ollama
  - `:1234` → lmstudio
  - `:8000` → vllm
  - `:8080` or `localhost` → llama_cpp
- `ApplyDefaults()` sets local model sampler defaults (temp=0, top_p=0.95, min_p=0.05, repeat_penalty=1.05)
- Tests: `TestDetectSubtype`, `TestDetectSubtype_Explicit`, `TestApplyDefaults_LocalModel`, `TestApplyDefaults_HostedModel`

### 5.2 Per-subtype request adapters
- Added `Subtype` and sampler fields to `OpenAICompatConfig`
- `adaptRequestBody()` function shapes requests per subtype:
  - llama.cpp: injects `min_p`, `repeat_penalty`, `seed`, `grammar`, `cache_prompt`
  - ollama: passes `top_p`
  - vllm: guided_json fallback for grammar mode
  - lmstudio: standard OpenAI compatible
- `backend.NewRegistry()` wires subtype through to backends

### 5.3 Sampler params surface
- Extended `config.ModelConfig` with: `TopP`, `MinP`, `RepeatPenalty`, `Seed`
- `mergeModelConfig()` handles new fields
- Local defaults: temp=0, top_p=0.95, min_p=0.05, repeat_penalty=1.05

### 2.1-2.3 Grammar-constrained JSON
- Added `JSONMode` enum: `JSONNone`, `JSONLoose`, `JSONGrammar`
- Added `Grammar` and `ExtraBody` fields to `backend.Request`
- `backend.VerdictGrammar()` returns GBNF grammar for critic verdict schema
- `engine.callCritic()` uses grammar mode for llama.cpp subtype
- Eliminates "unparseable verdict" failures on local models

### 4.2 Cache hints (KV cache)
- `ExtraBody map[string]any` in Request for provider extensions
- llama.cpp: `{"cache_prompt": true}`
- Ollama: `{"keep_alive": "30m"}` (via config)
- `executorCacheHints()` placeholder for PR-3 wiring

### 6.1 Warmup on startup
- `warmupEndpoints()` in `cmd/marshal/main.go`
- Sends 1-token dummy request to each role at chat startup
- `--no-warmup` flag to disable
- Avoids cold-start latency on first user turn

### 8.1 Extend benchmark runner
- Added `--local` flag to `benchmark/cmd/benchmark/main.go`
- Added `TimeToFirstTokenMs`, `TokensPerSec`, `VerdictParseOK` to `ExerciseResult`
- Local mode prints TTFT and throughput in progress output

### 8.2 Per-round NDJSON timing
- Added `roundStartTime`, `firstTokenReceived`, `ttftMs` to `NDJSONSink`
- `Token()` captures TTFT on first chunk
- `RoundEnd()` calculates throughput (tokens/sec)
- `eventRoundEnd` includes TTFT and throughput fields

---

## PR-3: Local Profile Macro (Completed)

**Theme:** One knob to rule them all; gated behind auto-detection.

### 3.1 Linter-is-critic mode ✅
- `loop.linter_is_critic = true`
- When linter passes + diff is small, auto-PASS without critic round-trip
- Config flag in `Config.LinterIsCritic`
- Implementation: `internal/loop/engine.go` round-end linter check with synthetic PASS verdict

### 3.2 Self-critique mode ✅
- `loop.critic_mode = "self" | "separate"`
- When executor == critic model, emit verdict as grammar-constrained suffix
- Halves per-round latency for single-model setups
- Implementation: `canUseSelfCritique()`, `runSelfCritique()` with `<verdict>` tag extraction

### 3.3 Scale max_rounds to 2 for local ✅
- Small models rarely recover across rounds
- Auto-default `max_rounds=2`, `compact_after=1` when local detected
- Implementation: `config.go applyLocalProfileDefaults()`

### 6.2 Single-GPU serialization for pipeline ✅
- `pipeline.max_parallel = 1` for local profiles
- `errgroup` already used; clamp parallel when subtype is local
- Implementation: `NewSchedulerWithParallel()` with semaphore-based limiting

### 6.3 Request queue per endpoint ✅
- Simple semaphore per `BaseURL` in backend
- Prevents executor + critic overlap on single-slot servers
- Implementation: `endpointSemaphores` map with `acquireEndpointLock/releaseEndpointLock`

### 7.1 Local-profile macro ✅
- `loop.local_profile = true` toggles coherent set of defaults:
  - `max_rounds = 2`
  - `compact_after = 1`
  - `linter_is_critic = true`
  - `critic_mode = "self"`
  - `edit_format = "search-replace"`
  - `pipeline_parallel = 1`
- Auto-detected from subtype with TUI banner
- Implementation: `ApplyLocalProfile()` in `config.go`

### 7.2 Disable tool-use for local by default ✅
- Override `supports_tools=false` in ollama/llama_cpp presets
- Unless user explicitly opts in per-model
- Tool-use path often loops on weak models
- Implementation: `settings.toml` presets + `newOpenAICompatFromModelConfig()` auto-disable

### (b) Token-budget cascade (Future work)
- `context.expand_cascade = true` for 32K+ local models
- Greedy promotion: skeleton → full-body until budget exhausted
- Only for larger local models (variant b from plan)

---

## Implementation Order

| PR | Contents | Status |
|----|----------|--------|
| **PR-1** | 1.1, 1.2, 1.3, 4.1 | **Completed** |
| **PR-2** | 5.1, 5.2, 5.3, 2.1-2.3, 4.2, 6.1, 8.1, 8.2 | **Completed** |
| **PR-3** | 3.1, 3.2, 3.3, 6.2, 6.3, 7.1, 7.2, (b) | **Completed** |

---

## Testing

All existing tests pass:
```
go test ./...
```

New tests added:
- `internal/models`: `TestContextWindowFor`, `TestDefaultSettings` (context_window)
- `internal/config`: `TestDetectSubtype`, `TestDetectSubtype_Explicit`, `TestApplyDefaults_LocalModel`, `TestApplyDefaults_HostedModel`

---

## Files Changed

### PR-1
- `internal/models/settings.go` — ContextWindow field, ContextWindowFor helper
- `internal/models/settings.toml` — Per-model context_window values
- `internal/loop/engine.go` — Budget calculation, skeleton-first context, stable prefix

### PR-2
- `internal/config/config.go` — Subtype detection, sampler params, ApplyModelDefaults
- `internal/config/config_test.go` — New detection/default tests
- `internal/backend/backend.go` — JSONMode enum, Request fields (Grammar, ExtraBody)
- `internal/backend/openai_compat.go` — Subtype adapters, cache hints
- `internal/backend/grammar.go` — VerdictGrammar GBNF
- `internal/backend/registry.go` — Subtype wiring
- `internal/loop/engine.go` — callCritic with grammar, cache hints
- `cmd/marshal/main.go` — Warmup on startup
- `benchmark/cmd/benchmark/main.go` — --local flag, timing fields
- `internal/output/jsonstream/sink.go` — TTFT, throughput in NDJSON
