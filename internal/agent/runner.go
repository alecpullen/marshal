package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

const (
	DefaultMaxToolIterations    = 100
	DefaultMaxRetries           = 2
	DefaultMaxParallelActions   = 4
	DefaultMaxTurnContextTokens = 60000
	compactKeepRecentMessages   = 6
	finalizePressureThreshold   = 2
	finalizePressureMessage     = "You are near the tool budget. Unless one specific missing fact is required, produce a final answer now using the results you already have."
	maxConsecutiveParseFailures = 3
)

var ErrMaxIterationsExceeded = errors.New("agent: exceeded max tool iterations without a final answer")

var ErrModelOutputMalformed = errors.New("agent: model output could not be parsed after consecutive attempts")

// isLengthFinish reports whether the provider cut the response off at the
// output-token limit ("length" for OpenAI-compatible providers, "max_tokens"
// for Anthropic-style ones). Tool calls in such a response may carry
// silently truncated arguments and must not be executed (pi's guard).
func isLengthFinish(reason string) bool {
	return reason == "length" || reason == "max_tokens"
}

// normalizeArgs returns a canonical JSON representation of a tool's
// arguments so that {"b":1,"a":2} and {"a":2,"b":1} share the same
// cache key. Empty arguments normalise to {}.
func normalizeArgs(args json.RawMessage) ([]byte, error) {
	if len(args) == 0 || string(args) == "null" {
		return []byte("{}"), nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(args, &m); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

type RouteResolver interface {
	Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error)
}

// WriteGate serialises non-read-only tool execution across concurrently
// running Runners. The swarm sets one shared gate on every role runner so
// that "only one agent may write files at a time" (docs/07 swarm safety)
// holds even if a future orchestration mode overlaps role turns.
type WriteGate interface {
	// Acquire blocks until the gate is free and returns its release func.
	Acquire() (release func())
}

// MemoryProvider supplies durable project memories for injection into the
// context pack at the start of each turn. It returns contextpack.MemoryNote
// (not a type from internal/knowledge) so that internal/agent never needs
// to depend on internal/knowledge (the two packages must not import each
// other; see the Milestone N design doc).
type MemoryProvider interface {
	Memories(projectID int64) ([]contextpack.MemoryNote, error)
}

// Runner drives one agent turn end to end: classify -> (optionally plan) ->
// loop { call the model, parse its action, execute or answer } -> summarise.
// It is the only thing in Marshal that calls Provider.Chat, Registry.Lookup,
// and PolicyEngine.Evaluate together — everything else (TUI, tools,
// registry, policy) stays decoupled and is exercised independently by
// Milestones C-G's own tests.
type UsageObserver func(promptTokens, completionTokens int)

type Runner struct {
	Provider             provider.Provider
	Registry             *registry.Registry
	Policy               *policy.PolicyEngine
	State                *session.State
	Model                string
	RouteResolver        RouteResolver
	MemoryProvider       MemoryProvider
	ProjectID            int64
	Now                  func() time.Time
	MaxToolIterations    int
	MaxRetries           int
	MaxTurnContextTokens int
	RequestTimeout       time.Duration
	ResponseFormat       *schema.ResponseFormat
	NativeTools          bool
	MaxParallelActions   int
	MaxToolResultChars   int
	ForceClass           string // if set, overrides Classify() in Run()
	SkillIndex           *skills.Index

	// Role selects the system-prompt role addendum. Zero value behaves as
	// RoleGeneral, so existing single-agent construction is unchanged.
	// Swarm sub-runners set this to planner/repo_scout/implementer/reviewer.
	Role AgentRole

	// WriteGate serialises non-read-only tool execution. When nil, no
	// serialisation is performed (default single-agent behaviour).
	WriteGate WriteGate

	UsageObserver UsageObserver

	// MetricsObserver, when set, receives one TurnMetrics per RunTask,
	// emitted on every exit path (answer, salvage, failure). Nil disables
	// collection output; counter bookkeeping still runs.
	MetricsObserver func(TurnMetrics)

	forceClassMu sync.Mutex
	tracker      *progressTracker
	trackerMu    sync.Mutex
	stats        *turnStats
	statsMu      sync.Mutex
}

type chatResult struct {
	Text         string
	ToolCalls    []schema.ToolCall
	FinishReason string
}

func NewRunner(p provider.Provider, reg *registry.Registry, pol *policy.PolicyEngine, state *session.State, model string) *Runner {
	return &Runner{
		Provider:             p,
		Registry:             reg,
		Policy:               pol,
		State:                state,
		Model:                model,
		Now:                  time.Now,
		MaxToolIterations:    DefaultMaxToolIterations,
		MaxRetries:           DefaultMaxRetries,
		MaxParallelActions:   DefaultMaxParallelActions,
		MaxToolResultChars:   DefaultMaxToolResultChars,
		MaxTurnContextTokens: DefaultMaxTurnContextTokens,
	}
}

func (r *Runner) SetForceClass(class string) {
	r.forceClassMu.Lock()
	r.ForceClass = class
	r.forceClassMu.Unlock()
}

func (r *Runner) role() AgentRole {
	if r.Role == "" {
		return RoleGeneral
	}
	return r.Role
}

// Run executes one full agent turn for goal. It records the user's message,
// the assistant's plan (if any), every tool call/result, and the final
// answer directly onto r.State, so the TUI's existing transcript/audit-log/
// approval rendering picks all of it up with no TUI changes.
func (r *Runner) Run(ctx context.Context, goal string) error {
	_, err := r.RunTask(ctx, goal)
	return err
}

// RunTask is Run plus access to the finished Task, so orchestrators (the
// swarm) can read a role's final summary and status without re-parsing
// the session transcript.
func (r *Runner) RunTask(ctx context.Context, goal string) (*Task, error) {
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
	r.State.AddMessage(session.RoleUser, goal, session.ContentTypePlain)
	r.State.ClearTurnToolCache()
	r.trackerMu.Lock()
	r.tracker = newProgressTracker()
	r.trackerMu.Unlock()

	r.statsMu.Lock()
	r.stats = &turnStats{m: TurnMetrics{
		StartedAt: r.Now(),
		Goal:      truncateGoal(goal, 200),
		Role:      string(r.role()),
	}}
	r.statsMu.Unlock()

	task := NewTask(goal, r.Now())
	defer func() { r.emitMetrics(task) }()
	r.forceClassMu.Lock()
	fc := r.ForceClass
	r.forceClassMu.Unlock()
	if fc != "" {
		task.Class = TaskClass(fc)
	} else {
		task.Class = Classify(goal)
	}
	turnProvider, turnModel, route := r.resolveRoute(task)
	r.withStats(func(s *turnStats) {
		s.m.Provider = turnProvider.Name()
		s.m.Model = turnModel
	})
	r.mergeMemories(route.ContextBudget.MaxRepoContextTokens)

	messages := []schema.ChatMessage{
		BuildSystemPrompt(r.role(), r.Registry.List(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools),
	}
	messages = appendContextPackMessage(messages, r.State.ContextPack())
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})

	if task.Class != ClassQuestion {
		task.Status = TaskStatusPlanning
		planMessages := append(append([]schema.ChatMessage{}, messages...), BuildPlanningPrompt(goal))
		planRes, err := r.chatWithRetryNoNativeTools(ctx, turnProvider, turnModel, planMessages)
		if err != nil {
			return task, r.fail(task, err)
		}
		planText := planRes.Text
		task.Plan = splitPlanLines(planText)
		r.State.SetPlan(task.Plan)
		if current := r.State.ContextPack(); !current.IsEmpty() {
			maxTokens := current.TokenUsage.MaxTokens
			if route.ContextBudget.MaxRepoContextTokens > 0 {
				maxTokens = route.ContextBudget.MaxRepoContextTokens
			}
			updatedPack := contextpack.RefreshPlanWithBudget(current, task.Plan, maxTokens, r.Now)
			r.State.SetContextPack(updatedPack)
			messages = []schema.ChatMessage{BuildSystemPrompt(r.role(), r.Registry.List(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools)}
			messages = appendContextPackMessage(messages, updatedPack)
			messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
		}
		r.State.AddMessage(session.RoleAssistant, planText, session.ContentTypePlan)
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: "Plan:\n" + planText})
	}

	task.Status = TaskStatusExecuting
	lastRenderedSkills := r.State.ActiveSkills()
	pressureSent := false
	producedValidAction := false
	consecutiveParseFailures := 0
	consecutiveEmpty := 0
	iteration := 0
	for {
		if iteration >= r.MaxToolIterations {
			break
		}
		r.State.SetToolBudget(session.ToolBudget{Used: iteration, Max: r.MaxToolIterations})

		if !pressureSent && r.MaxToolIterations-iteration <= finalizePressureThreshold {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: finalizePressureMessage})
			r.State.AddMessage(session.RoleSystem, finalizePressureMessage, session.ContentTypePlain)
			pressureSent = true
		}

		currentSkills := r.State.ActiveSkills()
		if skillsChanged(lastRenderedSkills, currentSkills) {
			messages[0] = BuildSystemPrompt(r.role(), r.Registry.List(), r.SkillIndex, currentSkills, r.NativeTools)
			lastRenderedSkills = currentSkills
		}

		if r.MaxTurnContextTokens > 0 && estimateTokens(messages) > r.MaxTurnContextTokens {
			if fresh, serr := r.summarizeAndContinue(ctx, turnProvider, turnModel, messages, goal); serr == nil {
				messages = fresh
				pressureSent = false // the fresh transcript may legitimately approach the budget again
			} else {
				// Summarization failed (transport error or empty text): fall
				// back to lossy in-place compaction rather than aborting the turn.
				messages = compactMessages(messages, r.MaxTurnContextTokens, compactKeepRecentMessages)
			}
		}

		res, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages)
		if err != nil {
			return task, r.fail(task, err)
		}
		raw := res.Text

		if r.NativeTools {
			if len(res.ToolCalls) == 0 {
				if strings.TrimSpace(res.Text) == "" {
					// Empty response: the model went silent. Count this turn
					// against the budget (MaxToolIterations is a turn budget,
					// not just a tool-call budget) so a silent model cannot
					// loop forever. Record an idle entry so the stall detector
					// can see sustained silence, and short-circuit to finalize
					// after a couple of consecutive empties rather than
					// re-prompting indefinitely.
					iteration++
					r.withStats(func(s *turnStats) { s.m.Iterations = iteration })
					consecutiveEmpty++
					r.trackerMu.Lock()
					r.tracker.recordIdle(res.FinishReason)
					r.trackerMu.Unlock()
					messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: "Call a tool or give a final answer."})
					if consecutiveEmpty >= 2 {
						return r.finalize(ctx, turnProvider, turnModel, messages, task, reasonEmpty)
					}
					if finalized, resTask, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task); finalized {
						return resTask, ferr
					} else if nudge != "" {
						messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
					}
					continue
				}
				task.Summary = res.Text
				task.Status = TaskStatusCompleted
				r.State.AddMessageFinal(session.RoleAssistant, res.Text, session.ContentTypeMarkdown)
				return task, nil
			}

			if isLengthFinish(res.FinishReason) {
				iteration++
				r.withStats(func(s *turnStats) { s.m.Iterations = iteration })
				messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: res.Text, ToolCalls: res.ToolCalls})
				for _, call := range res.ToolCalls {
					messages = append(messages, schema.ChatMessage{
						Role:       schema.RoleTool,
						ToolCallID: call.ID,
						Content:    fmt.Sprintf("Tool call %s was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the call with complete arguments.", call.Name),
					})
				}
				continue
			}

			iteration++
			consecutiveEmpty = 0
			r.withStats(func(s *turnStats) { s.m.Iterations = iteration })
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: res.Text, ToolCalls: res.ToolCalls})
			producedValidAction = true
			if inProgress := r.State.InProgress(); inProgress.Reasoning != "" {
				r.State.LogThinking(session.ThinkingEntry{
					Text:      inProgress.Reasoning,
					Duration:  time.Since(inProgress.StartedAt),
					StartedAt: inProgress.StartedAt,
				})
			}

			resultMsgs, execErr := r.executeNativeToolCalls(ctx, res.ToolCalls)
			if execErr != nil {
				return task, r.fail(task, execErr)
			}
			messages = append(messages, resultMsgs...)
			if finalized, resTask, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task); finalized {
				return resTask, ferr
			} else if nudge != "" {
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
			}
			continue
		}

		action, parseErr := ParseAction(raw)
		if parseErr != nil {
			consecutiveParseFailures++
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
			messages = append(messages, BuildCorrectionMessage(parseErr))
			r.withStats(func(s *turnStats) { s.m.ParseFailures++ })
			if consecutiveParseFailures == 2 {
				repairMsg := BuildRepairMessage()
				messages = append(messages, repairMsg)
				r.State.AddMessage(session.RoleSystem, repairMsg.Content, session.ContentTypePlain)
				if turnProvider.Capabilities(ctx).JSONMode && r.ResponseFormat == nil {
					r.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}
				}
			}
			if consecutiveParseFailures >= maxConsecutiveParseFailures {
				break
			}
			continue
		}
		consecutiveParseFailures = 0
		consecutiveEmpty = 0
		iteration++
		r.withStats(func(s *turnStats) { s.m.Iterations = iteration })
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
		producedValidAction = true

		if inProgress := r.State.InProgress(); inProgress.Reasoning != "" && action.Type != ActionAnswer && action.Type != ActionFinal {
			r.State.LogThinking(session.ThinkingEntry{
				Text:      inProgress.Reasoning,
				Duration:  time.Since(inProgress.StartedAt),
				StartedAt: inProgress.StartedAt,
			})
		}

		if len(action.Actions) > 0 {
			if err := r.allReadOnly(action.Actions); err != nil {
				messages = append(messages, BuildCorrectionMessage(err))
				continue
			}
			resultMsgs, execErr := r.executeActions(ctx, action.Actions)
			if execErr != nil {
				return task, r.fail(task, execErr)
			}
			messages = append(messages, resultMsgs...)
			if finalized, res, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task); finalized {
				return res, ferr
			} else if nudge != "" {
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
			}
			continue
		}

		switch action.Type {
		case ActionAnswer, ActionFinal:
			task.Summary = action.Content
			task.Status = TaskStatusCompleted
			r.State.AddMessageFinal(session.RoleAssistant, action.Content, session.ContentTypeMarkdown)
			return task, nil
		case ActionToolCall, ActionPatch:
			resultMsgs, err := r.executeToolCall(ctx, action)
			if err != nil {
				return task, r.fail(task, err)
			}
			messages = append(messages, resultMsgs...)
			if finalized, res, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task); finalized {
				return res, ferr
			} else if nudge != "" {
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
			}
		case ActionAskUser:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("ask_user is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
			answer, waitErr := r.requestAnswer(ctx, action.Content)
			if waitErr != nil {
				return task, r.fail(task, waitErr)
			}
			// An ask_user round-trip consumes a turn of the budget: a model
			// that re-asks the same (or a declined) question would otherwise
			// loop ask→decline→ask without the budget ever decreasing. A
			// declined answer is non-progress and is recorded as idle so the
			// stall detector sees a repeated ask as churn too.
			iteration++
			r.withStats(func(s *turnStats) { s.m.Iterations = iteration })
			if strings.TrimSpace(answer) == "" {
				consecutiveEmpty++
				r.trackerMu.Lock()
				r.tracker.recordIdle("ask_user declined")
				r.trackerMu.Unlock()
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "The user declined to answer. Proceed with your best judgment and state the assumption you made."})
				if finalized, resTask, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task); finalized {
					return resTask, ferr
				} else if nudge != "" {
					messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
				}
			} else {
				consecutiveEmpty = 0
				r.State.AddMessage(session.RoleUser, answer, session.ContentTypePlain)
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "User answered: " + answer})
			}
		default:
			messages = append(messages, BuildCorrectionMessage(fmt.Errorf("unsupported action type %q", action.Type)))
		}
	}

	if consecutiveParseFailures >= maxConsecutiveParseFailures {
		if producedValidAction {
			if res, ferr := r.finalize(ctx, turnProvider, turnModel, messages, task, reasonMalformed); ferr == nil {
				return res, nil
			}
		}
		task.Status = TaskStatusFailed
		r.State.AddMessage(session.RoleSystem, "Agent stopped: model output could not be parsed after consecutive attempts.", session.ContentTypePlain)
		return task, ErrModelOutputMalformed
	}

	if producedValidAction {
		if res, ferr := r.finalize(ctx, turnProvider, turnModel, messages, task, reasonExhausted); ferr == nil {
			return res, nil
		}
	}
	task.Status = TaskStatusFailed
	r.State.AddMessage(session.RoleSystem, "Agent stopped: exceeded max tool iterations without a final answer.", session.ContentTypePlain)
	return task, ErrMaxIterationsExceeded
}

// maybeFinalizeOnStall handles a hard stall. For the interactive general
// role it asks the user how to proceed (opencode's doom-loop permission
// pattern): a non-empty answer is returned as guidance for the caller to
// append as a user message, and repeat counts are reset so the loop gets a
// fresh start. An empty answer, or any non-general (swarm) role, falls back
// to finalize, which produces a flagged salvaged summary.
func (r *Runner) maybeFinalizeOnStall(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task) (finalized bool, res *Task, err error, guidance string) {
	r.trackerMu.Lock()
	a := r.tracker.assess()
	name, args, _ := r.tracker.lastCall()
	r.trackerMu.Unlock()

	if a != assessHardStall {
		return false, task, nil, ""
	}
	r.withStats(func(s *turnStats) { s.m.HardStalls++ })

	if r.role() == RoleGeneral {
		question := fmt.Sprintf(
			"I appear to be stuck: I have repeated %s with args %s without making progress. Reply with guidance on how to proceed, or send an empty reply to stop and get a summary of progress so far.",
			name, args,
		)
		answer, waitErr := r.requestAnswer(ctx, question)
		if waitErr != nil {
			return true, task, r.fail(task, waitErr), ""
		}
		if strings.TrimSpace(answer) != "" {
			r.trackerMu.Lock()
			r.tracker.resetCounts()
			r.trackerMu.Unlock()
			r.State.AddMessage(session.RoleUser, answer, session.ContentTypePlain)
			return false, task, nil, "User guidance: " + answer
		}
	}

	res, ferr := r.finalize(ctx, p, model, messages, task, reasonStalled)
	return true, res, ferr, ""
}

func (r *Runner) resolveRoute(task *Task) (provider.Provider, string, routing.Route) {
	turnProvider := r.Provider
	turnModel := r.Model
	if r.RouteResolver == nil {
		return turnProvider, turnModel, routing.Route{}
	}

	route, resolvedProvider, err := r.RouteResolver.Resolve(routing.TaskProfile{Class: string(task.Class)})
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

// chatWithRetry calls chatOnce up to MaxRetries+1 times, returning the first
// success. This is the loop's only retry point: transport-level failures
// (connection errors, malformed HTTP responses) are retried; malformed
// model *output* is handled separately in Run via BuildCorrectionMessage; it
// is not retried here because it is not a chatOnce failure — chatOnce
// succeeded, the text just didn't parse as an action.
func (r *Runner) chatWithRetry(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (chatResult, error) {
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, true)
}

func (r *Runner) chatWithRetryNoNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage) (chatResult, error) {
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, false)
}

func (r *Runner) chatWithRetryWithNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, includeNativeTools bool) (chatResult, error) {
	attempts := r.MaxRetries + 1
	var lastErr error
	for i := 0; i < attempts; i++ {
		res, err := r.chatOnce(ctx, p, model, messages, includeNativeTools)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	return chatResult{}, lastErr
}

func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, includeNativeToolsOpt ...bool) (chatResult, error) {
	if r.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.RequestTimeout)
		defer cancel()
	}

	includeNativeTools := true
	if len(includeNativeToolsOpt) > 0 {
		includeNativeTools = includeNativeToolsOpt[0]
	}
	var responseFormat *schema.ResponseFormat
	var tools []schema.ToolDefinition
	if r.NativeTools {
		if includeNativeTools {
			tools = r.buildToolDefinitions()
		}
	} else {
		responseFormat = r.ResponseFormat
	}

	events, err := p.Chat(ctx, schema.ChatRequest{
		Model:          model,
		Messages:       messages,
		Stream:         true,
		ResponseFormat: responseFormat,
		Tools:          tools,
	})
	if err != nil {
		return chatResult{}, err
	}

	r.State.BeginStreaming()
	r.State.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "thinking...", StartedAt: r.Now()})
	defer r.State.EndStreaming()
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})

	var sb strings.Builder
	var usage *schema.TokenUsage
	var toolCalls []schema.ToolCall
	var finishReason string
	for event := range events {
		switch event.Type {
		case schema.ChatEventDelta:
			if event.Kind == schema.DeltaThinking {
				r.State.AppendThinking(event.Delta)
			} else {
				sb.WriteString(event.Delta)
			}
		case schema.ChatEventError:
			return chatResult{}, event.Err
		case schema.ChatEventDone:
			usage = event.Usage
			toolCalls = event.ToolCalls
			finishReason = event.FinishReason
		}
	}
	if r.UsageObserver != nil && usage != nil {
		r.UsageObserver(usage.PromptTokens, usage.CompletionTokens)
	}
	if usage != nil {
		r.withStats(func(s *turnStats) {
			s.m.PromptTokens += usage.PromptTokens
			s.m.CompletionTokens += usage.CompletionTokens
		})
	}
	return chatResult{Text: sb.String(), ToolCalls: toolCalls, FinishReason: finishReason}, nil
}

func (r *Runner) buildToolDefinitions() []schema.ToolDefinition {
	tools := r.Registry.List()
	defs := make([]schema.ToolDefinition, 0, len(tools)+1)
	for _, tool := range tools {
		parameters := tool.Schema
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object"}`)
		}
		defs = append(defs, schema.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  parameters,
		})
	}
	if r.role() == RoleGeneral {
		defs = append(defs, schema.ToolDefinition{
			Name:        "ask_user",
			Description: "Ask the user one specific clarifying question.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`),
		})
	}
	return defs
}

// executeToolCall evaluates policy, blocks for user approval if required,
// executes the tool, logs an audit event, and returns one or more
// schema.ChatMessages to feed the result (or failure reason) back to the
// model. Loop-detection/stall handling is done by the caller (RunTask), not
// here — this only records the call into the progress tracker.
func (r *Runner) executeToolCall(ctx context.Context, action ModelAction) ([]schema.ChatMessage, error) {
	toolName := action.Tool
	if action.Type == ActionPatch {
		toolName = "file.write_patch"
	}
	toolCallID := action.ToolCallID

	tool, ok := r.Registry.Lookup(toolName)
	if !ok {
		r.countToolCall(true, false)
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "unknown tool", toolCallID)}, nil
	}

	args := action.Args
	if action.Type == ActionPatch {
		encoded, err := json.Marshal(map[string]string{"patch": action.Content})
		if err != nil {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "failed to encode patch arguments", toolCallID)}, nil
		}
		args = encoded
	}

	argsMap := map[string]interface{}{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "arguments are not a valid JSON object", toolCallID)}, nil
		}
	}

	normalizedArgs, normErr := normalizeArgs(args)
	if normErr != nil {
		r.countToolCall(true, false)
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "failed to normalize arguments", toolCallID)}, nil
	}

	// Cacheable read-only cache lookup.
	if tool.Cacheable {
		if cached, hit := r.State.GetTurnToolResult(toolName, normalizedArgs); hit {
			r.trackerMu.Lock()
			count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(cached.Content))
			r.trackerMu.Unlock()
			logged := cached
			logged.Summary = "(cached) " + logged.Summary
			call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
			event := registry.NewAuditEvent(r.Now(), tool, call, logged, registry.ApprovalNotRequired, nil)
			r.State.LogToolCall(event)
			r.countToolCall(false, true)
			msg := r.buildCachedToolResultMessage(toolName, cached, toolCallID)
			msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
			return []schema.ChatMessage{msg}, nil
		}
	}

	r.Policy.SetSessionRules(r.State.SessionRules())
	decision, reason, err := r.Policy.Evaluate(toolName, argsMap)
	if err != nil {
		r.countToolCall(true, false)
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, err.Error(), toolCallID)}, nil
	}

	approval := registry.ApprovalNotRequired
	switch decision {
	case policy.DecisionDeny:
		event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("denied: %s", reason))
		r.State.LogToolCall(event)
		r.countToolCall(true, false)
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "denied by policy: "+reason, toolCallID)}, nil
	case policy.DecisionConfirm:
		approved, edited, waitErr := r.requestApproval(ctx, tool, toolName, args, argsMap, reason)
		if waitErr != nil {
			return nil, waitErr
		}
		if !approved {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, errors.New("denied by user"))
			r.State.LogToolCall(event)
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "denied by user", toolCallID)}, nil
		}
		approval = registry.ApprovalApproved
		if edited != "" {
			if toolName == "shell.run" {
				argsMap["command"] = edited
				if remarshalled, merr := json.Marshal(argsMap); merr == nil {
					args = remarshalled
					normalizedArgs, _ = normalizeArgs(args)
				}
			} else {
				if json.Valid([]byte(edited)) {
					args = json.RawMessage(edited)
					normalizedArgs, _ = normalizeArgs(args)
					_ = json.Unmarshal(args, &argsMap)
				}
			}
		}
	case policy.DecisionAllow:
		approval = registry.ApprovalNotRequired
	}

	label := toolName
	if command, ok := argsMap["command"].(string); ok && command != "" {
		label = fmt.Sprintf("%s: %s", toolName, command)
	}
	r.State.SetActivity(session.Activity{Kind: session.ActivityTool, Label: label, StartedAt: r.Now()})
	r.State.SetActiveToolCall(session.ActiveToolCall{
		Name:      toolName,
		Args:      SummarizeToolArgs(toolName, args),
		StartedAt: r.Now(),
	})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
	defer r.State.ClearActiveToolCall()

	if r.WriteGate != nil && tool.Risk != registry.RiskReadOnly {
		release := r.WriteGate.Acquire()
		defer release()
	}

	callID := toolCallID
	if callID == "" {
		callID = fmt.Sprintf("call_%d", r.Now().UnixNano())
	}
	call := registry.ToolCall{ID: callID, Name: toolName, Args: args}
	result, execErr := tool.Handler(ctx, call)
	if execErr != nil {
		event := registry.NewAuditEvent(r.Now(), tool, call, registry.ToolResult{}, approval, execErr)
		r.State.LogToolCall(event)
		r.trackerMu.Lock()
		count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(execErr.Error()))
		r.trackerMu.Unlock()
		r.countToolCall(true, false)
		msg := r.buildToolErrorMessage(toolName, execErr.Error(), toolCallID)
		msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
		return []schema.ChatMessage{msg}, nil
	}

	summarized := SummarizeToolResult(toolName, result, 0) // per-tool line limits only; 0 keeps the default char cap out of play here
	summarized = spillToolResult(r.State.WorkingDir, toolName, summarized, r.MaxToolResultChars)
	if tool.Cacheable {
		r.State.SetTurnToolResult(toolName, normalizedArgs, summarized)
	}
	event := registry.NewAuditEvent(r.Now(), tool, call, summarized, approval, nil)
	r.State.LogToolCall(event)

	msg := r.buildToolResultMessage(toolName, summarized, toolCallID)
	r.trackerMu.Lock()
	count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(summarized.Content))
	r.trackerMu.Unlock()
	msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
	r.countToolCall(false, false)
	return []schema.ChatMessage{msg}, nil
}

func (r *Runner) buildToolResultMessage(name string, result registry.ToolResult, toolCallID string) schema.ChatMessage {
	if toolCallID != "" {
		return BuildNativeToolResultMessage(name, result, toolCallID)
	}
	return BuildToolResultMessage(name, result)
}

func (r *Runner) buildCachedToolResultMessage(name string, result registry.ToolResult, toolCallID string) schema.ChatMessage {
	if toolCallID != "" {
		return BuildCachedNativeToolResultMessage(name, result, toolCallID)
	}
	return BuildCachedToolResultMessage(name, result)
}

func (r *Runner) buildToolErrorMessage(name, reason, toolCallID string) schema.ChatMessage {
	if toolCallID != "" {
		return BuildNativeToolErrorMessage(name, reason, toolCallID)
	}
	return BuildToolErrorMessage(name, reason)
}

func (r *Runner) executeNativeToolCalls(ctx context.Context, calls []schema.ToolCall) ([]schema.ChatMessage, error) {
	msgs := make([]schema.ChatMessage, 0, len(calls))
	for _, call := range calls {
		if call.Name == "ask_user" {
			msg, err := r.executeNativeAskUser(ctx, call)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, msg)
			continue
		}
		resultMsgs, err := r.executeToolCall(ctx, ModelAction{
			Type:       ActionToolCall,
			Tool:       call.Name,
			Args:       call.Args,
			ToolCallID: call.ID,
		})
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, resultMsgs...)
	}
	return msgs, nil
}

func (r *Runner) executeNativeAskUser(ctx context.Context, call schema.ToolCall) (schema.ChatMessage, error) {
	if r.role() != RoleGeneral {
		return BuildNativeToolErrorMessage("ask_user", fmt.Sprintf("ask_user is not available for the %s role", r.role()), call.ID), nil
	}
	var payload struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(call.Args, &payload); err != nil || strings.TrimSpace(payload.Question) == "" {
		r.countToolCall(true, false)
		return BuildNativeToolErrorMessage("ask_user", "arguments must include a question string", call.ID), nil
	}
	answer, waitErr := r.requestAnswer(ctx, payload.Question)
	if waitErr != nil {
		return schema.ChatMessage{}, waitErr
	}
	r.countToolCall(false, false)
	if strings.TrimSpace(answer) == "" {
		return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "The user declined to answer. Proceed with your best judgment and state the assumption you made."}, nil
	}
	r.State.AddMessage(session.RoleUser, answer, session.ContentTypePlain)
	return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "User answered: " + answer}, nil
}

func (r *Runner) allReadOnly(actions []ModelAction) error {
	for _, a := range actions {
		if a.Type != ActionToolCall {
			return fmt.Errorf("action type %q in actions array is not a tool_call", a.Type)
		}
		tool, ok := r.Registry.Lookup(a.Tool)
		if !ok {
			return fmt.Errorf("unknown tool %q in actions array", a.Tool)
		}
		if tool.Risk != registry.RiskReadOnly {
			return fmt.Errorf("tool %q is read-write, not read-only — actions array only supports read-only tools", a.Tool)
		}
	}
	return nil
}

func (r *Runner) executeActions(ctx context.Context, actions []ModelAction) ([]schema.ChatMessage, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, r.MaxParallelActions)
	results := make([][]schema.ChatMessage, len(actions))

	for i, a := range actions {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, act ModelAction) {
			defer wg.Done()
			defer func() { <-sem }()
			msgs, err := r.executeToolCall(ctx, act)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			results[idx] = msgs
		}(i, a)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	var flat []schema.ChatMessage
	for _, msgs := range results {
		flat = append(flat, msgs...)
	}
	return flat, nil
}

// requestApproval blocks until the TUI (or any caller driving
// session.PendingToolCall) resolves the pending approval, or ctx is
// cancelled. It follows the exact protocol internal/app/tui/model.go already
// implements for Milestone F/G: set PendingApproval, wait on ResponseChan,
// clear PendingApproval.
func (r *Runner) requestApproval(ctx context.Context, tool registry.Tool, toolName string, args json.RawMessage, argsMap map[string]interface{}, reason string) (approved bool, edited string, err error) {
	command, _ := argsMap["command"].(string)
	if command == "" {
		command = toolName
	}

	diff := ""
	if toolName == "file.write_patch" {
		if patchText, ok := argsMap["patch"].(string); ok {
			if preview, previewErr := PreviewPatchDiff(r.State.WorkingDir, patchText); previewErr == nil {
				diff = preview
			}
		}
	}

	tc := &session.PendingToolCall{
		ID:           fmt.Sprintf("call_%d", r.Now().UnixNano()),
		Name:         toolName,
		Args:         string(args),
		Command:      command,
		Risk:         string(tool.Risk),
		Reason:       reason,
		Diff:         diff,
		Schema:       tool.Description,
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	r.State.SetPendingApproval(tc)

	label := fmt.Sprintf("waiting for approval: %s", command)
	r.State.SetActivity(session.Activity{Kind: session.ActivityApproval, Label: label, StartedAt: r.Now()})

	select {
	case decision := <-tc.ResponseChan:
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return decision.Approved, decision.Edited, nil
	case <-ctx.Done():
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return false, "", ctx.Err()
	}
}

func (r *Runner) requestAnswer(ctx context.Context, question string) (string, error) {
	r.State.AddMessage(session.RoleAssistant, question, session.ContentTypeMarkdown)
	q := &session.PendingQuestion{
		Question:     question,
		ResponseChan: make(chan string, 1),
	}
	r.State.SetPendingQuestion(q)
	r.State.SetActivity(session.Activity{Kind: session.ActivityQuestion, Label: "waiting for your answer", StartedAt: r.Now()})

	select {
	case answer := <-q.ResponseChan:
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return answer, nil
	case <-ctx.Done():
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return "", ctx.Err()
	}
}

func skillsChanged(prev, curr []string) bool {
	if len(prev) != len(curr) {
		return true
	}
	prevSet := make(map[string]bool, len(prev))
	for _, s := range prev {
		prevSet[s] = true
	}
	for _, s := range curr {
		if !prevSet[s] {
			return true
		}
	}
	return false
}
