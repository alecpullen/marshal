# Phase 1.5 Implementation: Gateway Integration

## Summary

Phase 1.5 integrates the new gateway system with Marshal's existing codebase, adds CLI commands for doctor and profile management, and creates comprehensive tests.

## Completed Work

### 1. Gateway Integration Layer (`internal/gateway/integration/`)

**File**: `registry.go`

Created `GatewayRegistry` that wraps the gateway system and implements the existing `backend.Registry` interface. This allows the rest of Marshal to use the gateway without code changes.

**Key Features**:
- Converts existing `config.ModelConfig` to gateway `Binding` structures
- Auto-detects providers on startup
- Bridges `backend.Request` ↔ `gateway.ChatRequest`
- Bridges `gateway.StreamEvent` ↔ `backend.Chunk`
- Maintains backward compatibility with existing code

**Usage**:
```go
// Old way (still works)
reg, _ := backend.NewRegistry(cfg, nil, modelReg)
b, _ := reg.For(config.RoleExecutor)

// New way with gateway
reg, _ := integration.NewGatewayRegistry(cfg, modelReg)
b, _ := reg.For(config.RoleExecutor) // Returns gateway-backed backend
```

### 2. CLI Commands

#### `marshal doctor`

**File**: `cmd/marshal/doctor.go`

Diagnoses Marshal setup and displays:
- Detected providers (cloud and local)
- Recommended profile based on available providers
- Current configuration status
- Active bindings
- Next steps for setup

**Example Output**:
```
🔍 Marshal Doctor
==================================================

📡 Detecting providers...
   ✅ anthropic
   ✅ ollama (3 models)

🎯 Recommended profile: balanced
   Run: marshal profile use balanced

⚙️ Configuration
   ✅ Executor: claude-sonnet-4-7
   ⚠️  No critic model configured

🔗 Gateway Bindings
   executor → anthropic/claude-sonnet-4-7
   codegen → anthropic/claude-sonnet-4-7

==================================================
✅ Marshal is ready to use!
   Run 'marshal chat' to start an interactive session
```

#### `marshal profile`

**File**: `cmd/marshal/doctor.go` (profile subcommands)

Manages configuration profiles:
- `marshal profile list` - Show available profiles
- `marshal profile use <name>` - Activate a profile
- `marshal profile show` - Display current profile

**Available Profiles**:
- **local-only**: All inference local (Ollama)
- **balanced**: Cloud orchestration + local execution
- **quality**: Frontier models for everything (Anthropic)
- **budget**: Cheapest viable mix (GPT-4-mini + local)

### 3. Profile Configuration

Profile definitions (hardcoded in `doctor.go`):

| Profile | Orchestrator | Codegen | Critic | Compactor |
|---------|--------------|---------|--------|-----------|
| local-only | qwen2.5:7b | qwen2.5-coder:14b | deepseek-r1:32b | qwen2.5:7b |
| balanced | Claude Opus | qwen2.5-coder | Claude Opus | qwen2.5:7b |
| quality | Claude Opus | Claude Sonnet | Claude Opus | Claude Haiku |
| budget | GPT-4-mini | qwen2.5-coder | GPT-4-mini | qwen2.5:7b |

### 4. Comprehensive Tests

**Test Coverage**:
- `binding_test.go` (90 lines): Provider, Binding, RoleHint, ResolvedBinding tests
- `budget_test.go` (156 lines): Budget tracking, warnings, role budgets, resets
- `router_test.go` (169 lines): Registration, resolution, auto-resolve, fallback
- `detector_test.go` (222 lines): Provider detection, cloud/local detection, formatting

**Test Results**:
```
ok  internal/gateway         0.612s
ok  internal/gateway/detect  1.064s
```

All 30+ test cases pass.

## Files Changed/Created

```
cmd/marshal/
├── main.go          (add doctor and profile commands)
└── doctor.go        (new - doctor and profile commands)

internal/gateway/
├── integration/
│   └── registry.go  (new - bridge to existing backend)
├── binding_test.go  (new)
├── budget_test.go   (new)
└── router_test.go   (new)

internal/gateway/detect/
└── detector_test.go (new)
```

## Integration Architecture

```
┌─────────────────┐
│   CLI Commands  │
│  doctor/profile │
└────────┬────────┘
         │
┌────────▼────────┐
│  GatewayRegistry│  Bridges old → new
│ (integration/)  │  Implements backend.Registry
└────────┬────────┘
         │
┌────────▼────────┐
│     Router      │  Role → Binding
│  Budget Tracker │  Per-session/role budgets
└────────┬────────┘
         │
┌────────▼────────┐
│    Providers    │  Anthropic, OpenAI, Ollama...
│   (Adapters)    │  With normalization
└─────────────────┘
```

## Backward Compatibility

✅ **Fully backward compatible** - Existing Marshal code continues to work:
- `backend.NewRegistry()` still works
- All existing CLI commands unchanged
- Config file format unchanged
- Can gradually migrate to gateway

## Next Steps

Phase 2 will add:
1. **Context Store** - SQLite + FTS5 for searchable content
2. **Context Ref Integration** - Content-addressed references in tools
3. **Knowledge Tier** - Three-layer retrieval system

## Compliance with Requirements

✅ **Anthropic beta with thinking**: Fully supported via `EnableThinking` flag  
✅ **Auto-priority**: Anthropic(100) > OpenAI(80) > Local(50)  
✅ **Per-session/role budgets**: BudgetTracker with `CheckRole()`  
✅ **Fallback on unrecoverable**: `IsUnrecoverable()` + fallback logic  
✅ **Tool normalization**: `ToolDef.ToOpenAITool()` + `ToAnthropicTool()`  
✅ **Integration layer**: `GatewayRegistry` implements `backend.Registry`  
✅ **CLI commands**: `doctor` and `profile` commands added  
✅ **Comprehensive tests**: 30+ test cases, all passing  

## Summary

Phase 1.5 successfully integrates the gateway system with Marshal's existing codebase while maintaining full backward compatibility. The new `marshal doctor` and `marshal profile` commands provide an easy onboarding experience, and comprehensive tests ensure reliability.
