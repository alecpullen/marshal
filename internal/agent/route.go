package agent

import (
	"context"
	"fmt"
	"log/slog"

	"marshal/internal/app/config"
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
// (0.85*window - maxOutput) can collapse below this floor; the derivation
// then falls back to a window-proportional value (window/2) rather than the
// 60000-token unknown-window safety net, which would be many times the real
// window and would disable compaction exactly when it is needed most. The
// derivedCollapsed flag is surfaced so callers can label the result.
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
// derivedCollapsed reports whether the model-derived branch fell below
// minDerivedTurnTokens (a very small window) and was clamped to the
// window-proportional floor (window/2) instead. It is distinct from
// usedFallback: the window is known, but the derived value is too small to
// be a genuine 0.85*window-reserve derivation, so the caller labels the
// result as a fallback rather than a genuine derivation.
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
		// A genuinely tiny window: the 60000 unknown-window safety net would
		// be many times the real window, so compaction would never fire and
		// requests would overflow server-side. Clamp to a window-proportional
		// floor that still leaves room for output.
		if floor := window / 2; effective < floor {
			effective = floor
		}
		return effective, false, true
	}
	return effective, false, false
}

// thresholdSource labels where a turn's threshold came from, for the
// per-turn budget log line and the /context panel. derivedCollapsed is the
// flag reported by effectiveTurnThreshold when the model-derived branch fell
// below minDerivedTurnTokens and was clamped to the window-proportional
// floor; such a value is not a genuine derivation, so it is labeled
// "fallback".
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
		r.State.SetNotice(session.Notice{
			Category: session.NoticeProvider,
			Severity: session.SeverityError,
			Message:  err.Error(),
			Source:   "route",
		})
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
	// F12: resolve the model's context window, preferring explicit config on
	// the preset, then the fetched limits table, then the curated local
	// catalog. The window is recorded on the state and consumed by the per-turn
	// effectiveTurnThreshold call below — resolveRoute no longer mutates
	// r.MaxTurnContextTokens, so one small-model turn cannot poison later
	// big-model turns.
	window, maxOut := r.resolveModelLimits(route.Preset)
	// Bound the per-turn local (carried by the caller) so the state
	// continues to reflect the known window for dashboards and rollovers.
	route.Window = window
	route.MaxOutput = maxOut
	r.State.SetTurnContextWindow(window)

	// Rebudget the pack now that the window is known. Explicit per-role
	// config still wins; otherwise the budget scales with the model
	// rather than sitting at the flat default. This runs after the
	// window resolves — the previous placement, above resolveModelLimits,
	// could never have used it.
	//
	// Sections keep their untruncated text (Section.Full), so a pack
	// seeded at the default budget before any route existed recovers its
	// full content here when the model turns out to have a large window.
	packBudget := route.ContextBudget.MaxRepoContextTokens
	if packBudget <= 0 {
		packBudget = contextpack.BudgetForWindow(window)
	}
	r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		if pack.IsEmpty() {
			return pack
		}
		return contextpack.Rebudget(pack, packBudget, r.Now)
	})

	return turnProvider, turnModel, route
}

// ResolveModelLimits resolves a preset's context window and max output tokens.
// Each field falls back independently: explicit config always wins; then the
// fetched limits table, which matches across provider naming variance; then
// the curated local catalog, which returns conservative defaults for unknown
// models. A zero return is only possible when the preset's model ID is empty;
// in that case the caller keeps its configured budget rather than guessing.
//
// logger is threaded through to catalog.Lookup so the "unknown model" warning
// lands in the log file rather than stderr; pass nil to suppress it.
func ResolveModelLimits(preset routing.ModelPreset, table *limits.Table, logger *slog.Logger) (window, maxOutput int) {
	window = preset.ContextWindow
	maxOutput = preset.MaxOutputTokens

	if table != nil {
		if lim, kind := table.Lookup(preset.Provider, preset.Model); kind != limits.MatchNone {
			if window == 0 {
				window = lim.ContextWindow
			}
			if maxOutput == 0 {
				maxOutput = lim.MaxOutputTokens
			}
		}
	}

	if window == 0 || maxOutput == 0 {
		catWindow, catOutput := catalog.Lookup(preset.Model, logger)
		if window == 0 {
			window = catWindow
		}
		if maxOutput == 0 {
			maxOutput = catOutput
		}
	}
	return window, maxOutput
}

func (r *Runner) resolveModelLimits(preset routing.ModelPreset) (window, maxOutput int) {
	var logger *slog.Logger
	if r.State != nil {
		logger = r.State.Logger()
	}
	return ResolveModelLimits(preset, r.LimitsTable, logger)
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

	r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		maxTokens := maxTokenOverride
		if maxTokens <= 0 {
			maxTokens = pack.TokenUsage.MaxTokens
		}
		if maxTokens <= 0 {
			maxTokens = contextpack.DefaultMaxTokens
		}
		return contextpack.MergeMemories(pack, memories, maxTokens, r.Now)
	})
}

// mergeScratchpad injects the session's current scratchpad entries into
// the context pack as a projection section. Called during turn setup so
// the agent can see what keys it has parked without consuming the full
// content in the context budget.
func (r *Runner) mergeScratchpad(maxTokenOverride int) {
	entries := r.State.Scratchpad()
	if len(entries) == 0 {
		// Nothing to inject, but we still need to clear a stale projection
		// if the previous turn left one behind.
		hasScratchpad := false
		for _, s := range r.State.ContextPack().Sections {
			if s.Kind == contextpack.SectionScratchpad {
				hasScratchpad = true
				break
			}
		}
		if !hasScratchpad {
			return
		}
	}
	cpEntries := make([]contextpack.ScratchpadEntry, len(entries))
	for i, e := range entries {
		cpEntries[i] = contextpack.ScratchpadEntry{Key: e.Key, Content: e.Content, Format: e.Format, Updated: e.Updated}
	}
	projectionMax := r.State.ScratchpadConfig().ProjectionMaxTokens
	r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		maxTokens := maxTokenOverride
		if maxTokens <= 0 {
			maxTokens = pack.TokenUsage.MaxTokens
		}
		if maxTokens <= 0 {
			maxTokens = contextpack.DefaultMaxTokens
		}
		return contextpack.MergeScratchpad(pack, cpEntries, maxTokens, projectionMax, r.Now)
	})
}

// mergeTodos injects the session's current todo list into the context
// pack as a projection section. Called during turn setup and before each
// model call so todo.write replacements are visible to the model on the
// next iteration.
func (r *Runner) mergeTodos(maxTokenOverride int) {
	todos := r.State.Todos()
	if len(todos) == 0 {
		// Nothing to inject, but we still need to clear a stale projection
		// if the previous turn left one behind.
		hasTodos := false
		for _, s := range r.State.ContextPack().Sections {
			if s.Kind == contextpack.SectionTodos {
				hasTodos = true
				break
			}
		}
		if !hasTodos {
			return
		}
	}
	cpTodos := make([]contextpack.TodoItem, len(todos))
	for i, item := range todos {
		cpTodos[i] = contextpack.TodoItem{Content: item.Content, Status: item.Status}
	}
	r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		maxTokens := maxTokenOverride
		if maxTokens <= 0 {
			maxTokens = pack.TokenUsage.MaxTokens
		}
		if maxTokens <= 0 {
			maxTokens = contextpack.DefaultMaxTokens
		}
		return contextpack.MergeTodos(pack, cpTodos, maxTokens, r.Now)
	})
}

// resolveEmbedder builds an embedder from the configured embedding preset.
// Returns nil when embeddings are unconfigured or the backend cannot be
// constructed (graceful-off). Construction is cheap (struct init); the cost
// is in Embed calls.
func resolveEmbedder(cfg config.Config) embedding.Embedder {
	// Build a fresh StaticRouter from the config to resolve the embedding
	// role. The runner's RouteResolver interface does not expose
	// ResolveEmbedding, so we reconstruct the router from the config.
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
	return embedder
}

// semanticSource resolves the embedding source for passive semantic context
// injection. Returns nil when embeddings are not configured (graceful-off).
func (r *Runner) semanticSource(projectID int64) retrieval.Source {
	if r.RouteResolver == nil {
		return nil
	}
	embedder := resolveEmbedder(r.State.Config)
	if embedder == nil {
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
	if r.semTracker != nil {
		r.semTracker.addSnippets(snips)
	}
	r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		maxTokens := maxTokenOverride
		if maxTokens <= 0 {
			maxTokens = pack.TokenUsage.MaxTokens
		}
		if maxTokens <= 0 {
			maxTokens = contextpack.DefaultMaxTokens
		}
		return contextpack.MergeSemanticContext(pack, snips, maxTokens, r.Now)
	})
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

// appendSkillHint appends this turn's skill suggestion message, if any.
// Called after setContextPackMessage so it lands behind the cache prefix.
func (r *Runner) appendSkillHint(messages []schema.ChatMessage) []schema.ChatMessage {
	if r.SkillIndex == nil || len(r.skillHints) == 0 {
		return messages
	}
	hinted := make([]skills.Skill, 0, len(r.skillHints))
	for _, name := range r.skillHints {
		if sk, ok := r.SkillIndex.Load(name); ok {
			hinted = append(hinted, sk)
		}
	}
	msg, ok := BuildSkillHintMessage(hinted)
	if !ok {
		return messages
	}
	return append(messages, msg)
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
	full := 0
	if r.State.Config.Skills.BodyFullTurns > 0 {
		full = r.State.Config.Skills.BodyFullTurns
	}
	for _, name := range r.State.ActiveSkills() {
		if r.emittedSkills[name] {
			continue
		}
		skill, ok := r.SkillIndex.Load(name)
		if !ok {
			continue
		}
		content := skills.WrapBody(skill)
		// Past its full-body window, a skill degrades to a stub. Bodies do
		// not replay from history (buildHistoryMessages handles only user
		// and assistant roles), so this is the only copy on the wire —
		// the stub has to tell the model how to get the text back.
		if full > 0 && r.State.SkillBodyAge(name) >= full {
			content = fmt.Sprintf(
				"Skill `%s` is active but its full text is no longer in context. "+
					"Call skill.load with name=%q to re-read it, or skill.unload to drop it.\n",
				name, name)
		}
		messages = append(messages, schema.ChatMessage{
			Role:    schema.RoleSystem,
			Content: content,
		})
		r.emittedSkills[name] = true
	}
	return messages
}

func (r *Runner) fail(task *Task, err error) error {
	task.Status = TaskStatusFailed
	r.State.SetNotice(session.Notice{
		Category: session.NoticeProvider,
		Severity: session.SeverityError,
		Message:  err.Error(),
		Source:   "agent",
	})
	r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Agent failed: %s", err.Error()), session.ContentTypePlain)
	return err
}
