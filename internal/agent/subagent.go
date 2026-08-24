package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"marshal/internal/app/session"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// SubagentRequest carries the caller's explicit choices for one agent.run
// child. Agent is "" for an ad-hoc subtask or the name of a configured
// custom agent. Model is an optional "provider/model" pair; "" means use
// the default model selection (the factory's captured base model / the
// named agent's own preset).
//
// Role requests model resolution through the profile's binding for this
// role. It applies only when Model and Agent are both empty, and only
// when the role is explicitly bound in the active profile
// (router.ResolveRoleIfBound); otherwise the default model is used.
type SubagentRequest struct {
	Agent string
	Model string
	Role  routing.AgentRole
}

// SubagentModelPreview describes what model a subagent will use before it
// runs, so the handler can ask for user consent when the cost differs from
// the parent's model.
type SubagentModelPreview struct {
	Model    string
	Provider string
	Pricing  pricing.ModelPricing
}

// SubagentModelResolver resolves the model a SubagentRequest would use
// without building a full child runner. Returns the resolved model name,
// provider name, and pricing rates. When the request uses the parent's
// default model (no explicit model, no bound role, no named agent), the
// returned preview matches the parent's model.
type SubagentModelResolver func(req SubagentRequest) (SubagentModelPreview, error)

// SubagentRunnerFactory builds a fresh Runner bound to a fresh child
// session state. The factory deliberately does NOT take the prompt as an
// argument: the caller (agent.run) constructs the runner with the parent's
// provider, session config, and shared policy engine, and then passes the
// prompt to RunTask on the returned runner. This keeps the per-session
// construction (provider resolution, route, registry view sizing, role
// binding) in one place while leaving the prompt fetching/decoding to the
// tool handler.
type SubagentRunnerFactory func(req SubagentRequest) (*Runner, *session.State, error)

// runSubagentChild is the default runner runtime the agent.run tool uses to
// execute a child runner. It is a small wrapper around RunTask that returns
// the task summary and any salvage reason for tool-result rendering; tests
// inject a stub via SubagentOptionWithExec to avoid spinning a real provider.
func runSubagentChild(ctx context.Context, child *Runner, prompt string) (summary, salvagedReason string, err error) {
	if child == nil {
		return "", "", errors.New("agent.run: child runner is nil")
	}
	if prompt == "" {
		return "", "", errors.New("agent.run: prompt is required")
	}
	task, err := child.RunTask(ctx, prompt)
	if err != nil {
		return "", "", err
	}
	if task == nil {
		return "", "", nil
	}
	return task.Summary, task.SalvagedReason, nil
}

// SubtaskScopeView returns a registry view for a subtask child. It excludes
// agent.run plus its companions agent.await and agent.output (a subtask's
// own session has no subagents to wait on or peek at), deferred tools (MCP
// tools above the disclosure threshold and low-use native tools — the child
// should not auto-load arbitrary tool surfaces during an ad-hoc task), and
// question.ask plus its alias ask_user (a subtask runs in its own orphaned
// child session.State that no ACP client or TUI ever sees — there is no live
// user who could answer a question, so the call would block forever). All
// other tools are visible so the child can perform implementation work;
// their own Risk levels and the shared policy engine still gate approval
// for writes, commands, and destructive tools.
func SubtaskScopeView(src *registry.Registry) *registry.Registry {
	view := registry.New()
	for _, tool := range src.List() {
		if tool.Deferred {
			continue
		}
		if tool.Name == "agent.run" || tool.Name == "agent.await" || tool.Name == "agent.output" || tool.Name == "question.ask" || tool.Name == "ask_user" {
			continue
		}
		_ = view.Register(tool)
	}
	return view
}

// SubagentOption configures NewSubagentTool in tests.
type SubagentOption func(*subagentToolConfig)

type subagentToolConfig struct {
	exec           func(ctx context.Context, child *Runner, prompt string) (summary, salvagedReason string, err error)
	resolver       SubagentModelResolver
	parentModel    string
	parentProvider string
	parentPricing  pricing.ModelPricing

	// consentMu serializes model-consent approvals. State.PendingApproval
	// is a single slot; two concurrent agent.run calls that both need
	// consent would overwrite each other and strand the first caller's
	// ResponseChan. The tool handler is shared across the parent runner's
	// parallel batch, so the mutex lives on the config (not the handler).
	consentMu sync.Mutex
}

// WithSubagentExec overrides the default child runner executor. Used in
// tests so the suite does not need a live provider/model stub.
func WithSubagentExec(exec func(ctx context.Context, child *Runner, prompt string) (summary, salvagedReason string, err error)) SubagentOption {
	return func(cfg *subagentToolConfig) {
		cfg.exec = exec
	}
}

// WithSubagentModelResolver sets the model resolver used to preview the
// child's model before execution. When set, the handler checks whether the
// child's model has different cost implications and asks for user consent
// if so. When nil (test default), the consent gate is skipped.
func WithSubagentModelResolver(resolver SubagentModelResolver) SubagentOption {
	return func(cfg *subagentToolConfig) {
		cfg.resolver = resolver
	}
}

// WithSubagentParentModel records the parent runner's model and pricing so
// the consent gate can compare against the child's resolved model.
func WithSubagentParentModel(model string, p pricing.ModelPricing) SubagentOption {
	return func(cfg *subagentToolConfig) {
		cfg.parentModel = model
		cfg.parentPricing = p
	}
}

// WithSubagentParentProvider records the parent runner's provider name so
// the consent gate can require approval whenever the child resolves to a
// different provider, even when the two providers' pricing tables are equal
// or unknown (both zero). A provider switch is a different billing system,
// which the user should always be asked about.
func WithSubagentParentProvider(provider string) SubagentOption {
	return func(cfg *subagentToolConfig) {
		cfg.parentProvider = provider
	}
}

// agentRunArgs captures the JSON payload the agent passes to agent.run.
// Description is a short human label used in the tool result summary so the
// parent agent can identify which subtask produced which artefact.
// Agent is an optional custom-agent name; when set, the factory resolves
// the named agent's overrides. Model is an optional "provider/model" pair
// that overrides the default model selection (or the named agent's own
// preset).
type agentRunArgs struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
}

// NewSubagentTool returns the registry.Tool entry for agent.run. The
// factory builds a fresh subagent runner; the state enforces the depth and
// concurrency guards.
func NewSubagentTool(factory SubagentRunnerFactory, resolver SubagentModelResolver, reg *registry.Registry, state *session.State, opts ...SubagentOption) registry.Tool {
	cfg := subagentToolConfig{exec: runSubagentChild, resolver: resolver}
	for _, opt := range opts {
		opt(&cfg)
	}
	// State the configured cap, not a hardcoded number: the session enforces
	// exactly this, and a stale description teaches the model to batch the
	// wrong number of parallel calls. The description must also teach the
	// async contract — a model that assumes a synchronous result will
	// hallucinate completions.
	concurrency := state.SubagentMaxConcurrency()
	tool := registry.Tool{
		Name:        "agent.run",
		Description: fmt.Sprintf("Delegate a scoped subtask to a fresh child agent context. The child runs in the BACKGROUND: this tool returns immediately with a handle (subagent N) and the child's report is delivered to you as a [subagent N finished] message when it completes — do NOT assume the task is done when this tool returns. Call agent.await with the subagent id when you need the result before continuing, or agent.output to peek at progress. Maximum depth: 1. Maximum concurrency: %d. Multiple agent.run calls in a single response all start immediately (max %d in flight); writes across agents are serialized. Pass `agent` to run as a named custom agent (configured via /agents); omit for an ad-hoc subtask. Pass `model` as an explicit provider/model pair to override the model selection; an explicit `model` takes precedence over the named `agent`'s own preset. The child has the same implementation tools as the parent except nested agent.run and question.ask (its session has no user who could answer). Use this tool when a loaded skill instructs you to dispatch or spawn a subagent.", concurrency, concurrency),
		Schema: json.RawMessage(
			`{"type":"object","properties":{"prompt":{"type":"string","description":"The subtask description passed verbatim to the child agent."},"description":{"type":"string","description":"A short label for the subtask shown in the tool result summary."},"agent":{"type":"string","description":"Name of a configured custom agent to run as. Omit for an ad-hoc subtask."},"model":{"type":"string","description":"Optional provider/model pair (e.g. \"openai/gpt-4o-mini\") to run the child on. Omitted uses the default model selection; explicit overrides the named agent's own preset."}},"required":["prompt","description"],"additionalProperties":false}`,
		),
		// The dispatch itself stays RiskWorkspaceWrite even though the handler
		// now returns immediately: the dispatch-time pre-write snapshot
		// (execute.go) is the only snapshot covering the child's forthcoming
		// writes — child runners have no Snapshotter.
		Risk: registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeAgentRunArgs(tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		// Admission stays synchronous so cap/depth violations fail the tool
		// call directly. The pairing ExitSubagent moves to the completion
		// goroutine below: the slot must stay held for the child's whole
		// background life, not just the dispatch.
		if err := state.EnterSubagent(); err != nil {
			return registry.ToolResult{}, err
		}

		// Model-cost consent: if a resolver is configured, preview the
		// child's model before building the runner. When the child's model
		// has different cost implications (different provider, paid vs free,
		// or >2x rate change), ask the user for approval via the session's
		// pending-approval mechanism. This bypasses copilot mode's
		// auto-approve because it is a cost-consent issue, not a
		// workspace-write issue. The gate stays synchronous: it fires before
		// spawn, which is already non-blocking from the parent's
		// perspective.
		if cfg.resolver != nil {
			preview, err := cfg.resolver(SubagentRequest{Agent: args.Agent, Model: args.Model})
			if err != nil {
				state.ExitSubagent()
				return registry.ToolResult{}, fmt.Errorf("agent.run: resolve model: %w", err)
			}
			if modelChangeNeedsConsent(cfg.parentModel, cfg.parentProvider, preview.Model, preview.Provider, cfg.parentPricing, preview.Pricing) {
				// Serialize consent on the shared config: State.PendingApproval
				// is a single slot, and two concurrent agent.run calls that
				// both need consent would otherwise overwrite each other and
				// strand the first caller's ResponseChan.
				cfg.consentMu.Lock()
				approved, denied := requestSubagentConsent(ctx, state, fmt.Sprintf("Subagent will use %s @ %s (different model/provider/cost than parent %s @ %s). Approve?", preview.Model, preview.Provider, cfg.parentModel, cfg.parentProvider))
				cfg.consentMu.Unlock()
				if !approved {
					state.ExitSubagent()
					if denied {
						return registry.ToolResult{}, fmt.Errorf("agent.run: user denied subagent model change to %s @ %s", preview.Model, preview.Provider)
					}
					return registry.ToolResult{}, fmt.Errorf("agent.run: consent request cancelled")
				}
			}
		}

		child, childState, err := factory(SubagentRequest{Agent: args.Agent, Model: args.Model})
		if err != nil {
			state.ExitSubagent()
			return registry.ToolResult{}, fmt.Errorf("agent.run: build child: %w", err)
		}
		// Register a summary card in the parent transcript in place of the
		// child's full tool log; the drill-down view reaches the live child
		// transcript through the registered Child state. Registration is
		// synchronous so the immediate result carries the ID and the card
		// anchors at the dispatch position.
		meta := session.SubagentMeta{
			Role: RoleSubtask,
		}
		if child.Model != "" {
			meta.Model = child.Model
		}
		if child.Provider != nil {
			meta.Provider = child.Provider.Name()
		}
		view := state.RegisterSubagentWithMeta(args.Description, childState, meta)
		// The child's context derives from the SESSION, not this tool call:
		// a background child must survive the parent turn ending normally,
		// and turn-cancel (Esc) must not kill it. Session Shutdown still
		// cancels it (State.Context is cancelled by Shutdown).
		childCtx, cancel := context.WithCancel(state.Context())
		state.SetSubagentCancel(view.ID, cancel)
		prev := child.UsageObserver
		child.UsageObserver = func(u schema.TokenUsage) {
			if prev != nil {
				prev(u)
			}
			state.AddSubagentTokens(view.ID, u.PromptTokens+u.CompletionTokens)
		}
		go func() {
			defer cancel()
			// I1: a panic in the child (cfg.exec, a tool handler, the SQLite
			// write path) must not crash the whole process. The child now runs
			// on a bare goroutine with no parent-runner panic handling, so
			// recover here and surface the failure as a failed subagent.
			defer func() {
				if r := recover(); r != nil {
					state.Logger().Error("subagent panicked", "id", view.ID, "panic", r)
					state.PushSubagentReport(fmt.Sprintf("[subagent %d failed] subagent %d (%s) panicked: %v", view.ID, view.ID, args.Description, r))
					state.ExitSubagent()
					state.FinishSubagent(view.ID, "", fmt.Errorf("subagent %d panicked: %v", view.ID, r))
				}
			}()
			summary, salvagedReason, runErr := cfg.exec(childCtx, child, args.Prompt)
			if salvagedReason != "" {
				state.SetSubagentSalvaged(view.ID, salvagedReason)
			}
			errText := ""
			if runErr != nil {
				errText = runErr.Error()
			}
			summaryLine, content := subagentResultText(view.ID, args.Description, summary, salvagedReason, errText)
			// Completion delivery is two-channel and unconditional.
			// The transcript notice is for the user and appears immediately
			// (the repaint rides the FinishSubagent broker event). The report
			// push is for the model: RoleSystem transcript messages never
			// replay into the wire (buildHistoryMessages only replays user
			// turns and final answers) and session mail is transcript-only, so
			// the subagent report queue is the one channel that reaches the
			// model from a busy turn (drained as a user message at the next
			// loop-top) and from an idle session (drained at the next turn's
			// start). It is deliberately separate from the human steering
			// queue so a turn-cancel (ClearSteering) or blank-Enter follow-up
			// (PopSteering) can never drop a background child's report.
			mark, verb := "✓", "finished"
			if runErr != nil {
				mark, verb = "✗", "failed"
			}
			state.AddMessage(session.RoleSystem, mark+" "+summaryLine, session.ContentTypePlain)
			report := fmt.Sprintf("[subagent %d %s] %s", view.ID, verb, content)
			if salvagedReason != "" {
				// The salvage marker lives on the summary line; fold it into
				// the report message so the model sees the partial-report
				// caveat alongside the report body.
				report = fmt.Sprintf("[subagent %d %s] %s\n\n%s", view.ID, verb, summaryLine, content)
			}
			// I-1: persist the report as a RoleUser message so it survives
			// rollover/restart and is replayed by buildHistoryMessages
			// (which only replays RoleUser and final RoleAssistant, never
			// RoleSystem). The in-memory report queue handles live delivery
			// (drained at loop-top); this persisted copy is the durability
			// backstop for when the session restarts before the queue is
			// drained. The queue drain and the persisted message do not
			// double-deliver: the queue is cleared on drain, and
			// buildHistoryMessages only replays prior-turn messages.
			state.AddMessage(session.RoleUser, report, session.ContentTypePlain)
			// C2: push the report and release the concurrency slot BEFORE
			// FinishSubagent closes the done channel. A WaitSubagent caller
			// (agent.await) unblocks on that close, so it must observe the
			// report already queued and the slot already released — otherwise
			// there is no happens-before edge and the caller can race the
			// report push / ExitSubagent.
			state.PushSubagentReport(report)
			state.ExitSubagent()
			state.FinishSubagent(view.ID, summary, runErr)
		}()
		return registry.ToolResult{
			Summary: fmt.Sprintf("started as subagent %d — %s", view.ID, args.Description),
			Content: fmt.Sprintf("Subagent %d (%s) is running in the background (%d in flight, max %d). Its report will be delivered to you as a [subagent %d finished] message when it completes. To wait for it, call agent.await with \"id\": %d; to peek at progress, call agent.output with \"id\": %d. Do not treat the task as complete until its result arrives.",
				view.ID, args.Description, state.SubagentConcurrency(), state.SubagentMaxConcurrency(), view.ID, view.ID, view.ID),
		}, nil
	}
	return tool
}

// subagentResultText renders a finished subagent's outcome in the two
// shapes the async flow needs: a one-line summary (tool-result summaries,
// transcript notices) and a content body carrying the full report (await
// results, steering injections). The salvage remedy hint lives here so the
// await result and the completion message read identically.
func subagentResultText(id int64, label, summary, salvagedReason, errText string) (summaryLine, content string) {
	if errText != "" {
		return fmt.Sprintf("subagent %d failed: %s", id, label),
			fmt.Sprintf("subagent %d (%s) failed: %s", id, label, errText)
	}
	if salvagedReason != "" {
		return fmt.Sprintf("subagent %d completed (salvaged: %s): %s", id, salvagedReason, label),
			fmt.Sprintf("[note: this subagent hit its iteration budget (%s) and the report below is partial. Raise [agent] subtask_iterations or the custom agent's max_iterations for a longer budget.]\n\n%s", salvagedReason, summary)
	}
	return fmt.Sprintf("subagent %d completed: %s", id, label), summary
}

func decodeAgentRunArgs(tool registry.Tool, raw json.RawMessage) (agentRunArgs, error) {
	var args agentRunArgs
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return agentRunArgs{}, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
	}
	if args.Prompt == "" {
		return agentRunArgs{}, fmt.Errorf("%s requires non-empty prompt", tool.Name)
	}
	if args.Description == "" {
		return agentRunArgs{}, fmt.Errorf("%s requires non-empty description", tool.Name)
	}
	return args, nil
}

// modelChangeNeedsConsent reports whether switching from the parent's model
// to the child's model has cost implications that warrant asking the user.
//
// Consent is needed when:
//   - The provider differs (different billing system). This is checked
//     first and independently of pricing: two providers may have equal or
//     unknown (zero) pricing tables, but a provider switch is still a
//     different billing relationship the user should be asked about.
//   - The parent's pricing is zero but the child's is non-zero (paid model
//     spawned from a free/local one).
//   - The child's rates are more than 2x the parent's in either direction
//     (materially different cost tier).
//
// Same model string on the same provider never needs consent.
func modelChangeNeedsConsent(parentModel, parentProvider, childModel, childProvider string, parentPricing, childPricing pricing.ModelPricing) bool {
	if parentModel == childModel && parentProvider == childProvider {
		return false
	}
	if parentProvider != "" && childProvider != "" && parentProvider != childProvider {
		return true
	}
	parentFree := parentPricing.InputPerMTokCents == 0 && parentPricing.OutputPerMTokCents == 0
	childFree := childPricing.InputPerMTokCents == 0 && childPricing.OutputPerMTokCents == 0
	if parentFree && !childFree {
		return true
	}
	if !parentFree && !childFree {
		if parentPricing.InputPerMTokCents > 0 {
			if childPricing.InputPerMTokCents > 2*parentPricing.InputPerMTokCents ||
				parentPricing.InputPerMTokCents > 2*childPricing.InputPerMTokCents {
				return true
			}
		}
		if parentPricing.OutputPerMTokCents > 0 {
			if childPricing.OutputPerMTokCents > 2*parentPricing.OutputPerMTokCents ||
				parentPricing.OutputPerMTokCents > 2*childPricing.OutputPerMTokCents {
				return true
			}
		}
	}
	return false
}

// requestSubagentConsent sets a pending approval on the session state and
// blocks until the user responds. Returns (true, false) when approved,
// (false, true) when explicitly denied, and (false, false) when the
// context is cancelled (e.g. shutdown). The approval uses the existing
// PendingToolCall mechanism so the TUI renders the standard approve/deny
// panel — the consent reason is displayed as the approval reason.
func requestSubagentConsent(ctx context.Context, state *session.State, reason string) (approved, denied bool) {
	ch := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:           "subagent-model-consent",
		Name:         "agent.run",
		Args:         "",
		Risk:         "model-cost-consent",
		Reason:       reason,
		ResponseChan: ch,
	}
	state.SetPendingApproval(tc)
	defer state.SetPendingApproval(nil)
	select {
	case d := <-ch:
		return d.Approved, !d.Approved
	case <-ctx.Done():
		return false, false
	}
}
