/**
 * Provider templates for the Add provider form. Mirrors the `templates` map
 * in internal/llm/provider/templates.go (id, label, type, base URL, local,
 * oauth): keep the two in sync when a template is added or changed.
 */
export interface ProviderTemplate {
  id: string
  label: string
  type: string
  baseUrl: string
  local?: boolean
  /** The template's default for the provider's tool_calling flag. */
  toolCalling?: boolean
  /** "oauth" templates sign in through the browser and take no API key. */
  auth?: 'oauth'
}

export const PROVIDER_TEMPLATES: ProviderTemplate[] = [
  { id: 'ollama', label: 'Ollama (local)', type: 'ollama', baseUrl: 'http://localhost:11434', local: true , toolCalling: true },
  { id: 'ollama-cloud', label: 'Ollama Cloud', type: 'ollama', baseUrl: 'https://ollama.com', toolCalling: true },
  { id: 'lmstudio', label: 'LM Studio (local)', type: 'openai_compatible', baseUrl: 'http://localhost:1234/v1', local: true },
  { id: 'openrouter', label: 'OpenRouter', type: 'openai_compatible', baseUrl: 'https://openrouter.ai/api/v1', toolCalling: true },
  { id: 'groq', label: 'Groq', type: 'openai_compatible', baseUrl: 'https://api.groq.com/openai/v1', toolCalling: true },
  { id: 'openai', label: 'OpenAI', type: 'openai_compatible', baseUrl: 'https://api.openai.com/v1', toolCalling: true },
  { id: 'opencode-go', label: 'OpenCode Zen (Go)', type: 'openai_compatible', baseUrl: 'https://opencode.ai/zen/go/v1', toolCalling: true },
  { id: 'openai-codex', label: 'OpenAI (ChatGPT subscription)', type: 'openai_codex', baseUrl: 'https://chatgpt.com/backend-api', auth: 'oauth', toolCalling: true },
  { id: 'openai_compatible', label: 'Custom (OpenAI-compatible)', type: 'openai_compatible', baseUrl: '' },
  { id: 'anthropic', label: 'Anthropic', type: 'anthropic', baseUrl: 'https://api.anthropic.com', toolCalling: true },
  { id: 'gemini', label: 'Google Gemini', type: 'openai_compatible', baseUrl: 'https://generativelanguage.googleapis.com/v1beta/openai', toolCalling: true },
  { id: 'deepseek', label: 'DeepSeek', type: 'openai_compatible', baseUrl: 'https://api.deepseek.com/v1', toolCalling: true },
  { id: 'kimi', label: 'Kimi (Moonshot AI)', type: 'openai_compatible', baseUrl: 'https://api.kimi.com/coding/v1', toolCalling: true },
  { id: 'mistral', label: 'Mistral', type: 'openai_compatible', baseUrl: 'https://api.mistral.ai/v1', toolCalling: true },
  { id: 'together', label: 'Together AI', type: 'openai_compatible', baseUrl: 'https://api.together.xyz/v1', toolCalling: true },
  { id: 'fireworks', label: 'Fireworks AI', type: 'openai_compatible', baseUrl: 'https://api.fireworks.ai/inference/v1', toolCalling: true },
  { id: 'xai', label: 'xAI', type: 'openai_compatible', baseUrl: 'https://api.x.ai/v1', toolCalling: true },
  { id: 'cerebras', label: 'Cerebras', type: 'openai_compatible', baseUrl: 'https://api.cerebras.ai/v1', toolCalling: true },
  { id: 'vllm', label: 'vLLM (local)', type: 'openai_compatible', baseUrl: 'http://localhost:8000/v1', local: true },
  { id: 'llamacpp', label: 'llama.cpp (local)', type: 'openai_compatible', baseUrl: 'http://localhost:8080/v1', local: true },
]

export const templateById = (id: string) => PROVIDER_TEMPLATES.find((t) => t.id === id)

/** A provider name not already taken: `groq`, then `groq-2`, `groq-3`. */
export function uniqueName(base: string, taken: Iterable<string>): string {
  const used = new Set(taken)
  if (!used.has(base)) return base
  for (let i = 2; ; i++) if (!used.has(`${base}-${i}`)) return `${base}-${i}`
}
