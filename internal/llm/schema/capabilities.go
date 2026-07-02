package schema

type ProviderCapabilities struct {
	Streaming        bool
	ToolCalling      bool
	JSONMode         bool
	StructuredOutput bool
	Embeddings       bool
	Vision           bool
	ReasoningTokens  bool
	ContextWindow    int
	MaxOutputTokens  int
}
