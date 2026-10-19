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

	"marshal/internal/app/session"
	"marshal/internal/hooks"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

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
		r.logToolCall(toolCallID, event)
		r.countToolCall(true, false)
		return policyLoopResult{Messages: []schema.ChatMessage{r.buildToolErrorMessage(toolName, "denied by policy: "+reason, toolCallID)}}, nil
	case policy.DecisionConfirm:
		// State.PendingApproval is a single slot; two concurrent approvals
		// would overwrite each other and strand the first caller. Serialize
		// all approval requests on this runner, including those launched
		// from the parallel read-only batch in executeActions.
		r.approvalMu.Lock()
		approved, edited, waitErr := r.requestApproval(ctx, tool, toolName, args, argsMap, reason)
		r.approvalMu.Unlock()
		if waitErr != nil {
			return policyLoopResult{}, waitErr
		}
		if !approved {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, errors.New("denied by user"))
			r.logToolCall(toolCallID, event)
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
				newDecision, newReason, perr := r.Policy.Evaluate(toolName, argsMap, policy.WithSystem(r.State.SystemAccess()))
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

// toolArgsFor returns the arguments an audit event should record for an
// action: a patch action carries its payload in Content, every other action in
// Args.
func toolArgsFor(action ModelAction) json.RawMessage {
	if action.Type != ActionPatch {
		return action.Args
	}
	encoded, err := json.Marshal(map[string]string{"patch": action.Content})
	if err != nil {
		return nil
	}
	return encoded
}

// actionToolName resolves the registered tool name for an action; a patch
// action is the envelope spelling of file.write_patch.
func actionToolName(action ModelAction) string {
	if action.Type == ActionPatch {
		return "file.write_patch"
	}
	return action.Tool
}

// internalToolRedirect explains why a directly-named internal tool was refused
// and names the public tool that does the job. Internal tools are never
// offered to the model — no definition, no prompt listing, no deferred
// announcement — so a model naming one is reaching for a tool it was never
// shown. The envelope patch action is the one legitimate path and is exempted
// by the caller.
func internalToolRedirect(name string) string {
	switch name {
	case "file.write_patch":
		return "file.write_patch is not available to you; edit files with file.edit (exact old_string/new_string) or file.write (whole file)."
	case "test.run":
		return "test.run is not available to you; run tests with shell.run."
	default:
		return fmt.Sprintf("%s is an internal tool and is not available to you.", name)
	}
}

// newCallID mints the per-call identity used when the provider supplied none.
func (r *Runner) newCallID() string {
	return fmt.Sprintf("call_%d", r.Now().UnixNano())
}

// steeringNoticeKind is the ToolNotice kind marking a settled tool result the
// user's steering interrupted, rather than a tool failure.
const steeringNoticeKind = "steering_interruption"

// steeringInterruption wraps a steering cancellation with the operation
// identity it belongs to. The settled-result builder is reached from four
// different admission stages and from the question path, each of which has the
// operation on its context but not in its parameters; wrapping the cause is how
// the identity and the settled state reach the builder without widening a
// signature the batch machinery shares.
type steeringInterruption struct {
	ref   session.OperationRef
	state string
	cause error
}

func (e *steeringInterruption) Error() string { return e.cause.Error() }
func (e *steeringInterruption) Unwrap() error { return e.cause }

// steeringCause tags a cancellation with the state it settled in. An empty
// state derives from whether the call started and what it left behind.
func steeringCause(ctx context.Context, state string, cause error) error {
	if cause == nil {
		cause = session.ErrSendNow
	}
	ref, _ := session.OperationFromContext(ctx)
	return &steeringInterruption{ref: ref, state: state, cause: cause}
}

// steeringStopped reports whether err is this operation's own steering
// cancellation. A full-turn cancel, a shutdown, or a genuine command or
// provider failure is not: those keep their existing error handling, so a real
// failure that won the race is never relabelled as a user interruption.
func steeringStopped(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, session.ErrSendNow) || session.IsSteeringCancellation(ctx, err))
}

// steeringStateFor classifies a settled interrupted call. A call that never
// started is not_started; a call that ran and left observable evidence is
// interrupted. A call that ran and left nothing behind is unknown — whether
// its remote side effects landed is exactly what this runner cannot say, and
// the spec forbids claiming termination we did not confirm.
func steeringStateFor(started bool, result registry.ToolResult) string {
	if !started {
		return "not_started"
	}
	if partialResultEvidence(result) != "" {
		return "interrupted"
	}
	return "unknown"
}

// admitToolCall is the admission gate every stage of executeToolCall passes
// through. It reports stop=true with the call's settled messages when steering
// already owns this operation, so an unadmitted call is skipped instead of
// validated, prompted or launched. Any other error — a full-turn cancel, a
// shutdown — is not steering and is left to the stage's normal handling.
func (r *Runner) admitToolCall(ctx context.Context, action ModelAction) ([]schema.ChatMessage, bool) {
	err := session.CheckSteering(ctx)
	if !steeringStopped(ctx, err) {
		return nil, false
	}
	return r.settleSteeringCall(ctx, action, "", registry.ToolResult{}, false, steeringCause(ctx, "", err)), true
}

// settleSteeringCall settles one steering-interrupted call: it builds the
// model-facing message, records this operation's outcome entry, and counts the
// call as a tool call — deliberately NOT as an error, because an intentional
// interruption must not feed the repetitive-failure ladder or the todo-failure
// nudge (see steeringToolResult).
func (r *Runner) settleSteeringCall(ctx context.Context, action ModelAction, callID string, result registry.ToolResult, started bool, cause error) []schema.ChatMessage {
	msgs := r.steeringToolResult(action, result, started, cause)
	if callID == "" {
		callID = action.ToolCallID
	}
	r.recordSteeringOutcomes(ctx, callID, steeringOutcomesFor(action, result, started, cause))
	return msgs
}

// toolCallOutcomes builds the ledger entries for one settled call: the call
// itself in the given state, plus every target identity a structured result
// notice names (a managed command's execution identity, for instance). Targets
// travel separately so a receipt can report what the batch's interrupted work
// was doing without conflating it with the call row.
func toolCallOutcomes(action ModelAction, result registry.ToolResult, state string) []session.SteeringOutcome {
	outcomes := []session.SteeringOutcome{{
		Kind:    "tool",
		State:   state,
		Summary: SummarizeToolArgs(actionToolName(action), toolArgsFor(action)),
	}}
	if ex, ok := registry.DecodeCommandExecution(result.Notice); ok && ex.ExecutionID != "" {
		outcomes = append(outcomes, session.SteeringOutcome{
			Kind:        "command",
			State:       steeringStateForExecution(ex.State),
			ExecutionID: ex.ExecutionID,
			Summary:     ex.DisplayID,
		})
	}
	return outcomes
}

// steeringOutcomesFor is toolCallOutcomes for an interrupted call, where the
// state is derived from whether the call started and what it left behind (or
// from the interruption's own settled state when it has one). A result whose
// own notice already names structured target outcomes (agent.await's surviving
// children and jobs) contributes them alongside the call-level entry.
func steeringOutcomesFor(action ModelAction, result registry.ToolResult, started bool, cause error) []session.SteeringOutcome {
	state := steeringStateFor(started, result)
	if si, ok := cause.(*steeringInterruption); ok && si.state != "" {
		state = si.state
	}
	outcomes := toolCallOutcomes(action, result, state)
	return append(outcomes, steeringTargetOutcomes(result.Notice)...)
}

// steeringTargetOutcomes reads the structured target outcomes a settled
// interruption notice carries under Data["outcomes"]. Empty for every other
// notice: target identities are the await path's contribution only.
func steeringTargetOutcomes(notice *registry.ToolNotice) []session.SteeringOutcome {
	if notice == nil || notice.Kind != steeringNoticeKind || len(notice.Data) == 0 {
		return nil
	}
	targets, _ := notice.Data["outcomes"].([]session.SteeringOutcome)
	if len(targets) == 0 {
		return nil
	}
	return append([]session.SteeringOutcome(nil), targets...)
}

// steeringStateForExecution maps a managed execution's lifecycle state onto the
// outcome vocabulary. A running or stopping execution continues — the
// interruption ended the wait, not the command.
func steeringStateForExecution(state string) string {
	switch state {
	case "running", "stopping":
		return "continues"
	case "completed":
		return "completed"
	case "", "unknown":
		return "unknown"
	default:
		return "interrupted"
	}
}

// steeringToolResult builds the settled result for a call the user's steering
// interrupted. It is a RESULT, never a failure: the evidence a partially
// executed call produced is preserved verbatim (output, exit code, sandbox and
// stop evidence, files changed and the handler's own structured notice), a
// single bounded sentence explains the interruption, and one audit event
// records the call. It caches nothing — a partial result served from cache in a
// later turn would present interrupted output as current — and it never feeds
// the failed-repeat tracker, so an intentional interruption cannot escalate
// into repetitive-tool-failure coaching or mark the todo list failed.
func (r *Runner) steeringToolResult(action ModelAction, result registry.ToolResult, started bool, cause error) []schema.ChatMessage {
	toolName := actionToolName(action)
	toolCallID := action.ToolCallID
	state := steeringStateFor(started, result)
	ref := session.OperationRef{}
	summary := "interrupted by user steering (state: " + state + ")"
	if si, ok := cause.(*steeringInterruption); ok {
		ref = si.ref
		if si.state != "" {
			state = si.state
			summary = "interrupted by user steering (state: " + state + ")"
		}
	}
	if !started {
		summary = "not started: the operation was interrupted by user steering"
	}

	operation := ref.ID
	if operation == "" {
		operation = "the current operation"
	}
	// One short sentence: it names the operation and the state it settled in.
	// It deliberately does NOT repeat the tool's output — that output already
	// travels in the preserved evidence below, and duplicating raw output is
	// what the interruption-note budget exists to prevent.
	explanation := fmt.Sprintf("User steering interrupted %s before this call completed (state: %s).", operation, state)

	body := partialResultEvidence(result)
	if body != "" {
		body += "\n\n"
	}
	body += explanation

	// The notice rides on the audited result and on the message's tool result
	// so a consumer can switch on Kind and read the operation identity and
	// settled state as data instead of parsing the sentence.
	notice := &registry.ToolNotice{
		Kind: steeringNoticeKind,
		Text: explanation,
		Data: map[string]any{"operationId": ref.ID, "state": state},
	}
	carried := result
	carried.Summary = summary
	carried.Content = body
	carried.Notice = notice

	tool, _ := r.Registry.Lookup(toolName)
	call := registry.ToolCall{ID: toolCallID, Name: toolName, Args: toolArgsFor(action)}
	event := registry.NewAuditEvent(r.Now(), tool, call, carried, registry.ApprovalNotRequired, cause)
	event.Notice = notice
	r.logToolCall(toolCallID, event)
	// Counted as a tool call — one settled reply per declared call — and not as
	// an error: the run did not fail, it was steered.
	r.countToolCall(false, false)
	return []schema.ChatMessage{r.buildToolResultMessage(toolName, carried, toolCallID)}
}

// recordSteeringOutcomes appends this operation's settled outcomes to the
// per-operation ledger, stamping the call ID onto entries that carry none. The
// ledger is keyed by the COMPLETE OperationRef and initialized lazily so a
// Runner assembled as a bare struct literal still records.
func (r *Runner) recordSteeringOutcomes(ctx context.Context, toolCallID string, outcomes []session.SteeringOutcome) {
	if len(outcomes) == 0 {
		return
	}
	ref, _ := session.OperationFromContext(ctx)
	if ref.ID == "" {
		// No operation is open on this context, so there is no ledger entry to
		// attribute the outcome to. Recording under the zero ref would file it
		// where takeSteeringOutcomes never looks, so it would never reach a
		// receipt.
		return
	}
	stamped := make([]session.SteeringOutcome, 0, len(outcomes))
	for _, o := range outcomes {
		if o.ToolCallID == "" {
			o.ToolCallID = toolCallID
		}
		stamped = append(stamped, o)
	}
	r.steeringOutcomeMu.Lock()
	if r.steeringOutcomes == nil {
		r.steeringOutcomes = make(map[session.OperationRef][]session.SteeringOutcome)
	}
	r.steeringOutcomes[ref] = append(r.steeringOutcomes[ref], stamped...)
	r.steeringOutcomeMu.Unlock()
}

// takeSteeringOutcomes removes and returns one operation's recorded outcomes.
// It removes only that operation's copied slice, after the batch's workers have
// joined, so a later batch for a different operation never reads another
// operation's evidence.
func (r *Runner) takeSteeringOutcomes(ref session.OperationRef) []session.SteeringOutcome {
	r.steeringOutcomeMu.Lock()
	defer r.steeringOutcomeMu.Unlock()
	stored := r.steeringOutcomes[ref]
	if len(stored) == 0 {
		return nil
	}
	delete(r.steeringOutcomes, ref)
	return append([]session.SteeringOutcome(nil), stored...)
}

// acquireToolGate admits one non-read-only call through the shared write gate.
// A gate that implements ContextWriteGate wakes on cancellation and reports why
// it did not acquire. A legacy gate cannot, so the runner rechecks steering as
// soon as the token is held and releases it immediately rather than launching a
// call the user already interrupted — the wait itself stays waiting-safe, which
// is the honest behaviour for a fake gate that cannot be woken.
func (r *Runner) acquireToolGate(ctx context.Context) (func(), error) {
	if r.WriteGate == nil {
		return func() {}, nil
	}
	if gate, ok := r.WriteGate.(ContextWriteGate); ok {
		return gate.AcquireContext(ctx)
	}
	release := r.WriteGate.Acquire()
	if err := session.CheckSteering(ctx); steeringStopped(ctx, err) {
		release()
		return nil, err
	}
	return release, nil
}

func (r *Runner) executeToolCall(ctx context.Context, action ModelAction) ([]schema.ChatMessage, error) {
	if err := r.State.WaitUnheld(ctx); err != nil {
		return nil, err
	}
	toolName := actionToolName(action)
	toolCallID := action.ToolCallID
	callID := toolCallID
	if callID == "" {
		callID = r.newCallID()
	}

	// First admission gate: steering accepted for this operation outranks a
	// call the model already queued. Nothing is validated, prompted or
	// executed, and the call still gets a settled not-started result so its
	// tool_call has a pairing. A full-turn or session cancellation is not
	// steering: it keeps its existing error path.
	if msgs, stop := r.admitToolCall(ctx, action); stop {
		return msgs, nil
	}

	tool, ok := r.Registry.Lookup(toolName)
	if !ok {
		r.countToolCall(true, false)
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "unknown tool", toolCallID)}, nil
	}

	// Internal tools are never offered to the model, so a model naming one
	// directly is reaching for a tool it was never shown. The envelope patch
	// action is the runtime's own spelling of file.write_patch and is the one
	// legitimate path; every other direct call is refused with a redirect to
	// the public tool that does the job. Without this guard the flag only
	// suppressed advertisement — the handler still ran, so a model that
	// guessed the name got a tool it was never told about.
	if tool.Internal && action.Type != ActionPatch {
		r.countToolCall(true, false)
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, internalToolRedirect(toolName), toolCallID)}, nil
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

	// Validate before the cache lookup and before policy/approval, so the
	// user is never prompted to approve a call that cannot run and a bad
	// call can never be answered from cache.
	if err := registry.ValidateArgs(tool, args); err != nil {
		r.countToolCall(true, false)
		r.noteInvalidArgs()
		return []schema.ChatMessage{r.buildToolErrorMessage(toolName, err.Error(), toolCallID)}, nil
	}

	// Skill load gate: at small (or unknown) context windows every
	// skill.load — including re-fetches of an active skill — prompts the
	// user before loading, so a small model cannot burn its window on
	// unneeded skill bodies. Sits after ValidateArgs (a call that cannot
	// parse never prompts) and before the policy loop, so the copilot-mode
	// Confirm→Allow transform can never auto-approve it. Subagent/swarm
	// runners never gate (role != general); auto mode auto-allows.
	if toolName == "skill.load" && r.role() == RoleGeneral {
		if msgs, handled := r.skillLoadGate(ctx, tool, args, argsMap, toolCallID); handled {
			return msgs, nil
		}
	}

	// Cacheable read-only cache lookup.
	if tool.Cacheable {
		if cached, hit := r.State.GetTurnToolResult(toolName, normalizedArgs); hit {
			r.trackerMu.Lock()
			count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(cached.Content), true)
			r.trackerMu.Unlock()
			logged := cached
			logged.Summary = "(cached) " + logged.Summary
			call := registry.ToolCall{ID: fmt.Sprintf("call_%d", r.Now().UnixNano()), Name: toolName, Args: args}
			event := registry.NewAuditEvent(r.Now(), tool, call, logged, registry.ApprovalNotRequired, nil)
			r.logToolCall(toolCallID, event)
			r.countToolCall(false, true)
			msg := r.buildCachedToolResultMessage(toolName, cached, toolCallID)
			msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
			msg.Content += r.todoNudge()
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

		// Re-validate on every pass: a pre_tool_use hook may rewrite the
		// arguments into any shape after the user approved the original.
		if err := registry.ValidateArgs(tool, args); err != nil {
			r.countToolCall(true, false)
			r.noteInvalidArgs()
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, err.Error(), toolCallID)}, nil
		}

		r.Policy.SetSessionRules(r.State.SessionRules())
		decision, reason, evalErr := r.Policy.Evaluate(toolName, argsMap, policy.WithSystem(r.State.SystemAccess()))
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
			r.logToolCall(toolCallID, event)
			r.countToolCall(true, false)
			return []schema.ChatMessage{r.buildToolErrorMessage(toolName, "blocked by pre_tool_use hook: "+hookErr.Error(), toolCallID)}, nil
		}
		if hookOut.Decision == hooks.DecisionBlock {
			event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: toolName, Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("blocked by pre_tool_use hook: %s", hookOut.Reason))
			event.Hooks = hookAuditMetadata(hookOut)
			r.logToolCall(toolCallID, event)
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

	// Admission gate after approval and hooks: a rewrite (or an approval the
	// user granted just before steering was accepted) must not launch under
	// stale intent. The call is still admitted-or-not before any mutation, so
	// an interrupted call is reported as not started rather than as a failure.
	if msgs, stop := r.admitToolCall(ctx, action); stop {
		return msgs, nil
	}

	label := toolName
	if command, ok := argsMap["command"].(string); ok && command != "" {
		label = fmt.Sprintf("%s: %s", toolName, command)
	} else if summary := SummarizeToolArgs(toolName, args); toolName == "agent.await" && summary != "" {
		label = fmt.Sprintf("%s: %s", toolName, summary)
	}
	r.State.SetActivity(session.Activity{Kind: session.ActivityTool, Label: label, StartedAt: r.Now()})
	r.State.SetActiveToolCall(session.ActiveToolCall{
		Name:       toolName,
		Args:       SummarizeToolArgs(toolName, args),
		Path:       firstPatchPathFromArgs(toolName, args),
		StartedAt:  r.Now(),
		StepID:     r.curStep,
		ToolCallID: callID,
	})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
	defer r.State.ClearActiveToolCallID(callID)

	// Admission gate immediately before the mutation/launch. Between the gate
	// above and here the call may have captured a pre-write snapshot, which is
	// the last moment an unadmitted mutation can still be skipped: the
	// snapshot is protection for a write that actually launches, so a call
	// cancelled now leaves no side effect behind either.
	if msgs, stop := r.admitToolCall(ctx, action); stop {
		return msgs, nil
	}

	// Pre-write capture, non-read-only tools only. A capture that fails or is
	// SKIPPED now produces a user-visible warning through the shared helper;
	// a session DB row is written only for a capture that PUBLISHED a hash.
	// The tool still runs either way — no continuation, permission, or
	// approval behaviour changes here.
	if tool.Risk != registry.RiskReadOnly {
		r.snapshotCapture(ctx, SnapshotPhasePreWrite, changedFilesForTool(toolName, argsMap))
	}

	// The write gate serialises non-read-only execution across runners, so a
	// call can wait here for an unbounded time. A cancelable gate wakes when
	// this operation is interrupted; a legacy gate cannot, so the runner
	// rechecks admission as soon as it acquires and releases the token rather
	// than launching a call the user already interrupted.
	if tool.Risk != registry.RiskReadOnly {
		release, gateErr := r.acquireToolGate(ctx)
		if gateErr != nil {
			return r.settleSteeringCall(ctx, action, callID, registry.ToolResult{}, false,
				steeringCause(ctx, "", gateErr)), nil
		}
		defer release()
	}

	call := registry.ToolCall{ID: callID, Name: toolName, Args: args}
	start := time.Now()
	// Stamp the execution origin on the handler's context only now: policy,
	// approval and pre_tool_use validation have all passed, so a command this
	// handler launches is genuinely this turn's work. A call rejected before
	// this point must not leave an admission record naming a turn that never
	// dispatched it. The step/`callID` pair is the identity a persisted
	// execution record pairs back to this row.
	handlerCtx := session.WithExecutionOrigin(ctx, r.executionOrigin(r.curStep, callID))
	result, execErr := tool.Handler(handlerCtx, call)
	if execErr != nil {
		// Steering that interrupted an admitted call is not a tool failure:
		// the call genuinely started, so its partial result is preserved as
		// evidence and settled as an interrupted call. It never enters the
		// failed-repeat ladder (an intentional interruption is not a model
		// mistake to coach) and never counts as a tool error.
		if steeringStopped(ctx, execErr) {
			return r.settleSteeringCall(ctx, action, callID, result, true,
				steeringCause(ctx, steeringStateFor(true, result), execErr)), nil
		}
		// The handler failed, but it may still have returned a partial result:
		// a command that started and then failed has real output, an actual
		// exit code and possibly a stop cause, and discarding them left the
		// transcript with an empty failure row and the model with nothing to
		// reason about. Audit the partial result and carry its evidence into
		// the model-facing error.
		event := registry.NewAuditEvent(r.Now(), tool, call, result, approval, execErr)
		event.Duration = time.Since(start)
		event.Hooks = hookAuditMetadata(lastHookOut)
		event.OriginalArgs = originalApprovedArgs
		event.Rewritten = toolWasRewritten
		r.logToolCall(callID, event)
		r.trackerMu.Lock()
		count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(execErr.Error()), false)
		tier := r.tracker.failedRepeatTier(toolName, string(normalizedArgs))
		streak := r.tracker.failedStreak(toolName, string(normalizedArgs))
		if tier >= failedRepeatStall {
			// Arm the failure-path hard stall so checkStall breaks the loop
			// instead of waiting for the much slower success-side ladder.
			r.tracker.noteFailedRepeatStall()
		}
		r.trackerMu.Unlock()
		r.noteFailedRepeatTelemetry(tier, streak)
		r.countToolCall(true, false)
		msg := r.buildToolErrorResultMessage(toolName, result, execErr, toolCallID)
		// repeatReminder tells the model the repeated call produced IDENTICAL
		// OUTPUT. On the failure path that is precisely what is NOT true of the
		// spiral this ladder exists to catch: the args repeat while the error
		// text differs every attempt. Asserting identical output beside a
		// failure-streak line would contradict it, so from the nudge threshold
		// up the failure ladder owns the coaching text.
		//
		// The success-side ladder is untouched: a call that keeps SUCCEEDING
		// with identical output never enters this branch and still gets every
		// reminder.
		if tier < failedRepeatNudge {
			msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
		}
		if tier >= failedRepeatNudge {
			msg.Content += fmt.Sprintf("\n\nthis identical call already failed %d times; re-read the target and retry with exact bytes from disk.", streak)
		}
		msg.Content += r.todoNudge()
		// Forced retry correction (tier 3+): on the third identical-args
		// write-tool failure, stop coaching and name the exact bytes to
		// re-read. Purely additive diagnostic text — it never changes control
		// flow, the returned messages, or the error.
		if (toolName == "file.write_patch" || toolName == "file.edit") && tier >= failedRepeatInject {
			msg.Content += failedPatchTargetHint(r, toolName, args, execErr)
		}
		return []schema.ChatMessage{msg}, nil
	}

	// Record referenced paths only on success: a denied, hook-blocked, or
	// failed read never actually surfaced content to the agent, so it must
	// not count toward the mid-turn semantic re-query threshold (AI-10).
	if r.semTracker != nil {
		r.semTracker.note(referencedPathsForTool(toolName, argsMap))
	}

	// The call completed. Intent that arrives immediately afterwards does not
	// rewrite that: the work is done and its result is truthful, so it settles
	// as completed and the interruption is reported beside it rather than
	// instead of it.
	r.recordSteeringOutcomes(ctx, callID, toolCallOutcomes(action, result, "completed"))

	summarized := SummarizeToolResult(toolName, result, 0) // per-tool line limits only; 0 keeps the default char cap out of play here
	summarized = spillToolResult(r.State.WorkingDir, toolName, summarized, r.toolResultChars())
	// A tool that mutated anything invalidates every cached read: serving
	// pre-write content as current is worse than paying for the re-read.
	// Deliberately blunt — path-scoped invalidation can come later.
	if tool.Risk != registry.RiskReadOnly {
		r.State.ClearToolCache()
		if r.CacheInvalidator != nil {
			r.CacheInvalidator()
		}
	}
	if tool.Cacheable {
		r.State.SetTurnToolResult(toolName, normalizedArgs, summarized)
	}
	event := registry.NewAuditEvent(r.Now(), tool, call, summarized, approval, nil)
	event.Duration = time.Since(start)
	event.Hooks = hookAuditMetadata(lastHookOut)
	event.OriginalArgs = originalApprovedArgs
	event.Rewritten = toolWasRewritten
	r.logToolCall(callID, event)

	// A successful todo.write is the list being maintained: restart the stale
	// clock so the nudge does not fire on the very next call.
	if toolName == "todo.write" {
		r.noteTodoWrite()
	}

	msg := r.buildToolResultMessage(toolName, summarized, toolCallID)
	r.trackerMu.Lock()
	count := r.tracker.record(toolName, string(normalizedArgs), hashToolResult(summarized.Content), true)
	r.trackerMu.Unlock()
	msg.Content += repeatReminder(count, toolName, string(normalizedArgs))
	msg.Content += r.todoNudge()
	r.countToolCall(false, false)
	return []schema.ChatMessage{msg}, nil
}

func (r *Runner) buildToolResultMessage(name string, result registry.ToolResult, toolCallID string) schema.ChatMessage {
	toolCallID = wireCallID(toolCallID)
	if toolCallID != "" {
		return BuildNativeToolResultMessage(name, result, toolCallID)
	}
	return BuildToolResultMessage(name, result)
}

func (r *Runner) buildCachedToolResultMessage(name string, result registry.ToolResult, toolCallID string) schema.ChatMessage {
	toolCallID = wireCallID(toolCallID)
	if toolCallID != "" {
		return BuildCachedNativeToolResultMessage(name, result, toolCallID)
	}
	return BuildCachedToolResultMessage(name, result)
}

func (r *Runner) buildToolErrorMessage(name, reason, toolCallID string) schema.ChatMessage {
	toolCallID = wireCallID(toolCallID)
	if toolCallID != "" {
		return BuildNativeToolErrorMessage(name, reason, toolCallID)
	}
	return BuildToolErrorMessage(name, reason)
}

// buildToolErrorResultMessage builds the model-facing failure message for a
// tool whose handler returned BOTH a partial result and an error. The reason
// is the handler's error; the partial result's evidence is appended so the
// model sees what actually happened — the output a command produced before it
// failed, its real exit code, its stop cause and sandbox limits — instead of an
// error string alone. A handler that returned nothing keeps the plain error
// message, so nothing changes for the ordinary failure path.
func (r *Runner) buildToolErrorResultMessage(name string, result registry.ToolResult, err error, toolCallID string) schema.ChatMessage {
	reason := err.Error()
	if evidence := partialResultEvidence(result); evidence != "" {
		reason += "\n\n" + evidence
	}
	return r.buildToolErrorMessage(name, reason, toolCallID)
}

// partialResultEvidence renders the evidence a failed tool handler still
// produced. Empty when the handler returned nothing worth reporting, which is
// the common case for a rejection that never started any work.
func partialResultEvidence(result registry.ToolResult) string {
	var b strings.Builder
	if summary := strings.TrimSpace(result.Summary); summary != "" {
		fmt.Fprintf(&b, "partial result: %s", summary)
	}
	if content := strings.TrimSpace(result.Content); content != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(content)
	}
	if result.CommandExitCode != nil {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "exit code: %d", *result.CommandExitCode)
	}
	if reason := strings.TrimSpace(result.Sandbox.KilledReason); reason != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "stop/kill evidence: %s", reason)
	}
	if result.Error != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "tool error field: %s", result.Error)
	}
	return b.String()
}

// normalizeToolName resolves truncated provider tool names (e.g. "read"
// for "file.read") to their fully-qualified registered name. If the name
// is already registered, it passes through unchanged. If the name is not
// registered but exactly one registered tool ends with "."+name, that
// tool's full name is returned. Otherwise the original name is returned
// unchanged (the caller will produce an "unknown tool" error).
func normalizeToolName(reg *registry.Registry, name string) string {
	if reg == nil {
		return name
	}
	if _, ok := reg.Lookup(name); ok {
		return name
	}
	// The tool schema advertised to the model uses toolNameToAlias's dotless
	// alias (see chat.go buildToolDefinitions), so a well-behaved provider
	// echoes the alias back verbatim — reverse it before falling back to the
	// truncated-name heuristic below.
	if canonical, ok := toolAliasMap(reg.List())[name]; ok {
		return canonical
	}
	suffix := "." + name
	var match string
	count := 0
	for _, tool := range reg.List() {
		if strings.HasSuffix(tool.Name, suffix) {
			match = tool.Name
			count++
		}
	}
	if count == 1 {
		return match
	}
	return name
}

// actionForCall converts a native tool call into the action shape the
// executor works in.
func actionForCall(call schema.ToolCall) ModelAction {
	return ModelAction{Type: ActionToolCall, Tool: call.Name, Args: call.Args, ToolCallID: call.ID}
}

// unlaunchedCallResult settles one call whose dispatch never produced a result.
// Steering settles it as not started (nothing was validated, prompted or
// launched), with one audit entry and one outcome; any other dispatch error —
// a full-turn cancel, a hook halt, a policy error — keeps the existing failure
// text, so a real error is never relabelled as a user interruption.
func (r *Runner) unlaunchedCallResult(ctx context.Context, action ModelAction, cause error) []schema.ChatMessage {
	if cause == nil {
		cause = ctx.Err()
	}
	if cause == nil {
		cause = errors.New("the call was not dispatched")
	}
	if steeringStopped(ctx, cause) {
		return r.settleSteeringCall(ctx, action, "", registry.ToolResult{}, false, steeringCause(ctx, "", cause))
	}
	return []schema.ChatMessage{r.toolCallOutcomeMessage(schema.ToolCall{ID: action.ToolCallID, Name: actionToolName(action)}, cause)}
}

// settleDispatch converts one call's dispatch outcome into its settled slot
// messages. A call the executor settled itself (steering mid-flight) passes its
// messages through unchanged; a call whose dispatch returned an error with no
// result is settled here so its slot is never left empty.
func (r *Runner) settleDispatch(ctx context.Context, action ModelAction, msgs []schema.ChatMessage, err error) []schema.ChatMessage {
	if err == nil || len(msgs) > 0 {
		return msgs
	}
	return r.unlaunchedCallResult(ctx, action, err)
}

func (r *Runner) executeNativeToolCalls(ctx context.Context, calls []schema.ToolCall) ([]schema.ChatMessage, error) {
	r.trackerMu.Lock()
	r.invalidArgsThisRound = 0
	r.trackerMu.Unlock()

	// One ordered result slot per declared call. Every path below fills its
	// slot — including a call the interruption skipped — so an interrupted batch
	// still returns exactly one settled message per tool_call, in declared
	// order. That pairing is what the provider requires of the next request; a
	// batch that dropped its unlaunched calls would leave dangling tool_calls.
	slots := make([][]schema.ChatMessage, len(calls))

	// agent.run calls are dispatched concurrently when a batch contains
	// consecutive runs. ask_user / question.ask stay sequential because
	// they share single-session state slots. Non-agent-run tools run
	// sequentially in their natural order.
	type runSlot struct {
		idx  int
		call schema.ToolCall
	}
	var runBatch []runSlot
	flushRunBatch := func() {
		if len(runBatch) == 0 {
			return
		}
		if len(runBatch) == 1 {
			slot := runBatch[0]
			action := actionForCall(slot.call)
			resultMsgs, err := r.executeToolCall(ctx, action)
			slots[slot.idx] = r.settleDispatch(ctx, action, resultMsgs, err)
			runBatch = runBatch[:0]
			return
		}
		batch := append([]runSlot(nil), runBatch...)
		runBatch = runBatch[:0]
		results := make([][]schema.ChatMessage, len(batch))
		errs := make([]error, len(batch))
		var wg sync.WaitGroup
		wg.Add(len(batch))
		for i, slot := range batch {
			i, slot := i, slot
			// A dispatch that never got to start (steering accepted between the
			// batch decision and this goroutine) is settled in place rather than
			// spawning work the user already interrupted.
			if msgs, stop := r.admitToolCall(ctx, actionForCall(slot.call)); stop {
				results[i] = msgs
				wg.Done()
				continue
			}
			go func() {
				defer wg.Done()
				results[i], errs[i] = r.executeToolCall(ctx, actionForCall(slot.call))
			}()
		}
		wg.Wait()
		// One call failing must not discard its siblings' results: every call
		// in the batch gets exactly one settled message, so the tool-call/result
		// pairing stays valid no matter which call was interrupted. Children a
		// sibling already dispatched keep their handles — the interruption
		// cancels this batch's waits, never an independent child.
		for i, slot := range batch {
			slots[slot.idx] = r.settleDispatch(ctx, actionForCall(slot.call), results[i], errs[i])
		}
	}

	for i, call := range calls {
		call.Name = normalizeToolName(r.Registry, call.Name)
		action := actionForCall(call)
		if call.Name == "question.ask" {
			flushRunBatch()
			if msgs, stop := r.admitToolCall(ctx, action); stop {
				slots[i] = msgs
				continue
			}
			msg, err := r.executeNativeQuestionAsk(ctx, call)
			if err != nil {
				// A cancelled question is not a failed batch: the call was
				// admitted and then interrupted, so it gets a settled
				// not-completed result and the remaining calls continue.
				slots[i] = r.settleDispatch(ctx, action, nil, err)
				continue
			}
			slots[i] = []schema.ChatMessage{msg}
			continue
		}
		if call.Name == "agent.run" {
			runBatch = append(runBatch, runSlot{idx: i, call: call})
			continue
		}
		flushRunBatch()
		if msgs, stop := r.admitToolCall(ctx, action); stop {
			slots[i] = msgs
			continue
		}
		resultMsgs, err := r.executeToolCall(ctx, action)
		slots[i] = r.settleDispatch(ctx, action, resultMsgs, err)
	}
	flushRunBatch()

	var msgs []schema.ChatMessage
	for _, slotMsgs := range slots {
		msgs = append(msgs, slotMsgs...)
	}
	return msgs, nil
}

// toolCallOutcomeMessage builds the settled result for one tool call whose
// dispatch returned an error, so an interrupted call still has a result that
// pairs with its call. A cancellation that never admitted the call is reported
// as not started; anything else keeps the runner's error text. The message is
// always a tool-role reply for the call's own ID, never a batch-level failure.
func (r *Runner) toolCallOutcomeMessage(call schema.ToolCall, err error) schema.ChatMessage {
	reason := err.Error()
	if errors.Is(err, context.Canceled) {
		reason = "not started: the turn was stopped before this call completed (" + reason + ")"
	}
	return r.buildToolErrorMessage(call.Name, reason, call.ID)
}

// executeNativeQuestionAsk handles the new structured question.ask tool,
// which accepts one or more questions with optional options/multi/other.
func (r *Runner) executeNativeQuestionAsk(ctx context.Context, call schema.ToolCall) (schema.ChatMessage, error) {
	if r.role() != RoleGeneral {
		return BuildNativeToolErrorMessage(call.Name, fmt.Sprintf("%s is not available for the %s role", call.Name, r.role()), call.ID), nil
	}
	if r.Policy != nil && r.Policy.ApprovalMode() == policy.ModeAuto {
		return BuildNativeToolErrorMessage(call.Name, "question.ask is not available in auto mode; proceed with your best judgment and state the assumption you made", call.ID), nil
	}
	// executeNativeToolCalls short-circuits on tool name before the agent
	// path's early validation runs, so question.ask needs an explicit
	// validation here. Use the bespoke validator rather than the generic
	// schema check so a rejected payload comes back with a field path and
	// a valid example the model can copy.
	if err := native.ValidateQuestionAsk(call.Args); err != nil {
		r.countToolCall(true, false)
		r.noteInvalidArgs()
		return BuildNativeToolErrorMessage(call.Name, err.Error(), call.ID), nil
	}
	var payload struct {
		Questions []session.Question `json:"questions"`
	}
	if err := json.Unmarshal(call.Args, &payload); err != nil {
		r.countToolCall(true, false)
		return BuildNativeToolErrorMessage(call.Name, "arguments are not valid JSON", call.ID), nil
	}
	answers, waitErr := r.requestQuestions(ctx, payload.Questions)
	if waitErr != nil {
		// A canceled question is settled here rather than returned as a batch
		// failure: the call has a pairing to fill, and the settled result says
		// the question was interrupted instead of presenting a fabricated human
		// answer. A real error (a malformed payload, a policy failure) has
		// already returned above with its own message.
		if steeringStopped(ctx, waitErr) {
			r.countToolCall(false, false)
			return schema.ChatMessage{
				Role:       schema.RoleTool,
				ToolCallID: call.ID,
				Content:    "Question canceled: user steering interrupted the operation before the questions were answered. No answer was given; do not assume one.",
			}, nil
		}
		return schema.ChatMessage{}, waitErr
	}
	r.countToolCall(false, false)
	if r.turnBudget != nil {
		r.turnBudget.overhead++
		r.withStats(func(s *turnStats) { s.m.Iterations = r.turnBudget.total() })
	}
	parts := []string{"User answers:"}
	allUnanswered := true
	for _, a := range answers {
		if a.Answer != session.AnswerUnanswered {
			allUnanswered = false
		}
		parts = append(parts, fmt.Sprintf("- %q: %q", a.Question, a.Answer))
	}
	if allUnanswered {
		r.trackerMu.Lock()
		r.tracker.recordIdle("question.ask declined")
		r.trackerMu.Unlock()
		return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: "The user declined to answer every question. Proceed with your best judgment."}, nil
	}
	return schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: strings.Join(parts, "\n")}, nil
}

func (r *Runner) allReadOnly(actions []ModelAction) error {
	var rejected []string
	for _, a := range actions {
		if a.Type != ActionToolCall {
			return fmt.Errorf("action type %q in actions array is not a tool_call", a.Type)
		}
		tool, ok := r.Registry.Lookup(a.Tool)
		if !ok {
			return fmt.Errorf("unknown tool %q in actions array", a.Tool)
		}
		if tool.Risk != registry.RiskReadOnly {
			rejected = append(rejected, fmt.Sprintf("%s (risk=%s)", a.Tool, tool.Risk))
		}
	}
	if len(rejected) > 0 {
		return fmt.Errorf("actions array accepts read-only tools only; rejected: %s. Read-only tools: %s",
			strings.Join(rejected, ", "), strings.Join(r.Registry.ReadOnlyNames(), ", "))
	}
	return nil
}

// requiresSerialTool is the deny list of tools that share a single
// process-wide slot (today: State.PendingQuestion, State.PendingSkillGate,
// and State.PendingApproval). They must never run concurrently inside
// executeActions, or two calls will clobber each other and leak the inner
// ResponseChan. They are still admitted by allReadOnly; executeActions is
// responsible for ordering them.
//
// If future question tool aliases are added (for example a renamed
// "question.ask.v2"), every spelling must be added to this switch.
// Adding a new alias without listing it here will reintroduce the
// parallel-batch race on the single PendingQuestion slot.
func requiresSerialTool(name string) bool {
	switch name {
	case "question.ask", "skill.load", "mode.request":
		return true
	}
	return false
}

// batchUnsafeReadOnlyTools are read-only tools that must not be advertised
// as valid actions[] entries even though allReadOnly admits them: they
// mutate session state that a sibling call in the same batch may be
// reading. requiresSerialTool names are excluded separately.
var batchUnsafeReadOnlyTools = map[string]bool{
	"tools.select": true,
}

// batchSafeReadOnlyNames returns the sorted names of read-only tools that
// are safe to run concurrently inside an actions[] batch. It is the list
// the system prompt advertises, so it must never name a tool that
// executeActions would serialise or that would race on shared state.
func batchSafeReadOnlyNames(tools []registry.Tool) []string {
	internal := make(map[string]bool)
	for _, t := range tools {
		if t.Internal {
			internal[t.Name] = true
		}
	}
	all := registry.ReadOnlyNames(tools)
	safe := make([]string, 0, len(all))
	for _, name := range all {
		if internal[name] || requiresSerialTool(name) || batchUnsafeReadOnlyTools[name] {
			continue
		}
		safe = append(safe, name)
	}
	return safe
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
		// Intent is checked before every serial launch: once steering owns this
		// operation, the serial calls that have not started are skipped (and
		// their slots filled) rather than launched under stale intent.
		if msgs, stop := r.admitToolCall(ctx, a); stop {
			results[i] = msgs
			continue
		}
		msgs, err := r.executeToolCall(ctx, a)
		if err != nil {
			// A serial tool that was interrupted still gets a settled result for
			// its own call ID, and the remaining serial tools keep running
			// rather than being dropped with the failure.
			results[i] = r.settleDispatch(ctx, a, msgs, err)
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
			act := actions[idx]
			if msgs, stop := r.admitToolCall(ctx, act); stop {
				results[idx] = msgs
				continue
			}
			// The semaphore wait is itself cancelable: a worker that would
			// otherwise park here until a slot frees must wake when the
			// operation is interrupted, and it must not launch after acquiring
			// a slot the interruption already invalidated.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[idx] = r.unlaunchedCallResult(ctx, act, session.CheckSteering(ctx))
				continue
			}
			if msgs, stop := r.admitToolCall(ctx, act); stop {
				<-sem
				results[idx] = msgs
				continue
			}
			wg.Add(1)
			go func(i int, act ModelAction) {
				defer wg.Done()
				defer func() { <-sem }()
				msgs, err := r.executeToolCall(ctx, act)
				if err != nil {
					// Interruption is per call, never per batch: a sibling that
					// already ran keeps its results and a cancelled one still has
					// a settled outcome to pair with its call.
					results[i] = r.settleDispatch(ctx, act, msgs, err)
					return
				}
				results[i] = msgs
			}(idx, act)
		}
		wg.Wait()
	}

	var flat []schema.ChatMessage
	for _, msgs := range results {
		flat = append(flat, msgs...)
	}
	return flat, nil
}

// logToolCall stamps the current turn's provider finish reason onto the event
// and records it. Every audit event the runner emits goes through here: the
// events are built at seven sites, and a stamp applied at each of them is a
// stamp that will be forgotten at the eighth.
func (r *Runner) logToolCall(toolCallID string, event registry.AuditEvent) {
	if event.FinishReason == "" {
		event.FinishReason = r.getTurnFinishReason()
	}
	// Who asked for the call and in which step; without them the transcript
	// can only guess ownership from timestamp adjacency.
	if event.StepID == 0 {
		event.StepID = r.curStep
	}
	if event.ToolCallID == "" {
		event.ToolCallID = toolCallID
	}
	if event.AgentRole == "" {
		event.AgentRole = r.actorRole()
	}
	if event.Model == "" {
		event.Model = r.curModel
	}
	r.State.LogToolCall(event)
}

// skillLoadGate implements the skill-load gate for one skill.load call.
// Returns (messages, true) when the gate fully handled the call (sticky
// deny, cancelled prompt, or a deny chosen in the prompt); (nil, false)
// means proceed with normal dispatch. The prompt path serializes on
// approvalMu because State.PendingSkillGate is a single slot, exactly like
// requestApproval serializes on State.PendingApproval.
func (r *Runner) skillLoadGate(ctx context.Context, tool registry.Tool, args json.RawMessage, argsMap map[string]interface{}, toolCallID string) ([]schema.ChatMessage, bool) {
	threshold := r.State.Config.Skills.LoadGateThresholdTokens
	if threshold <= 0 {
		return nil, false
	}
	name, _ := argsMap["name"].(string)
	if name == "" {
		return nil, false // unparseable name: the loader's own error path handles it
	}
	if r.SkillIndex != nil {
		if _, ok := r.SkillIndex.Load(name); !ok {
			return nil, false // unknown skill: the loader's "unknown skill" error is more useful than a prompt
		}
	}
	_, window := r.State.TurnUsage()
	switch r.State.SkillGateApplies(name, threshold, window) {
	case session.SkillGateProceed:
		return nil, false
	case session.SkillGateDenied:
		r.State.SkillGateRecordDeny(name)
		return r.skillGateDenyMessages(name, args, toolCallID), true
	}
	if r.Policy != nil && r.Policy.ApprovalMode() == policy.ModeAuto {
		r.State.SkillGateRecordAllow(name, false)
		return nil, false
	}
	r.approvalMu.Lock()
	choice, waitErr := r.requestSkillGate(ctx, name)
	r.approvalMu.Unlock()
	if waitErr != nil {
		// Steering outranks the ordinary denial and Unanswered handling below:
		// the call is settled as interrupted, records NO sticky skill denial and
		// does not present a prompt cancellation as a user decision.
		if steeringStopped(ctx, waitErr) {
			return r.settleSteeringCall(ctx, ModelAction{Type: ActionToolCall, Tool: "skill.load", Args: args, ToolCallID: toolCallID},
				toolCallID, registry.ToolResult{}, false, steeringCause(ctx, "", waitErr)), true
		}
		return []schema.ChatMessage{r.buildToolErrorMessage("skill.load", "skill load was not approved (prompt cancelled)", toolCallID)}, true
	}
	switch choice {
	case session.SkillGateAllowOnce:
		if err := session.CheckSteering(ctx); steeringStopped(ctx, err) {
			// Approved, but the operation was interrupted between the grant and
			// the load: recheck admission so an approval cannot launch a load
			// under stale intent.
			return r.settleSteeringCall(ctx, ModelAction{Type: ActionToolCall, Tool: "skill.load", Args: args, ToolCallID: toolCallID},
				toolCallID, registry.ToolResult{}, false, steeringCause(ctx, "", err)), true
		}
		return nil, false
	case session.SkillGateAllowSkill:
		r.State.SkillGateRecordAllow(name, false)
		return nil, false
	case session.SkillGateAllowAll:
		r.State.SkillGateRecordAllow(name, true)
		return nil, false
	default:
		if ctx.Err() != nil {
			// A shutdown-sent deny is a cancellation, not a user decision:
			// record nothing (a cancelled prompt is not a deny).
			return []schema.ChatMessage{r.buildToolErrorMessage("skill.load", "skill load was not approved (prompt cancelled)", toolCallID)}, true
		}
		r.State.SkillGateRecordDeny(name)
		return r.skillGateDenyMessages(name, args, toolCallID), true
	}
}

// skillGateDenyMessages builds the model-facing denial: the audit event
// (with the call's args and the refused skill's name, so the audit log
// can distinguish which load was denied), the tool-call counter, and the
// guidance message verbatim from the spec.
func (r *Runner) skillGateDenyMessages(name string, args json.RawMessage, toolCallID string) []schema.ChatMessage {
	tool, _ := r.Registry.Lookup("skill.load")
	event := registry.NewAuditEvent(r.Now(), tool, registry.ToolCall{Name: "skill.load", Args: args}, registry.ToolResult{}, registry.ApprovalDenied, fmt.Errorf("denied by skill load gate: %s", name))
	r.logToolCall(toolCallID, event)
	r.countToolCall(true, false)
	return []schema.ChatMessage{r.buildToolErrorMessage("skill.load",
		fmt.Sprintf("Skill load denied: you do not need the skill %q at this point. You may attempt to load it again later if the situation changes.", name),
		toolCallID)}
}
