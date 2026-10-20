package agent

import (
	"context"
	"fmt"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/provider"
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

// effectiveMaxToolIterations derives the per-turn tool ceiling. Explicit
// config (agent.max_tool_iterations > 0) always wins. When it is unset,
// local routes get LocalDefaultMaxToolIterations while remote routes keep
// the unlimited default — see the const's comment for the rationale. Like
// effectiveTurnThreshold this is a per-turn computation, so one local-model
// turn cannot poison later remote turns and vice versa.
func (r *Runner) effectiveMaxToolIterations(route routing.Route) int {
	if r.MaxToolIterations > 0 {
		return r.MaxToolIterations
	}
	if route.Preset.LocalOnly {
		return LocalDefaultMaxToolIterations
	}
	return DefaultMaxToolIterations
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

func (r *Runner) resolveRoute(ctx context.Context, task *Task) (provider.Provider, string, routing.Route) {
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
		// The error notice replaced any clamp banner. Clearing the key makes
		// the next successful turn re-banner the clamp instead of assuming the
		// user still sees it.
		r.clampNoticeKey = ""
		return turnProvider, turnModel, routing.Route{}
	}
	if resolvedProvider != nil {
		turnProvider = resolvedProvider
	}
	if route.Preset.Model != "" {
		turnModel = route.Preset.Model
	}
	// Mirror the chat path's capability gate (chat.go): the preset effort is
	// only sent when the provider reports reasoning support, so the route
	// snapshot display surfaces consume must carry the same verdict.
	reasoningCapable := false
	if turnProvider != nil {
		reasoningCapable = turnProvider.Capabilities(ctx).Reasoning
	}
	r.State.SetActiveRoute(session.RouteInfo{
		Role:             route.Role,
		Profile:          route.Profile,
		Preset:           route.Preset.Name,
		Provider:         route.Preset.Provider,
		Model:            route.Preset.Model,
		LocalOnly:        route.Preset.LocalOnly,
		Thinking:         route.Preset.Thinking,
		ReasoningCapable: reasoningCapable,
		Active:           true,
	})
	// F12: resolve the model's context window, preferring explicit config on
	// the preset, then the documented default pair for the preset's locality.
	// A provider-reported probe may lower it, never raise it. The window is
	// recorded on the state and consumed by the per-turn effectiveTurnThreshold
	// call below — resolveRoute no longer mutates r.MaxTurnContextTokens, so
	// one small-model turn cannot poison later big-model turns.
	lim := r.resolveModelLimits(route.Preset)
	window, maxOut := lim.Window, lim.MaxOutput
	// Bound the per-turn local (carried by the caller) so the state
	// continues to reflect the known window for dashboards and rollovers.
	route.Window = window
	route.MaxOutput = maxOut
	r.State.SetTurnContextWindow(window)

	if key, message, ok := clampNoticeFor(route.Preset, lim); ok {
		if key != r.clampNoticeKey {
			r.clampNoticeKey = key
			r.State.SetNotice(session.Notice{
				Category: session.NoticeProvider,
				Severity: session.SeverityWarn,
				Message:  message,
				Hint:     "Set context_window or max_output_tokens on the preset to match, or raise agent.max_turn_context_tokens to cap the threshold separately.",
				Source:   "route-limits",
			})
		}
	} else if r.clampNoticeKey != "" {
		// The route no longer clamps (or no longer lowers an explicit
		// declaration). Clear only our banner; a provider error that replaced
		// it must survive.
		r.clampNoticeKey = ""
		if n, ok := r.State.Notice(); ok && n.Source == "route-limits" {
			r.State.DismissNotice()
		}
	}

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

// clampNoticeFor builds the banner for a provider clamp that lowered a value
// the preset explicitly declared. It returns a stable key so resolveRoute
// re-notices when the clamp changes but never re-stamps an unchanged one.
//
// A clamp of a value the preset did not declare (a default pair that the
// provider then corrected) is silent: there is no user declaration to report,
// and the resolved value already reaches every consumer.
func clampNoticeFor(preset routing.ModelPreset, lim *ModelLimits) (key, message string, ok bool) {
	if lim == nil || !lim.Clamped || lim.ClampSource != "probe" {
		return "", "", false
	}
	windowClamped := lim.PresetWindow > 0 && lim.ProbeWindow > 0 && lim.Window < lim.PresetWindow
	outputClamped := lim.PresetOutput > 0 && lim.ProbeOutput > 0 && lim.MaxOutput < lim.PresetOutput
	if !windowClamped && !outputClamped {
		return "", "", false
	}
	key = fmt.Sprintf("%s|%s|%s|%d|%d|%d|%d",
		preset.Name, preset.Provider, preset.Model,
		lim.PresetWindow, lim.PresetOutput, lim.Window, lim.MaxOutput)
	// A value equal to the documented locality default came from Marshal's
	// seeding (Materialize, switchModelPreset, or a config migration), not
	// from a user decision. Saying "declares" would blame the user for a
	// number this program wrote.
	defCtx, defOut := routing.DefaultModelLimits(preset.LocalOnly)
	windowPhrase := fmt.Sprintf("declares a %d-token context window", lim.PresetWindow)
	if lim.PresetWindow == defCtx {
		windowPhrase = fmt.Sprintf("uses Marshal's default %d-token context window", lim.PresetWindow)
	}
	outputPhrase := fmt.Sprintf("declares %d output tokens", lim.PresetOutput)
	if lim.PresetOutput == defOut {
		outputPhrase = fmt.Sprintf("uses Marshal's default %d output tokens", lim.PresetOutput)
	}
	switch {
	case windowClamped && outputClamped:
		message = fmt.Sprintf(
			"Preset %s %s and %s, but the provider reports %d and %d. Using %d and %d.",
			preset.Name, windowPhrase, outputPhrase, lim.ProbeWindow, lim.ProbeOutput, lim.Window, lim.MaxOutput)
	case windowClamped:
		message = fmt.Sprintf(
			"Preset %s %s, but the provider reports %d. Using %d.",
			preset.Name, windowPhrase, lim.ProbeWindow, lim.Window)
	default:
		message = fmt.Sprintf(
			"Preset %s %s, but the provider reports %d. Using %d.",
			preset.Name, outputPhrase, lim.ProbeOutput, lim.MaxOutput)
	}
	return key, message, true
}

// ModelProbe is the provider's own reported limits for one model, as
// recorded by the provider's /models probe. OK=false means the probe is
// absent, stale, or hash-invalidated — never clamp on a guess.
type ModelProbe struct {
	Window, MaxOutput int
	OK                bool
}

// ModelLimits is a preset's resolved budget plus the provenance a caller
// needs to report a clamp. Probe* is 0 when the provider had nothing to say.
type ModelLimits struct {
	Window, MaxOutput          int // in use
	PresetWindow, PresetOutput int // as declared in config
	ProbeWindow, ProbeOutput   int // provider-reported, 0 = unknown
	Clamped                    bool
	ClampSource                string // "", "probe"
}

// ResolveModelLimits resolves a preset's context window and max output tokens.
// Each field falls back independently: an explicit preset value always wins;
// otherwise Marshal's documented default pair applies (1M context / 128K output
// for remote models, 8K / 4K for local ones). Both figures are therefore
// always nonzero; the zero-window handling downstream belongs to the
// resolver-error path, not to this function.
//
// After resolution, a known provider-reported limit (probe) clamps the result
// down. The clamp only ever lowers: a deliberately conservative preset is never
// expanded. The probe is the only clamp source, because it is a statement by
// the party that enforces the limit; when a provider reports nothing, an
// oversize prompt is caught by the runner's context-overflow recovery instead.
func ResolveModelLimits(preset routing.ModelPreset, probe ModelProbe) *ModelLimits {
	lim := &ModelLimits{
		Window:       preset.ContextWindow,
		MaxOutput:    preset.MaxOutputTokens,
		PresetWindow: preset.ContextWindow,
		PresetOutput: preset.MaxOutputTokens,
	}

	if lim.Window == 0 || lim.MaxOutput == 0 {
		defWindow, defOutput := routing.DefaultModelLimits(preset.LocalOnly)
		if lim.Window == 0 {
			lim.Window = defWindow
		}
		if lim.MaxOutput == 0 {
			lim.MaxOutput = defOutput
		}
	}

	if probe.OK {
		lim.ProbeWindow = probe.Window
		lim.ProbeOutput = probe.MaxOutput
	}

	// Post-pass clamp. Only a provider-reported probe may override an
	// explicit preset. When the provider does not report limits at all, an
	// oversize prompt is caught by the runner's context-overflow recovery
	// instead.
	if probe.OK && probe.Window > 0 {
		if clampField(&lim.Window, probe.Window) {
			lim.Clamped, lim.ClampSource = true, "probe"
		}
	}
	if probe.OK && probe.MaxOutput > 0 {
		if clampField(&lim.MaxOutput, probe.MaxOutput) {
			lim.Clamped = true
			if lim.ClampSource == "" {
				lim.ClampSource = "probe"
			}
		}
	}
	return lim
}

// clampField lowers *cur to limit when limit is known and smaller. It
// reports whether it clamped.
func clampField(cur *int, limit int) bool {
	if limit > 0 && *cur > limit {
		*cur = limit
		return true
	}
	return false
}

func (r *Runner) resolveModelLimits(preset routing.ModelPreset) *ModelLimits {
	var probe ModelProbe
	if r.ModelProbe != nil {
		probe = r.ModelProbe(preset.Provider, preset.Model)
	}
	return ResolveModelLimits(preset, probe)
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

// markCacheAnchor flags the last message as the end of the cross-turn
// stable region and clears any earlier flag, so a wire carries exactly one
// anchor. Call it when a wire is built from scratch, after the replayed
// history and skill bodies and before the skill hint and the goal: those
// change every turn and must sit after the anchor. See
// docs/prompt-caching/followups-design.md (F1).
func markCacheAnchor(messages []schema.ChatMessage) {
	for i := range messages {
		messages[i].CacheAnchor = false
	}
	if n := len(messages); n > 0 {
		messages[n-1].CacheAnchor = true
	}
}

// setContextPackMessage ensures messages contains exactly one up-to-date
// context-pack message, replacing the one tracked at r.contextPackMsgIndex.
// It updates r.contextPackMsgIndex to the message's final index. Callers that
// rebuild messages from scratch must reset r.contextPackMsgIndex to -1 first.
func (r *Runner) setContextPackMessage(messages []schema.ChatMessage, pack contextpack.Pack) []schema.ChatMessage {
	msg, ok := BuildContextPackMessage(pack)
	if !ok {
		// Empty pack: remove the tracked message if it still exists. An
		// anchor it carried moves to the message before it, so the wire
		// keeps exactly one.
		if i := r.contextPackMsgIndex; i >= 0 && i < len(messages) {
			anchored := messages[i].CacheAnchor
			messages = append(messages[:i], messages[i+1:]...)
			if anchored && i > 0 {
				messages[i-1].CacheAnchor = true
			}
		}
		r.contextPackMsgIndex = -1
		return messages
	}

	if r.contextPackMsgIndex >= 0 && r.contextPackMsgIndex < len(messages) {
		msg.CacheAnchor = messages[r.contextPackMsgIndex].CacheAnchor
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

// withWorkingState returns messages plus this request's working-state
// tail. The tail is rebuilt for every request and never stored in the
// runner's message slice, so it cannot leak into compaction, pruning,
// archives or history replay, and a retry never stacks a second copy.
// The input is not modified. Used excludes the tail itself, so the budget
// line is not recursive.
func (r *Runner) withWorkingState(messages []schema.ChatMessage) []schema.ChatMessage {
	if r.State == nil {
		return messages
	}
	window, threshold, _ := r.State.TurnBudget()
	msg, ok := BuildWorkingStateMessage(r.State.ContextPack(), WorkingStateBudget{
		Used:      r.calibratedEstimate(messages),
		Window:    window,
		Threshold: threshold,
	})
	if !ok {
		return messages
	}
	out := make([]schema.ChatMessage, len(messages), len(messages)+1)
	copy(out, messages)
	return append(out, msg)
}

// systemPromptMessage builds messages[0] from the runner's live state.
// Every build site goes through here. The system prompt heads the
// provider's cache prefix, so two sites that disagree on an option produce
// different bytes and miss the cache. They also send the model a different
// prompt: rollover and plan-first used to drop SystemAccess and Narration.
func (r *Runner) systemPromptMessage() schema.ChatMessage {
	return BuildSystemPromptWithSystem(SystemPromptOptions{
		Role:         r.role(),
		Tools:        r.Registry.List(),
		Deferred:     r.Registry.ListDeferred(),
		SkillIndex:   r.SkillIndex,
		ActiveSkills: r.State.ActiveSkills(),
		NativeTools:  r.NativeTools,
		Mode:         r.Policy.ApprovalMode(),
		Addendum:     r.SystemPromptAddendum,
		WorkingDir:   r.State.WorkingDir,
		Roster:       r.agentRoster(),
		LoadedNames:  r.State.LoadedToolNames(),
		SystemAccess: r.State.SystemAccess(),
		Narration:    r.NarrationPrompt,
	})
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
					"Call skill.load with name=%q to re-read it.\n",
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
