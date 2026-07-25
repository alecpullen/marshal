# Local-Friendly Provider Catalog Design

Date: 2026-07-25
Status: Draft — pending implementation plan

## Problem

Marshal today is described as **local-first**:

- `config.Default()` ships with no `[providers.*]` entries and no presets.
- `privacy.remote_providers_allowed` defaults to `false`.
- The built-in provider template catalog (`internal/llm/provider/templates.go`) only has six templates: Ollama, LM Studio, OpenRouter, Groq, OpenAI, and a generic OpenAI-compatible slot.
- The built-in model context catalog (`internal/llm/catalog/catalog.go`) only covers nine local Ollama-style models.

This makes Marshal hard to use with common hosted providers even when a developer *wants* to use them. The product framing should shift to **local-friendly**: it still works great fully offline/local, but it also ships a rich, curated catalog of local and remote providers and no longer treats remote providers as disallowed by default. First-run onboarding should explicitly ask the user to choose their default setup, making the choice transparent.

## Goals

1. Expand Marshal's built-in provider template catalog to cover the most common local and hosted OpenAI-compatible endpoints.
2. Expand the built-in model metadata catalog to cover well-known local and hosted chat/embedding models.
3. Change the default value of `privacy.remote_providers_allowed` from `false` to `true`.
4. Keep `config.Default()` free of concrete provider entries and API keys, preserving privacy-by-design, but make the first-run onboarding wizard write a complete `[providers.*] + [models.presets.*] + [agent_profiles.*]` block once the user picks a setup.
5. Leave an extension point so that the remote-catalog work on `main` can later augment or replace the static catalogs without restructuring the code.
6. Keep `web.enabled` unchanged (still `false` by default). Remote providers are a separate opt-in dimension from web fetch/search.
7. Update documentation and CLAUDE.md to reflect the local-friendly framing.

## Non-goals

- Implementing a runtime network fetch for provider/model metadata on this branch. The remote catalog on `main` is out of scope here; this design leaves a clean seam for it.
- Adding non-OpenAI-compatible provider types (e.g., native Anthropic SDK, Gemini SDK, Azure SDK). All new templates use the existing `openai_compatible` provider type.
- Changing shell/web/desktop policy defaults.
- Shipping API keys or pre-configured paid accounts.

## Design

### 1. Catalog structure

#### 1.1 Provider templates

`internal/llm/provider/templates.go` already defines:

```go
type ProviderTemplate struct {
    ID          string
    Label       string
    Type        string
    BaseURL     string
    Local       bool
    ToolCalling bool
    KeyEnv      string
    KeyHint     string
    Models      []string
}
```

We keep this type and the `templates` map. We expand the map to cover the following categories:

**Local / self-hosted**

| ID | Label | BaseURL | KeyEnv | Recommended | ToolCalling | Models (suggested defaults) |
|---|---|---|---|---|---|---|---|
| `ollama` | Ollama | `http://localhost:11434/v1` | — | true | false | `qwen2.5-coder:7b`, `qwen2.5-coder:14b`, `llama3.1:8b`, `deepseek-coder-v2:16b` |
| `lmstudio` | LM Studio | `http://localhost:1234/v1` | — | false | false | — |
| `llamacpp` | llama.cpp server | `http://localhost:8080/v1` | — | false | false | — |
| `vllm` | vLLM | `http://localhost:8000/v1` | — | false | true | — |
| `tabbyapi` | TabbyAPI | `http://localhost:5000/v1` | — | false | false | — |
| `koboldcpp` | koboldcpp | `http://localhost:5001/v1` | — | false | false | — |

**Hosted OpenAI-compatible**

| ID | Label | BaseURL | KeyEnv | Recommended | ToolCalling | Models (suggested defaults) |
|---|---|---|---|---|---|---|---|
| `openai` | OpenAI | `https://api.openai.com/v1` | `OPENAI_API_KEY` | true | true | `gpt-4o`, `gpt-4o-mini`, `o3-mini` |
| `anthropic` | Anthropic | `https://api.anthropic.com/v1` | `ANTHROPIC_API_KEY` | true | true | `claude-sonnet-4-20250514`, `claude-opus-4-20250514`, `claude-haiku-4-20250514` |
| `google` | Google Gemini (OpenAI-compatible) | `https://generativelanguage.googleapis.com/v1beta/openai` | `GEMINI_API_KEY` | true | true | `gemini-2.5-pro`, `gemini-2.5-flash` |
| `groq` | Groq | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | true | true | `llama-3.3-70b-versatile`, `qwen-2.5-32b`, `deepseek-r1-distill-llama-70b` |
| `openrouter` | OpenRouter | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | true | true | `anthropic/claude-sonnet-4`, `google/gemini-2.5-pro`, `meta-llama/llama-3.3-70b-instruct` |
| `together` | Together AI | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` | true | false | `meta-llama/Llama-3.3-70B-Instruct-Turbo`, `Qwen/Qwen2.5-Coder-32B-Instruct` |
| `fireworks` | Fireworks AI | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | true | false | `accounts/fireworks/models/llama-v3p3-70b-instruct` |
| `deepseek` | DeepSeek | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | true | true | `deepseek-chat`, `deepseek-reasoner` |
| `perplexity` | Perplexity | `https://api.perplexity.ai` | `PERPLEXITY_API_KEY` | true | false | `sonar`, `sonar-pro`, `sonar-reasoning` |
| `mistral` | Mistral AI | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | true | false | `mistral-large-latest`, `codestral-latest` |
| `cohere` | Cohere | `https://api.cohere.ai/v1` | `COHERE_API_KEY` | true | false | `command-r-plus`, `command-r` |
| `azure_openai` | Azure OpenAI | `https://{your-resource}.openai.azure.com/openai/deployments/{deployment-id}` | `AZURE_OPENAI_API_KEY` | true | false | — |
| `xai` | xAI | `https://api.x.ai/v1` | `XAI_API_KEY` | true | false | `grok-3`, `grok-3-mini` |

**Generic**

| ID | Label | BaseURL | KeyEnv | Recommended | ToolCalling | Models |
|---|---|---|---|---|---|---|
| `openai_compatible` | Custom (OpenAI-compatible) | — | — | false | false | — |

Total: ~20 built-in templates.

Notes:

- Anthropic, Google, Mistral, Cohere, and xAI expose OpenAI-compatible endpoints; we use those so no new provider type is required. The `Type` field remains `"openai_compatible"`.
- Azure OpenAI keeps its URL as a placeholder with clear `{your-resource}`/`{deployment-id}` tokens; the user must edit it. Tool calling is marked true because Azure OpenAI supports it for supported deployments.
- The generic `openai_compatible` template stays for arbitrary endpoints.

#### 1.2 Model context catalog

`internal/llm/catalog/catalog.go` already has a static map keyed by lowercased model id. We expand it to cover the models referenced by the new provider templates plus a few common embedding models.

For each model we store:

- `contextWindow`: maximum context window in tokens.
- `maxOutput`: maximum output tokens.

When a preset does not specify `context_window` or `max_output_tokens`, the router will look up the model id in this catalog and apply the values. If the model is unknown, it leaves the fields at zero, preserving the existing "keep configured budget" behavior.

Representative entries to add:

- `gpt-4o`, `gpt-4o-mini`, `o3-mini`
- `claude-sonnet-4-20250514`, `claude-opus-4-20250514`, `claude-haiku-4-20250514`
- `gemini-2.5-pro`, `gemini-2.5-flash`
- `llama-3.3-70b-versatile`, `llama-3.1-8b`, `llama-3.1-70b`
- `deepseek-chat`, `deepseek-reasoner`, `deepseek-coder-v2:16b`
- `qwen-2.5-32b`, `qwen2.5-coder:7b`, `qwen2.5-coder:14b`, `qwen2.5-coder:32b`
- `mistral-large-latest`, `codestral-latest`, `codestral:22b`, `mistral:7b`
- `command-r-plus`, `command-r`
- `grok-3`, `grok-3-mini`
- `text-embedding-3-small`, `text-embedding-3-large`, `text-embedding-004` (for embedding presets)
- OpenRouter/Together/Fireworks prefixed ids where we have stable defaults.

Where a model id is ambiguous or provider-specific, we add the provider-prefixed variant too, e.g. `anthropic/claude-sonnet-4-20250514` for OpenRouter.

Values must be conservative and sourced from public documentation. Every entry gets a one-line comment citing the source or noting that it is an estimate.

### 2. Privacy default change

`internal/app/config/defaults.go` changes:

```go
Privacy: PrivacyConfig{
    RemoteProvidersAllowed: true,  // was false
    RedactSecrets:          true,
    IncludeGitignoredFiles: false,
},
```

This flips the semantics from opt-in to opt-out. The privacy gate itself (`StaticRouter.resolvePresetBinding` and `legacyRoute`) stays intact; a user can still set `remote_providers_allowed = false` to block remote providers. Error messages and UI copy are updated from "blocked" to "disabled by privacy setting" where appropriate.

### 3. First-run onboarding

The existing `internal/app/onboarding.go` already runs when no project config exists. It currently offers three providers and writes a minimal `onboarded` profile.

We change onboarding to:

1. Present a richer provider picker using `provider.All()` rather than the hardcoded `[]string{"Ollama (Local)", "OpenRouter", "OpenAI"}`. The picker should group providers by `Local` vs `Remote` badges.
2. Still ask for the project name first.
3. Use the existing `connect.Model` wizard for provider/model setup. The connect wizard already uses `provider.All()`; this step mostly requires removing the hardcoded provider list in `NewOnboardingModel` and wiring the connect result directly.
4. On completion, write a complete config block:
   - `[project]` name
   - `[profile] default = "onboarded"`
   - `[providers.<name>]` with `type = "openai_compatible"`, `base_url`, `api_key_env` (or inline key into global config), `tool_calling = true/false`
   - `[models.presets.onboarded_preset]` referencing the provider and model, with `local_only = true` for local templates and `local_only = false` for hosted templates
   - `[agent_profiles.onboarded]` mapping every `routing.AllRoles` entry to `onboarded_preset`
   - `[agent] max_tool_iterations = 32`
   - `[privacy] remote_providers_allowed = true` written explicitly when a remote template is selected, so the project config is self-describing.

If the user cancels onboarding, behavior remains unchanged: Marshal uses the default config (which now has remote providers allowed but no configured providers, so the agent loop stays disabled until configured).

### 4. Connect wizard UX updates

`internal/app/tui/connect/connect.go` already has a `stepRemoteGate` that blocks remote providers when `RemoteProvidersAllowed` is false. With the new default, that gate will rarely fire on first run, but it remains important when a user later flips the setting off. We keep it.

One small change: the connect wizard's provider picker currently shows `local`/`remote` badges. We should also surface the `KeyHint` as a detail line so users know where to get a key before they pick a hosted provider.

### 5. Remote catalog extension seam

The remote-catalog work on `main` will eventually want to fetch a manifest of provider templates and model metadata. We define the seam now so that work does not need to restructure files:

- `internal/llm/provider/templates.go` keeps the static `templates` map as the **fallback catalog**.
- A future `internal/llm/catalog/remote.go` (name TBD by the other branch) can implement:
  ```go
  func LoadRemote(ctx context.Context, url string, ttl time.Duration) (ProviderCatalog, ModelCatalog, error)
  ```
  and merge its results over the static maps.
- Until that lands, nothing in this spec references remote loading.

To make merging safe, we keep `provider.Lookup` and `provider.All` deterministic and side-effect free, and we avoid storing mutable state in the `ProviderTemplate` type.

### 6. Config defaults remain provider-free

`config.Default()` continues to leave these fields empty:

- `Providers: map[string]ProviderConfig{}` (not nil, but empty)
- `Models.Presets: map[string]routing.ModelPreset{}`
- `AgentProfiles: map[string]routing.AgentProfile{}`
- `Agent.Provider` and `Agent.Model` blank

This preserves the invariant that a fresh Marshal install does not assume any provider or model. The only difference is that the privacy gate is open by default and the catalog is large enough that the wizard can configure most common providers in a few keystrokes.

### 7. Pricing table

`internal/llm/pricing/prices.go` already holds a small static pricing table. We add entries for the new hosted models where reliable public per-token pricing exists. Unknown models continue to report zero cost, which the telemetry layer already handles.

### 8. Documentation updates

- `CLAUDE.md`: change "local-first" to "local-friendly" and update the bullet "default config has `remote_providers_allowed = false`" to "default config has `remote_providers_allowed = true` but no built-in providers".
- `docs/09-configuration-examples.md`: update the example to show `remote_providers_allowed = true` as default, and add examples for Anthropic, Google, Groq, DeepSeek, OpenRouter.
- `docs/04-tooling-and-shell-safety.md`: if it mentions the remote-provider policy, update the default value only.
- `docs/03-provider-and-model-routing.md` (if it exists): note the expanded catalog and the new default.

### 9. Testing strategy

- Unit tests for `provider.All()` and `provider.Lookup()` covering all new templates.
- Unit tests for `catalog.Lookup()` covering all new model ids.
- Update `config_test.go` expectations for `RemoteProvidersAllowed` to `true`.
- Update `router_test.go` cases that assumed the default blocked remote providers.
- Update `onboarding_test.go` to assert the richer provider list and the new profile output.
- Add a test that verifies the generic `openai_compatible` custom template still works.
- Add a test that `ResolveRole` allows a remote preset by default and still blocks it when `RemoteAllowed` is explicitly false.

## Resolved decisions

1. **Recommended flag:** Yes. `ProviderTemplate` gains a `Recommended bool` field. The connect/provider picker surfaces recommended providers first, then the full catalog, so the first screen is short but everything remains searchable.
2. **Embedding default in onboarding:** No. Onboarding writes only a chat preset. Embedding is a separate feature and remains off by default (`use_embeddings = false`).
3. **Reasoning output behavior:** No change to the `entry` struct. Reasoning-budget tracking is handled in the provider/telemetry layer by a separate plan.

## Open questions

None.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Increasing binary size with a large static map | Go maps of small structs are cheap; provider templates with only strings are negligible. Model catalog is a few hundred entries at most. |
| Catalog becomes stale | Document that values are conservative defaults and overridable via `[models.presets.<id>]`. Leave the remote-catalog seam for future updates. |
| Default `remote_providers_allowed = true` surprises users who expected local-first | First-run onboarding explicitly asks the user to pick a setup, and the wizard writes the chosen config. No provider is pre-selected silently. |
| Hosted endpoints' OpenAI-compatible paths drift | Keep BaseURL values as public documented endpoints and note that provider-specific SDKs are out of scope. |
| Tests asserting old default break | Update all affected tests as part of the implementation plan. |

## Success criteria

- `provider.All()` returns at least 20 templates.
- `catalog.Lookup("gpt-4o")` returns non-zero context/max-output values.
- `config.Default().Privacy.RemoteProvidersAllowed == true`.
- A fresh install with no config runs onboarding and can configure Ollama, OpenAI, Anthropic, Google, Groq, OpenRouter, DeepSeek, or others without hand-editing TOML.
- Setting `privacy.remote_providers_allowed = false` still blocks remote presets.
- All existing tests pass after updates.
