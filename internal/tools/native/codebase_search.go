package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/llm/embedding"
	"marshal/internal/llm/routing"
	"marshal/internal/retrieval"
	"marshal/internal/tools/registry"
)

type codebaseSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
	Path  string `json:"path"`
}

func (t *toolSet) codebaseSearchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "codebase_search",
		Description: "Semantic search over the indexed codebase. Returns the most relevant code/doc snippets for a natural-language query.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"},"path":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[codebaseSearchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		var embedder embedding.Embedder
		if t.resolveEmbedder != nil {
			embedder, err = t.resolveEmbedder()
		} else {
			err = routing.ErrEmbeddingNotConfigured
		}
		if errors.Is(err, routing.ErrEmbeddingNotConfigured) || embedder == nil {
			return registry.ToolResult{Summary: "semantic search unavailable",
				Content: "Semantic search is not configured: no embedding model configured. Set an `embedding` role in your profile to enable it."}, nil
		}
		if err != nil {
			return registry.ToolResult{}, err
		}
		if count, _, _ := t.db.ChunkGeneration(t.projectID); count == 0 {
			return registry.ToolResult{Summary: "no semantic index",
				Content: "No semantic index yet — run `repo.index` to build it."}, nil
		}
		src := retrieval.NewSemanticSource(t.db, embedder, t.projectID)
		hits, err := src.Retrieve(ctx, retrieval.Query{Text: args.Query, Limit: args.Limit, PathPrefix: args.Path})
		if err != nil {
			return registry.ToolResult{}, err
		}
		if len(hits) == 0 {
			return registry.ToolResult{Summary: "no matches", Content: "No semantic matches for that query."}, nil
		}
		var b strings.Builder
		for _, h := range hits {
			fmt.Fprintf(&b, "`%s:%d-%d` (score %.2f)\n%s\n\n", h.FilePath, h.StartLine, h.EndLine, h.Score, h.Content)
		}
		return registry.ToolResult{Summary: fmt.Sprintf("%d matches", len(hits)), Content: strings.TrimSpace(b.String())}, nil
	}
	return tool
}
