package provider

import (
	"fmt"
	"sort"
)

type ProviderTemplate struct {
	ID          string
	Label       string
	Type        string
	BaseURL     string
	Local       bool
	ToolCalling bool
	// StructuredOutput defaults the provider's structured_output flag
	// (format / response_format enforcement). True only for templates
	// known to enforce format: ollama.com cloud silently ignores format
	// constraints, so its template leaves this off.
	StructuredOutput bool
	// TemperatureLocked defaults the provider's temperature_locked flag for
	// endpoints that only accept their own fixed sampling temperature
	// (e.g. Kimi's coding endpoint accepts only temperature = 1.0).
	TemperatureLocked bool
	// Auth is the authentication mode this template implies: "" (api key)
	// or "oauth". An OAuth template has no KeyEnv — the connect flow walks
	// the browser login instead of prompting for a key.
	Auth    string
	KeyEnv  string
	KeyHint string
	Models  []string
}

var templates = map[string]ProviderTemplate{
	"ollama": {
		ID:               "ollama",
		Label:            "Ollama (local)",
		Type:             "ollama",
		BaseURL:          "http://localhost:11434",
		Local:            true,
		ToolCalling:      true,
		StructuredOutput: true,
		Models:           []string{"qwen2.5-coder:7b", "qwen2.5-coder:14b", "qwen2.5:7b", "llama3.1:8b"},
	},
	"ollama-cloud": {
		ID:          "ollama-cloud",
		Label:       "Ollama Cloud",
		Type:        "ollama",
		BaseURL:     "https://ollama.com",
		ToolCalling: true,
		KeyEnv:      "OLLAMA_API_KEY",
		KeyHint:     "Get a key at https://ollama.com/settings/keys",
	},
	"lmstudio": {
		ID:      "lmstudio",
		Label:   "LM Studio (local)",
		Type:    "openai_compatible",
		BaseURL: "http://localhost:1234/v1",
		Local:   true,
	},
	"openrouter": {
		ID:          "openrouter",
		Label:       "OpenRouter",
		Type:        "openai_compatible",
		BaseURL:     "https://openrouter.ai/api/v1",
		ToolCalling: true,
		KeyEnv:      "OPENROUTER_API_KEY",
		KeyHint:     "Get a key at https://openrouter.ai/keys",
		Models:      []string{"anthropic/claude-sonnet-4", "google/gemini-2.5-pro", "meta-llama/llama-3.3-70b-instruct"},
	},
	"groq": {
		ID:          "groq",
		Label:       "Groq",
		Type:        "openai_compatible",
		BaseURL:     "https://api.groq.com/openai/v1",
		ToolCalling: true,
		KeyEnv:      "GROQ_API_KEY",
		KeyHint:     "Get a key at https://console.groq.com/keys",
	},
	"openai": {
		ID:          "openai",
		Label:       "OpenAI",
		Type:        "openai_compatible",
		BaseURL:     "https://api.openai.com/v1",
		ToolCalling: true,
		KeyEnv:      "OPENAI_API_KEY",
		KeyHint:     "Get a key at https://platform.openai.com/api-keys",
		Models:      []string{"gpt-4o", "gpt-4o-mini", "o3-mini"},
	},
	"opencode-go": {
		ID:                "opencode-go",
		Label:             "OpenCode Zen (Go)",
		Type:              "openai_compatible",
		BaseURL:           "https://opencode.ai/zen/go/v1",
		ToolCalling:       true,
		TemperatureLocked: true,
		KeyEnv:            "OPENCODE_API_KEY",
		KeyHint:           "Get a key from OpenCode Zen (https://opencode.ai/zen)",
		Models:            []string{"gpt-5.6-luna", "deepseek-v4-pro", "deepseek-v4-flash"},
	},
	// openai-codex is the ChatGPT-subscription backend. It is the only
	// template with Auth = "oauth": there is no API key, and the connect
	// flow drives a browser login instead. The model list is the
	// visibility == "list" subset of the live catalog captured in
	// docs/codex-spike-findings-2026-09-14.md §4; gpt-5.5 is deliberately
	// excluded (retires 2026-10-14). This list is only the static fallback
	// — Models() prefers the live catalog, then the disk model cache.
	//
	// Reasoning is not a template field: the factory hardcodes the codex
	// capability set (tool calling, structured output, reasoning, streaming)
	// from the spike's verified results, the same way the anthropic case
	// does.
	//
	// Instructions: marshal's system prompt rides as the Responses API
	// `instructions` field, exactly as the openai_compatible backend sends
	// it. The endpoint does NOT inject a persona of its own and does not
	// override caller instructions — the spike's A/B test measured
	// ENFORCED=false, INJECTION_WORKS=true (findings §7). The slim-preamble
	// fallback the design originally budgeted for is therefore unnecessary.
	"openai-codex": {
		ID:               "openai-codex",
		Label:            "OpenAI (ChatGPT subscription)",
		Type:             "openai_codex",
		BaseURL:          "https://chatgpt.com/backend-api",
		Auth:             "oauth",
		ToolCalling:      true,
		StructuredOutput: true,
		KeyHint:          "Uses your ChatGPT subscription via browser login — no API key required.",
		Models:           []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"},
	},
	"openai_compatible": {
		ID:      "openai_compatible",
		Label:   "Custom (OpenAI-compatible)",
		Type:    "openai_compatible",
		BaseURL: "",
		Local:   false,
	},
	"anthropic": {
		ID:          "anthropic",
		Label:       "Anthropic",
		Type:        "anthropic",
		BaseURL:     "https://api.anthropic.com",
		ToolCalling: true,
		KeyEnv:      "ANTHROPIC_API_KEY",
		KeyHint:     "Get a key at https://console.anthropic.com/settings/keys",
		Models:      []string{"claude-sonnet-4-5", "claude-opus-4-1", "claude-haiku-4-5"},
	},
	"gemini": {
		ID:          "gemini",
		Label:       "Google Gemini",
		Type:        "openai_compatible",
		BaseURL:     "https://generativelanguage.googleapis.com/v1beta/openai",
		ToolCalling: true,
		KeyEnv:      "GEMINI_API_KEY",
		KeyHint:     "Get a key at https://aistudio.google.com/apikey",
		Models:      []string{"gemini-2.5-pro", "gemini-2.5-flash"},
	},
	"deepseek": {
		ID:          "deepseek",
		Label:       "DeepSeek",
		Type:        "openai_compatible",
		BaseURL:     "https://api.deepseek.com/v1",
		ToolCalling: true,
		KeyEnv:      "DEEPSEEK_API_KEY",
		KeyHint:     "Get a key at https://platform.deepseek.com/api_keys",
		Models:      []string{"deepseek-chat", "deepseek-reasoner"},
	},
	"kimi": {
		ID:                "kimi",
		Label:             "Kimi (Moonshot AI)",
		Type:              "openai_compatible",
		BaseURL:           "https://api.kimi.com/coding/v1",
		ToolCalling:       true,
		TemperatureLocked: true,
		KeyEnv:            "KIMI_API_KEY",
		KeyHint:           "Get a key at https://platform.moonshot.ai/console/api-keys",
	},
	"mistral": {
		ID:          "mistral",
		Label:       "Mistral",
		Type:        "openai_compatible",
		BaseURL:     "https://api.mistral.ai/v1",
		ToolCalling: true,
		KeyEnv:      "MISTRAL_API_KEY",
		KeyHint:     "Get a key at https://console.mistral.ai/api-keys",
		Models:      []string{"mistral-large-latest", "codestral-latest"},
	},
	"together": {
		ID:          "together",
		Label:       "Together AI",
		Type:        "openai_compatible",
		BaseURL:     "https://api.together.xyz/v1",
		ToolCalling: true,
		KeyEnv:      "TOGETHER_API_KEY",
		KeyHint:     "Get a key at https://api.together.ai/settings/api-keys",
	},
	"fireworks": {
		ID:          "fireworks",
		Label:       "Fireworks AI",
		Type:        "openai_compatible",
		BaseURL:     "https://api.fireworks.ai/inference/v1",
		ToolCalling: true,
		KeyEnv:      "FIREWORKS_API_KEY",
		KeyHint:     "Get a key at https://fireworks.ai/account/api-keys",
	},
	"xai": {
		ID:          "xai",
		Label:       "xAI",
		Type:        "openai_compatible",
		BaseURL:     "https://api.x.ai/v1",
		ToolCalling: true,
		KeyEnv:      "XAI_API_KEY",
		KeyHint:     "Get a key at https://console.x.ai",
		Models:      []string{"grok-4", "grok-3-mini"},
	},
	"cerebras": {
		ID:          "cerebras",
		Label:       "Cerebras",
		Type:        "openai_compatible",
		BaseURL:     "https://api.cerebras.ai/v1",
		ToolCalling: true,
		KeyEnv:      "CEREBRAS_API_KEY",
		KeyHint:     "Get a key at https://cloud.cerebras.ai",
	},
	"vllm": {
		ID:      "vllm",
		Label:   "vLLM (local)",
		Type:    "openai_compatible",
		BaseURL: "http://localhost:8000/v1",
		Local:   true,
	},
	"llamacpp": {
		ID:      "llamacpp",
		Label:   "llama.cpp (local)",
		Type:    "openai_compatible",
		BaseURL: "http://localhost:8080/v1",
		Local:   true,
	},
}

func Lookup(id string) (ProviderTemplate, bool) {
	tpl, ok := templates[id]
	return tpl, ok
}

// All returns every provider template in a deterministic, ID-sorted order.
// Templates are stored in a map, so without sorting the returned slice's
// order would vary between calls (map iteration is randomized), which would
// make the provider picker and template listings non-deterministic.
func All() []ProviderTemplate {
	out := make([]ProviderTemplate, 0, len(templates))
	for _, tpl := range templates {
		out = append(out, tpl)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func UniqueName(base string, existing map[string]bool) string {
	if !existing[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !existing[candidate] {
			return candidate
		}
	}
}
