# 09. Configuration Examples

## Global config

Path idea:

```text
~/.config/marshal/config.toml
```

Example:

```toml
[tui]
theme = "dracula"
mode  = "light"        # or "dark" (default)
palette = { accent_primary = "#bd93f9", fg_default = "#f8f8f2" }

[profile]
default = "local_balanced"

[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "ollama"

[providers.lmstudio]
type = "openai_compatible"
base_url = "http://localhost:1234/v1"
api_key = "lm-studio"

[providers.openrouter]
type = "openai_compatible"
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"

[providers.groq]
type = "openai_compatible"
base_url = "https://api.groq.com/openai/v1"
api_key_env = "GROQ_API_KEY"
tool_calling = true  # provider advertises native OpenAI-compatible tool support
```

## Model presets

```toml
[models.presets.tiny]
provider = "ollama"
model = "qwen2.5-coder:1.5b"
context_window = 8192
temperature = 0.0
max_output_tokens = 1024
tool_calling = "json"        # unconstrained text decoding via Marshal's JSON action protocol
local_only = true

[models.presets.fast]
provider = "ollama"
model = "qwen2.5-coder:7b"
context_window = 32768
temperature = 0.1
max_output_tokens = 2048
tool_calling = "json"
local_only = true

[models.presets.coder]
provider = "ollama"
model = "qwen2.5-coder:14b"
context_window = 32768
temperature = 0.1
max_output_tokens = 4096
tool_calling = "native"
local_only = true

[models.presets.reasoning]
provider = "openrouter"
model = "anthropic/claude-sonnet-4"
context_window = 200000
temperature = 0.2
max_output_tokens = 8192
tool_calling = "native"
local_only = false
```

`tool_calling` controls how Marshal asks the model to return tools/actions:

- A provider advertises native tool-calling support by setting `tool_calling = true` in its `[providers.<name>]` block.
- `native` opts into provider-native OpenAI-compatible `tools[]` / `tool_calls[]` when the provider advertises tool-calling support.
- If `native` is selected but the provider does not support native tool calls, Marshal degrades through the first supported fallback:
  1. `json_schema` — strict JSON action envelope when the provider supports structured output.
  2. `json_object` — OpenAI-style `response_format={"type":"json_object"}` when the provider supports JSON mode.
  3. Unconstrained text — Marshal's text JSON action protocol.
- `json_schema` requests Marshal's strict JSON action envelope when structured output is supported; otherwise it degrades to `json_object` when JSON mode is supported; otherwise unconstrained text.
- `json` leaves decoding unconstrained and uses Marshal's text JSON action protocol.
- An empty value also leaves decoding unconstrained and uses Marshal's text JSON action protocol.

## Agent loop config

```toml
[agent]
provider = "ollama"
model = "qwen3-coder"
max_tool_iterations = 32
max_retries = 2
```

`max_tool_iterations` caps how many back-to-back tool calls the agent may make before it must produce a final answer (default 16). `max_retries` controls how many times a transient provider request is retried (default 2). Raise `max_tool_iterations` for complex local tasks if the model frequently runs out of iterations.

## Agent profile

```toml
[agent_profiles.local_balanced]
router = "tiny"
knowledge = "tiny"
summarizer = "tiny"
repo_scout = "fast"
tester = "fast"
planner = "coder"
implementer = "coder"
reviewer = "coder"
security_reviewer = "coder"

[agent_profiles.hybrid_saver]
router = "tiny"
knowledge = "tiny"
summarizer = "tiny"
repo_scout = "fast"
tester = "fast"
planner = "coder"
implementer = "coder"
reviewer = "reasoning"
security_reviewer = "reasoning"
```

## Project config

Path idea:

```text
.marshal/config.toml
```

Example:

```toml
[project]
name = "marshal"
languages = ["go", "markdown"]

[commands]
test = "go test ./..."
lint = "golangci-lint run"
format = "gofmt -w ."

[profile]
default = "local_balanced"

[privacy]
remote_providers_allowed = false
redact_secrets = true
include_gitignored_files = false

[indexing]
use_treesitter = true
use_embeddings = false
summarise_files = true
ignore = [
  "node_modules/**",
  "vendor/**",
  "dist/**",
  ".git/**"
]
```

## Role-specific context config

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
max_conversation_tokens = 4000
include_diff = true
include_tests = true
include_raw_code = true

[agents.router.context]
max_repo_context_tokens = 2000
include_raw_code = false
include_summaries = true
```

## Notes on parsed-but-reserved fields

Milestone L parses and stores the following fields, but the current static runtime does not yet apply all of them:

- In `[models.presets.<name>]`, `context_window`, `max_output_tokens`, `temperature`, `top_p`, and `reasoning_effort` are reserved for future milestones that will pass preset metadata to the provider factory and runner. `tool_calling` is used today to select the decoding mode.
- In `[agents.<role>.context]`, `max_conversation_tokens`, `include_raw_code`, `include_summaries`, `include_symbols`, `include_diff`, and `include_tests` are reserved for future context-pack filtering and conversation budget behavior.

`provider`, `model`, `local_only`, `tool_calling`, and `max_repo_context_tokens` affect runtime behavior.

## Routing escalation

Dynamic routing escalation is not implemented yet. The following keys are a
future configuration sketch and are currently ignored by Marshal.

```toml
[routing.rules]
allow_escalation = true
allow_remote_fallback = false
require_approval_for_remote = true

[[routing.escalation]]
role = "implementer"
if = "test_failed_twice"
from = "coder"
to = "reasoning"

[[routing.escalation]]
role = "planner"
if = "context_required_gt_32k"
from = "fast"
to = "coder"

[[routing.escalation]]
role = "reviewer"
if = "security_sensitive"
from = "fast"
to = "reasoning"
```

## Tool policy

```toml
[tools.shell]
default_timeout_seconds = 120
max_output_bytes = 200000
allow_network = false
allow_sudo = false
allow_destructive = false

[tools.shell.allow]
commands = [
  "go test",
  "go vet",
  "git status",
  "git diff",
  "rg",
  "ls",
  "cat"
]

[tools.shell.confirm]
commands = [
  "npm install",
  "go get",
  "docker compose up",
  "git checkout",
  "git stash"
]

[tools.shell.deny]
patterns = [
  "rm -rf /",
  "curl * | sh",
  "wget * | sh",
  "git reset --hard",
  "git clean -fd"
]
```

## Local resource config

```toml
[local_resources]
max_parallel_inference = 1
avoid_loading_multiple_large_models = true
unload_idle_models_after = "10m"
reserve_vram_mb = 2048
```

## Per-tool permission rules (F4 + per-MCP-tool)

`permission` may be a native tool name (e.g. `shell.run`) or a full MCP
tool name (e.g. `mcp.github.create_issue`). An MCP rule overrides the
default confirm fallback; deny wins over `[mcp.policies]` allow.

```toml
[[permissions.rules]]
permission = "shell.run"
pattern    = "git push"
action     = "allow"

[[permissions.rules]]
permission = "mcp.github.create_issue"
action     = "allow"

[[permissions.rules]]
permission = "mcp.github.delete_repo"
action     = "deny"

[[permissions.rules]]
permission = "mcp.filesystem.read"
action     = "confirm"
```

## Budget config

```toml
[budgets]
max_remote_cost_per_day_usd = 2.00
max_remote_cost_per_task_usd = 0.25
max_local_parallel_models = 2
max_context_tokens_per_task = 200000
prefer_local = true
```
