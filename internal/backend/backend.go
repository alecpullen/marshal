package backend

import "context"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
	CacheHit         bool
	CachedTokens     int
}

type Backend interface {
	Complete(ctx context.Context, model string, messages []Message) (Response, error)
	Name() string
}
