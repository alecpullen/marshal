# Phase 3 Implementation Summary: Agent Runtime

## Overview

Phase 3 implements the **Agent Runtime** system that transforms Marshal's single-task executor-critic loop into a swarm-capable agent architecture. This provides manifest-based configuration, read-set tracking with BLAKE3 checksums, and output schema enforcement.

## Files Created

### Core Agent Package (`internal/agent/`)

1. **`manifest.go`** - YAML role manifest parsing
   - `Manifest` struct with full configuration
   - ContextPolicy with inherit/exclude/max_tokens
   - Sub-agent spawning capabilities
   - LoadManifest() and LoadManifestForRole() functions

2. **`readset.go`** - Read-set tracking with BLAKE3
   - Tracks files read at which content hash
   - Staleness detection for concurrent modifications
   - Large file optimization (mtime+size heuristic >1MB)
   - `ReadSet`, `ReadRecord`, `StalenessError` types

3. **`agent.go`** - Core Agent types
   - `Agent` struct with identity, state, sub-agents
   - `TaskSpec` for task definition
   - `Round` for iteration tracking
   - Sub-agent spawning with `SpawnSubAgent()`
   - EventPublisher interface for event emission

4. **`runtime.go`** - Tool-call execution loop
   - `Runtime` struct executing agent iterations
   - `Run()` main loop with timeout handling
   - Schema validation with automatic retry
   - Critical vs retryable error handling
   - Tool execution with enforcement

5. **`result.go`** - Result types
   - `Result` with status, output, read-set, usage
   - `ResultStatus` enum (success, error, timeout, etc.)
   - `ValidateOutput()` with JSON schema
   - `IsRetryable()` and `IsCritical()` helpers

6. **`agent_test.go`** - Unit tests
   - Manifest loading and validation
   - ReadSet tracking and staleness detection
   - ToolError classification
   - Agent spawning capabilities
   - Result validation

### Context Assembly (`internal/agent/context/`)

7. **`assembler.go`** - Context building
   - `Assembler` with policy-based context construction
   - `Assemble()` with inheritance and exclusion
   - Token limit enforcement with truncation
   - Preparation for Phase 3.8 summarization

### Tool Registry (`internal/agent/tools/`)

8. **`tool.go`** - Tool interface
   - `Tool` interface for agent-facing tools
   - `Result` with content, data, errors
   - `ToolError` with Code/Message/Hint/Details
   - Critical vs retryable error classification

9. **`adapter.go`** - Enforcement wrapper
   - `Adapter` wrapping tools with enforcement
   - Read-before-edit validation
   - Staleness detection via BLAKE3 checksums
   - Context store integration for reads

## User Configuration

Role manifests are stored in `~/.config/marshal/roles/`:

### `codegen.yaml` (Primary Coding Agent)
```yaml
role: codegen
version: "1.0"
model_binding: codegen
system_prompt: |
  You are an expert software developer...
tools:
  - read_file
  - write_file
  - search_replace
  - run_command
  - ctx_fetch
  - ctx_list
  - ctx_search
  - finish
context_policy:
  inherit: [project_root, active_files_index]
  exclude: [full_conversation, task_graph]
  max_tokens: 8000
  summarize_if_over: 12000
output_schema: {...}
max_iterations: 3
timeout: 5m
requires_read_before_edit: true
can_spawn_agents: true
allowed_sub_roles: [knowledge, summariser]
max_concurrent_subs: 2
```

## Key Design Decisions

1. **BLAKE3 for Read-Set**: SHA256 for context store compatibility, BLAKE3 for agent read-set performance
2. **Tool Registry Abstraction**: Separate `internal/agent/tools/` layer over `internal/tools/`
3. **Schema Enforcement with Retry**: Strict validation, but agent can retry on failure
4. **Auto-Opt-In with Config Override**: Agent runtime auto-enabled when manifest exists, disable via config
5. **Critical vs Retryable Errors**: User sees critical errors, agent handles retryable ones
6. **Sub-Agent Support**: Built-in spawning with inheritance and limits

## Testing

```bash
# Run agent tests
go test ./internal/agent/... -v

# All tests pass
ok      github.com/alecpullen/marshal/internal/agent    0.651s
```

## Integration Points

### Bridge to Legacy Loop
The agent runtime can be invoked from `internal/loop/` via:
- `shouldUseAgent()` - checks for manifest existence
- `runAsAgent()` - executes via agent runtime
- Falls back to legacy loop on failure

### Configuration Options
```toml
[agent]
enabled = true              # Master switch
disable_for_roles = []      # Roles to exclude
fallback_to_legacy = true   # Use legacy on failure

[agent.safety]
read_before_edit = true
max_file_size_for_hash = 1048576  # 1MB threshold
```

## Next Steps (Phase 3.5)

1. Knowledge tier agent implementation
2. Three-layer retrieval system (ctx_fetch → ctx_search → query_knowledge)
3. Knowledge answer types with required citations
4. Query cache with LRU invalidation

## Compliance

✅ **Manifest-based configuration** - YAML roles in ~/.config/marshal/roles/
✅ **Read-set tracking** - BLAKE3 hashes with staleness detection
✅ **Read-before-edit enforcement** - Tool adapter validates
✅ **Output schema validation** - JSON Schema with retry
✅ **Sub-agent spawning** - Supported with inheritance
✅ **Auto-retry for recoverable errors** - Critical errors surface to user
✅ **Context assembly with policy** - Full inheritance/exclusion/limit support
✅ **Tool registry abstraction** - Clean interface over internal/tools/
