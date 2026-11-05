package acpwire

import (
	"encoding/json"
	"testing"
)

// TestProvidersJSON pins the bytes the provider catalog and a per-model probe
// carry: a template the connect wizard offers and the capabilities a probe
// reports. Each must encode exactly as the contract spells it.
func TestProvidersJSON(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name: "codex template",
			value: ProviderTemplate{
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
			want: `{"id":"openai-codex","label":"OpenAI (ChatGPT subscription)","type":"openai_codex","baseUrl":"https://chatgpt.com/backend-api","toolCalling":true,"structuredOutput":true,"auth":"oauth","keyHint":"Uses your ChatGPT subscription via browser login — no API key required.","models":["gpt-6-astra","gpt-5.6-sol","gpt-5.6-terra","gpt-5.6-luna"]}`,
		},
		{
			name: "local template",
			value: ProviderTemplate{
				ID:      "lmstudio",
				Label:   "LM Studio (local)",
				Type:    "openai_compatible",
				BaseURL: "http://localhost:1234/v1",
				Local:   true,
			},
			want: `{"id":"lmstudio","label":"LM Studio (local)","type":"openai_compatible","baseUrl":"http://localhost:1234/v1","local":true}`,
		},
		{
			name:  "capabilities the probe could not tell",
			value: ModelCapabilities{},
			want:  `{}`,
		},
		{
			name:  "capabilities with tool calling and a window",
			value: ModelCapabilities{ToolCalling: &yes, ContextWindow: 32768},
			want:  `{"toolCalling":true,"contextWindow":32768}`,
		},
		{
			name:  "capabilities with false flags",
			value: ModelCapabilities{ToolCalling: &no, Reasoning: &no},
			want:  `{"toolCalling":false,"reasoning":false}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if string(got) != tc.want {
				t.Errorf("%s JSON:\n got: %s\nwant: %s", tc.name, got, tc.want)
			}
		})
	}
}
