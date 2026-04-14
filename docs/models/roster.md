# Marshal Model Roster

Marshal uses a **four-role multi-model architecture**. Each role can be assigned to a different model provider, allowing you to optimize for cost, speed, and capability.

## The Four Roles

| Role | Purpose | Typical Model Size | API Calls per Task |
|------|---------|-------------------|-------------------|
| **Marshal** | Task decomposition and clarification | 7-14B | 1 per task |
| **Executor** | Code generation and edits | 30B+ or frontier API | 1-3 per round |
| **Critic** | Code review and verdict | 14-30B | 1 per round |
| **Compactor** | History summarization | 7-14B | As needed |

## Role Details

### Marshal (Gate Model)
- **Purpose**: Classifies user prompts as "proceed", "chat", or "clarify"
- **When to use**: Every user input goes through the Marshal first
- **Requirements**: Can be a smaller model; needs to understand natural language well
- **Typical models**: Qwen2.5-7B, Llama 3.1-8B, GPT-4o-mini

### Executor (Code Generation)
- **Purpose**: Generates code changes via edit formats or tool use
- **When to use**: Every round (1-3 rounds per task typically)
- **Requirements**: Strong coding capability; largest/fanciest model you can afford
- **Typical models**: 
  - API: Claude Sonnet 4.6, GPT-4o, Kimi K2.5 Turbo
  - Local: Qwen3-Coder-30B, DeepSeek-Coder-33B

### Critic (Code Review)
- **Purpose**: Reviews changes and outputs JSON verdict
- **When to use**: After every executor round
- **Requirements**: Good at evaluation; can be smaller than executor
- **Typical models**: Qwen2.5-14B, Llama 3.1-70B, Claude Haiku

### Compactor (History Management)
- **Purpose**: Summarizes failure history after consecutive FAILs
- **When to use**: Only when `compact_after` threshold is reached
- **Requirements**: Can be same as Marshal; occasional calls
- **Typical models**: Share with Marshal role

## Model Capabilities Matrix

| Model | Supports Tools | Edit Formats | Context | Speed | Cost |
|-------|---------------|--------------|---------|-------|------|
| **Claude Sonnet 4.6** | ✅ | Search-replace, whole | 200K | Fast | $$ |
| **GPT-4o** | ✅ | All formats | 128K | Fast | $$ |
| **Kimi K2.5 Turbo** | ✅ | All formats | 256K | Fast | $ |
| **Qwen3-Coder-30B** | ✅ | All formats | 128K | Slow | Free* |
| **Qwen2.5-14B** | ❌ | All formats | 32K | Medium | Free* |
| **Llama 3.1-8B** | ❌ | All formats | 128K | Fast | Free* |

*Free if running locally via LM Studio/Ollama

## Configuration Examples

### High-Quality (All API)
```toml
[model.executor]
provider = "openai-compat"
model = "claude-sonnet-4-6"
api_key = "${ANTHROPIC_API_KEY}"
base_url = "https://api.anthropic.com/v1"
supports_tools = true

[model.critic]
provider = "openai-compat"
model = "claude-haiku"
api_key = "${ANTHROPIC_API_KEY}"
base_url = "https://api.anthropic.com/v1"
```

### Cost-Optimized (Hybrid)
```toml
[model.executor]
provider = "openai-compat"
model = "accounts/fireworks/routers/kimi-k2p5-turbo"
api_key = "${FIREWORKS_API_KEY}"
base_url = "https://api.fireworks.ai/inference/v1"
supports_tools = true

[model.critic]
provider = "lmstudio"
model = "qwen2.5-14b-instruct"
base_url = "http://localhost:1234/v1"
```

### Fully Local
```toml
[model.executor]
provider = "lmstudio"
model = "qwen3-coder-30b-a3b-instruct"
base_url = "http://localhost:1234/v1"
supports_tools = true

[model.critic]
provider = "lmstudio"
model = "qwen2.5-14b-instruct"
base_url = "http://localhost:1234/v1"
```

## Tool Support

Models with `supports_tools = true` can use:
- `read_file` - Read files with pagination
- `write_file` - Write files with path validation  
- `run_command` - Execute shell commands

Models without tool support fall back to edit formats (search/replace, udiff, whole-file).

## Performance Tips

1. **Executor is the bottleneck**: Use your best model here
2. **Critic can be smaller**: 14B models work well for review
3. **Marshal/Compactor are cheap**: Can share a 7B local model
4. **Tool use reduces tokens**: For capable models, tools are more efficient than edit formats

## See Also

- [Configuration Reference](../config/reference.md) - All config options
- [Skill Authoring](../skills/authoring.md) - Customizing prompts per role
