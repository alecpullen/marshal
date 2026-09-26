package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/gitinfo"
	"marshal/internal/llm/routing"
	"marshal/internal/pipeline"
	"marshal/internal/pubsub"
	"marshal/internal/strutil"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/watch"
)

// PromptTurnParams is the JSON-RPC body for session/prompt.
type PromptTurnParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// PromptTurnResult is the JSON-RPC result for session/prompt.
type PromptTurnResult struct {
	StopReason string `json:"stopReason"`
}

// SwarmStartParams is the JSON-RPC body for session/swarm_start.
type SwarmStartParams struct {
	SessionID string `json:"sessionId"`
	Goal      string `json:"goal"`
}

// SwarmCastEntry is one role in the SwarmTurnResult's post-run cast list.
type SwarmCastEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// SwarmTurnResult is the JSON-RPC result for session/swarm_start.
type SwarmTurnResult struct {
	StopReason string           `json:"stopReason"`
	Cast       []SwarmCastEntry `json:"cast,omitempty"`
}

// RunnerFunc is the per-turn execution surface. Decoupling the turn
// manager from *agent.Runner keeps the ACP package free of the
// provider/tool/registry/policy dependency chain and lets unit tests stub
// turns in a few lines.
type RunnerFunc func(ctx context.Context, prompt string) error

// AgentRunner is the swarm/SDD execution surface. Its method set matches
// tui.AgentRunner structurally, so *swarm.Orchestrator and
// PipelineFactory-built runners (both sourced from app.Runtime) satisfy it
// without internal/acp importing internal/app/tui — the same decoupling
// RunnerFunc already gives regular turns.
type AgentRunner interface {
	Run(ctx context.Context, goal string) error
	SetForceClass(class string)
	SetPolicyRules(rules []config.PermissionRule)
	SetApprovalMode(mode policy.ApprovalMode)
	AnswerGate(answer string)
}

// TurnRuntime is the per-session slice of state the turn manager needs.
type TurnRuntime struct {
	SessionID string
	BeginWork func(context.Context) (context.Context, func(), error)
	Run       RunnerFunc
	Events    *pubsub.Broker[session.Event]
	// SetMode applies a session-level approval mode (plan, default, edit,
	// copilot, auto). Nil means the runtime does not support mode switching.
	SetMode func(mode string) error
	// SetSystemAccess applies the per-session system-access modifier. Nil
	// means the runtime falls back to State.SetSystemAccess; when State is
	// also nil the runtime does not support system access.
	SetSystemAccess func(on bool)
	// Steer enqueues a mid-turn steering message, consumed by the runner
	// at its next loop-top. Nil means the runtime does not support steering.
	Steer func(text string)
	// State is the session's shared state, needed for reading swarm/SDD
	// progress and gate status. Never nil for a runtime built by
	// SessionManager; tests may still construct a TurnRuntime with a nil
	// State when they don't exercise a path that reads it.
	State *session.State
	// SwarmRunner runs a swarm turn for a goal. Nil when swarm is unavailable.
	SwarmRunner AgentRunner
	// PipelineFactory builds a plan-execution runner for one plan path. The
	// overrides map carries per-run role→preset overrides from the castlist;
	// nil when no overrides are set. Nil when plan execution is unavailable.
	PipelineFactory func(planPath string, overrides map[routing.AgentRole]string) AgentRunner
	// WatchResumeEnabled reads the config gate at wake time. Nil-safe:
	// a runtime that doesn't supply it never resumes (tests). Set by the
	// host's Lookup closure from rt.State.Config.Watch.ResumeEnabled (the
	// same live source the TUI wake reads).
	WatchResumeEnabled func() bool
	// WatchResumeGatePending reports whether a human gate is pending
	// (SDD). Nil means never gated.
	WatchResumeGatePending func() bool
}

// systemAccess reports the runtime session's live system-access flag.
// Nil-safe: a runtime without state reports false.
func (rt *TurnRuntime) systemAccess() bool {
	if rt == nil || rt.State == nil {
		return false
	}
	return rt.State.SystemAccess()
}

// applySystemAccess toggles the runtime session's system-access modifier
// through the SetSystemAccess seam, falling back to the session state's own
// setter when the seam is not wired. It reports whether a setter was found.
func (rt *TurnRuntime) applySystemAccess(on bool) bool {
	if rt == nil {
		return false
	}
	if rt.SetSystemAccess != nil {
		rt.SetSystemAccess(on)
		return true
	}
	if rt.State != nil {
		rt.State.SetSystemAccess(on)
		return true
	}
	return false
}

// modeRequestWantsSystem reports whether a mode.request tool call's args
// carry a system-access elevation request.
func modeRequestWantsSystem(args string) bool {
	if strings.TrimSpace(args) == "" {
		return false
	}
	var a struct {
		System bool `json:"system"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return false
	}
	return a.System
}

// Lookup returns the runtime registered for an ACP session id.
type Lookup func(sessionID string) (*TurnRuntime, bool)

// NotifyFunc is the JSON-RPC notification sink.
type NotifyFunc func(method string, params any) error

// TurnManagerConfig wires a TurnManager to external dependencies.
type TurnManagerConfig struct {
	Lookup    Lookup
	Notify    NotifyFunc
	Perms     PermissionClient
	Questions QuestionClient
}

// activeTurn tracks a single in-flight turn for one session. At most one
// activeTurn may exist per session ID; the slot is reserved atomically
// when PromptTurn starts and deleted when the Turn completes.
type activeTurn struct {
	cancel          context.CancelFunc
	done            chan struct{}
	clientCancelled atomic.Bool
}

// TurnManager dispatches session/prompt and session/cancel. At most one
// prompt may run per session; different sessions may run concurrently.
type TurnManager struct {
	lookup  Lookup
	notify  NotifyFunc
	perms   PermissionClient
	bridge  *PermissionBridge
	qbridge *QuestionBridge

	activeTurnsMu sync.Mutex
	activeTurns   map[string]*activeTurn

	// childForwardersMu guards childForwarders, the per-session
	// lifetime subscriptions that surface a background subagent's
	// parent.ask to the client while NO turn is live (the idle-parent
	// case, spec §5.4). Mid-turn questions are owned by the turn-scoped
	// forwarder inside runTurn; this one exists precisely because a
	// turn-scoped subscription dies when the turn ends while background
	// children keep running.
	childForwardersMu sync.Mutex
	childForwarders   map[string]*childForwarder

	// childAsked guards against double-asking the client for one child
	// question identity (keyed by ResponseChan, F-BUG-51 pattern). It is
	// manager-scoped, not turn-scoped, so the idle forwarder and a turn
	// forwarder racing across a turn boundary cannot both ask.
	childAsked sync.Map

	// pipelineRunnersMu guards pipelineRunners, tracking the in-flight SDD
	// runner (and the plan path it was built for) per session so
	// SDDAnswer can resume the same instance a human gate paused.
	pipelineRunnersMu sync.Mutex
	pipelineRunners   map[string]*sddRun

	// baseRefsMu guards baseRefs, the per-session fixed diff base for
	// session_telemetry's changed-files section — computed once on first
	// use and cached, mirroring the TUI's railBaseRef (fixed at
	// construction, not recomputed against current HEAD on every refresh,
	// since SDD/swarm runs make commits mid-session).
	baseRefsMu sync.Mutex
	baseRefs   map[string]string

	// cancelTimeout overrides cancelWait for testing; zero means use the
	// default const. Access is safe without a mutex because it is set
	// only during construction and read only in CancelAndWait.
	cancelTimeout time.Duration
}

// sddRun is one session's in-flight (possibly gated) SDD run.
type sddRun struct {
	runner   AgentRunner
	planPath string
}

func NewTurnManager(cfg TurnManagerConfig) *TurnManager {
	if cfg.Lookup == nil {
		panic("acp: TurnManagerConfig.Lookup is required")
	}
	if cfg.Notify == nil {
		panic("acp: TurnManagerConfig.Notify is required")
	}
	tm := &TurnManager{
		lookup:          cfg.Lookup,
		notify:          cfg.Notify,
		perms:           cfg.Perms,
		activeTurns:     map[string]*activeTurn{},
		childForwarders: map[string]*childForwarder{},
		pipelineRunners: map[string]*sddRun{},
		baseRefs:        map[string]string{},
	}
	if cfg.Perms != nil {
		tm.bridge = NewPermissionBridge(cfg.Perms)
	}
	if cfg.Questions != nil {
		tm.qbridge = NewQuestionBridge(cfg.Questions)
	}
	return tm
}

// messageUpdate projects a session.Message into a session/update body.
// User messages become user_message_chunk; assistant and system messages
// become agent_message_chunk. The content is always {type:"text", text:…}.
//
// A salvaged message (e.g. one flagged "unverified" because the model never
// made a tool call despite being asked to — see the grounding check in
// agent.Runner.RunTask) is marked with a leading bracketed note, the same
// signal the TUI renders as a "salvaged" badge (transcript.go:
// renderFinalAnswer). ACP has no separate metadata channel for this in v1,
// so folding it into the plain text is the only way an ACP client sees it
// at all — without this, a fabricated-but-flagged answer and a genuine one
// are wire-identical to any ACP client.
func messageUpdate(msg session.Message) map[string]any {
	kind := "user_message_chunk"
	if msg.Role == session.RoleAssistant || msg.Role == session.RoleSystem {
		kind = "agent_message_chunk"
	}
	text := msg.Content
	if msg.Salvaged {
		note := "salvaged"
		if msg.SalvageReason != "" {
			note += " · " + msg.SalvageReason
		}
		text = "[" + note + "] " + text
	}
	return map[string]any{
		"kind": kind,
		"content": map[string]any{
			"type": "text",
			"text": text,
		},
	}
}

// turnProjection carries per-turn state used to project session events into
// wire updates: the accumulated thinking text (for deltas) and the most
// recent active tool call (for correlating tool_call_update with tool_call).
type turnProjection struct {
	lastThinking string
	lastToolID   string
	lastToolName string
}

// toolTextCap bounds args/output text in tool_call wire events.
const toolTextCap = 4096

// capToolText truncates s to toolTextCap runes with a visible suffix.
// Rune-aware so multi-byte UTF-8 sequences are never split.
func capToolText(s string) string {
	if len([]rune(s)) <= toolTextCap {
		return s
	}
	return strutil.Truncate(s, toolTextCap, false) + "… (truncated)"
}

// eventToSessionUpdate projects a session event into a session/update
// envelope body. Returns (nil, false) for internal events that should not
// be forwarded. Thinking updates compute the delta from the previous value;
// tool_call/tool_call_update come from active-tool and audit events. The
// caller's projection is updated in place.
func eventToSessionUpdate(ev pubsub.Event[session.Event], proj *turnProjection) (map[string]any, bool) {
	switch ev.Type {
	case session.EventMessageAdded:
		if ev.Payload.Message != nil {
			// Skill bodies are model context, not user-facing text;
			// forwarding them would dump the full skill file into the
			// client's transcript. The compact ContentTypeSkill tag
			// still goes through as the visible trace of the load.
			if ev.Payload.Message.ContentType == session.ContentTypeSkillBody {
				return nil, false
			}
			return messageUpdate(*ev.Payload.Message), true
		}
	case session.EventThinkingChanged:
		if ev.Payload.Thinking != nil {
			reasoning := ev.Payload.Thinking.Reasoning
			delta := reasoning
			if proj.lastThinking != "" && strings.HasPrefix(reasoning, proj.lastThinking) {
				delta = reasoning[len(proj.lastThinking):]
			}
			proj.lastThinking = reasoning
			if delta != "" {
				return map[string]any{
					"kind": "agent_thought_chunk",
					"content": map[string]any{
						"type": "text",
						"text": delta,
					},
				}, true
			}
			return nil, false
		}
	case session.EventActiveToolChanged:
		if ev.Payload.ActiveTool != nil {
			atc := ev.Payload.ActiveTool
			id := fmt.Sprintf("%s-%d", atc.Name, atc.StartedAt.UnixNano())
			proj.lastToolID = id
			proj.lastToolName = atc.Name
			return map[string]any{
				"kind":       "tool_call",
				"toolCallId": id,
				"toolName":   atc.Name,
				"args":       capToolText(atc.Args),
				"status":     "running",
			}, true
		}
	case session.EventAuditAdded:
		if ev.Payload.Audit != nil {
			ae := ev.Payload.Audit
			status := "done"
			if ae.Error != "" {
				status = "error"
			}
			id := proj.lastToolID
			if id == "" || ae.ToolName != proj.lastToolName {
				id = fmt.Sprintf("%s-%d", ae.ToolName, ae.Timestamp.UnixNano())
			}
			output := ae.ResultContent
			if output == "" {
				output = ae.ResultSummary
			}
			if status == "error" && output == "" {
				output = ae.Error
			}
			return map[string]any{
				"kind":       "tool_call_update",
				"toolCallId": id,
				"status":     status,
				"output":     capToolText(output),
			}, true
		}
	case session.EventPendingSkillGateChanged:
		// The gate reaches the client through the permission request
		// bridge, not as a transcript update.
		return nil, false
	}
	return nil, false
}

// HasActiveTurn reports whether sessionID currently has an in-flight
// prompt turn. Used by CommandManager to reject session/command while a
// turn is running, the same way the TUI disables command dispatch while
// the agent is busy.
func (m *TurnManager) HasActiveTurn(sessionID string) bool {
	m.activeTurnsMu.Lock()
	defer m.activeTurnsMu.Unlock()
	_, ok := m.activeTurns[sessionID]
	return ok
}

// PromptTurn drives a single agent turn for the named session. It looks
// up the runtime, normalises the prompt, and delegates to runTurn.
//
// If a turn is already running for the same session, the duplicate is
// rejected with a serverError (-32000) and the first turn is unaffected.
func (m *TurnManager) PromptTurn(ctx context.Context, params json.RawMessage) (any, error) {
	var p PromptTurnParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/prompt params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/prompt requires sessionId")
	}

	// Normalise content blocks into a flat prompt string.
	prompt, err := normalizePrompt(p.Prompt)
	if err != nil {
		return nil, err
	}

	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}

	m.ensureChildForwarder(p.SessionID, rt)
	return m.runTurn(ctx, p.SessionID, rt, rt.Run, prompt, resultOrError)
}

// ensureChildForwarder subscribes, once per session per TurnManager
// lifetime, to the session's event broker for as long as the runtime is
// alive. The goroutine forwards a background subagent's child question to
// the client via the question bridge ONLY while no turn is active: with a
// turn running, runTurn's own subscription delivers it (the parent model
// or the client answers mid-turn), and double-asking is prevented by the
// shared childAsked guard.

// childForwarder records one session's idle-question subscription: the
// cancel ends it, and the broker it subscribed to identifies the runtime
// generation (a session/load swaps both).
type childForwarder struct {
	cancel context.CancelFunc
	broker *pubsub.Broker[session.Event]
}

func (m *TurnManager) ensureChildForwarder(sessionID string, rt *TurnRuntime) {
	if rt == nil || rt.Events == nil || m.qbridge == nil {
		return
	}
	broker := rt.Events
	m.childForwardersMu.Lock()
	if existing, ok := m.childForwarders[sessionID]; ok {
		if existing.broker == broker {
			m.childForwardersMu.Unlock()
			return
		}
		// Runtime swap (session/load): the old broker's subscription is
		// dead or dying; replace it with one on the new broker so idle
		// child questions on the reloaded runtime still surface.
		existing.cancel()
	}
	fwdCtx, cancel := context.WithCancel(context.Background())
	m.childForwarders[sessionID] = &childForwarder{cancel: cancel, broker: broker}
	m.childForwardersMu.Unlock()

	sub := rt.Events.Subscribe(fwdCtx, pubsub.WithTerminal[session.Event]())
	go func() {
		defer cancel()
		for ev := range sub {
			if ev.Type != session.EventChildQuestionChanged {
				continue
			}
			pending := ev.Payload.PendingChildQuestion
			if pending == nil {
				continue
			}
			// A live turn owns delivery: its forwarder will drive the
			// bridge with turnCtx so a turn cancel also cancels the ask.
			if m.turnActive(sessionID) {
				continue
			}
			if _, loaded := m.childAsked.LoadOrStore(pending.ResponseChan, true); loaded {
				continue
			}
			// Each ask runs on its own goroutine so the terminal
			// subscription keeps draining: a blocking Ask here would block
			// publishEvent (must-deliver), stalling the child's own
			// AskParent publish. fwdCtx bounds the ask's lifetime to the
			// runtime subscription: a session teardown cancels the
			// in-flight client ask and answers Unanswered below on error.
			ask := pending
			go func() {
				if err := m.qbridge.AskChild(fwdCtx, sessionID, ask); err != nil {
					slog.Default().Warn("acp: idle child question bridge failed; answering Unanswered",
						"session", sessionID, "child", ask.ChildID, "err", err)
					ask.Respond(session.UnansweredAnswers(ask.Questions))
				}
			}()
		}
	}()
}

// turnActive reports whether sessionID currently has an in-flight turn.
func (m *TurnManager) turnActive(sessionID string) bool {
	m.activeTurnsMu.Lock()
	defer m.activeTurnsMu.Unlock()
	_, active := m.activeTurns[sessionID]
	return active
}

// SwarmStart handles session/swarm_start. Like session/prompt, this call is
// synchronous and blocks until the run finishes or is cancelled via
// session/cancel from another in-flight request; Cast reflects the final
// role roster read from session state after the run completes, not a live
// preflight snapshot.
func (m *TurnManager) SwarmStart(ctx context.Context, params json.RawMessage) (any, error) {
	var p SwarmStartParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/swarm_start params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/swarm_start requires sessionId")
	}
	if strings.TrimSpace(p.Goal) == "" {
		return nil, invalidParamsError("session/swarm_start requires a non-empty goal")
	}

	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.SwarmRunner == nil {
		return nil, serverErrorf("session %s does not support swarm runs", p.SessionID)
	}

	res, err := m.runTurn(ctx, p.SessionID, rt, RunnerFunc(rt.SwarmRunner.Run), p.Goal, resultOrError)
	if err != nil {
		return nil, err
	}
	ptr := res.(PromptTurnResult)
	result := SwarmTurnResult{StopReason: ptr.StopReason}
	if rt.State != nil {
		for _, r := range rt.State.SwarmProgress().Roles {
			result.Cast = append(result.Cast, SwarmCastEntry{Name: r.Name, Status: string(r.Status)})
		}
	}
	return result, nil
}

// runTurn reserves the per-session active-turn slot, runs run(turnCtx, arg)
// to completion while forwarding session events as session/update
// notifications, and resolves the result via resultOf once run finishes
// (or the turn is cancelled). Shared by PromptTurn, SwarmStart, SDDStart,
// and SDDAnswer — the only things that differ between them are which
// RunnerFunc executes, what argument it receives, and how a terminal error
// maps to a wire result.
//
// If a turn is already running for sessionID, the duplicate is rejected
// with a serverError (-32000) and the first turn is unaffected.
func (m *TurnManager) runTurn(
	ctx context.Context,
	sessionID string,
	rt *TurnRuntime,
	run RunnerFunc,
	arg string,
	resultOf func(runErr error, slot *activeTurn) (any, error),
) (any, error) {
	// Create a slot context before making the slot visible so that
	// CancelAndWait never observes a slot without a cancel function.
	slotCtx, slotCancel := context.WithCancel(ctx)
	slot := &activeTurn{
		cancel: slotCancel,
		done:   make(chan struct{}),
	}

	// Reserve the per-session slot.
	m.activeTurnsMu.Lock()
	if _, exists := m.activeTurns[sessionID]; exists {
		m.activeTurnsMu.Unlock()
		slotCancel()
		return nil, serverErrorf("session %s already has an active turn", sessionID)
	}
	m.activeTurns[sessionID] = slot
	m.activeTurnsMu.Unlock()

	// Cleanup: cancel slot, remove from map, close done channel.
	defer func() {
		slotCancel()
		m.activeTurnsMu.Lock()
		if m.activeTurns[sessionID] == slot {
			delete(m.activeTurns, sessionID)
		}
		m.activeTurnsMu.Unlock()
		close(slot.done)
	}()

	// Post-turn auto-resume (spec §5): a resume fire that landed after
	// this turn's final loop-top drain armed the session latch. The
	// turn-end residual drain already persisted the report; the latch
	// still says "wake when idle". This waiter fires when the cleanup
	// defer releases the slot (close(slot.done) runs AFTER the slot is
	// deleted from the map), then checks the latch and starts the
	// follow-on turn while the session is genuinely idle. In-function
	// post-release checks are impossible: the defers run after finishTurn
	// computes its return value.
	go func() {
		<-slot.done
		if rt.State == nil {
			return
		}
		if name, repeat, ok := rt.State.TakeWatchResume(); ok {
			m.ResumeIfIdle(sessionID, name, repeat)
		}
	}()

	// Register runtime work. If the session is quiescing, return
	// requestCancelled without starting the runner.
	turnCtx, finish, err := rt.BeginWork(slotCtx)
	if err != nil {
		return nil, &jsonRPCError{Code: requestCancelled, Message: err.Error()}
	}
	defer finish()

	subCtx, subCancel := context.WithCancel(turnCtx)
	defer subCancel()
	sub := rt.Events.Subscribe(subCtx, pubsub.WithTerminal[session.Event]())

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(turnCtx, arg)
	}()

	// proj carries per-turn projection state: the accumulated thinking
	// text (for computing deltas) and the most recent active tool call
	// (for correlating tool_call_update with tool_call).
	proj := &turnProjection{}

	// turnAnswered guards the pending-question send so the unanswered
	// answer is delivered at most once per question identity (F-BUG-51).
	var turnAnswered sync.Map

	// turnGateAnswered guards the skill-gate permission request so a
	// duplicate pending publish cannot double-request the client (same
	// identity pattern as turnAnswered).
	var turnGateAnswered sync.Map

	// forward dispatches one session event to the ACP client. Defined
	// once and used in both the main loop and the post-run drain.
	forward := func(ev pubsub.Event[session.Event]) {
		update, hasUpdate := eventToSessionUpdate(ev, proj)
		if hasUpdate {
			if notifyErr := m.notify("session/update", SessionUpdateParams{
				SessionID: sessionID,
				Update:    update,
			}); notifyErr != nil {
				// Treat notify error as fatal.
				slotCancel()
				subCancel()
			}
		}

		// Drive pending approvals through the permission bridge
		// in a goroutine so the forwarder never blocks on the
		// bridge (F-CON-54).
		if ev.Type == session.EventPendingApprovalChanged &&
			ev.Payload.PendingApproval != nil {
			pa := ev.Payload.PendingApproval
			if m.bridge == nil {
				// F-SEC-13: without a bridge, the runner is blocked on
				// ResponseChan. Send a deny so the runner unblocks and the
				// turn proceeds. Log so operators can see the misconfig.
				pa.Respond(session.UserApprovalDecision{Approved: false})
				slog.Default().Warn("acp: pending approval arrived but no permission bridge; denied",
					"session", sessionID, "approval", pa.ID)
			} else {
				go func() {
					decision, err := m.bridge.Request(turnCtx, sessionID, pa)
					if err != nil {
						slotCancel()
						subCancel()
						return
					}
					// mode.request elevation: the tool contract makes the
					// responding transport responsible for applying the mode
					// (internal/tools/native/mode_request.go). Apply it and
					// broadcast mode_changed so every attached client stays
					// in sync. An empty Edited defaults to "edit", matching
					// the tool handler.
					if pa.Name == "mode.request" && decision.Approved {
						chosen := decision.Edited
						if chosen == "" {
							chosen = "edit"
						}
						// A mode.request may also carry a system-access
						// elevation (internal/tools/native/mode_request.go).
						// Apply it alongside the mode so the grant and the
						// mode land together.
						if modeRequestWantsSystem(pa.Args) {
							if !rt.applySystemAccess(true) {
								// The tool contract already told the model the grant
								// succeeded, so a silent failure would send it into
								// writes that cannot work. Surface it.
								slog.Default().Warn("acp: mode.request system elevation not applied: runtime has no system-access setter",
									"session", sessionID, "approval", pa.ID)
							}
						}
						if rt.SetMode != nil {
							if err := rt.SetMode(chosen); err != nil {
								slog.Default().Warn("acp: apply mode elevation",
									"session", sessionID, "mode", chosen, "err", err)
							} else {
								_ = m.notify("session/update", SessionUpdateParams{
									SessionID: sessionID,
									Update:    map[string]any{"kind": "mode_changed", "mode": chosen, "system_access": rt.systemAccess()},
								})
							}
						}
					}
				}()
			}
		}

		// Drive pending questions through the question bridge in a
		// goroutine so the forwarder never blocks on the client (mirrors
		// the permission bridge, F-CON-54). Without a bridge, preserve the
		// pre-extension behavior: auto-answer Unanswered. The turn-scoped
		// sync.Map guards against duplicate delivery when the event
		// re-fires (F-BUG-51).
		if ev.Type == session.EventPendingQuestionChanged &&
			ev.Payload.PendingQuestion != nil {
			pending := ev.Payload.PendingQuestion
			if _, loaded := turnAnswered.LoadOrStore(pending.ResponseChan, true); loaded {
				return
			}
			if m.qbridge == nil {
				pending.Respond(session.UnansweredAnswers(pending.Questions))
			} else {
				go func() {
					// No wall-clock question timeout: the wait ends on client
					// disconnect / session shutdown (turnCtx cancellation) or
					// a bridge error, which still answers Unanswered.
					if err := m.qbridge.Ask(turnCtx, sessionID, pending); err != nil {
						slog.Default().Warn("acp: question bridge failed; answering Unanswered",
							"session", sessionID, "err", err)
						pending.Respond(session.UnansweredAnswers(pending.Questions))
					}
				}()
			}
		}

		// Drive questions escalated from background subagents through the
		// same question bridge, tagged with a child attribution so the client
		// can render "from subagent <desc>". Mirrors the pending-question
		// branch above and reuses its turnAnswered guard (keyed by
		// ResponseChan, which is distinct per child) so a duplicate publish
		// cannot double-ask (F-BUG-51).
		if ev.Type == session.EventChildQuestionChanged &&
			ev.Payload.PendingChildQuestion != nil {
			pending := ev.Payload.PendingChildQuestion
			// The child-question guard is manager-scoped (m.childAsked),
			// not turnAnswered: the idle forwarder shares it so the two
			// paths cannot double-ask across a turn boundary.
			if _, loaded := m.childAsked.LoadOrStore(pending.ResponseChan, true); loaded {
				return
			}
			if m.qbridge == nil {
				pending.Respond(session.UnansweredAnswers(pending.Questions))
			} else {
				go func() {
					if err := m.qbridge.AskChild(turnCtx, sessionID, pending); err != nil {
						slog.Default().Warn("acp: child question bridge failed; answering Unanswered",
							"session", sessionID, "child", pending.ChildID, "err", err)
						pending.Respond(session.UnansweredAnswers(pending.Questions))
					}
				}()
			}
		}

		// Drive skill-gate prompts through the permission bridge in a
		// goroutine so the forwarder never blocks on the bridge (mirrors
		// the approval/question bridges, F-CON-54). ACP clients see an
		// approve/deny pair: approve = allow once, deny = sticky deny.
		if ev.Type == session.EventPendingSkillGateChanged &&
			ev.Payload.PendingSkillGate != nil {
			sg := ev.Payload.PendingSkillGate
			if _, loaded := turnGateAnswered.LoadOrStore(sg.ResponseChan, true); loaded {
				return
			}
			if m.bridge == nil {
				// Without a bridge the runner is blocked on ResponseChan.
				// Deny so the turn proceeds; log the misconfig (F-SEC-13).
				sg.Respond(session.SkillGateDeny)
				slog.Default().Warn("acp: skill gate prompt arrived but no permission bridge; denied",
					"session", sessionID, "skill", sg.Skill)
			} else {
				go func() {
					if _, err := m.bridge.RequestSkillGate(turnCtx, sessionID, sg); err != nil {
						slotCancel()
						subCancel()
					}
				}()
			}
		}
	}

	forwarding := true
	var runErrVal error
	for forwarding {
		select {
		case <-turnCtx.Done():
			// Turn cancelled (client cancel or parent shutdown).
			subCancel()
			err = <-runErr
			runErrVal = err
			forwarding = false
		case err = <-runErr:
			forwarding = false
			runErrVal = err
		case ev, ok := <-sub:
			if !ok {
				sub = nil
				continue
			}
			forward(ev)
			// If forwarding encountered a fatal error, cancel the
			// turn and wait for the runner.
			if turnCtx.Err() != nil {
				subCancel()
				err = <-runErr
				runErrVal = err
				forwarding = false
			}
		}
	}

	// Drain remaining buffered events from the terminal subscription.
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				return m.finishTurn(sessionID, rt, runErrVal, slot, resultOf)
			}
			forward(ev)
		default:
			return m.finishTurn(sessionID, rt, runErrVal, slot, resultOf)
		}
	}
}

// ResumeIfIdle starts a server-initiated auto-resume turn for sessionID
// when the session is idle: no active turn slot, the config gate is on,
// and no human gate is pending. It is the ACP twin of the TUI's
// handleWatchMsg wake. Called from two sites: the Runtime.WatchResume
// hook (idle fire; spawned in a goroutine because OnFire runs on the
// watch goroutine) and the post-turn latch check (final-window fire).
// Occupied or guarded → return; the running turn's loop-top drain or the
// turn-end latch handles the report.
func (m *TurnManager) ResumeIfIdle(sessionID string, name string, repeat bool) {
	rt, ok := m.lookup(sessionID)
	if !ok || rt == nil || rt.Run == nil || rt.State == nil {
		return
	}
	if rt.WatchResumeEnabled != nil && !rt.WatchResumeEnabled() {
		return
	}
	if rt.WatchResumeGatePending != nil && rt.WatchResumeGatePending() {
		return
	}
	m.activeTurnsMu.Lock()
	if _, exists := m.activeTurns[sessionID]; exists {
		m.activeTurnsMu.Unlock()
		return
	}
	m.activeTurnsMu.Unlock()
	// Reserve the slot via runTurn itself — its atomic reservation is the
	// single serialization point; a race with a concurrent client prompt is
	// decided by runTurn's duplicate rejection (loser returns, no side
	// effects). Build the wrapper goal (quote from rt.State's last
	// assistant message) and start the turn in a goroutine so the caller
	// (the watch goroutine) never blocks on RPC.
	lastAssistant := lastAssistantText(rt.State)
	goal := watch.ResumeGoal(name, lastAssistant, repeat)
	go func() {
		// A client prompt arriving mid-resume-turn is rejected by the
		// existing one-turn-per-session rule, same as any duplicate.
		_, _ = m.runTurn(context.Background(), sessionID, rt, rt.Run, goal, resultOrError)
	}()
}

// lastAssistantText returns the last RoleAssistant message content in
// state, or "" (same backward-scan pattern as the TUI's helper).
func lastAssistantText(state *session.State) string {
	if state == nil {
		return ""
	}
	msgs := state.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == session.RoleAssistant {
			return msgs[i].Content
		}
	}
	return ""
}

// resultOrError maps the finished-turn state to a return value.
func resultOrError(runErr error, slot *activeTurn) (any, error) {
	if slot.clientCancelled.Load() {
		return PromptTurnResult{StopReason: "cancelled"}, nil
	}
	if runErr != nil {
		// Turn failures (provider errors, malformed model output) are
		// server-generated, so they are safe to expose per F-SEC-37 —
		// without this the client gets an opaque -32603 "internal error"
		// and the actual cause exists only in the server's stderr log.
		return nil, serverErrorf("turn failed: %v", runErr)
	}
	return PromptTurnResult{StopReason: "end_turn"}, nil
}

// SDDStartParams is the JSON-RPC body for session/sdd_start.
type SDDStartParams struct {
	SessionID string `json:"sessionId"`
	PlanPath  string `json:"planPath"`
}

// SDDGateInfo describes a pending human-gate question raised by a plan
// subagent. Named distinctly from session.SDDGate to avoid colliding with
// that (unexported-field-bearing) type on the wire.
type SDDGateInfo struct {
	TaskN    int    `json:"taskN"`
	Question string `json:"question"`
}

// SDDTurnResult is the JSON-RPC result for session/sdd_start and
// session/sdd_answer.
type SDDTurnResult struct {
	StopReason string       `json:"stopReason"`
	Gate       *SDDGateInfo `json:"gate,omitempty"`
}

// sddResultOf maps a finished (or gated) SDD turn to a wire result. Unlike
// resultOrError, a pipeline.ErrHumanGateRequired terminal error is not an
// error at the wire level — it's a distinct stopReason the client resolves
// via session/sdd_answer.
func sddResultOf(runErr error, slot *activeTurn) (any, error) {
	if slot.clientCancelled.Load() {
		return SDDTurnResult{StopReason: "cancelled"}, nil
	}
	if errors.Is(runErr, pipeline.ErrHumanGateRequired) {
		return SDDTurnResult{StopReason: "gate"}, nil
	}
	if runErr != nil {
		return nil, runErr
	}
	return SDDTurnResult{StopReason: "end_turn"}, nil
}

// finishSDDResult fills in Gate details (sddResultOf can't reach
// rt.State) and retires the stored pipeline runner once the run is no
// longer resumable (anything but a fresh gate). Shared by SDDStart and
// SDDAnswer.
func (m *TurnManager) finishSDDResult(sessionID string, rt *TurnRuntime, res any, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	result := res.(SDDTurnResult)
	if result.StopReason == "gate" {
		if rt.State != nil {
			g := rt.State.SDDGate()
			if g.Question != "" {
				result.Gate = &SDDGateInfo{TaskN: g.TaskN, Question: g.Question}
			}
		}
		return result, nil
	}
	m.pipelineRunnersMu.Lock()
	delete(m.pipelineRunners, sessionID)
	m.pipelineRunnersMu.Unlock()
	return result, nil
}

// TelemetryContext is the "context" section of a session_telemetry
// session/update payload.
type TelemetryContext struct {
	Messages      int `json:"messages"`
	MessageChars  int `json:"messageChars"`
	PackTokens    int `json:"packTokens"`
	PackMaxTokens int `json:"packMaxTokens"`
	PackSections  int `json:"packSections"`
}

func buildTelemetryContext(state *session.State) TelemetryContext {
	msgs := state.Messages()
	var chars int
	for _, msg := range msgs {
		chars += len(msg.Content)
	}
	pack := state.ContextPack()
	return TelemetryContext{
		Messages:      len(msgs),
		MessageChars:  chars,
		PackTokens:    pack.TokenUsage.EstimatedTokens,
		PackMaxTokens: pack.TokenUsage.MaxTokens,
		PackSections:  len(pack.Sections),
	}
}

// TelemetryToolStat is one entry in the "toolStats" section.
type TelemetryToolStat struct {
	Name      string `json:"name"`
	Calls     int    `json:"calls"`
	Errors    int    `json:"errors"`
	SlowestMs int64  `json:"slowestMs"`
}

// buildToolStats aggregates an audit log by tool name, most-called first
// (ties break alphabetically), the same ordering as the TUI's
// sidepanel.ToolStats — reimplemented here rather than imported to keep
// internal/acp free of any internal/app/tui/sidepanel dependency.
func buildToolStats(events []registry.AuditEvent) []TelemetryToolStat {
	idx := map[string]*TelemetryToolStat{}
	for _, e := range events {
		s, ok := idx[e.ToolName]
		if !ok {
			s = &TelemetryToolStat{Name: e.ToolName}
			idx[e.ToolName] = s
		}
		s.Calls++
		if e.Error != "" {
			s.Errors++
		}
		if ms := e.Duration.Milliseconds(); ms > s.SlowestMs {
			s.SlowestMs = ms
		}
	}
	out := make([]TelemetryToolStat, 0, len(idx))
	for _, s := range idx {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// TelemetrySessionFooter is the "sessionFooter" section. Unlike the TUI's
// DB-backed SessionSection (cumulative tokens/elapsed across up to 24
// turns), this is built entirely from in-memory session.State — no DB
// dependency was added to TurnRuntime for this. Turns counts completed
// assistant turns; token fields reflect only the most recent turn.
type TelemetrySessionFooter struct {
	Turns                int `json:"turns"`
	LastTurnTokensUsed   int `json:"lastTurnTokensUsed"`
	LastTurnTokensWindow int `json:"lastTurnTokensWindow"`
}

func buildSessionFooter(state *session.State) TelemetrySessionFooter {
	var turns int
	for _, msg := range state.Messages() {
		if msg.Role == session.RoleAssistant && msg.Final {
			turns++
		}
	}
	used, window := state.TurnUsage()
	return TelemetrySessionFooter{
		Turns:                turns,
		LastTurnTokensUsed:   used,
		LastTurnTokensWindow: window,
	}
}

// baseRefFor returns the fixed commit sessionID's changed-files diff is
// computed against, computing and caching it via gitinfo.HeadSHA on first
// use.
func (m *TurnManager) baseRefFor(sessionID, workingDir string) string {
	m.baseRefsMu.Lock()
	defer m.baseRefsMu.Unlock()
	if ref, ok := m.baseRefs[sessionID]; ok {
		return ref
	}
	ref := gitinfo.HeadSHA(workingDir)
	m.baseRefs[sessionID] = ref
	return ref
}

// TelemetryChangedFile is one entry in the "changedFiles" section.
type TelemetryChangedFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

func (m *TurnManager) buildChangedFiles(sessionID string, state *session.State) []TelemetryChangedFile {
	ref := m.baseRefFor(sessionID, state.WorkingDir)
	files := changedfiles.Read(state.WorkingDir, ref)
	out := make([]TelemetryChangedFile, len(files))
	for i, f := range files {
		out[i] = TelemetryChangedFile{Path: f.Path, Added: f.Added, Removed: f.Removed}
	}
	return out
}

// buildTelemetry assembles the full session_telemetry payload. Every
// top-level field is always present (never a nil-marshaling-to-null
// slice), so a client can treat this as a full-replace snapshot.
func (m *TurnManager) buildTelemetry(sessionID string, state *session.State) map[string]any {
	rules := state.SessionRules()
	if rules == nil {
		rules = []string{}
	}
	return map[string]any{
		"kind":          "session_telemetry",
		"context":       buildTelemetryContext(state),
		"changedFiles":  m.buildChangedFiles(sessionID, state),
		"toolStats":     buildToolStats(state.AuditLog()),
		"rules":         rules,
		"sessionFooter": buildSessionFooter(state),
	}
}

// finishTurn resolves a turn's result via resultOf, then fires a
// best-effort session_telemetry notification built from rt.State before
// returning. A telemetry notify failure is logged, never surfaced as the
// turn's own error — the turn's result is already decided by the time
// this runs. Fires on every outcome (end_turn, cancelled, and SDD's
// gate) since state may have changed even when a run didn't reach a
// terminal success.
func (m *TurnManager) finishTurn(
	sessionID string,
	rt *TurnRuntime,
	runErrVal error,
	slot *activeTurn,
	resultOf func(runErr error, slot *activeTurn) (any, error),
) (any, error) {
	result, err := resultOf(runErrVal, slot)
	if rt.State != nil {
		if notifyErr := m.notify("session/update", SessionUpdateParams{
			SessionID: sessionID,
			Update:    m.buildTelemetry(sessionID, rt.State),
		}); notifyErr != nil {
			slog.Default().Warn("acp: session_telemetry notify failed", "session", sessionID, "err", notifyErr)
		}
	}
	return result, err
}

// SDDAnswerParams is the JSON-RPC body for session/sdd_answer.
type SDDAnswerParams struct {
	SessionID string `json:"sessionId"`
	Answer    string `json:"answer"`
}

// SDDAnswer handles session/sdd_answer: it resolves a pending human gate
// raised by SDDStart (or a previous SDDAnswer) and resumes the same
// runner instance on the same plan path, re-entering runTurn — this
// re-reserves the active-turn slot for the duration of the resumed run.
func (m *TurnManager) SDDAnswer(ctx context.Context, params json.RawMessage) (any, error) {
	var p SDDAnswerParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/sdd_answer params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/sdd_answer requires sessionId")
	}
	if strings.TrimSpace(p.Answer) == "" {
		return nil, invalidParamsError("session/sdd_answer requires a non-empty answer")
	}

	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}

	m.pipelineRunnersMu.Lock()
	run, exists := m.pipelineRunners[p.SessionID]
	m.pipelineRunnersMu.Unlock()
	if !exists {
		return nil, serverErrorf("session %s has no plan-execution run waiting on an answer", p.SessionID)
	}

	run.runner.AnswerGate(p.Answer)
	res, err := m.runTurn(ctx, p.SessionID, rt, RunnerFunc(run.runner.Run), run.planPath, sddResultOf)
	return m.finishSDDResult(p.SessionID, rt, res, err)
}

// SDDStart handles session/sdd_start. Like session/prompt, this call is
// synchronous. When the underlying controller raises a human gate, the
// call still returns successfully with StopReason "gate" and the pending
// question — resolve it with session/sdd_answer.
func (m *TurnManager) SDDStart(ctx context.Context, params json.RawMessage) (any, error) {
	var p SDDStartParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/sdd_start params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/sdd_start requires sessionId")
	}
	if strings.TrimSpace(p.PlanPath) == "" {
		return nil, invalidParamsError("session/sdd_start requires a non-empty planPath")
	}

	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.PipelineFactory == nil {
		return nil, serverErrorf("session %s does not support plan execution", p.SessionID)
	}
	runner := rt.PipelineFactory(p.PlanPath, nil)
	if runner == nil {
		return nil, serverErrorf("could not build a runner for plan %q", p.PlanPath)
	}

	m.pipelineRunnersMu.Lock()
	m.pipelineRunners[p.SessionID] = &sddRun{runner: runner, planPath: p.PlanPath}
	m.pipelineRunnersMu.Unlock()

	res, err := m.runTurn(ctx, p.SessionID, rt, RunnerFunc(runner.Run), p.PlanPath, sddResultOf)
	return m.finishSDDResult(p.SessionID, rt, res, err)
}

// SetModeParams is the JSON-RPC body for session/set_mode.
type SetModeParams struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode"`
	// SystemAccess is an overlay field: when true, the session's
	// system-access modifier is enabled alongside the mode. Absent or
	// false leaves the current flag untouched.
	SystemAccess bool `json:"system_access"`
	// SystemAccessSet records whether the client sent the field at all, so
	// "revoke to false" is distinguishable from "leave unchanged". It is a
	// wire-presence flag, not a value.
	SystemAccessSet bool `json:"-"`
}

// UnmarshalJSON records the presence of system_access alongside its value so
// an explicit false revokes and an absent field leaves the flag untouched.
func (p *SetModeParams) UnmarshalJSON(data []byte) error {
	type alias SetModeParams
	var raw struct {
		alias
		SystemAccess *bool `json:"system_access"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = SetModeParams(raw.alias)
	if raw.SystemAccess != nil {
		p.SystemAccess = *raw.SystemAccess
		p.SystemAccessSet = true
	}
	return nil
}

// SetMode handles session/set_mode: it applies the requested approval mode
// to the session's runtime and broadcasts a mode_changed session/update so
// every attached client stays in sync.
func (m *TurnManager) SetMode(ctx context.Context, params json.RawMessage) (any, error) {
	var p SetModeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/set_mode params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/set_mode requires sessionId")
	}
	// Normalize case before validation: ValidApprovalMode lowercases, so
	// "Auto" passes the check but would then be handed to the runtime as-is,
	// where the passthrough SetMode rejects anything that isn't lowercase.
	// Canonicalize here so the applied mode, the response, and the
	// mode_changed notification all carry the same lowercase value.
	p.Mode = strings.ToLower(p.Mode)
	if !policy.ValidApprovalMode(p.Mode) {
		return nil, invalidParamsError("invalid mode %q: want one of plan, default, edit, copilot, auto", p.Mode)
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	if rt.SetMode == nil {
		return nil, serverErrorf("session %s does not support mode switching", p.SessionID)
	}
	if err := rt.SetMode(p.Mode); err != nil {
		return nil, serverErrorf("set mode: %v", err)
	}
	// system_access is an overlay: true grants, false revokes, and an absent
	// field leaves the current value untouched (SystemAccessSet records
	// whether the client actually sent it). A client that granted the flag
	// must be able to take it back.
	if p.SystemAccessSet {
		if !rt.applySystemAccess(p.SystemAccess) {
			return nil, serverErrorf("session %s does not support system access", p.SessionID)
		}
	}
	// Broadcast the session's live system-access flag, not the request
	// field, so a plain set_mode call still reports the current value.
	if err := m.notify("session/update", SessionUpdateParams{
		SessionID: p.SessionID,
		Update:    map[string]any{"kind": "mode_changed", "mode": p.Mode, "system_access": rt.systemAccess()},
	}); err != nil {
		return nil, err
	}
	return map[string]any{"mode": p.Mode}, nil
}

// SteerParams is the JSON-RPC body for session/steer.
type SteerParams struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

// Steer handles session/steer: it enqueues a steering message into the
// session's steering queue. Steering requires an active turn — when the
// turn has ended, the client should send session/prompt instead, so a
// steer against an idle session is an explicit error rather than silently
// queued.
func (m *TurnManager) Steer(ctx context.Context, params json.RawMessage) (any, error) {
	var p SteerParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/steer params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/steer requires sessionId")
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, invalidParamsError("session/steer requires non-empty text")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	m.activeTurnsMu.Lock()
	_, active := m.activeTurns[p.SessionID]
	m.activeTurnsMu.Unlock()
	if !active {
		return nil, serverErrorf("session %s has no active turn; send session/prompt instead", p.SessionID)
	}
	if rt.Steer == nil {
		return nil, serverErrorf("session %s does not support steering", p.SessionID)
	}
	rt.Steer(p.Text)
	return map[string]any{}, nil
}

// sessionIDParams is the shared JSON-RPC params body for poll-style status
// methods that only need a sessionId.
type sessionIDParams struct {
	SessionID string `json:"sessionId"`
}

// SwarmRoleInfo mirrors session.SwarmRole for JSON transport.
type SwarmRoleInfo struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
	Tokens    int       `json:"tokens"`
	StartedAt time.Time `json:"startedAt,omitempty"`
}

// SwarmStatusResult is the JSON-RPC result for session/swarm_status.
type SwarmStatusResult struct {
	Goal       string          `json:"goal,omitempty"`
	Active     bool            `json:"active"`
	Roles      []SwarmRoleInfo `json:"roles,omitempty"`
	TokensUsed int             `json:"tokensUsed"`
	TokensMax  int             `json:"tokensMax"`
}

// SwarmStatus handles session/swarm_status: a poll-style read of the
// session's current swarm progress. Always safe to call, including before
// any run has started (returns the zero value, Active: false).
func (m *TurnManager) SwarmStatus(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/swarm_status params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/swarm_status requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.State == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no state"}
	}
	prog := rt.State.SwarmProgress()
	result := SwarmStatusResult{
		Goal:       prog.Goal,
		Active:     prog.Active,
		TokensUsed: prog.TokensUsed,
		TokensMax:  prog.TokensMax,
	}
	for _, r := range prog.Roles {
		result.Roles = append(result.Roles, SwarmRoleInfo{
			Name: r.Name, Status: string(r.Status), Detail: r.Detail,
			Tokens: r.Tokens, StartedAt: r.StartedAt,
		})
	}
	return result, nil
}

// SDDStatusResult is the JSON-RPC result for session/sdd_status.
type SDDStatusResult struct {
	Active       bool         `json:"active"`
	PlanName     string       `json:"planName,omitempty"`
	PlanPath     string       `json:"planPath,omitempty"`
	Branch       string       `json:"branch,omitempty"`
	Tasks        []string     `json:"tasks,omitempty"`
	TotalTasks   int          `json:"totalTasks"`
	DoneTasks    int          `json:"doneTasks"`
	CurrentTask  int          `json:"currentTask"`
	Phase        string       `json:"phase,omitempty"`
	Detail       string       `json:"detail,omitempty"`
	FixRound     int          `json:"fixRound"`
	MaxFixRounds int          `json:"maxFixRounds"`
	TokensUsed   int          `json:"tokensUsed"`
	TokensMax    int          `json:"tokensMax"`
	Finished     bool         `json:"finished"`
	Succeeded    bool         `json:"succeeded"`
	Gate         *SDDGateInfo `json:"gate,omitempty"`
}

// SDDStatus handles session/sdd_status: a poll-style read of the session's
// current plan-execution progress plus any pending gate. Always safe to
// call, including before any run has started.
func (m *TurnManager) SDDStatus(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/sdd_status params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/sdd_status requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.State == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no state"}
	}
	prog := rt.State.SDDProgress()
	g := rt.State.SDDGate()
	result := SDDStatusResult{
		Active:       prog.Active,
		PlanName:     prog.PlanName,
		PlanPath:     prog.PlanPath,
		Branch:       prog.Branch,
		Tasks:        prog.Tasks,
		TotalTasks:   prog.TotalTasks,
		DoneTasks:    prog.DoneTasks,
		CurrentTask:  prog.CurrentTask,
		Phase:        prog.Phase,
		Detail:       prog.Detail,
		FixRound:     prog.FixRound,
		MaxFixRounds: prog.MaxFixRounds,
		TokensUsed:   prog.TokensUsed,
		TokensMax:    prog.TokensMax,
		Finished:     prog.Finished,
		Succeeded:    prog.Succeeded,
	}
	if g.Question != "" {
		result.Gate = &SDDGateInfo{TaskN: g.TaskN, Question: g.Question}
	}
	return result, nil
}

// Cancel is the notification handler for session/cancel. It marks the
// active turn for the named session as client-cancelled, cancels its
// context, and returns immediately without waiting for the runner.
//
// Returns nil, nil. The caller dispatches this as a JSON-RPC notification;
// the return value is discarded and no response is sent to the client.
func (m *TurnManager) Cancel(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/cancel params: %w", err)
		}
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("acp: session/cancel requires sessionId")
	}

	m.activeTurnsMu.Lock()
	slot, ok := m.activeTurns[p.SessionID]
	m.activeTurnsMu.Unlock()
	if ok && slot != nil {
		slot.clientCancelled.Store(true)
		slot.cancel()
	}
	return nil, nil
}

// cancelWait is the default fallback timeout for CancelAndWait. Tests
// override it per-instance via TurnManager.cancelTimeout.
const cancelWait = 30 * time.Second

// CancelAndWait cancels the active turn for the named session and blocks
// until the runner has fully completed. Returns nil if no turn is active
// (benign double-cancel). The wait is bounded by cancelWait even when the
// caller's context never cancels (F-BUG-50).
func (m *TurnManager) CancelAndWait(ctx context.Context, sessionID string) error {
	m.activeTurnsMu.Lock()
	slot, ok := m.activeTurns[sessionID]
	m.activeTurnsMu.Unlock()
	if !ok || slot == nil {
		return nil
	}

	slot.clientCancelled.Store(true)
	slot.cancel()

	timeout := cancelWait
	if m.cancelTimeout > 0 {
		timeout = m.cancelTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-slot.done:
		return nil
	case <-timer.C:
		return fmt.Errorf("acp: CancelAndWait timed out after %v waiting for slot %s", timeout, sessionID)
	case <-ctx.Done():
		return ctx.Err()
	}
}
