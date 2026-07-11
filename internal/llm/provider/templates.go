package provider

import "fmt"

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
		Type:    "openai_compatible",
		BaseURL: "http://localhost:11434/v1",
		Local:   true,
		Models:  []string{"qwen2.5-coder:7b", "qwen2.5-coder:14b", "qwen2.5:7b", "llama3.1:8b"},
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
}

func Lookup(id string) (ProviderTemplate, bool) {
	tpl, ok := templates[id]
	return tpl, ok
}

func All() []ProviderTemplate {
	out := make([]ProviderTemplate, 0, len(templates))
	for _, tpl := range templates {
		out = append(out, tpl)
	}
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
