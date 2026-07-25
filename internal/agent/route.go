package agent

import (
	"context"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/catalog"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/retrieval"
)

func (r *Runner) resolveRoute(task *Task) (provider.Provider, string, routing.Route) {
	turnProvider := r.Provider
	turnModel := r.Model
	if r.RouteResolver == nil {
		return turnProvider, turnModel, routing.Route{}
	}

	route, resolvedProvider, err := r.RouteResolver.Resolve(string(task.Class))
	if err != nil {
		r.State.SetProviderError(err)
		r.State.SetActiveRoute(session.RouteInfo{})
		return turnProvider, turnModel, routing.Route{}
	}
	if resolvedProvider != nil {
		turnProvider = resolvedProvider
	}
	if route.Preset.Model != "" {
		turnModel = route.Preset.Model
	}
	r.State.SetActiveRoute(session.RouteInfo{
		Role:      route.Role,
		Profile:   route.Profile,
		Preset:    route.Preset.Name,
		Provider:  route.Preset.Provider,
		Model:     route.Preset.Model,
		LocalOnly: route.Preset.LocalOnly,
		Legacy:    route.Legacy,
		Active:    true,
	})
	if route.ContextBudget.MaxRepoContextTokens > 0 {
		pack := r.State.ContextPack()
		if !pack.IsEmpty() {
			pack = contextpack.Rebudget(pack, route.ContextBudget.MaxRepoContextTokens, r.Now)
			r.State.SetContextPack(pack)
		}
	}

	// F12: resolve the model's context window, preferring explicit config on
	// the preset, falling back to the curated catalog. Unknown (0) leaves the
	// configured turn budget untouched — never guess.
	window := route.Preset.ContextWindow
	maxOut := route.Preset.MaxOutputTokens
	if window == 0 {
		window, maxOut = catalog.Lookup(route.Preset.Model)
	}
	if window > 0 {
		reserved := maxOut
		effective := int(float64(window)*0.85) - reserved
		if effective < 0 {
			effective = 0
		}
		// F-SEC-10: use the smaller of the configured value and the
		// model-derived value. The configured value is a CEILING (never
		// exceed the user's setting), not a floor. The previous code
		// raised the configured value to the model-derived value, which
		// meant a generous user config on a small model fed the model
		// more tokens than its window supports.
		if r.MaxTurnContextTokens == 0 || effective < r.MaxTurnContextTokens {
			r.MaxTurnContextTokens = effective
		}
		r.State.SetTurnContextWindow(window)
	} else {
		r.State.SetTurnContextWindow(0)
	}
	return turnProvider, turnModel, route
}

// mergeMemories injects the project's current durable memories into the
// context pack, if a MemoryProvider is configured. Failures are ignored so a
// missing or unhealthy memory source never blocks a turn.
func (r *Runner) mergeMemories(maxTokenOverride int) {
	if r.MemoryProvider == nil {
		return
	}

	memories, err := r.MemoryProvider.Memories(r.ProjectID)
	if err != nil {
		return
	}

	current := r.State.ContextPack()
	maxTokens := maxTokenOverride
	if maxTokens <= 0 {
		maxTokens = current.TokenUsage.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = contextpack.DefaultMaxTokens
	}
	r.State.SetContextPack(contextpack.MergeMemories(current, memories, maxTokens, r.Now))
}

// semanticSource resolves the embedding source for passive semantic context
// injection. Returns nil when embeddings are not configured (graceful-off).
func (r *Runner) semanticSource(projectID int64) retrieval.Source {
	if r.RouteResolver == nil {
		return nil
	}
	// Build a fresh StaticRouter from the runner's config to resolve the
	// embedding role. The runner's RouteResolver interface does not expose
	// ResolveEmbedding, so we reconstruct the router from the config.
	cfg := r.State.Config
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	route, err := router.ResolveEmbedding()
	if err != nil {
		return nil // includes routing.ErrEmbeddingNotConfigured
	}
	pc, ok := cfg.Providers[route.Preset.Provider]
	if !ok {
		return nil
	}
	embedder, err := embedding.NewFromConfig(route.Preset.Provider, pc, route.Preset.Model)
	if err != nil {
		return nil
	}
	db := r.State.DB()
	if db == nil {
		return nil
	}
	return retrieval.NewSemanticSource(db, embedder, projectID)
}

// mergeSemantic injects semantic context snippets into the pack alongside
// memories. A nil source (unconfigured embedder) is a no-op.
func (r *Runner) mergeSemantic(ctx context.Context, goal string, projectID int64, maxTokenOverride int) {
	src := r.semanticSource(projectID)
	if src == nil {
		return
	}
	snips := retrieveSemanticContext(ctx, goal, src)
	if len(snips) == 0 {
		return
	}
	current := r.State.ContextPack()
	maxTokens := maxTokenOverride
	if maxTokens <= 0 {
		maxTokens = current.TokenUsage.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = contextpack.DefaultMaxTokens
	}
	r.State.SetContextPack(contextpack.MergeSemanticContext(current, snips, maxTokens, r.Now))
}

func appendContextPackMessage(messages []schema.ChatMessage, pack contextpack.Pack) []schema.ChatMessage {
	if msg, ok := BuildContextPackMessage(pack); ok {
		return append(messages, msg)
	}
	return messages
}

func (r *Runner) fail(task *Task, err error) error {
	task.Status = TaskStatusFailed
	r.State.SetProviderError(err)
	r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Agent failed: %s", err.Error()), session.ContentTypePlain)
	return err
}
