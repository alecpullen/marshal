package agent

import (
	"context"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/catalog"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/retrieval"
	"marshal/internal/skills"
)

// minDerivedTurnTokens is the smallest effective per-turn threshold the
// model-derived heuristic will produce. At very small windows
// (0.85*window - maxOutput) can collapse below this floor, in which case
// fall back to DefaultMaxTurnContextTokens (the model-window-unknown
// safety net) and surface a state flag so callers know to use it.
const minDerivedTurnTokens = 4000

// effectiveTurnThreshold derives the mid-turn compaction threshold for the
// resolved route's model window. It is computed every turn (carried as a
// per-turn local) because resolveRoute used to mutate r.MaxTurnContextTokens
// monotonically — one small-model turn poisoned later big-model turns
// (audit F-POL-85). With a per-turn local, the threshold always tracks the
// model actually in use.
//
// Rules:
//   - configured > 0            : hard ceiling — never exceed user config
//   - configured == 0, window>0 : 0.85*window - maxOutput (model-derived)
//   - configured == 0, window<=0: DefaultMaxTurnContextTokens (60000), the
//     safety net for unknown models
//   - the output reserve is capped at window/8 so a model whose advertised
//     max output rivals its window still derives a usable budget
//
// usedFallback reports whether the window-unknown fallback fired (window
// was <=0 with no configured override).
//
// derivedCollapsed reports whether the model-derived branch collapsed to
// the DefaultMaxTurnContextTokens safety net because 0.85*window - reserve
// fell below minDerivedTurnTokens (a very small window). It is distinct
// from usedFallback: the window is known, but the derived value is too
// small to be usable, so the caller must label the result as a fallback
// rather than a genuine derivation.
func (r *Runner) effectiveTurnThreshold(window int, maxOutput int, configured int) (threshold int, usedFallback bool, derivedCollapsed bool) {
	if configured > 0 {
		return configured, false, false
	}
	if window <= 0 {
		return DefaultMaxTurnContextTokens, true, false
	}
	// Reserve room for the answer, but never more than an eighth of the
	// window. Modern models advertise a max output that is a large
	// fraction of the window (256k window against a 262k max output);
	// subtracting it whole drives the budget negative and lands on the
	// unknown-window fallback. The 0.85 factor already holds back 15%.
	reserve := maxOutput
	if limit := window / 8; reserve > limit {
		reserve = limit
	}
	effective := int(float64(window)*0.85) - reserve
	if effective < minDerivedTurnTokens {
		return DefaultMaxTurnContextTokens, false, true
	}
	return effective, false, false
}

// thresholdSource labels where a turn's threshold came from, for the
// per-turn budget log line and the /context panel. derivedCollapsed is the
// flag reported by effectiveTurnThreshold when the model-derived branch
// collapsed to the DefaultMaxTurnContextTokens safety net; such a value is
// a fallback, not a genuine derivation, so it is labeled "fallback".
func thresholdSource(window, configured int, derivedCollapsed bool) string {
	switch {
	case configured > 0:
		return "configured"
	case window <= 0:
		return "fallback"
	case derivedCollapsed:
		return "fallback"
	default:
		return "derived"
	}
}

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
	// the preset, then the fetched limits table, then the curated local
	// catalog. The window is recorded on state and consumed by the per-turn
	// effectiveTurnThreshold call below — resolveRoute no longer mutates
	// r.MaxTurnContextTokens, so one small-model turn cannot poison later
	// big-model turns.
	window, maxOut := r.resolveModelLimits(route.Preset)
	// Bound the per-turn local (carried by the caller) so the state
	// continues to reflect the known window for dashboards and rollovers.
	route.Window = window
	route.MaxOutput = maxOut
	r.State.SetTurnContextWindow(window)
	return turnProvider, turnModel, route
}

// resolveModelLimits resolves a preset's context window and max output
// tokens. Explicit config always wins; then the fetched limits table, which
// matches across provider naming variance; then the curated local catalog.
// (0, 0) means unknown — the caller keeps its configured budget rather than
// guessing.
func (r *Runner) resolveModelLimits(preset routing.ModelPreset) (window, maxOutput int) {
	if preset.ContextWindow != 0 {
		return preset.ContextWindow, preset.MaxOutputTokens
	}
	if r.LimitsTable != nil {
		if lim, kind := r.LimitsTable.Lookup(preset.Provider, preset.Model); kind != limits.MatchNone {
			if lim.ContextWindow != 0 {
				return lim.ContextWindow, lim.MaxOutputTokens
			}
		}
	}
	return catalog.Lookup(preset.Model)
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

// mergeScratchpad injects the session's current scratchpad entries into
// the context pack as a projection section. Called during turn setup so
// the agent can see what keys it has parked without consuming the full
// content in the context budget.
func (r *Runner) mergeScratchpad(maxTokenOverride int) {
	entries := r.State.Scratchpad()
	current := r.State.ContextPack()
	if len(entries) == 0 {
		// Nothing to inject, but we still need to clear a stale projection
		// if the previous turn left one behind.
		hasScratchpad := false
		for _, s := range current.Sections {
			if s.Kind == contextpack.SectionScratchpad {
				hasScratchpad = true
				break
			}
		}
		if !hasScratchpad {
			return
		}
	}
	maxTokens := maxTokenOverride
	if maxTokens <= 0 {
		maxTokens = current.TokenUsage.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = contextpack.DefaultMaxTokens
	}
	cpEntries := make([]contextpack.ScratchpadEntry, len(entries))
	for i, e := range entries {
		cpEntries[i] = contextpack.ScratchpadEntry{Key: e.Key, Content: e.Content, Format: e.Format, Updated: e.Updated}
	}
	r.State.SetContextPack(contextpack.MergeScratchpad(current, cpEntries, maxTokens, r.State.ScratchpadConfig().ProjectionMaxTokens, r.Now))
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

// setContextPackMessage ensures messages contains exactly one up-to-date
// context-pack message, replacing the one tracked at r.contextPackMsgIndex.
// It updates r.contextPackMsgIndex to the message's final index. Callers that
// rebuild messages from scratch must reset r.contextPackMsgIndex to -1 first.
func (r *Runner) setContextPackMessage(messages []schema.ChatMessage, pack contextpack.Pack) []schema.ChatMessage {
	msg, ok := BuildContextPackMessage(pack)
	if !ok {
		// Empty pack: remove the tracked message if it still exists.
		if r.contextPackMsgIndex >= 0 && r.contextPackMsgIndex < len(messages) {
			messages = append(messages[:r.contextPackMsgIndex], messages[r.contextPackMsgIndex+1:]...)
		}
		r.contextPackMsgIndex = -1
		return messages
	}

	if r.contextPackMsgIndex >= 0 && r.contextPackMsgIndex < len(messages) {
		messages[r.contextPackMsgIndex] = msg
		return messages
	}

	// No tracked message or index is stale: insert right after the system
	// prompt (index 0). If there is no system prompt, prepend.
	if len(messages) > 0 {
		messages = append(messages[:1], append([]schema.ChatMessage{msg}, messages[1:]...)...)
		r.contextPackMsgIndex = 1
	} else {
		messages = []schema.ChatMessage{msg}
		r.contextPackMsgIndex = 0
	}
	return messages
}

// appendSkillBodies appends the wrapped body of every active skill whose
// body is not already on the wire, and records it as emitted. The system
// prompt tells the model an active skill's "body already in context", so
// something has to actually put it there: the session-state copy written
// by skills.LoadSkillIntoSession is transcript-only, and history replay
// (buildHistoryMessages) drops system messages, so neither delivers it.
//
// Bodies are appended at the end rather than pinned near the top: skills
// are append-only (never deactivated), appending keeps a mid-turn load
// adjacent to its skill.load tool result, and it leaves
// contextPackMsgIndex undisturbed.
//
// Callers that rebuild the wire from scratch must reset r.emittedSkills
// to nil first so every active skill is re-emitted onto the new wire.
func (r *Runner) appendSkillBodies(messages []schema.ChatMessage) []schema.ChatMessage {
	if r.SkillIndex == nil || r.State == nil {
		return messages
	}
	if r.emittedSkills == nil {
		r.emittedSkills = make(map[string]bool)
	}
	for _, name := range r.State.ActiveSkills() {
		if r.emittedSkills[name] {
			continue
		}
		skill, ok := r.SkillIndex.Load(name)
		if !ok {
			continue
		}
		messages = append(messages, schema.ChatMessage{
			Role:    schema.RoleSystem,
			Content: skills.WrapBody(skill),
		})
		r.emittedSkills[name] = true
	}
	return messages
}

func (r *Runner) fail(task *Task, err error) error {
	task.Status = TaskStatusFailed
	r.State.SetProviderError(err)
	r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Agent failed: %s", err.Error()), session.ContentTypePlain)
	return err
}
