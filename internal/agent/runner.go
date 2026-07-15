package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/hooks"
	"marshal/internal/llm/catalog"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/permissions"
	"marshal/internal/skills"
	"marshal/internal/tools/patch"
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

// ErrRequestTimedOut is returned by requestApproval / requestQuestions when
// the TUI (or bridge channel) does not respond within RequestTimeout. It
// prevents a goroutine leak when the TUI exits without sending a decision.
var ErrRequestTimedOut = errors.New("agent: request timed out")

const defaultRequestTimeout = 5 * time.Minute

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

// Snapshotter tracks and restores shadow-git snapshots of the working tree.
// It is defined here so the agent package can use snapshots without importing
// internal/snapshot.
type Snapshotter interface {
	Track(ctx context.Context) (string, error)
	Diff(ctx context.Context, hash string) (string, error)
	Restore(ctx context.Context, hash string) error
	Revert(ctx context.Context, fromHash, toHash string) error
}

// SnapshotRecorder persists snapshot metadata to durable storage.
type SnapshotRecorder interface {
	SaveSnapshot(sessionID string, turnIndex int, hash string, files []string, at time.Time) (int64, error)
}

// WriteGate serialises non-read-only tool execution across concurrently
// running Runners. The swarm sets one shared gate on every role runner so
// that "only one agent may write files at a time" (docs/07 swarm safety)
// holds even if a future orchestration mode overlaps role turns.
type WriteGate interface {
	// Acquire blocks until the gate is free and returns its release func.
	Acquire() (release func())
}

// HookRunner executes user-configured pre_tool_use and turn_end lifecycle
// hooks. The interface is declared package-local so unit tests can supply a
// fake without depending on internal/hooks. A nil HookRunner on a Runner
// disables hook execution entirely.
type HookRunner interface {
	RunPreToolUse(ctx context.Context, input hooks.PreToolUseInput) (hooks.Output, error)
	RunTurnEnd(ctx context.Context, input hooks.TurnEndInput) (hooks.Output, error)
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
//
// Concurrency contract:
//
//   - A *Runner is NOT safe for concurrent calls to Run() / RunTask() on
//     the same instance. Callers (TUI, swarm orchestrator) must serialise
//     RunTask invocations.
//
//   - A *Runner IS safe for sequential re-use: after one RunTask returns,
//     the next call starts from a clean per-turn state. Fields that persist
//     across calls (Provider, Registry, Policy, State, Model, RouteResolver,
//     Now, MaxToolIterations, MaxRetries, MaxTurnContextTokens, RequestTimeout,
//     ResponseFormat (seed), NativeTools, MaxParallelActions, MaxToolResultChars,
//     ForceClass, SkillIndex, Role, WriteGate, UsageObserver,
//     MetricsObserver, Snapshotter, SnapshotRecorder, HookRunner, TitleGenerator,
//     RunTaskFunc, PlanFirst, HistoryBudgetTokens, MemoryProvider, ProjectID,
//     fileIndexCache) are initialised once; resolveRoute may grow
//     MaxTurnContextTokens (monotonically) when the route-resolved context
//     window exceeds the configured value. The seed persists across RunTask
//     calls.
//
//   - Per-turn state (tracker, stats, route, pressureMessageSent,
//     consecutiveParseFailures, consecutiveEmpty) is reset at the top of
//     RunTask and never shared across calls.
//
//   - tracker, stats, and ForceClass have dedicated mutexes for their
//     accessor methods (withStats, trackerMu, forceClassMu). All other
//     field reads and writes are not synchronised — hence the
//     single-caller-at-a-time rule.
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

	// PlanFirst enables the legacy pre-loop planning round-trip. Default
	// false: planning happens inside the loop like every mainstream agent
	// (crush/kimi/opencode/pi), saving a model call and avoiding a pinned
	// plan that mid-turn compaction can orphan.
	PlanFirst bool

	// HistoryBudgetTokens caps replayed prior-turn history (0 = default).
	HistoryBudgetTokens int

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

	Snapshotter      Snapshotter
	SnapshotRecorder SnapshotRecorder

	// HookRunner, when set, runs pre_tool_use and turn_end lifecycle hooks
	// against every tool call. The interface is kept package-local so tests
	// can substitute a fake without depending on internal/hooks internals.
	// nil disables hook execution.
	HookRunner HookRunner

	// TitleGenerator, when set, is invoked once per session at the end of the
	// first user turn to produce a short session title (F13). Fire-and-forget.
	TitleGenerator TitleGenerator

	// RunTaskFunc overrides RunTask for testing (see the named type below).
	RunTaskFunc RunTaskFunc

	forceClassMu sync.Mutex
	tracker      *progressTracker
	trackerMu    sync.Mutex
	stats        *turnStats
	statsMu      sync.Mutex

	// iterationBudget is set by RunTask to point to its local iteration
	// counter so that executeNativeAskUser / executeNativeQuestionAsk can
	// increment the budget when they perform a native ask round-trip
	// (mirroring the envelope path's iteration++ in ActionAskUser /
	// ActionQuestionAsk). Nil outside of RunTask.
	iterationBudget *int

	// fileIndexCache memoises the per-project file index across RunTask
	// calls and across steering-message drains. Auto-invalidates when the
	// projectID changes (see fileIndexCache.get).
	fileIndexCache fileIndexCache
}

// RunTaskFunc, if non-nil, overrides RunTask for testing. It returns a
// canned Task without calling the provider. Used by the SDD orchestrator
// tests to inject scripted responses.
type RunTaskFunc func(ctx context.Context, prompt string) (*Task, error)

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

func (r *Runner) SetPolicyRules(rules []config.PermissionRule) {
	prules := make([]permissions.Rule, 0, len(rules))
	for _, rl := range rules {
		prules = append(prules, permissions.Rule{
			Permission: rl.Permission,
			Pattern:    rl.Pattern,
			Action:     permissions.Action(rl.Action),
		})
	}
	r.Policy.SetRules(prules)
}

func (r *Runner) CopyFrom(other *Runner) {
	if other == nil {
		return
	}
	r.Provider = other.Provider
	r.Registry = other.Registry
	r.Policy = other.Policy
	r.State = other.State
	r.Model = other.Model
	r.RouteResolver = other.RouteResolver
	r.MemoryProvider = other.MemoryProvider
	r.ProjectID = other.ProjectID
	r.Now = other.Now
	r.MaxToolIterations = other.MaxToolIterations
	r.MaxRetries = other.MaxRetries
	r.MaxTurnContextTokens = other.MaxTurnContextTokens
	r.RequestTimeout = other.RequestTimeout
	r.ResponseFormat = other.ResponseFormat
	r.NativeTools = other.NativeTools
	r.MaxParallelActions = other.MaxParallelActions
	r.MaxToolResultChars = other.MaxToolResultChars
	r.ForceClass = other.ForceClass
	r.SkillIndex = other.SkillIndex
	r.Role = other.Role
	r.WriteGate = other.WriteGate
	r.UsageObserver = other.UsageObserver
	r.MetricsObserver = other.MetricsObserver
	r.Snapshotter = other.Snapshotter
	r.SnapshotRecorder = other.SnapshotRecorder
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
	// Early return for test injection: when RunTaskFunc is set, bypass the
	// full provider loop and return the canned result directly. Must be
	// before the defer so a nil State on the test runner does not panic.
	if r.RunTaskFunc != nil {
		return r.RunTaskFunc(ctx, goal)
	}

	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})

	priorTranscript := r.State.Messages()
	firstTurn := len(priorTranscript) <= 1
	if r.TitleGenerator != nil && firstTurn {
		defer func(g string) {
			if r.State.Title() == "" && !r.State.TitleManuallySet() {
				r.TitleGenerator.Generate(context.Background(), g)
			}
		}(goal)
	}
	r.State.AddMessage(session.RoleUser, goal, session.ContentTypePlain)
	r.State.ClearTurnToolCache()
	r.State.IncrementTurnIndex()
	if r.Snapshotter != nil {
		if _, err := r.Snapshotter.Track(ctx); err != nil {
			r.State.Logger().Warn("turn-start snapshot failed", "error", err)
		}
	}

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

	effectiveRF := r.ResponseFormat

	// F18: extract @file references from the goal and pin them into the
	// context pack before it is appended to the model messages. Unknown
	// paths and unreadable files are silently skipped (see
	// extractPinnedFiles); the TUI only inserts the literal "@path" text.
	if pinned := extractPinnedFiles(goal, r, r.ProjectID); len(pinned) > 0 {
		pack := r.State.ContextPack()
		pack = contextpack.PinFiles(pack, pinned)
		r.State.SetContextPack(pack)
	}

	messages := []schema.ChatMessage{
		BuildSystemPromptWithDeferred(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools),
	}
	messages = appendContextPackMessage(messages, r.State.ContextPack())
	if r.role() == RoleGeneral {
		messages = append(messages, buildHistoryMessages(priorTranscript, r.HistoryBudgetTokens)...)
	}
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})

	if r.PlanFirst && task.Class != ClassQuestion {
		task.Status = TaskStatusPlanning
		planMessages := append(append([]schema.ChatMessage{}, messages...), BuildPlanningPrompt(goal))
		planRes, err := r.chatWithRetryNoNativeTools(ctx, turnProvider, turnModel, planMessages, effectiveRF)
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
			messages = []schema.ChatMessage{BuildSystemPromptWithDeferred(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools)}
			messages = appendContextPackMessage(messages, updatedPack)
			if r.role() == RoleGeneral {
				messages = append(messages, buildHistoryMessages(priorTranscript, r.HistoryBudgetTokens)...)
			}
			messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
		}
		r.State.AddMessage(session.RoleAssistant, planText, session.ContentTypePlan)
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: "Plan:\n" + planText})
	}

	task.Status = TaskStatusExecuting
	lastRenderedSkills := r.State.ActiveSkills()
	pressureMessageSent := false
	producedValidAction := false
	consecutiveParseFailures := 0
	consecutiveEmpty := 0
	iteration := 0
	r.iterationBudget = &iteration
	defer func() { r.iterationBudget = nil }()
	turnEndContinued := false
	runTurnEnd := func(messages []schema.ChatMessage, task *Task) ([]schema.ChatMessage, bool, error) {
		if r.HookRunner == nil || turnEndContinued {
			return messages, false, nil
		}
		out, err := r.HookRunner.RunTurnEnd(ctx, hooks.TurnEndInput{
			Event:     hooks.EventTurnEnd,
			SessionID: r.State.SessionID(),
			TurnIndex: r.State.TurnIndex(),
			WorkDir:   r.State.WorkingDir,
		})
		if err != nil {
			return messages, false, err
		}
		if out.Continue && strings.TrimSpace(out.Message) != "" {
			turnEndContinued = true
			msg := strings.TrimSpace(out.Message)
			r.State.AddMessage(session.RoleUser, msg, session.ContentTypePlain)
			messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: msg})
			task.Status = TaskStatusExecuting
			return messages, true, nil
		}
		return messages, false, nil
	}
	for {
		if iteration >= r.MaxToolIterations {
			break
		}
		r.State.SetToolBudget(session.ToolBudget{Used: iteration, Max: r.MaxToolIterations})

		// F16: drain steering messages typed mid-turn and inject them as
		// user messages before the next model call. Runs before the
		// context-pressure/summarize check so steering is part of the live
		// context rather than getting compacted away. steeringArrived also
		// guards the doom-loop stall finalize below — if the user just
		// intervened, the loop is no longer auto-iterating.
		steeringArrived := false
		var steeringPins []contextpack.FileSnippet
		for _, msg := range r.State.DrainSteering() {
			steeringPins = append(steeringPins, extractPinnedFiles(msg, r, r.ProjectID)...)
			messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: msg})
			steeringArrived = true
		}
		if len(steeringPins) > 0 {
			pack := contextpack.PinFiles(r.State.ContextPack(), steeringPins)
			r.State.SetContextPack(pack)
			messages = appendContextPackMessage(messages, pack)
		}

		if !pressureMessageSent && r.MaxToolIterations-iteration <= finalizePressureThreshold {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: finalizePressureMessage})
			r.State.AddMessage(session.RoleSystem, finalizePressureMessage, session.ContentTypePlain)
			pressureMessageSent = true
		}

		currentSkills := r.State.ActiveSkills()
		if skillsChanged(lastRenderedSkills, currentSkills) {
			messages[0] = BuildSystemPromptWithDeferred(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, currentSkills, r.NativeTools)
			lastRenderedSkills = currentSkills
		}

		if r.MaxTurnContextTokens > 0 && estimateTokens(messages) > r.MaxTurnContextTokens {
			if fresh, serr := r.summarizeAndContinue(ctx, turnProvider, turnModel, messages, goal, effectiveRF); serr == nil {
				messages = fresh
				pressureMessageSent = false // the fresh transcript may legitimately approach the budget again
			} else {
				r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Context window exceeded and summarization failed: %s. The turn is being terminated to prevent transcript corruption.", serr), session.ContentTypePlain)
				return task, r.fail(task, fmt.Errorf("context overflow and summarization failed: %w", serr))
			}
		}

		res, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages, effectiveRF)
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
						return r.finalize(ctx, turnProvider, turnModel, messages, task, reasonEmpty, effectiveRF)
					}
					if !steeringArrived {
						if finalized, resTask, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task, effectiveRF); finalized {
							return resTask, ferr
						} else if nudge != "" {
							messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
						}
					}
					continue
				}
				task.Summary = res.Text
				task.Status = TaskStatusCompleted
				if next, continued, err := runTurnEnd(messages, task); err != nil {
					return task, r.fail(task, err)
				} else if continued {
					messages = next
					continue
				}
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
			if !steeringArrived {
				if finalized, resTask, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task, effectiveRF); finalized {
					return resTask, ferr
				} else if nudge != "" {
					messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
				}
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
					effectiveRF = &schema.ResponseFormat{Type: "json_object"}
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
			if !steeringArrived {
				if finalized, res, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task, effectiveRF); finalized {
					return res, ferr
				} else if nudge != "" {
					messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
				}
			}
			continue
		}

		switch action.Type {
		case ActionAnswer, ActionFinal:
			task.Summary = action.Content
			task.Status = TaskStatusCompleted
			if next, continued, err := runTurnEnd(messages, task); err != nil {
				return task, r.fail(task, err)
			} else if continued {
				messages = next
				continue
			}
			r.State.AddMessageFinal(session.RoleAssistant, action.Content, session.ContentTypeMarkdown)
			return task, nil
		case ActionToolCall, ActionPatch:
			resultMsgs, err := r.executeToolCall(ctx, action)
			if err != nil {
				return task, r.fail(task, err)
			}
			messages = append(messages, resultMsgs...)
			if !steeringArrived {
				if finalized, res, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task, effectiveRF); finalized {
					return res, ferr
				} else if nudge != "" {
					messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
				}
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
				if !steeringArrived {
					if finalized, resTask, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task, effectiveRF); finalized {
						return resTask, ferr
					} else if nudge != "" {
						messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
					}
				}
			} else {
				consecutiveEmpty = 0
				r.State.AddMessage(session.RoleUser, answer, session.ContentTypePlain)
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "User answered: " + answer})
			}
		case ActionQuestionAsk:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("question.ask is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
			if len(action.Questions) == 0 {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("question.ask requires a non-empty questions array")))
				continue
			}
			answers, waitErr := r.requestQuestions(ctx, action.Questions)
			if waitErr != nil {
				return task, r.fail(task, waitErr)
			}
			iteration++
			r.withStats(func(s *turnStats) { s.m.Iterations = iteration })
			allUnanswered := true
			for _, a := range answers {
				if a.Answer != session.AnswerUnanswered {
					allUnanswered = false
					break
				}
			}
			if allUnanswered {
				consecutiveEmpty++
				r.trackerMu.Lock()
				r.tracker.recordIdle("question.ask declined")
				r.trackerMu.Unlock()
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "The user declined to answer every question. Proceed with your best judgment and state the assumptions you made."})
				if !steeringArrived {
					if finalized, resTask, ferr, nudge := r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task, effectiveRF); finalized {
						return resTask, ferr
					} else if nudge != "" {
						messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
					}
				}
			} else {
				consecutiveEmpty = 0
				parts := make([]string, 0, len(answers))
				for _, a := range answers {
					parts = append(parts, fmt.Sprintf("%q: %q", a.Question, a.Answer))
				}
				r.State.AddMessage(session.RoleUser, "User answers: "+strings.Join(parts, ", "), session.ContentTypePlain)
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "User answers: " + strings.Join(parts, ", ")})
			}
		default:
			messages = append(messages, BuildCorrectionMessage(fmt.Errorf("unsupported action type %q", action.Type)))
		}
	}

	if consecutiveParseFailures >= maxConsecutiveParseFailures {
		if producedValidAction {
			if res, ferr := r.finalize(ctx, turnProvider, turnModel, messages, task, reasonMalformed, effectiveRF); ferr == nil {
				return res, nil
			}
		}
		task.Status = TaskStatusFailed
		r.State.AddMessage(session.RoleSystem, "Agent stopped: model output could not be parsed after consecutive attempts.", session.ContentTypePlain)
		return task, ErrModelOutputMalformed
	}

	if producedValidAction {
		if res, ferr := r.finalize(ctx, turnProvider, turnModel, messages, task, reasonExhausted, effectiveRF); ferr == nil {
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
func (r *Runner) maybeFinalizeOnStall(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, responseFormat *schema.ResponseFormat) (finalized bool, res *Task, err error, guidance string) {
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

	res, ferr := r.finalize(ctx, p, model, messages, task, reasonStalled, responseFormat)
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
		// configured value is the floor (max(configured, 0.85*window - reserved)).
		if effective > r.MaxTurnContextTokens {
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
func (r *Runner) chatWithRetry(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat) (chatResult, error) {
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, responseFormat, true)
}

func (r *Runner) chatWithRetryNoNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat) (chatResult, error) {
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, responseFormat, false)
}

func (r *Runner) chatWithRetryWithNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeTools bool) (chatResult, error) {
	attempts := r.MaxRetries + 1
	var lastErr error
	for i := 0; i < attempts; i++ {
		res, err := r.chatOnce(ctx, p, model, messages, responseFormat, includeNativeTools)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	return chatResult{}, lastErr
}

func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeToolsOpt ...bool) (chatResult, error) {
	if r.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.RequestTimeout)
		defer cancel()
	}

	includeNativeTools := true
	if len(includeNativeToolsOpt) > 0 {
		includeNativeTools = includeNativeToolsOpt[0]
	}
	var tools []schema.ToolDefinition
	if r.NativeTools {
		if includeNativeTools {
			tools = r.buildToolDefinitions()
		}
	}
	// responseFormat is passed in from RunTask (or a caller in the chain)
	// so that per-turn mutations (e.g. JSON-mode escalation after parse
	// failures) do not leak across RunTask calls on the same *Runner.

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
	deferred := make(map[string]bool)
	for _, t := range r.Registry.ListDeferred() {
		deferred[t.Name] = true
	}
	loaded := make(map[string]bool)
	if r.State != nil {
		for _, name := range r.State.LoadedToolNames() {
			loaded[name] = true
		}
	}
	defs := make([]schema.ToolDefinition, 0, len(tools)+1)
	for _, tool := range tools {
		// Deferred MCP tools are hidden from the agent's prompt by default
		// and only revealed once the agent explicitly opts in via
		// tools.select. Native tools are never deferred.
		if deferred[tool.Name] && !loaded[tool.Name] {
			continue
		}
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

// parseToolArgs splits a tool's raw JSON argument bytes into a Go map and
// the canonical normalized JSON used as the cache key. An empty / null
// argument normalises to {} and produces an empty map.
func parseToolArgs(args json.RawMessage) (map[string]interface{}, json.RawMessage, error) {
	argsMap := map[string]interface{}{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return nil, nil, fmt.Errorf("arguments are not a valid JSON object")
		}
	}
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to normalize arguments")
	}
	return argsMap, normalizedArgs, nil
}

// runPreToolUseHook runs the configured pre_tool_use hooks for toolName.
// The returned args are either the original (when the hook has nothing to
// say) or the rewritten JSON the hook wants the caller to use instead.
// When the hook signals block/halt the original args are still returned so
// the caller can record an accurate audit event for the original call.
func (r *Runner) runPreToolUseHook(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, hooks.Output, error) {
	if r.HookRunner == nil {
		return args, hooks.Output{}, nil
	}
	out, err := r.HookRunner.RunPreToolUse(ctx, hooks.PreToolUseInput{
		Event:     hooks.EventPreToolUse,
		ToolName:  toolName,
		Args:      append(json.RawMessage(nil), args...),
		SessionID: r.State.SessionID(),
		TurnIndex: r.State.TurnIndex(),
		WorkDir:   r.State.WorkingDir,
	})
	if err != nil {
		return args, out, err
	}
	if len(out.Rewrite) > 0 {
		return append(json.RawMessage(nil), out.Rewrite...), out, nil
	}
	return args, out, nil
}

// hookAuditMetadata converts a hooks.Output into a single-element slice
// suitable for AuditEvent.Hooks. Returns nil when no hook actually ran
// (HookCount == 0 and no decision came back), so the audit event is
// indistinguishable from a pre-hooks baseline record.
func hookAuditMetadata(out hooks.Output) []registry.HookMetadata {
	if out.HookCount == 0 && out.Decision == "" && !out.FailedOpen && len(out.Rewrite) == 0 {
		return nil
	}
	return []registry.HookMetadata{{
		Event:      hooks.EventPreToolUse,
		Decision:   string(out.Decision),
		Reason:     out.Reason,
		Rewrote:    len(out.Rewrite) > 0,
		FailedOpen: out.FailedOpen,
	}}
}

// policyLoopResult carries the result of running policy + approval for one
// iteration of the pre_tool_use rewrite loop. Messages is non-empty when the
// caller should return immediately (deny, user-declined, hook error, etc).
type policyLoopResult struct {
	Args           json.RawMessage
	NormalizedArgs json.RawMessage
	Approval       registry.ApprovalState
	Messages       []schema.ChatMessage
}

// handlePolicyDecision runs one iteration of policy + approval for the
// (possibly rewritten) args. The two early-return cases (deny /
// user-declined) are returned as Messages so the caller can short-circuit.
// On an error from approval (context cancel, etc.) it is returned to the
// caller. When the decision is allow, Approval is set to ApprovalNotRequired.
func (r *Runner) handlePolicyDecision(ctx context.Context, tool registry.Tool, toolName string, args json.RawMessage, argsMap map[string]interface{}, normalizedArgs json.RawMessage, decision policy.Decision, reason, toolCallID string) (policyLoopResult, error) {
	approval := registry.ApprovalNotRequired
	switch decision {
	case policy.DecisionDeny:
		event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("denied: %s", reason))
		r.State.LogToolCall(event)
		r.countToolCall(true, false)
		return policyLoopResult{Messages: []schema.ChatMessage{r.buildToolErrorMessage(toolName, "denied by policy: "+reason, toolCallID)}}, nil
	case policy.DecisionConfirm:
		approved, edited, waitErr := r.requestApproval(ctx, tool, toolName, args, argsMap, reason)
		if waitErr != nil {
			return policyLoopResult{}, waitErr
		}
		if !approved {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, errors.New("denied by user"))
			r.State.LogToolCall(event)
			r.countToolCall(true, false)
			return policyLoopResult{Messages: []schema.ChatMessage{r.buildToolErrorMessage(toolName, "denied by user", toolCallID)}}, nil
		}
		approval = registry.ApprovalApproved
		if edited != "" {
			var nerr error
			if toolName == "shell.run" {
				argsMap["command"] = edited
				if remarshalled, merr := json.Marshal(argsMap); merr == nil {
					args = remarshalled
					normalizedArgs, nerr = normalizeArgs(args)
					if nerr != nil {
						slog.Default().Warn("tool-arg-edit normalize failed", "tool", toolName, "error", nerr)
					}
				} else {
					slog.Default().Warn("tool-arg-edit marshal failed", "tool", toolName, "error", merr)
				}
			} else {
				if !json.Valid([]byte(edited)) {
					return policyLoopResult{}, fmt.Errorf("user-supplied edit for %s is not valid JSON: %q", toolName, edited)
				}
				args = json.RawMessage(edited)
				normalizedArgs, nerr = normalizeArgs(args)
				if nerr != nil {
					return policyLoopResult{}, fmt.Errorf("normalize edited %s args: %w", toolName, nerr)
				}
				updated := map[string]interface{}{}
				if uerr := json.Unmarshal(args, &updated); uerr != nil {
					return policyLoopResult{}, fmt.Errorf("decode edited %s args: %w", toolName, uerr)
				}
				argsMap = updated
				// Re-evaluate policy against the new args. The user has
				// already approved this call, so only a DecisionDeny from
				// the edited args should block execution. DecisionConfirm
				// is treated as "proceed" because the user has already
				// provided consent.
				newDecision, newReason, perr := r.Policy.Evaluate(toolName, argsMap)
				if perr != nil {
					return policyLoopResult{}, fmt.Errorf("policy re-evaluate after edit: %w", perr)
				}
				if newDecision == policy.DecisionDeny {
					return policyLoopResult{Messages: []schema.ChatMessage{
						r.buildToolErrorMessage(toolName, "denied by policy after edit: "+newReason, toolCallID),
					}}, nil
				}
				// DecisionAllow and DecisionConfirm: proceed — the user
				// already confirmed in the approval dialog.
			}
		}
	case policy.DecisionAllow:
		approval = registry.ApprovalNotRequired
	}
	return policyLoopResult{
		Args:           args,
		NormalizedArgs: normalizedArgs,
		Approval:       approval,
	}, nil
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

	// Bounded pre_tool_use rewrite loop. The hook runs AFTER policy +
	// user approval so the user is always in control of the original call
	// before any silent rewrite takes effect; a Rewrite value forces the
	// next iteration to re-parse, re-evaluate policy, and re-prompt the
	// user. The loop is hard-bounded at one rewrite to prevent a hostile
	// hook from thrashing the tool budget.
	var approval registry.ApprovalState
	var lastHookOut hooks.Output
	var originalApprovedArgs json.RawMessage
	var toolWasRewritten bool
	for rewriteCount := 0; ; rewriteCount++ {
		if rewriteCount > 1 {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "pre_tool_use hook rewrote arguments more than once", toolCallID)}, nil
		}

		argsMap, normalizedArgs, parseErr := parseToolArgs(args)
		if parseErr != nil {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, parseErr.Error(), toolCallID)}, nil
		}

		r.Policy.SetSessionRules(r.State.SessionRules())
		decision, reason, evalErr := r.Policy.Evaluate(toolName, argsMap)
		if evalErr != nil {
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, evalErr.Error(), toolCallID)}, nil
		}

		policyResult, err := r.handlePolicyDecision(ctx, tool, toolName, args, argsMap, normalizedArgs, decision, reason, toolCallID)
		if err != nil {
			return nil, err
		}
		if len(policyResult.Messages) > 0 {
			return policyResult.Messages, nil
		}
		args = policyResult.Args
		normalizedArgs = policyResult.NormalizedArgs
		approval = policyResult.Approval

		// Capture the user-approved args before any hook rewrite so the
		// audit event can record what the user approved vs what was
		// actually executed after a rewrite.
		preHookArgs := args
		rewrittenArgs, hookOut, hookErr := r.runPreToolUseHook(ctx, toolName, args)
		lastHookOut = hookOut
		if hookErr != nil {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("blocked by pre_tool_use hook: %s", hookErr.Error()))
			event.Hooks = hookAuditMetadata(hookOut)
			r.State.LogToolCall(event)
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "blocked by pre_tool_use hook: "+hookErr.Error(), toolCallID)}, nil
		}
		if hookOut.Decision == hooks.DecisionBlock {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("blocked by pre_tool_use hook: %s", hookOut.Reason))
			event.Hooks = hookAuditMetadata(hookOut)
			r.State.LogToolCall(event)
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "blocked by pre_tool_use hook: "+hookOut.Reason, toolCallID)}, nil
		}
		if hookOut.Decision == hooks.DecisionHalt {
			return nil, fmt.Errorf("halted by pre_tool_use hook: %s", hookOut.Reason)
		}
		if len(hookOut.Rewrite) > 0 {
			originalApprovedArgs = preHookArgs
			toolWasRewritten = true
			args = rewrittenArgs
			continue
		}
		break
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

	if r.Snapshotter != nil && r.SnapshotRecorder != nil && tool.Risk != registry.RiskReadOnly {
		files := changedFilesForTool(toolName, argsMap)
		if hash, snapErr := r.Snapshotter.Track(ctx); snapErr == nil && hash != "" {
			if _, saveErr := r.SnapshotRecorder.SaveSnapshot(r.State.SessionID(), r.State.TurnIndex(), hash, files, r.Now()); saveErr != nil {
				r.State.Logger().Warn("failed to record pre-write snapshot", "error", saveErr)
			}
		} else if snapErr != nil {
			r.State.Logger().Warn("pre-write snapshot failed", "error", snapErr)
		}
	}

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
		event.Hooks = hookAuditMetadata(lastHookOut)
		event.OriginalArgs = originalApprovedArgs
		event.Rewritten = toolWasRewritten
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
	event.Hooks = hookAuditMetadata(lastHookOut)
	event.OriginalArgs = originalApprovedArgs
	event.Rewritten = toolWasRewritten
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
		if call.Name == "question.ask" {
			msg, err := r.executeNativeQuestionAsk(ctx, call)
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
		return BuildNativeToolErrorMessage(call.Name, fmt.Sprintf("%s is not available for the %s role", call.Name, r.role()), call.ID), nil
	}
	var payload struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(call.Args, &payload); err != nil || strings.TrimSpace(payload.Question) == "" {
		r.countToolCall(true, false)
		return BuildNativeToolErrorMessage(call.Name, "arguments must include a question string", call.ID), nil
	}
	answer, waitErr := r.requestAnswer(ctx, payload.Question)
	if waitErr != nil {
		return schema.ChatMessage{}, waitErr
	}
	r.countToolCall(false, false)
	if r.iterationBudget != nil {
		*r.iterationBudget++
		r.withStats(func(s *turnStats) { s.m.Iterations = *r.iterationBudget })
	}
	if strings.TrimSpace(answer) == "" {
		r.trackerMu.Lock()
		r.tracker.recordIdle("ask_user declined")
		r.trackerMu.Unlock()
		return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "The user declined to answer. Proceed with your best judgment and state the assumption you made."}, nil
	}
	r.State.AddMessage(session.RoleUser, answer, session.ContentTypePlain)
	return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "User answered: " + answer}, nil
}

// executeNativeQuestionAsk handles the new structured question.ask tool,
// which accepts one or more questions with optional options/multi/other.
func (r *Runner) executeNativeQuestionAsk(ctx context.Context, call schema.ToolCall) (schema.ChatMessage, error) {
	if r.role() != RoleGeneral {
		return BuildNativeToolErrorMessage(call.Name, fmt.Sprintf("%s is not available for the %s role", call.Name, r.role()), call.ID), nil
	}
	var payload struct {
		Questions []session.Question `json:"questions"`
	}
	if err := json.Unmarshal(call.Args, &payload); err != nil || len(payload.Questions) == 0 {
		r.countToolCall(true, false)
		return BuildNativeToolErrorMessage(call.Name, "arguments must include a non-empty questions array", call.ID), nil
	}
	answers, waitErr := r.requestQuestions(ctx, payload.Questions)
	if waitErr != nil {
		return schema.ChatMessage{}, waitErr
	}
	r.countToolCall(false, false)
	if r.iterationBudget != nil {
		*r.iterationBudget++
		r.withStats(func(s *turnStats) { s.m.Iterations = *r.iterationBudget })
	}
	parts := []string{"User answers:"}
	allUnanswered := true
	for _, a := range answers {
		if a.Answer != session.AnswerUnanswered {
			allUnanswered = false
		}
		parts = append(parts, fmt.Sprintf("- %q: %q", a.Question, a.Answer))
	}
	r.State.AddMessage(session.RoleUser, strings.Join(parts[1:], "\n"), session.ContentTypePlain)
	if allUnanswered {
		r.trackerMu.Lock()
		r.tracker.recordIdle("question.ask declined")
		r.trackerMu.Unlock()
		return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "The user declined to answer every question. Proceed with your best judgment."}, nil
	}
	return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: strings.Join(parts, "\n")}, nil
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

// requiresSerialTool is the deny list of tools that share a single
// process-wide slot (today: State.PendingQuestion). They must never run
// concurrently inside executeActions, or two calls will clobber each
// other and leak the inner ResponseChan. They are still admitted by
// allReadOnly; executeActions is responsible for ordering them.
//
// If future question tool aliases are added (for example a renamed
// "question.ask.v2"), every spelling must be added to this switch.
// Adding a new alias without listing it here will reintroduce the
// parallel-batch race on the single PendingQuestion slot.
func requiresSerialTool(name string) bool {
	switch name {
	case "question.ask", "ask_user":
		return true
	}
	return false
}

func (r *Runner) executeActions(ctx context.Context, actions []ModelAction) ([]schema.ChatMessage, error) {
	results := make([][]schema.ChatMessage, len(actions))

	// Phase 1: run any question.ask / ask_user calls one at a time so they
	// can't race on the single PendingQuestion slot. If one errors we still
	// execute the remaining serial tools and substitute an error message
	// for the failed slot so the model sees one Tool message per call.
	var parallelIdx []int
	for i, a := range actions {
		if !requiresSerialTool(a.Tool) {
			parallelIdx = append(parallelIdx, i)
			continue
		}
		msgs, err := r.executeToolCall(ctx, a)
		if err != nil {
			results[i] = []schema.ChatMessage{BuildToolErrorMessage(a.Tool, err.Error())}
			continue
		}
		results[i] = msgs
	}

	// Phase 2: run the remaining read-only tools concurrently as before.
	// Tool errors are recorded as error messages in the appropriate results
	// slot so every tool call produces exactly one result batch.
	if len(parallelIdx) > 0 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, r.MaxParallelActions)
		for _, idx := range parallelIdx {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, act ModelAction) {
				defer wg.Done()
				defer func() { <-sem }()
				msgs, err := r.executeToolCall(ctx, act)
				if err != nil {
					results[i] = []schema.ChatMessage{BuildToolErrorMessage(act.Tool, err.Error())}
					return
				}
				results[i] = msgs
			}(idx, actions[idx])
		}
		wg.Wait()
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

	timeout := r.effectiveRequestTimeout()
	select {
	case decision := <-tc.ResponseChan:
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return decision.Approved, decision.Edited, nil
	case <-ctx.Done():
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return false, "", ctx.Err()
	case <-time.After(timeout):
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return false, "", ErrRequestTimedOut
	}
}

func (r *Runner) requestAnswer(ctx context.Context, question string) (string, error) {
	answers, err := r.requestQuestions(ctx, []session.Question{{Question: question}})
	if err != nil {
		return "", err
	}
	if len(answers) == 0 {
		return "", nil
	}
	return answers[0].Answer, nil
}

// requestQuestions blocks on the TUI for one or more structured Answers.
// It produces the same shape the native question.ask tool produces.
func (r *Runner) requestQuestions(ctx context.Context, questions []session.Question) ([]session.Answer, error) {
	for _, q := range questions {
		r.State.AddMessage(session.RoleAssistant, q.Question, session.ContentTypeMarkdown)
	}
	q := &session.PendingQuestion{
		Questions:    questions,
		ResponseChan: make(chan []session.Answer, 1),
	}
	r.State.SetPendingQuestion(q)
	label := buildQuestionLabel(questions)
	r.State.SetActivity(session.Activity{Kind: session.ActivityQuestion, Label: label, StartedAt: r.Now()})

	timeout := r.effectiveRequestTimeout()
	select {
	case answers := <-q.ResponseChan:
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return answers, nil
	case <-ctx.Done():
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return nil, ctx.Err()
	case <-time.After(timeout):
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return nil, ErrRequestTimedOut
	}
}

// effectiveRequestTimeout returns the request timeout to use, falling back
// to a sensible default if r.RequestTimeout is zero.
func (r *Runner) effectiveRequestTimeout() time.Duration {
	if r.RequestTimeout > 0 {
		return r.RequestTimeout
	}
	return defaultRequestTimeout
}

// buildQuestionLabel returns a human-readable activity label that includes a
// preview of the first question so the user knows what they are being asked.
func buildQuestionLabel(questions []session.Question) string {
	if len(questions) == 0 {
		return "waiting for your answer"
	}
	q := questions[0].Question
	if len(q) > 40 {
		q = q[:40] + "…"
	}
	if len(questions) == 1 {
		return "waiting for your answer: " + q
	}
	return fmt.Sprintf("waiting for your answer (Q1/%d): %s", len(questions), q)
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

func changedFilesForTool(toolName string, argsMap map[string]interface{}) []string {
	if toolName != "file.write_patch" {
		return nil
	}
	patchArg, ok := argsMap["patch"]
	if !ok {
		return nil
	}
	patchStr, ok := patchArg.(string)
	if !ok {
		return nil
	}
	patches, err := patch.Parse(patchStr)
	if err != nil {
		return nil
	}
	var files []string
	for _, p := range patches {
		files = append(files, p.Path)
	}
	return files
}
