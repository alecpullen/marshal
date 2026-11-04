package acpwire

// ProviderTemplate is one entry of the provider catalog the connect wizard
// offers (C10). Auth is "" for an API key or "oauth" for a browser sign-in.
type ProviderTemplate struct {
	ID                string   `json:"id"`
	Label             string   `json:"label"`
	Type              string   `json:"type"`
	BaseURL           string   `json:"baseUrl,omitempty"`
	Local             bool     `json:"local,omitempty"`
	ToolCalling       bool     `json:"toolCalling,omitempty"`
	StructuredOutput  bool     `json:"structuredOutput,omitempty"`
	TemperatureLocked bool     `json:"temperatureLocked,omitempty"`
	Auth              string   `json:"auth,omitempty"`
	KeyEnv            string   `json:"keyEnv,omitempty"`
	KeyHint           string   `json:"keyHint,omitempty"`
	Models            []string `json:"models,omitempty"`
}

// ModelCapabilities is what a per-model probe reports beyond the provider's
// model list. A nil flag or a zero window means the probe could not tell.
type ModelCapabilities struct {
	ToolCalling   *bool `json:"toolCalling,omitempty"`
	ContextWindow int   `json:"contextWindow,omitempty"`
	Reasoning     *bool `json:"reasoning,omitempty"`
}
