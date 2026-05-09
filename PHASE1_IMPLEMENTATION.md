# Phase 1 Implementation: Gateway Refactor

## Summary

Phase 1 transforms Marshal's backend into a multi-provider model gateway supporting Anthropic, OpenAI, Ollama, LM Studio, vLLM, Fireworks, and OpenRouter. This enables the swarm architecture with role-based routing, budget tracking, and provider auto-detection.

## Key Features Implemented

### 1. Unified Gateway Interface (`internal/gateway/`)

**Canonical Types**: Created unified types that work across all providers:
- `ContentBlock` - Normalized content representation (text, tool_use, tool_result, thinking)
- `Message` - Unified message format with role and content blocks
- `ChatRequest` - Common request structure
- `StreamEvent` - Normalized streaming events (delta, tool_call, thinking, done, error)
- `ToolDef` - Universal tool definition

**Key Design**: Content blocks use Anthropic's format internally (array of typed blocks) for maximum expressiveness while adapters handle conversion to provider-native formats.

### 2. Provider Adapters

**Anthropic Adapter** (`anthropic/adapter.go`):
- Native Messages API v1 support
- Beta extended thinking feature (for Claude 3.5 Sonnet with reasoning)
- Thinking budget configuration
- Redacted thinking support
- Tool use with streaming JSON accumulation
- Content block normalization

**OpenAI-Compatible Adapter** (`openai/adapter.go`):
- Supports OpenAI, OpenRouter, Ollama, LM Studio, vLLM, Fireworks, RunPod
- Provider-specific dialect handling
- Tool call streaming with argument accumulation
- Configurable capabilities per provider

### 3. Binding System (`binding.go`)

**Binding Structure**:
```go
type Binding struct {
    Provider Provider   // "anthropic", "openai", "ollama", etc.
    Model    string     // "claude-opus-4-7", "gpt-4", "qwen2.5-coder:14b"
    Endpoint string     // Optional override
    APIKey   string     // Auth (resolved from AuthRef)
    LoRAs    []string   // v1: max 1 LoRA
    RoleHint RoleHint   // "small", "code", "large", "fast"
    Priority int        // Auto-selection priority
    CostPer1KInput/Output float64 // For budgeting
}
```

**Auto-Priority (per your spec)**:
- Anthropic: 100 (highest)
- OpenAI: 80
- OpenRouter: 70
- Fireworks: 60
- RunPod: 55
- vLLM/Ollama/LMStudio: 50 (lowest)

### 4. Router (`router.go`)

**Features**:
- Role-to-binding resolution
- Auto-resolve based on detected providers (when enabled)
- Fallback support (primary fails → fallback binding)
- Budget checking before resolution
- Provider health tracking (placeholder for future)

**Auto-Resolution Strategy**:
1. Check explicit bindings first
2. If auto-resolve enabled and no binding, select highest priority available provider
3. For cloud providers, verify API key is available
4. Select appropriate model based on role (e.g., orchestrator → Claude Opus with thinking)

### 5. Budget Tracker (`budget.go`)

**Budget Types** (per your spec):
- **Per-session budget**: Total across all roles
- **Per-role budget**: Individual role limits
- **Daily budget**: Global daily cap

**Features**:
- Cost estimation before requests
- Budget exhaustion warnings at 50%, 80%, 95%
- Callbacks for warnings and exhaustion
- Daily reset at midnight UTC
- Tracks actual usage after completion

**Default Costs**:
- Anthropic: Opus ($15/1K input, $75/1K output), Sonnet ($3/$15), Haiku ($0.25/$1.25)
- OpenAI: GPT-4 ($30/$60), GPT-4-mini ($0.15/$0.60)
- Local: $0 (compute only)

### 6. Provider Detection (`detect/detector.go`)

**Detection Methods**:
- **Cloud**: Environment variables (ANTHROPIC_API_KEY, OPENAI_API_KEY, etc.)
- **Local**: HTTP probes on standard ports:
  - Ollama: localhost:11434
  - LM Studio: localhost:1234
  - vLLM: localhost:8000

**Parallel Probing**: All providers probed concurrently with 200ms timeout

**Profile Recommendations**:
- Anthropic + Local → "balanced"
- Anthropic only → "quality"
- Local only → "local-only"
- OpenAI + Local → "balanced"
- OpenAI only → "budget"

## Architecture

```
┌─────────────────┐
│   Application   │
│   (cmd/marshal) │
└────────┬────────┘
         │
┌────────▼────────┐
│     Router      │  Role → Binding resolution
│   (router.go)   │  Budget checking
└────────┬────────┘
         │
┌────────▼────────┐
│    Gateway      │  Unified interface
│  (gateway.go)   │  Canonical types
└────────┬────────┘
         │
    ┌────┴────┐
    │         │
┌───▼───┐  ┌──▼────┐
│Anthropic│  │ OpenAI │  Provider adapters
│ Adapter │  │ Adapter│  (normalization)
└────┬────┘  └───┬───┘
     │           │
┌────▼────┐  ┌───▼────┐
│ Anthropic│  │ OpenAI │  External APIs
│   API    │  │  API   │
└─────────┘  └────────┘
```

## Files Created

```
internal/gateway/
├── gateway.go              # Core types and Gateway interface
├── binding.go              # Binding struct and resolution
├── router.go               # Role-based routing
├── budget.go               # Per-session/role budget tracking
├── provider.go             # ProviderAdapter interface + normalization helpers
├── errors.go               # Gateway errors
├── anthropic/
│   └── adapter.go          # Anthropic Messages API with thinking
├── openai/
│   └── adapter.go          # OpenAI-compatible adapter
└── detect/
    └── detector.go         # Provider auto-detection
```

## Usage Example

```go
// Create budget tracker with per-role limits
budget := gateway.NewBudgetTracker(
    gateway.WithSessionBudget(10.0),  // $10 per session
    gateway.WithDailyBudget(50.0),    // $50 per day
    gateway.WithRoleBudget("orchestrator", 5.0),
    gateway.WithRoleBudget("codegen", 3.0),
)

// Create router with auto-resolution
router := gateway.NewRouter(budget, gateway.WithAutoResolve(true))

// Detect available providers
detector := detect.NewDetector()
providers := detector.Probe(ctx)
router.SetAvailableProviders(providers)

// Resolve binding for a role
resolved, err := router.Resolve(ctx, "orchestrator", 4000)
if err != nil {
    if err == gateway.ErrBudgetExceeded {
        // Try fallback
    }
}

// Create adapter based on binding
var adapter gateway.ProviderAdapter
switch resolved.Binding.Provider {
case gateway.ProviderAnthropic:
    adapter = anthropic.NewAdapter(
        resolved.Binding.APIKey,
        resolved.Binding.Model,
        anthropic.WithThinking(2048, false), // Enable for orchestrator
    )
case gateway.ProviderOpenAI:
    adapter = openai.NewAdapter(
        resolved.Binding.APIKey,
        resolved.Binding.Model,
    )
}

// Stream completion
events, err := adapter.Complete(ctx, gateway.ChatRequest{
    Messages: []gateway.Message{
        {Role: gateway.RoleUser, Content: []gateway.ContentBlock{
            {Type: gateway.ContentBlockTypeText, Text: "Hello"},
        }},
    },
    EnableThinking: true, // For reasoning
})

// Process events
for event := range events {
    switch event.Kind {
    case gateway.StreamEventDelta:
        fmt.Print(event.Text)
    case gateway.StreamEventThinking:
        // Claude's reasoning process
        fmt.Println("[Thinking]", event.Thinking.Thinking)
    case gateway.StreamEventToolCall:
        // Handle tool call
    case gateway.StreamEventDone:
        // Complete
    }
}

// Record actual usage
router.RecordUsage(ctx, "orchestrator", resolved.Binding, usage)
```

## Configuration

Example TOML configuration:
```toml
[gateway]
session_budget = 10.0
daily_budget = 50.0

[gateway.bindings]
orchestrator = { provider = "anthropic", model = "claude-opus-4-7", auth_ref = "env:ANTHROPIC_API_KEY", role_hint = "large" }
codegen = { provider = "anthropic", model = "claude-sonnet-4-7", role_hint = "code" }
critic = { provider = "anthropic", model = "claude-opus-4-7" }
compactor = { provider = "anthropic", model = "claude-haiku-4-5", role_hint = "small" }

# Fallback bindings (for unrecoverable errors)
[gateway.fallback]
orchestrator = { provider = "openai", model = "gpt-4o" }
codegen = { provider = "ollama", model = "qwen2.5-coder:14b" }

# Per-role budgets
[gateway.role_budgets]
orchestrator = 5.0
codegen = 3.0
critic = 2.0
```

## Next Steps (Phase 1.5)

1. **Integration**: Connect gateway to existing Marshal code
2. **CLI commands**: `marshal doctor` to show detection results
3. **Profile management**: `marshal profile use <name>`
4. **Local catalog**: Curated model recommendations for local providers
5. **Config migration**: Bridge existing Marshal config to gateway bindings

## Testing

The gateway is designed to be tested with:
- Mock ProviderAdapter implementations
- Recorded SSE fixtures for each provider
- Budget simulation
- Router resolution testing

No tests are included in this phase - they will be added in Phase 1.5 alongside integration.

## Compliance with Requirements

✅ **Anthropic beta API with thinking**: Implemented in `anthropic/adapter.go`
✅ **Auto-priority (Anthropic > OpenAI > Local)**: Implemented in `DefaultPriority()` and `autoResolveBinding()`
✅ **Per-session and per-role budgets**: Implemented in `budget.go`
✅ **Fallback only on unrecoverable errors**: `IsUnrecoverable()` and fallback logic in `router.go`
✅ **Tool usage normalization**: Tool definitions convert to both OpenAI and Anthropic formats
