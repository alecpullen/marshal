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
	KeyEnv      string
	KeyHint     string
	Models      []string
}

var templates = map[string]ProviderTemplate{
	"ollama": {
		ID:      "ollama",
		Label:   "Ollama (local)",
		Type:    "ollama",
		BaseURL: "http://localhost:11434",
		Local:   true,
		Models:  []string{"qwen2.5-coder:7b", "qwen2.5-coder:14b", "qwen2.5:7b", "llama3.1:8b"},
	},
	"ollama-cloud": {
		ID:      "ollama-cloud",
		Label:   "Ollama Cloud",
		Type:    "ollama",
		BaseURL: "https://ollama.com",
		KeyEnv:  "OLLAMA_API_KEY",
		KeyHint: "Get a key at https://ollama.com/settings/keys",
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
