package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/hooks"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/permissions"
	"marshal/internal/skills"
	"marshal/internal/strutil"
	"marshal/internal/tools/patch"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

const (
	// DefaultMaxToolIterations is 0: no tool ceiling unless the user sets
	// agent.max_tool_iterations. turnBudget treats base <= 0 as unlimited;
	// the overhead ceiling (maxOverheadTurns) and the progressTracker loop
	// detector remain the guardrails.
	DefaultMaxToolIterations    = 0
	DefaultMaxRetries           = 2
	DefaultMaxParallelActions   = 4
	DefaultMaxTurnContextTokens = 60000
	finalizePressureThreshold   = 2
	finalizePressureMessage     = "You are near the tool budget. Unless one specific missing fact is required, produce a final answer now using the results you already have."
	maxConsecutiveParseFailures = 3
	groundingNudgeMessage       = "You have not made any tool calls this turn, but this task requires code changes or commands. If the work is already done from an earlier turn, verify it now with a tool call (for example, re-read the changed file or re-run the test command) before declaring completion. Otherwise, use the appropriate tool to make the change now."
	verificationNudgeMessage    = "You made changes this session (last: %s %s) but have not verified them. Run test.run or diagnostics.check — or the project's test/build command via shell.run — before finishing. If verification is genuinely impossible (no test suite, docs-only change), say so in your final answer."
	// emptyModelResponsePlaceholder stands in for a truly empty model
	// response when recording the assistant's turn in the conversation.
	// Some providers reject the next request outright if any assistant
	// message has empty content, so the literal empty string must never be
	// sent back verbatim.
	emptyModelResponsePlaceholder = "(empty response)"
)

var ErrMaxIterationsExceeded = errors.New("agent: exceeded max tool iterations without a final answer")

var ErrModelOutputMalformed = errors.New("agent: model output could not be parsed after consecutive attempts")

// defaultChatTimeout is the fallback used when ChatTimeout is unset —
// notably by the ad-hoc agent.run subagent path, which never sets either
// timeout field explicitly.
const defaultChatTimeout = 5 * time.Minute

// isLengthFinish reports whether the provider cut the response off at the
// output-token limit ("length" for OpenAI-compatible providers, "max_tokens"
// for Anthropic-style ones). Tool calls in such a response may carry
// silently truncated arguments and must not be executed (pi's guard).
func isLengthFinish(reason string) bool {
	return reason == "length" || reason == "max_tokens"
}

// actionCarriesToolWork reports whether a parsed action would execute a
// tool: a direct call, a patch, or a parallel batch. Answers and questions
// carry no executable payload, so a truncated answer that still parsed is
// complete JSON and safe to accept (the native path likewise only guards
// when tool calls exist).
func actionCarriesToolWork(action ModelAction) bool {
	return action.Type == ActionToolCall || action.Type == ActionPatch || len(action.Actions) > 0
}

// actionToolNames names the tools an action would have run, for the
// truncation refusal message. A batch names every entry; JSON-mode
// providers need no per-call replies.
func actionToolNames(action ModelAction) []string {
	if len(action.Actions) > 0 {
		names := make([]string, 0, len(action.Actions))
		for _, a := range action.Actions {
			names = append(names, a.Tool)
		}
		return names
	}
	return []string{action.Tool}
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
	Resolve(class string) (routing.Route, provider.Provider, error)
}

// Snapshotter tracks and restores shadow-git snapshots of the working tree.
// Aliased to session.Snapshotter so the TUI/commands and the runner share
// one definition without either package importing internal/snapshot.
type Snapshotter = session.Snapshotter

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
//     Now, MaxToolIterations, MaxRetries, MaxTurnContextTokens, ChatTimeout,
//     ReconnectMaxWait, ResponseFormat (seed), NativeTools, MaxParallelActions, MaxToolResultChars,
//     ForceClass, SkillIndex, Role, WriteGate, UsageObserver,
//     MetricsObserver, Snapshotter, SnapshotRecorder, HookRunner, TitleGenerator,
//     RunTaskFunc, PlanFirst, HistoryBudgetTokens, MemoryProvider, ProjectID,
//     fileIndexCache) are initialised once; resolveRoute may lower
//     MaxTurnContextTokens (never raise it) when the route-resolved model's
//     context window is smaller than the configured value — the configured
//     value is a ceiling, and a reduction persists into later RunTask
//     calls. The seed persists across RunTask calls.
//
//   - Per-turn state (tracker, stats, route, pressureMessageSent,
//     consecutiveParseFailures, consecutiveEmpty, turnFinishReason,
//     contextPackMsgIndex) is reset at the top of RunTask and never shared
//     across calls.
//
//   - tracker, stats, and ForceClass have dedicated mutexes for their
//     accessor methods (withStats, trackerMu, forceClassMu). All other
//     field reads and writes are not synchronised — hence the
//     single-caller-at-a-time rule.
type UsageObserver func(usage schema.TokenUsage)

type Runner struct {
	Provider       provider.Provider
	Registry       *registry.Registry
	Policy         *policy.PolicyEngine
	State          *session.State
	Model          string
	RouteResolver  RouteResolver
	MemoryProvider MemoryProvider
	ProjectID      int64
	Now            func() time.Time
	// Sleep backs the reconnect wait loop's backoff. Nil means a ctx-aware
	// time.Sleep; tests substitute an instant (or clock-advancing) fake so
	// reconnect behaviour can be exercised without real waiting.
	Sleep                func(ctx context.Context, d time.Duration) error
	MaxToolIterations    int
	MaxRetries           int
	MaxTurnContextTokens int
	// ChatTimeout bounds chatOnce's per-request context deadline for the
	// model call itself. It is deliberately separate from approvals and
	// questions, which carry no wall-clock timeout: a short chat ceiling
	// must never cut off a slower-but-working completion from a
	// large-context cloud model.
	ChatTimeout time.Duration
	// ReconnectMaxWait bounds the total time waitForConnectivity will
	// spend resending a dropped chat request before failing the turn.
	// Zero uses defaultReconnectMaxWait.
	ReconnectMaxWait   time.Duration
	ResponseFormat     *schema.ResponseFormat
	NativeTools        bool
	MaxParallelActions int
	MaxToolResultChars int
	ForceClass         string // if set, overrides Classify() in Run()
	SkillIndex         *skills.Index

	// Pricing holds the resolved per-token-category rates for the active
	// model, used by emitMetrics to compute EstimatedCostCents. Set by
	// app.go from the resolved route preset via pricing.Lookup. Zero value
	// means local/unpriced (cost = 0).
	Pricing pricing.ModelPricing

	// SuppressParseRepairFeedback silences the note that tells the model its
	// envelope was repaired. Repairs still happen and are still counted —
	// only the per-incident feedback message is dropped, for users who want
	// the tokens back. Set from agent.parse_repair_feedback = false.
	SuppressParseRepairFeedback bool

	// LimitsTable is the merged OpenRouter/LiteLLM limit table used to
	// resolve a preset's context window and max output when the preset does
	// not state them. Nil means "not loaded" — resolution degrades to the
	// local catalog. Set by app.go from the on-disk cache.
	LimitsTable *limits.Table

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

	// SystemPromptAddendum, when non-empty, is appended to the system
	// prompt after the role addendum. Set by custom-agent runner
	// construction (roleRunnerSpec.newRunner) when a custom agent is
	// bound to the role.
	SystemPromptAddendum string

	// WriteGate serialises non-read-only tool execution. When nil, no
	// serialisation is performed (default single-agent behaviour).
	WriteGate WriteGate

	UsageObserver UsageObserver

	// CalibrationObserver, when set, receives the wire messages and the
	// provider-reported prompt-token count after each chatOnce that reports
	// usage. Used by the rollover calibration harness to record paired
	// estimator-vs-provider observations. Nil disables recording.
	CalibrationObserver func(wire []schema.ChatMessage, promptTokens int)

	// MetricsObserver, when set, receives one TurnMetrics per RunTask,
	// emitted on every exit path (answer, salvage, failure). Nil disables
	// collection output; counter bookkeeping still runs.
	MetricsObserver func(TurnMetrics)

	// CompactionObserver, when set, is invoked after each successful
	// compaction/rollover rebuild of the wire history. Used to schedule
	// knowledge extraction at natural checkpoints. Nil disables.
	CompactionObserver func()

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

	// Classifier, when set, is consulted once per Run when keyword
	// classification falls through to ClassQuestion. Intended as a cheap
	// one-shot router-role call (NewModelClassifier). Errors and unrecognized
	// answers leave the keyword class in place. Nil disables it.
	Classifier func(ctx context.Context, goal string) (TaskClass, error)

	// tokenRatio scales estimateTokens toward provider-reported prompt
	// tokens. 0 means unset (raw estimates). See calibration.go.
	// Written by notePromptTokens and read by calibratedEstimate on the
	// single Run/RunTask goroutine; never touched from parallel tool
	// execution, so it needs no lock (and is deliberately not part of
	// CopyFrom).
	tokenRatio float64

	// semTracker tracks tool-referenced paths for mid-turn semantic
	// re-queries (AI-10). Per-RunTask; nil outside Run. Guarded by its own
	// mutex because read-only tools mutate it from parallel goroutines.
	semTracker *semanticRequeryTracker

	// RunTaskFunc overrides RunTask for testing (see the named type below).
	RunTaskFunc RunTaskFunc

	forceClassMu sync.Mutex
	approvalMu   sync.Mutex
	tracker      *progressTracker
	trackerMu    sync.Mutex
	stats        *turnStats
	statsMu      sync.Mutex
	// finishReasonMu guards turnFinishReason, which is written by RunTask
	// (the model-response writer) and read by logToolCall from parallel tool
	// execution goroutines.
	finishReasonMu sync.Mutex

	// turnBudget is set by RunTask to point to its local budget so that
	// executeNativeAskUser / executeNativeQuestionAsk can charge a native
	// ask round-trip against it (mirroring the envelope path's
	// budget.overhead++ in ActionAskUser / ActionQuestionAsk). An ask
	// round-trip is overhead, not work: it must not eat the tool budget,
	// but it still needs a ceiling so ask→decline→ask cannot loop forever.
	// Nil outside of RunTask.
	turnBudget *turnBudget

	// invalidArgsThisRound counts tool calls rejected for schema violations
	// during the current executeNativeToolCalls pass. Guarded by trackerMu.
	invalidArgsThisRound int

	// DigestModel is the model name to use for LLM-based digest generation
	// during rollover. When empty, the runner's primary Model is used.
	DigestModel string

	// Rollover manages context-window archival and cross-turn generation
	// rollover. When nil, all rollover operations are no-ops.
	Rollover *Rollover

	// CloseRolloverOnDone, when true, tells RunTask to close the rollover
	// controller after the task finishes. Used for ephemeral child-session
	// runners (SDD pipeline roles) so their generation rows get an end timestamp.
	CloseRolloverOnDone bool

	// turnFinishReason is the provider finish reason of the most recent model
	// response this turn, stamped onto every audit event by logToolCall. It is
	// per-turn state: reset at the top of RunTask, never shared across calls.
	// Kept on the Runner rather than threaded through the execution call chain
	// because the tool executor is several frames below where the reason is
	// known. Guarded by finishReasonMu for concurrent access from tool
	// execution goroutines.
	turnFinishReason string
	// turnRequestOptions carries the resolved max-token and context-window
	// limits for the current turn, derived from the resolved route. Per-turn
	// state: populated after resolveRoute in RunTask, reset before the turn
	// returns, never shared across calls. Not part of CopyFrom.
	turnRequestOptions turnRequestOptions
	// contextPackMsgIndex tracks the position of the single context-pack
	// message in the current turn's wire transcript. -1 means no context-pack
	// message is currently tracked. Reset at the start of each RunTask.
	contextPackMsgIndex int
	// turnToolResultChars is the tool-result character cap derived from
	// this turn's context threshold. Per-turn state: reset at the top of
	// RunTask. 0 means "not derived yet"; toolResultChars falls back to
	// the package default.
	turnToolResultChars int
	// emittedSkills tracks which active skills already have their body on
	// the current turn's wire transcript, so appendSkillBodies stays
	// idempotent across loop iterations. Per-turn state: reset at the top
	// of RunTask and whenever the wire is rebuilt from scratch.
	emittedSkills map[string]bool
	// fileIndexCache memoises the per-project file index across RunTask
	// calls and across steering-message drains. Auto-invalidates when the
	// projectID changes (see fileIndexCache.get).
	fileIndexCache fileIndexCache
}

// RunTaskFunc, if non-nil, overrides RunTask for testing. It returns a
// canned Task without calling the provider. Used by the SDD orchestrator
// tests to inject scripted responses.
type RunTaskFunc func(ctx context.Context, prompt string) (*Task, error)

func NewRunner(p provider.Provider, reg *registry.Registry, pol *policy.PolicyEngine, state *session.State, model string) *Runner {
	return &Runner{
		Provider:           p,
		Registry:           reg,
		Policy:             pol,
		State:              state,
		Model:              model,
		Now:                time.Now,
		MaxToolIterations:  DefaultMaxToolIterations,
		MaxRetries:         DefaultMaxRetries,
		MaxParallelActions: DefaultMaxParallelActions,
		// 0 means "derive from this turn's context threshold" — see
		// toolResultChars. Same reason as MaxTurnContextTokens above.
		MaxToolResultChars: 0,
		// 0 means "derive from the resolved model window" — see
		// effectiveTurnThreshold. Seeding the default here made every turn
		// look like an explicit user ceiling and left the derivation dead.
		MaxTurnContextTokens: 0,
		tracker:              newProgressTracker(),
	}
}

// toolResultChars resolves the character cap for a single tool result:
// an explicit user setting wins, then this turn's window-derived value,
// then the package default. Never returns 0 — SummarizeToolResult reads
// 0 as "skip the cap" while spillToolResult reads it as "use the
// default", and the runner must not depend on either reading.
func (r *Runner) toolResultChars() int {
	if r.MaxToolResultChars > 0 {
		return r.MaxToolResultChars
	}
	if r.turnToolResultChars > 0 {
		return r.turnToolResultChars
	}
	return DefaultMaxToolResultChars
}

func (r *Runner) SetForceClass(class string) {
	r.forceClassMu.Lock()
	r.ForceClass = class
	r.forceClassMu.Unlock()
}

func (r *Runner) setTurnFinishReason(reason string) {
	r.finishReasonMu.Lock()
	r.turnFinishReason = reason
	r.finishReasonMu.Unlock()
}

func (r *Runner) getTurnFinishReason() string {
	r.finishReasonMu.Lock()
	defer r.finishReasonMu.Unlock()
	return r.turnFinishReason
}

// SetApprovalMode sets the active approval mode on the policy engine.
// Called by the TUI on mode switch and by app.StartRuntime to seed from
// config. Satisfies the AgentRunner interface's SetApprovalMode method.
func (r *Runner) SetApprovalMode(m policy.ApprovalMode) {
	if r.Policy != nil {
		r.Policy.SetApprovalMode(m)
	}
}

// AnswerGate satisfies tui.AgentRunner. The interactive runner has no
// human gates; this is a no-op.
func (r *Runner) AnswerGate(string) {}

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
	r.Sleep = other.Sleep
	r.MaxToolIterations = other.MaxToolIterations
	r.MaxRetries = other.MaxRetries
	r.MaxTurnContextTokens = other.MaxTurnContextTokens
	r.ChatTimeout = other.ChatTimeout
	r.ReconnectMaxWait = other.ReconnectMaxWait
	r.ResponseFormat = other.ResponseFormat
	r.NativeTools = other.NativeTools
	r.MaxParallelActions = other.MaxParallelActions
	r.MaxToolResultChars = other.MaxToolResultChars
	r.ForceClass = other.ForceClass
	r.SkillIndex = other.SkillIndex
	r.LimitsTable = other.LimitsTable
	r.Role = other.Role
	r.WriteGate = other.WriteGate
	r.UsageObserver = other.UsageObserver
	r.CalibrationObserver = other.CalibrationObserver
	r.MetricsObserver = other.MetricsObserver
	r.Snapshotter = other.Snapshotter
	r.SnapshotRecorder = other.SnapshotRecorder
	r.DigestModel = other.DigestModel
	r.Pricing = other.Pricing
	r.SystemPromptAddendum = other.SystemPromptAddendum

	// Refresh session-scoped hooks from the rebuilt runner so a config
	// reload picks up routing/role changes. CopyFrom is the only path that
	// mutates a live runner in place (app.reloadAgentRuntime) — without
	// these, reloads keep stale TitleGenerator/Classifier closures bound to
	// the old route.
	r.HookRunner = other.HookRunner
	r.TitleGenerator = other.TitleGenerator
	r.Classifier = other.Classifier
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
	r.State.IncrementTurnIndex()
	if r.Snapshotter != nil {
		if _, err := r.Snapshotter.Track(ctx); err != nil {
			r.State.Logger().Warn("turn-start snapshot failed", "error", err)
		}
	}
	if db := r.State.DB(); db != nil {
		if mails, err := db.UnreadMail(r.State.SessionID()); err == nil && len(mails) > 0 {
			var b strings.Builder
			b.WriteString("Mail from other sessions:\n")
			ids := make([]int64, 0, len(mails))
			for _, mail := range mails {
				fmt.Fprintf(&b, "- [%s] %s\n", mail.FromSession, mail.Body)
				ids = append(ids, mail.ID)
			}
			r.State.AddMessage(session.RoleSystem, strings.TrimRight(b.String(), "\n"), session.ContentTypePlain)
			if merr := db.MarkMailRead(ids, r.Now()); merr != nil {
				r.State.Logger().Warn("failed to mark session mail read", "error", merr)
			}
		} else if err != nil {
			r.State.Logger().Warn("failed to load session mail", "error", err)
		}
	}

	r.trackerMu.Lock()
	r.tracker = newProgressTracker()
	r.trackerMu.Unlock()

	r.statsMu.Lock()
	r.stats = &turnStats{m: TurnMetrics{
		StartedAt: r.Now(),
		Goal:      strutil.Truncate(goal, 200, false),
		Role:      string(r.role()),
	}}
	r.statsMu.Unlock()

	// Per-turn reset: there is no tracked context-pack message yet, and no
	// skill body has been written to this turn's wire.
	r.contextPackMsgIndex = -1
	r.emittedSkills = nil
	r.turnToolResultChars = 0

	task := NewTask(goal, r.Now())
	defer func() { r.emitMetrics(task) }()
	r.forceClassMu.Lock()
	fc := r.ForceClass
	r.forceClassMu.Unlock()
	if fc != "" {
		task.Class = TaskClass(fc)
	} else {
		task.Class = Classify(goal)
		if task.Class == ClassQuestion && r.Classifier != nil {
			if class, err := r.Classifier(ctx, goal); err == nil {
				switch class {
				case ClassEdit, ClassCommand, ClassQuestion:
					task.Class = class
				}
			}
		}
	}
	turnProvider, turnModel, route := r.resolveRoute(task)
	if route.MaxOutput > 0 {
		r.turnRequestOptions.maxTokens = intPtr(route.MaxOutput)
	} else {
		r.turnRequestOptions.maxTokens = nil
	}
	if route.Window > 0 {
		r.turnRequestOptions.contextWindow = intPtr(route.Window)
	} else {
		r.turnRequestOptions.contextWindow = nil
	}
	r.turnRequestOptions.thinking = route.Preset.Thinking
	r.turnRequestOptions.temperature = route.Preset.Temperature
	defer func() {
		r.turnRequestOptions = turnRequestOptions{}
		r.semTracker = nil
	}()
	r.withStats(func(s *turnStats) {
		s.m.Provider = turnProvider.Name()
		s.m.Model = turnModel
	})
	// D1: derive a per-turn compaction threshold from the resolved route's
	// window. Carried as a local so the threshold tracks the model actually
	// in use, never poisoned across turns by a smaller model's window.
	turnThreshold, _, turnThresholdCollapsed := r.effectiveTurnThreshold(route.Window, route.MaxOutput, r.MaxTurnContextTokens)
	budgetSource := thresholdSource(route.Window, r.MaxTurnContextTokens, turnThresholdCollapsed)
	r.State.SetTurnBudget(route.Window, turnThreshold, budgetSource)
	r.State.Logger().Info("turn context budget",
		"window", route.Window,
		"threshold", turnThreshold,
		"source", budgetSource)
	r.turnToolResultChars = deriveToolResultChars(turnThreshold)
	r.semTracker = newSemanticRequeryTracker()
	r.mergeMemories(route.ContextBudget.MaxRepoContextTokens)
	r.mergeSemantic(ctx, goal, r.ProjectID, route.ContextBudget.MaxRepoContextTokens)
	r.mergeScratchpad(route.ContextBudget.MaxRepoContextTokens)
	r.mergeTodos(route.ContextBudget.MaxRepoContextTokens)

	effectiveRF := r.ResponseFormat

	// F18: extract @file references from the goal and pin them into the
	// context pack before it is appended to the model messages. Unknown
	// paths and unreadable files are silently skipped (see
	// extractPinnedFiles); the TUI only inserts the literal "@path" text.
	if pinned := extractPinnedFiles(goal, r, r.ProjectID); len(pinned) > 0 {
		r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
			return contextpack.PinFiles(pack, pinned)
		})
	}

	messages := []schema.ChatMessage{
		BuildSystemPromptWithAddendum(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools, r.Policy.ApprovalMode(), r.SystemPromptAddendum, r.State.WorkingDir, RenderAgentRoster(r.State.Config)),
	}
	messages = r.setContextPackMessage(messages, r.State.ContextPack())
	messages = r.appendSkillBodies(messages)
	if r.role() == RoleGeneral {
		// Task 4/A2: load the cross-turn ledger. Best-effort — a DB
		// error here falls back to the legacy placeholder rather than
		// failing the turn.
		var ledger map[int64][]db.ToolAuditEntry
		if r.State != nil && r.State.DB() != nil && r.State.SessionID() != "" {
			if got, err := r.State.DB().LoadAllTurnToolAudit(r.State.SessionID()); err == nil {
				ledger = got
			} else {
				r.State.Logger().Warn("load turn tool audit for history failed", "error", err)
			}
		}
		// Task 5/B: adaptive history budget. r.HistoryBudgetTokens is
		// the user's explicit override (or 0); the rest is derived
		// from the resolved model window via historyBudget.
		budget := historyBudget(route.Window, r.HistoryBudgetTokens)
		messages = append(messages, buildHistoryMessages(priorTranscript, budget, r.State.Generation(), ledger)...)
	}
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})

	// T12: end-of-turn flushArchive and maybeRollover so cross-turn rollover
	// actually fires. The defer captures the final value of messages at return
	// time. Both methods are no-ops when Rollover is nil. Errors are logged
	// (not silently discarded) since defers cannot return values.
	defer func() {
		if r.Rollover != nil {
			if _, err := r.Rollover.flushArchive(ctx, messages); err != nil {
				r.State.Logger().Warn("end-of-turn flush archive failed", "error", err)
			}
			if _, err := r.Rollover.maybeRollover(ctx, messages, turnThreshold); err != nil {
				r.State.Logger().Warn("end-of-turn maybe rollover failed", "error", err)
			}
			if r.CloseRolloverOnDone && r.Rollover.Controller != nil {
				if err := r.Rollover.Controller.Close(ctx); err != nil {
					r.State.Logger().Warn("rollover controller close failed", "error", err)
				}
			}
		}
	}()

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
			r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
				return contextpack.RefreshPlanWithBudget(pack, task.Plan, maxTokens, r.Now)
			})
			updatedPack := r.State.ContextPack()
			r.contextPackMsgIndex = -1
			r.emittedSkills = nil
			messages = []schema.ChatMessage{BuildSystemPromptWithAddendum(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools, r.Policy.ApprovalMode(), r.SystemPromptAddendum, r.State.WorkingDir, RenderAgentRoster(r.State.Config))}
			messages = r.setContextPackMessage(messages, updatedPack)
			messages = r.appendSkillBodies(messages)
			if r.role() == RoleGeneral {
				var ledger map[int64][]db.ToolAuditEntry
				if r.State != nil && r.State.DB() != nil && r.State.SessionID() != "" {
					if got, err := r.State.DB().LoadAllTurnToolAudit(r.State.SessionID()); err == nil {
						ledger = got
					} else {
						r.State.Logger().Warn("load turn tool audit for history failed", "error", err)
					}
				}
				budget := historyBudget(route.Window, r.HistoryBudgetTokens)
				messages = append(messages, buildHistoryMessages(priorTranscript, budget, r.State.Generation(), ledger)...)
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
	// pendingRepairNote carries a "your envelope was malformed but I fixed
	// it" message, appended after the assistant turn so the model reads it
	// before its next response. Zero value means nothing to report.
	var pendingRepairNote *schema.ChatMessage
	consecutiveEmpty := 0
	r.setTurnFinishReason("")

	toolCallCountThisTurn := 0
	groundingNudgeSent := false
	verificationNudgeSent := false
	budget := newTurnBudget(r.MaxToolIterations, task.Class, len(task.Plan))
	r.turnBudget = budget
	defer func() { r.turnBudget = nil }()
	countIterations := func() { r.withStats(func(s *turnStats) { s.m.Iterations = budget.total() }) }
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
	var exhaustionReason finalizeReason
	for {
		// The task's real size is usually only known once the model has
		// written a todo list, which happens a few iterations in — plan_first
		// is off by default, so len(task.Plan) is normally 0 at loop entry.
		budget.grantSteps(len(r.State.Todos()))
		if reason := budget.exhaustionReason(); reason != "" {
			exhaustionReason = reason
			r.State.Logger().Debug("turn budget exhausted",
				"reason", reason,
				"tools", budget.tools,
				"max_tools", budget.maxTools,
				"overhead", budget.overhead)
			break
		}
		r.State.SetToolBudget(session.ToolBudget{Used: budget.tools, Max: budget.maxTools})

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
			r.State.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
				return contextpack.PinFiles(pack, steeringPins)
			})
		}

		if !pressureMessageSent && budget.remainingTools() <= finalizePressureThreshold {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: finalizePressureMessage})
			r.State.AddMessage(session.RoleSystem, finalizePressureMessage, session.ContentTypePlain)
			pressureMessageSent = true
		}

		currentSkills := r.State.ActiveSkills()
		if skillsChanged(lastRenderedSkills, currentSkills) {
			messages[0] = BuildSystemPromptWithAddendum(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, currentSkills, r.NativeTools, r.Policy.ApprovalMode(), r.SystemPromptAddendum, r.State.WorkingDir, RenderAgentRoster(r.State.Config))
			lastRenderedSkills = currentSkills
		}

		// Refresh the scratchpad projection before the next model call so
		// entries written in earlier turns (e.g. scratchpad.write) appear in
		// the context pack of subsequent turns.
		r.mergeScratchpad(route.ContextBudget.MaxRepoContextTokens)
		r.mergeTodos(route.ContextBudget.MaxRepoContextTokens)
		r.maybeRequerySemantic(ctx, goal, r.semanticSource(r.ProjectID), route.ContextBudget.MaxRepoContextTokens)
		messages = r.setContextPackMessage(messages, r.State.ContextPack())
		// Deliver the body of any skill loaded since the last iteration.
		messages = r.appendSkillBodies(messages)

		if turnThreshold > 0 && r.calibratedEstimate(messages) > turnThreshold {
			// D2: prune superseded tool outputs first. Most overflows are
			// 2-3 huge tool results, especially re-reads of the same
			// file. Pruning before summarizing is cheaper, preserves the
			// most recent copy, and often drops us back below the
			// threshold without needing the LLM at all.
			if prunedMsgs, n := pruneStaleToolOutputs(messages, pruneMinSizeDefault); n > 0 && r.calibratedEstimate(prunedMsgs) <= turnThreshold {
				messages = prunedMsgs
				r.State.Logger().Info("context pruning recovered window", "pruned_outputs", n)
				continue
			} else if n > 0 {
				// Pruning helped but not enough — keep the pruned wire
				// and proceed to compaction. The freshest reads survive.
				messages = prunedMsgs
			}

			// T13: unified intra-turn compaction — rollover when enabled,
			// fall back to summarizeAndContinue when disabled.
			if fresh, cerr := rolloverAndContinue(ctx, r, messages, goal, turnThreshold); cerr == nil {
				messages = fresh
				r.resetTokenRatio()
				if r.CompactionObserver != nil {
					r.CompactionObserver()
				}
				pressureMessageSent = false // the fresh transcript may legitimately approach the budget again
			} else {
				r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Context window exceeded and compaction failed: %s. The turn is being terminated to prevent transcript corruption.", cerr), session.ContentTypePlain)
				return task, r.fail(task, fmt.Errorf("context overflow and compaction failed: %w", cerr))
			}
		}

		res, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages, effectiveRF)
		r.setTurnFinishReason(res.FinishReason)
		if err != nil {
			// A stream that failed part-way may still have delivered a usable
			// response — a malformed SSE chunk aborts the stream without
			// invalidating the deltas already received. Salvage it and let the
			// normal path below judge it: if it parses, the turn continues; if
			// it does not, the parse-failure ladder handles it. Failing here
			// instead threw away work the model had already done.
			//
			// Cancellation is never salvaged: the user asked to stop. A
			// deadline-exhausted stream (sleep/wake, reconnect cap reached)
			// IS salvage-eligible: the partial text is real model output
			// the user paid for, so only user-initiated cancellation
			// discards it.
			if strings.TrimSpace(res.Text) == "" || errors.Is(err, context.Canceled) {
				return task, r.fail(task, err)
			}
			r.withStats(func(s *turnStats) { s.m.StreamRecoveries++ })
			r.State.Logger().Warn("recovered from mid-stream provider error",
				"error", err, "salvaged_bytes", len(res.Text))
			r.State.AddMessage(session.RoleSystem,
				fmt.Sprintf("Recovered from a provider stream error mid-turn (%v); continuing with the response received so far.", err),
				session.ContentTypePlain)
		}
		raw := res.Text

		if r.NativeTools {
			if len(res.ToolCalls) == 0 {
				if strings.TrimSpace(res.Text) == "" {
					// Empty response: the model went silent. Charge it to the
					// overhead budget so a silent model cannot loop forever,
					// but without spending a slot of the work budget on a turn
					// that did nothing. Record an idle entry so the stall
					// detector can see sustained silence, and short-circuit to
					// finalize after a couple of consecutive empties rather
					// than re-prompting indefinitely.
					budget.overhead++
					countIterations()
					consecutiveEmpty++
					r.trackerMu.Lock()
					r.tracker.recordIdle(res.FinishReason)
					r.trackerMu.Unlock()
					messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: "Call a tool or give a final answer."})
					if consecutiveEmpty >= 2 {
						return r.finalize(ctx, turnProvider, turnModel, messages, task, reasonEmpty, effectiveRF)
					}
					var finalized *Task
					messages, finalized, err = r.checkStall(ctx, turnProvider, turnModel, messages, task, effectiveRF, steeringArrived)
					if finalized != nil {
						return finalized, err
					}
					continue
				}
				if toolCallCountThisTurn == 0 && task.Class != ClassQuestion && !groundingNudgeSent {
					groundingNudgeSent = true
					budget.overhead++
					countIterations()
					r.State.AddMessage(session.RoleSystem, groundingNudgeMessage, session.ContentTypePlain)
					messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: groundingNudgeMessage})
					continue
				}
				// Verification gate: a turn that changed something must have
				// verified those changes before finishing. One nudge, then
				// the answer is accepted but flagged unverified below.
				lastMutation, needsVerification := r.unverifiedMutation()
				if needsVerification && !verificationNudgeSent {
					verificationNudgeSent = true
					budget.overhead++
					countIterations()
					msg := fmt.Sprintf(verificationNudgeMessage, lastMutation.name, SummarizeToolArgs(lastMutation.name, json.RawMessage(lastMutation.args)))
					r.State.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
					messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: msg})
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
				if (toolCallCountThisTurn == 0 && task.Class != ClassQuestion) || needsVerification {
					// Already nudged once to verify with a tool call, and
					// still didn't make one: accept the answer (retrying
					// further only produced a MORE confident, not a more
					// honest, response in testing) but flag it as unverified
					// rather than a trusted completion, and exclude it from
					// future history replay (see buildHistoryMessages).
					task.SalvagedReason = string(reasonUnverified)
					r.State.AddMessageSalvaged(session.RoleAssistant, res.Text, session.ContentTypeMarkdown, string(reasonUnverified))
					return task, nil
				}
				r.State.AddMessageFinalWithUsage(session.RoleAssistant, res.Text, session.ContentTypeMarkdown, toolCallCountThisTurn, r.turnUsageLine())
				return task, nil
			}

			if isLengthFinish(res.FinishReason) {
				// The response was truncated before its tool calls were
				// usable, so nothing ran: overhead, not work.
				budget.overhead++
				countIterations()
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

			budget.tools++
			consecutiveEmpty = 0
			countIterations()
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: res.Text, ToolCalls: res.ToolCalls})
			producedValidAction = true
			toolCallCountThisTurn += len(res.ToolCalls)
			if inProgress := r.State.InProgress(); !inProgress.StartedAt.IsZero() && inProgress.Reasoning != "" {
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
			// Re-arm the verification nudge when, after being nudged once,
			// the model's most recent recorded call was another successful
			// mutation: that fresh change deserves its own nudge rather than
			// a silently flagged final answer. (Only the immediately-last
			// call is checked, so a read following a mutation does not
			// re-arm — the model was nudged and then chose not to verify.)
			// The overhead cap still bounds how many nudges a turn can burn.
			if verificationNudgeSent && r.lastSuccessfulMutation() {
				verificationNudgeSent = false
			}
			// Every call this turn was rejected before running: nothing was
			// accomplished, so the turn is overhead, not work.
			if invalidArgs := r.invalidArgsCount(); invalidArgs > 0 && invalidArgs == len(res.ToolCalls) {
				budget.reclassifyAsOverhead()
				countIterations()
			}
			messages = append(messages, resultMsgs...)
			var finalized *Task
			messages, finalized, err = r.checkStall(ctx, turnProvider, turnModel, messages, task, effectiveRF, steeringArrived)
			if finalized != nil {
				return finalized, err
			}
			continue
		}

		action, repairs, parseErr := ParseActionRepairing(raw, r.knownTool)
		if parseErr == nil && len(repairs) > 0 {
			r.withStats(func(s *turnStats) { s.m.ParseRepairs += len(repairs) })
			r.State.Logger().Info("repaired model action envelope",
				"repairs", repairs,
				"raw", truncateForLog(raw),
			)
			// Fed back by default so the model corrects itself next turn
			// rather than leaning on the repair forever. Users on a tight
			// token budget can silence it (agent.parse_repair_feedback).
			if !r.SuppressParseRepairFeedback {
				pendingRepairNote = BuildRepairNoticeMessage(repairs)
			}
		}
		if parseErr != nil {
			consecutiveParseFailures++
			// Logged, not just counted: a parse failure that escalates to a
			// failed turn is otherwise undiagnosable after the fact — the
			// malformed output is gone and only a ParseFailures tally remains.
			r.State.Logger().Warn("model output did not parse as an action",
				"error", parseErr,
				"consecutive", consecutiveParseFailures,
				"raw", truncateForLog(raw),
			)
			// Some providers (Kimi's kimi-for-coding, confirmed live) reject
			// the very next request with HTTP 400 "message ... with role
			// 'assistant' must not be empty" if any assistant message has
			// empty content. An extended-thinking model can return an empty
			// res.Text (all output goes to the reasoning channel), which
			// fails ParseAction — never append that empty string verbatim.
			assistantContent := raw
			if strings.TrimSpace(assistantContent) == "" {
				assistantContent = emptyModelResponsePlaceholder
			}
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: assistantContent})
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
		if isLengthFinish(res.FinishReason) && actionCarriesToolWork(action) {
			// The response hit the output-token limit, so the action's
			// arguments may be silently truncated even though the envelope
			// still parsed — a payload can be cut exactly at a JSON
			// boundary. Refuse it, mirroring the native-path guard above.
			// Nothing ran, so this is overhead; it also counts as a parse
			// failure so a model stuck at the limit trips the ladder
			// instead of looping. Refuse before producedValidAction is set:
			// a turn that only produced truncated actions did no valid work.
			budget.overhead++
			countIterations()
			consecutiveParseFailures++
			r.withStats(func(s *turnStats) { s.m.ParseFailures++ })
			assistantContent := raw
			if strings.TrimSpace(assistantContent) == "" {
				assistantContent = emptyModelResponsePlaceholder
			}
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: assistantContent})
			messages = append(messages, BuildTruncationMessage(actionToolNames(action)))
			// Same consecutive-parse-failure ladder as the parseErr branch above.
			if consecutiveParseFailures >= maxConsecutiveParseFailures {
				break
			}
			continue
		}
		consecutiveParseFailures = 0
		consecutiveEmpty = 0
		budget.tools++
		countIterations()
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
		if pendingRepairNote != nil {
			messages = append(messages, *pendingRepairNote)
			pendingRepairNote = nil
		}
		producedValidAction = true

		if inProgress := r.State.InProgress(); !inProgress.StartedAt.IsZero() && inProgress.Reasoning != "" && action.Type != ActionAnswer && action.Type != ActionFinal {
			r.State.LogThinking(session.ThinkingEntry{
				Text:      inProgress.Reasoning,
				Duration:  time.Since(inProgress.StartedAt),
				StartedAt: inProgress.StartedAt,
			})
		}

		if len(action.Actions) > 0 {
			if err := r.allReadOnly(action.Actions); err != nil {
				// F-SEC-11: the violation is a parse failure for budget
				// purposes. Without this, a model that keeps emitting
				// non-read-only actions loops forever. Nothing executed, so
				// it is charged to overhead. `budget` and
				// `consecutiveParseFailures` are local to RunTask; update
				// them directly, the same way the parse-failure branch above
				// does.
				budget.overhead++
				consecutiveParseFailures++
				r.withStats(func(s *turnStats) { s.m.ParseFailures++ })
				messages = append(messages, BuildCorrectionMessage(err))
				continue
			}
			resultMsgs, execErr := r.executeActions(ctx, action.Actions)
			if execErr != nil {
				return task, r.fail(task, execErr)
			}
			toolCallCountThisTurn += len(action.Actions)
			messages = append(messages, resultMsgs...)
			var finalized *Task
			messages, finalized, err = r.checkStall(ctx, turnProvider, turnModel, messages, task, effectiveRF, steeringArrived)
			if finalized != nil {
				return finalized, err
			}
			continue
		}

		switch action.Type {
		case ActionAnswer, ActionFinal:
			// Grounding, same rule as the native path: a non-question task
			// that made no tool calls at all gets one nudge.
			if toolCallCountThisTurn == 0 && task.Class != ClassQuestion && !groundingNudgeSent {
				groundingNudgeSent = true
				budget.overhead++
				countIterations()
				r.State.AddMessage(session.RoleSystem, groundingNudgeMessage, session.ContentTypePlain)
				messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: groundingNudgeMessage})
				continue
			}
			// Verification gate, same rule as the native path.
			lastMutation, needsVerification := r.unverifiedMutation()
			if needsVerification && !verificationNudgeSent {
				verificationNudgeSent = true
				budget.overhead++
				countIterations()
				msg := fmt.Sprintf(verificationNudgeMessage, lastMutation.name, SummarizeToolArgs(lastMutation.name, json.RawMessage(lastMutation.args)))
				r.State.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
				messages = append(messages, schema.ChatMessage{Role: schema.RoleSystem, Content: msg})
				continue
			}
			task.Summary = action.Content
			task.Status = TaskStatusCompleted
			if next, continued, err := runTurnEnd(messages, task); err != nil {
				return task, r.fail(task, err)
			} else if continued {
				messages = next
				continue
			}
			if (toolCallCountThisTurn == 0 && task.Class != ClassQuestion) || needsVerification {
				task.SalvagedReason = string(reasonUnverified)
				r.State.AddMessageSalvaged(session.RoleAssistant, action.Content, session.ContentTypeMarkdown, string(reasonUnverified))
				return task, nil
			}
			r.State.AddMessageFinalWithUsage(session.RoleAssistant, action.Content, session.ContentTypeMarkdown, toolCallCountThisTurn, r.turnUsageLine())
			return task, nil
		case ActionToolCall, ActionPatch:
			toolCallCountThisTurn++
			resultMsgs, err := r.executeToolCall(ctx, action)
			if err != nil {
				return task, r.fail(task, err)
			}
			// Re-arm the verification nudge when, after being nudged once,
			// the model's most recent call was another successful mutation:
			// that fresh change deserves its own nudge rather than a
			// silently flagged final answer. (Only the immediately-last call
			// is checked — a read following a mutation does not re-arm.)
			// The overhead cap still bounds how many nudges a turn can burn.
			if verificationNudgeSent && r.lastSuccessfulMutation() {
				verificationNudgeSent = false
			}
			messages = append(messages, resultMsgs...)
			var finalized *Task
			messages, finalized, err = r.checkStall(ctx, turnProvider, turnModel, messages, task, effectiveRF, steeringArrived)
			if finalized != nil {
				return finalized, err
			}
		case ActionAskUser:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("ask_user is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
			if r.Policy != nil && r.Policy.ApprovalMode() == policy.ModeAuto {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("ask_user is not available in auto mode; proceed with your best judgment and state the assumption you made")))
				continue
			}
			answer, waitErr := r.requestAnswer(ctx, action.Content)
			if waitErr != nil {
				return task, r.fail(task, waitErr)
			}
			// An ask_user round-trip consumes an overhead turn: a model that
			// re-asks the same (or a declined) question would otherwise loop
			// ask→decline→ask without any budget ever decreasing. It is
			// charged to overhead rather than work so a genuinely useful
			// clarification does not cost the task a slot of real work. A
			// declined answer is non-progress and is recorded as idle so the
			// stall detector sees a repeated ask as churn too.
			budget.overhead++
			countIterations()
			if strings.TrimSpace(answer) == "" {
				consecutiveEmpty++
				r.trackerMu.Lock()
				r.tracker.recordIdle("ask_user declined")
				r.trackerMu.Unlock()
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "The user declined to answer. Proceed with your best judgment and state the assumption you made."})
				var finalized *Task
				messages, finalized, err = r.checkStall(ctx, turnProvider, turnModel, messages, task, effectiveRF, steeringArrived)
				if finalized != nil {
					return finalized, err
				}
			} else {
				consecutiveEmpty = 0
				messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: "User answered: " + answer})
			}
		case ActionQuestionAsk:
			if r.role() != RoleGeneral {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("question.ask is not available for the %s role; proceed with your best judgment or report findings", r.role())))
				continue
			}
			if r.Policy != nil && r.Policy.ApprovalMode() == policy.ModeAuto {
				messages = append(messages, BuildCorrectionMessage(fmt.Errorf("question.ask is not available in auto mode; proceed with your best judgment and state the assumption you made")))
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
			budget.overhead++
			countIterations()
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
				var finalized *Task
				messages, finalized, err = r.checkStall(ctx, turnProvider, turnModel, messages, task, effectiveRF, steeringArrived)
				if finalized != nil {
					return finalized, err
				}
			} else {
				consecutiveEmpty = 0
				parts := make([]string, 0, len(answers))
				for _, a := range answers {
					parts = append(parts, fmt.Sprintf("%q: %q", a.Question, a.Answer))
				}
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
		if exhaustionReason == "" {
			exhaustionReason = reasonExhausted
		}
		if res, ferr := r.finalize(ctx, turnProvider, turnModel, messages, task, exhaustionReason, effectiveRF); ferr == nil {
			return res, nil
		}
	}
	task.Status = TaskStatusFailed
	if exhaustionReason == reasonOverhead {
		r.State.AddMessage(session.RoleSystem, fmt.Sprintf("Agent stopped: hit the overhead turn cap (%d turns with no tool progress).", maxOverheadTurns), session.ContentTypePlain)
	} else {
		r.State.AddMessage(session.RoleSystem, "Agent stopped: exceeded max tool iterations without a final answer.", session.ContentTypePlain)
	}
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

// checkStall runs the hard-stall finalize/nudge check for one loop branch.
// When steeringArrived is true the check is skipped (the user just
// intervened, so the loop is no longer auto-iterating). A non-nil finalized
// result means the caller must return it with err.
func (r *Runner) checkStall(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, rf *schema.ResponseFormat, steeringArrived bool) ([]schema.ChatMessage, *Task, error) {
	if steeringArrived {
		return messages, nil, nil
	}
	finalized, res, ferr, nudge := r.maybeFinalizeOnStall(ctx, p, model, messages, task, rf)
	if finalized {
		return messages, res, ferr
	}
	if nudge != "" {
		messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: nudge})
	}
	return messages, nil, nil
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

// Chat calls the runner's provider with the given messages and returns
// a single ChatMessage response. It satisfies the rollover.chatModel interface
// used by LLMSummaryProvider for digest generation.
func (r *Runner) Chat(ctx context.Context, messages []schema.ChatMessage) (schema.ChatMessage, error) {
	model := r.Model
	if r.DigestModel != "" {
		model = r.DigestModel
	}
	events, err := r.Provider.Chat(ctx, schema.ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
	})
	if err != nil {
		return schema.ChatMessage{}, err
	}
	var resp schema.ChatMessage
	for event := range events {
		switch event.Type {
		case schema.ChatEventDelta:
			resp.Content += event.Delta
		case schema.ChatEventError:
			return schema.ChatMessage{}, event.Err
		case schema.ChatEventDone:
			resp.Content += event.Delta
		}
	}
	return resp, nil
}

func changedFilesForTool(toolName string, argsMap map[string]interface{}) []string {
	if toolName == "file.write" {
		if path, ok := argsMap["path"].(string); ok && path != "" {
			return []string{path}
		}
		return nil
	}
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

// referencedPathsForTool returns the workspace paths a tool call reads or
// writes — the trigger signal for mid-turn semantic re-queries (AI-10).
// Reads add file.read/file.page; writes reuse changedFilesForTool.
func referencedPathsForTool(toolName string, argsMap map[string]interface{}) []string {
	switch toolName {
	case "file.read", "file.page":
		if path, ok := argsMap["path"].(string); ok && path != "" {
			return []string{path}
		}
		return nil
	}
	return changedFilesForTool(toolName, argsMap)
}

// unverifiedMutation exposes the tracker's verification-gate scan under the
// runner's tracker mutex.
func (r *Runner) unverifiedMutation() (callEntry, bool) {
	r.trackerMu.Lock()
	defer r.trackerMu.Unlock()
	return r.tracker.unverifiedMutation()
}

// lastSuccessfulMutation exposes the tracker's re-arm check under the runner's
// tracker mutex.
func (r *Runner) lastSuccessfulMutation() bool {
	r.trackerMu.Lock()
	defer r.trackerMu.Unlock()
	return r.tracker.lastSuccessfulMutation()
}
