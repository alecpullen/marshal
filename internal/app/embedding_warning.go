package app

import (
	"errors"
	"fmt"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

// embeddingStartupWarning classifies why semantic indexing cannot resolve
// its embedding route and returns a user-facing hint, or ok=false when no
// hint is needed (embeddings disabled, or the route resolves cleanly).
//
// The startup path previously reported "no embedding preset is
// configured" for EVERY ResolveEmbedding failure. That masked a real
// regression class: [indexing] embedding_preset keeps its name while the
// [providers] and [models.presets] entries it needs are missing — the
// router answers ErrPresetNotFound, which is a different problem from
// nothing being set at all. The classification mirrors the router's error
// taxonomy so each failure points the user at the actual fix.
func embeddingStartupWarning(cfg config.Config) (string, bool) {
	if !cfg.Indexing.UseEmbeddings {
		return "", false
	}
	_, err := routing.NewStaticRouter(cfg.RoutingConfig()).ResolveEmbedding()
	if err == nil {
		return "", false
	}
	switch {
	case errors.Is(err, routing.ErrEmbeddingNotConfigured):
		return "Semantic search is enabled (indexing.use_embeddings) but no embedding preset is configured. " +
			"Set one with: [indexing] embedding_preset = '<provider>/<model>' — or via /settings → Indexing.", true
	case errors.Is(err, routing.ErrPresetNotFound):
		return fmt.Sprintf("Semantic search is enabled (indexing.use_embeddings), but its embedding preset cannot be resolved: %v. "+
			"A preset name is set, but no matching [models.presets] entry exists and its provider is missing from [providers]. "+
			"Re-add the provider and preset (e.g. via /connect or /settings → Indexing), or clear indexing.embedding_preset.", err), true
	case errors.Is(err, routing.ErrRemoteProviderBlocked):
		return fmt.Sprintf("Semantic search is enabled (indexing.use_embeddings), but its embedding preset is remote and blocked: %v. "+
			"Allow it with privacy.remote_providers_allowed = true (via /settings → Privacy), or point indexing.embedding_preset at a local model.", err), true
	default:
		return fmt.Sprintf("Semantic search is enabled (indexing.use_embeddings), but the embedding route could not be resolved: %v.", err), true
	}
}