# Configuration Reference

Marshal is configured via TOML files. Configuration is loaded from (in order of precedence):

1. Command-line flags (highest priority)
2. Environment variables
3. `./marshal.toml` (current directory)
4. `~/.config/marshal/config.toml` (user config)

## Example Configuration

```toml
[model.executor]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/models/llama-v3p1-70b-instruct"
supports_tools = true
edit_format = "search_replace"

[model.critic]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/models/llama-v3p1-8b-instruct"

[model.marshal]
provider = "lmstudio"
base_url = "http://localhost:1234/v1"
api_key = "lm-studio"
model = "qwen3-coder-30b"

[loop]
max_rounds = 3
compact_after = 2

[git]
enabled = true
```

## Model Configuration

Each role (executor, critic, marshal, compactor) has its own `[model.{role}]` section:

| Field | Type | Description | Default |
|-------|------|-------------|---------|
| `provider` | string | Backend type: `openai-compat`, `anthropic`, `google` | required |
| `base_url` | string | API endpoint URL | required for openai-compat |
| `api_key` | string | Authentication token (supports `${ENV_VAR}` syntax) | required |
| `model` | string | Model identifier | required |
| `supports_tools` | bool | Whether model supports function calling | false |
| `edit_format` | string | Output format: `search_replace`, `udiff`, `wholefile` | `search_replace` |
| `temperature` | float | Sampling temperature (0-2) | 0.0 |
| `max_tokens` | int | Maximum tokens per response | model-dependent |

## Loop Configuration

```toml
[loop]
max_rounds = 3        # Maximum retry rounds per task
compact_after = 2     # Call compactor after N consecutive failures
clarify = "ambiguous" # Marshal clarification mode
```

## Git Configuration

```toml
[git]
enabled = true        # Enable git integration
auto_ship = false     # Auto-merge on exit
```

## Profiles

Define alternate configurations under `[profiles.{name}]`:

```toml
[profiles.local.model.executor]
provider = "lmstudio"
base_url = "http://localhost:1234/v1"
model = "qwen3-coder-30b"

[profiles.local.model.critic]
provider = "lmstudio"
base_url = "http://localhost:1234/v1"
model = "qwen3-coder-30b"
```

Use with: `marshal chat --profile local`

## Environment Variables

Any config field supports `${VAR}` syntax for environment variable expansion:

```toml
api_key = "${FIREWORKS_API_KEY}"
```

Model-specific variables are also supported:
- `MARSHAL_EXECUTOR_API_KEY`
- `MARSHAL_CRITIC_API_KEY`
- `MARSHAL_MARSHAL_API_KEY`
