# 03. Provider and Model Routing

## Goal

Marshal should let users choose different models for different agent roles. This allows users to optimise for speed, memory usage, cost, context size, and reasoning quality.

This is especially important for local inference, where one machine may not be able to run multiple large models at once.

## Provider abstraction

Use an OpenAI-compatible internal request shape where possible, but do not assume every provider supports every feature.

```go
type Provider interface {
    Name() string
    Models(ctx context.Context) ([]ModelInfo, error)
    Chat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
    Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
    Capabilities(ctx context.Context) ProviderCapabilities
}
```

## Provider capabilities

```go
type ProviderCapabilities struct {
    Streaming        bool
    ToolCalling      bool
    JSONMode         bool
    StructuredOutput bool
    Embeddings       bool
    Vision           bool
    ReasoningTokens  bool
    ContextWindow    int
    MaxOutputTokens  int
}
```

## Supported provider types

Initial provider targets:

- Ollama
- LM Studio
- vLLM
- llama.cpp-compatible OpenAI servers
- OpenRouter
- generic OpenAI-compatible endpoint

Later:

- native OpenAI
- native Anthropic
- native Gemini
- local dedicated embedding providers

## Model presets

A model preset describes how a model should be used.

```go
type ModelPreset struct {
    Name            string
    Provider        string
    Model           string
    ContextWindow   int
    MaxOutputTokens int
    Temperature     float64
    TopP            float64
    ToolCalling     string // native, json, disabled
    ReasoningEffort string // none, low, medium, high
    LocalOnly       bool
}
```

## Agent profiles

An agent profile maps roles to model presets.

```toml
[agents.planner]
model_preset = "reasoning"

[agents.implementer]
model_preset = "coder"

[agents.reviewer]
model_preset = "reasoning"

[agents.repo_scout]
model_preset = "fast"

[agents.knowledge]
model_preset = "tiny"

[agents.summarizer]
model_preset = "tiny"
```

## Built-in profile ideas

### Local Minimal

For laptop users or no-GPU users.

```text
Router:       tiny local
Knowledge:    tiny local
Repo Scout:   tiny local
Tester:       tiny local
Planner:      small local
Implementer:  small local
Reviewer:     small local
```

### Local Balanced

Default target profile.

```text
Router:       tiny
Knowledge:    small
Repo Scout:   small
Tester:       small
Planner:      medium coder
Implementer:  medium coder
Reviewer:     medium coder
```

### Local Heavy

For users with strong GPUs or high RAM.

```text
Router:       small
Knowledge:    small
Repo Scout:   medium
Tester:       small
Planner:      large coder/reasoning
Implementer:  large coder
Reviewer:     large reasoning/coder
```

### Hybrid Saver

Local by default, remote escalation only when needed.

```text
Router:       tiny local
Knowledge:    small local
Repo Scout:   small local
Tester:       small local
Planner:      medium local, remote fallback
Implementer:  medium local
Reviewer:     remote only for high-risk tasks
```

### Remote Max

For best possible results.

```text
Planner:      frontier remote
Implementer:  strong coder remote
Reviewer:     frontier remote
Knowledge:    cheap local or cheap remote
Repo Scout:   local
```

## Model router

The model router resolves an agent role and task profile to a model preset.

```go
type TaskProfile struct {
    Complexity              string // trivial, normal, hard
    Risk                    string // low, medium, high
    RequiresEditing         bool
    RequiresLargeContext    bool
    RequiresSecurityReview  bool
    UserRequestedLocalOnly  bool
    PreviousFailures        int
}

type ModelRouter interface {
    Resolve(role AgentRole, task TaskProfile) (ModelPreset, error)
}
```

## Escalation rules

Role routing should support automatic escalation, but only within user-defined policy.

Examples:

```toml
[routing.rules]
allow_escalation = true
allow_remote_fallback = false

[[routing.escalation]]
role = "implementer"
if = "test_failed_twice"
from = "coder"
to = "reasoning"

[[routing.escalation]]
role = "reviewer"
if = "security_sensitive"
from = "fast"
to = "reasoning"
```

## Context allocation per role

Different roles should receive different context packs.

```toml
[agents.knowledge.context]
max_repo_context_tokens = 12000
max_conversation_tokens = 1000
include_raw_code = false
include_summaries = true
include_symbols = true

[agents.implementer.context]
max_repo_context_tokens = 48000
max_conversation_tokens = 8000
include_raw_code = true
include_summaries = true
include_symbols = true

[agents.reviewer.context]
max_repo_context_tokens = 64000
include_diff = true
include_tests = true
include_raw_code = true

[agents.router.context]
max_repo_context_tokens = 2000
include_raw_code = false
include_summaries = true
```

## Local resource controls

```toml
[local_resources]
max_parallel_inference = 1
avoid_loading_multiple_large_models = true
unload_idle_models_after = "10m"
reserve_vram_mb = 2048
```

## Budget controls

```toml
[budgets]
max_remote_cost_per_day_usd = 2.00
max_remote_cost_per_task_usd = 0.25
max_local_parallel_models = 2
max_context_tokens_per_task = 200000
prefer_local = true
```

## TUI transparency

The TUI should always show:

- active agent role
- selected model
- provider
- local or remote status
- context budget
- escalation reason, if applicable
- whether remote data transmission is about to happen

Example:

```text
Reviewer · qwen2.5-coder:14b · local · 32k ctx
```

Remote escalation prompt:

```text
Escalating reviewer:
  from qwen2.5-coder:14b
  to claude-sonnet-4

Reason:
  Patch touches shell sandbox and command approval policy.

Approve remote call?
[y] yes  [n] no  [l] local only
```
