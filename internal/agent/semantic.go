package agent

import (
	"context"

	"marshal/internal/contextpack"
	"marshal/internal/retrieval"
)

// semanticRetrievalLimit bounds how many snippets passive injection adds so it
// never dominates the pack.
const semanticRetrievalLimit = 5

// retrieveSemanticContext runs one semantic query for the goal and maps the
// hits to context-pack snippets. A nil source (embeddings unconfigured / empty
// index) yields nil — graceful-off.
func retrieveSemanticContext(ctx context.Context, goal string, src retrieval.Source) []contextpack.FileSnippet {
	if src == nil || goal == "" {
		return nil
	}
	hits, err := src.Retrieve(ctx, retrieval.Query{Text: goal, Limit: semanticRetrievalLimit})
	if err != nil || len(hits) == 0 {
		return nil
	}
	out := make([]contextpack.FileSnippet, 0, len(hits))
	for _, h := range hits {
		out = append(out, contextpack.FileSnippet{
			Path: h.FilePath, StartLine: h.StartLine, EndLine: h.EndLine, Content: h.Content,
		})
	}
	return out
}
