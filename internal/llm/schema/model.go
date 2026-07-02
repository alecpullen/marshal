package schema

// ModelInfo describes a model as reported by a provider's model-listing
// endpoint. Most OpenAI-compatible /v1/models endpoints (Ollama, LM Studio)
// return only id/owned_by — context window and output limits are not
// reliably available here, so they are omitted rather than guessed.
type ModelInfo struct {
	ID      string
	OwnedBy string
}
